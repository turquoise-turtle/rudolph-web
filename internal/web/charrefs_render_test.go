package web

import (
	"html"
	"html/template"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// Review of the stored-XSS hardening (2026-11-19): what render's pass over the finished page (invisible characters as
// character references) must and must not touch.

var hiddenValue = regexp.MustCompile(`<input type="hidden" name="n" value="([^"]*)">`)

// HTML reads a reference to most of U+0080-U+009F as a Windows-1252 character (&#x85; is an ellipsis), so those C1
// controls must reach a posted value as they are, and only the five Windows-1252 leaves undefined may be references.
// Wherever a C1 control is displayed rather than posted, it is shown as a visible escape.
func TestCharRefs_C1ControlsInFormValues(t *testing.T) {
	var v string
	for _, r := range []rune{'a', 0x85, 'b', 0x9b, 'c', 0x81, 0x8d, 0x8f, 0x90, 0x9d, 'd'} {
		v += string(r)
	}
	rec := renderPage(t, `<form><input type="hidden" name="n" value="{{verbatim .Page}}"></form><p>{{.Page}}</p>`, v)
	body := rec.Body.String()
	m := hiddenValue.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no hidden input in %q", body)
	}
	if got := html.UnescapeString(m[1]); got != v {
		t.Errorf("posted value decodes to %q, want %q (written %q)", got, v, m[1])
	}
	for _, r := range []rune{0x81, 0x8d, 0x8f, 0x90, 0x9d} {
		if strings.ContainsRune(m[1], r) {
			t.Errorf("U+%04X written raw in the form value %q; want a character reference", r, m[1])
		}
	}
	shown := body[len(m[0]):]
	for _, esc := range []string{`\u0085`, `\u009b`, `\u0081`, `\u008d`, `\u008f`, `\u0090`, `\u009d`} {
		if !strings.Contains(shown, esc) {
			t.Errorf("displayed value does not show %s: %q", esc, shown)
		}
	}
	for _, r := range shown {
		if r >= 0x80 && r <= 0x9f {
			t.Errorf("displayed value writes U+%04X raw: %q", r, shown)
		}
	}
}

// Character references are not decoded in a script or a style sheet, so their text must reach the browser as written,
// in whatever case the tag is written.
func TestCharRefs_ScriptsAndStylesLeftAlone(t *testing.T) {
	rlo := string(rune(0x202e))
	markup := template.HTML("<script>var s = '" + rlo + "';</script><SCRIPT type=\"module\">/*" + rlo + "*/</SCRIPT>" +
		"<style>p::after { content: '" + rlo + "'; }</style><p>" + rlo + "</p>")
	body := renderPage(t, `{{.Page}}`, markup).Body.String()
	for _, want := range []string{
		"<script>var s = '" + rlo + "';</script>", "<SCRIPT type=\"module\">/*" + rlo + "*/</SCRIPT>",
		"<style>p::after { content: '" + rlo + "'; }</style>", "<p>&#x202e;</p>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q:\n%q", want, body)
		}
	}
}

// Bytes that are not UTF-8 are not characters to reference; they must pass through as they are, and not stop the pass
// for what follows.
func TestCharRefs_InvalidUTF8PassesThrough(t *testing.T) {
	rlo := string(rune(0x202e))
	bad := "a\xffb\xc3(c\xe2\x80d\xf0\x9f"
	body := renderPage(t, `{{.Page}}`, template.HTML("<p>"+bad+"</p><p>"+rlo+"</p>")).Body.String()
	if !strings.Contains(body, "<p>"+bad+"</p>") {
		t.Errorf("invalid UTF-8 changed: %q", body)
	}
	if strings.Contains(body, rlo) || !strings.Contains(body, "<p>&#x202e;</p>") {
		t.Errorf("bidi override after invalid UTF-8 not written as a reference: %q", body)
	}
}

// "<script" in static text that is not a script element (an attribute's value) must not switch the pass off:
// the input after it carries a posted value with a bidi override, and nothing in the page ends that "script".
func TestCharRefs_ScriptNamedInTextDoesNotStopThePass(t *testing.T) {
	rlo := string(rune(0x202e))
	for name, src := range map[string]string{
		"attribute":  `<p title="see <script> tag">x</p><input type="hidden" name="n" value="{{verbatim .Page}}">`,
		"style attr": `<p title="no <style> here">x</p><input type="hidden" name="n" value="{{verbatim .Page}}">`,
	} {
		t.Run(name, func(t *testing.T) {
			body := renderPage(t, src, "a"+rlo+"b").Body.String()
			if strings.Contains(body, rlo) || !strings.Contains(body, `value="a&#x202e;b"`) {
				t.Errorf("bidi override written raw after static %q: %q", "<script>", body)
			}
		})
	}
}

// A script that is never closed must not leave the rest of the page unchecked: either the pass still writes later
// invisible characters as references, or the page is refused.
func TestCharRefs_UnclosedScript(t *testing.T) {
	rlo := string(rune(0x202e))
	data := struct {
		H template.HTML
		V string
	}{"<script>var a = 1;", "a" + rlo + "b"}
	rec := renderPage(t, `{{.Page.H}}<input type="hidden" name="n" value="{{verbatim .Page.V}}">`, data)
	if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), rlo) {
		t.Errorf("unclosed script left a raw bidi override on the page: %q", rec.Body.String())
	}
}

// The pass skips script and style contents, so a template with such an element elsewhere would hide what it prints
// from the pass; only the layout's one script exists.
func TestTemplates_OnlyTheLayoutScript(t *testing.T) {
	script := regexp.MustCompile(`(?i)<script`)
	style := regexp.MustCompile(`(?i)<style`)
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 10 {
		t.Fatalf("found only %d templates", len(names))
	}
	for _, name := range names {
		b, err := fs.ReadFile(templateFS, name)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if name == "templates/layout.html" {
			want = 1
		}
		if n := len(script.FindAll(b, -1)); n != want {
			t.Errorf("%s has %d <script elements, want %d", name, n, want)
		}
		if n := len(style.FindAll(b, -1)); n != 0 {
			t.Errorf("%s has %d <style elements, want none", name, n)
		}
	}
}
