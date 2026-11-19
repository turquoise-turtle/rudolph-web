package web

import (
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Review of the stored-XSS hardening (2026-11-19). render writes every invisible character as a character reference,
// so the guard that looks for raw characters on every page can no longer tell a value printed without show: the
// browser decodes &#x202e; back into a live bidi override. Only a posted form value (an input's value attribute or a
// textarea's text) may carry such a reference, since it must post back the bytes it was given; anywhere else a
// reference to an invisible character means a value reached the page unescaped. The tag characters of a subdivision
// flag, which show keeps, are the one exception.

// hiddenByRef reports whether a character reference to r would put a hidden, reordering or control character into the
// page: a control other than the page's own whitespace, a default-ignorable or format character other than the
// joiners and the emoji presentation selector, a line or paragraph separator, or the blank braille pattern.
func hiddenByRef(r rune) bool {
	switch r {
	case '\n', '\r', '\t', 0x200c, 0x200d, 0xfe0f:
		return false
	case 0x2800:
		return true
	}
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Other_Default_Ignorable_Code_Point,
		unicode.Variation_Selector)
}

// A numeric reference needs no semicolon for a browser to decode it; a named one is only looked at with its semicolon.
var charRefAt = regexp.MustCompile(`^&(?:#[xX][0-9a-fA-F]+;?|#[0-9]+;?|[A-Za-z][A-Za-z0-9]*;)`)

var (
	valueAttr    = regexp.MustCompile(`(?is)<[a-z][^<>]*?\svalue="([^"]*)"`)
	textareaText = regexp.MustCompile(`(?is)<textarea\b[^<>]*>(.*?)</textarea\s*>`)
)

const (
	blackFlagRune = 0x1f3f4
	cancelTagRune = 0xe007f
)

type pageChar struct {
	runes      []rune
	ref        string // the reference as written, or "" for a character written as it is
	start, end int
}

// InvisibleCharRefsOutsideFormValues is invisibleCharRefsOutsideFormValues for the guard over every page, which seeds
// the store from the package's external tests.
var InvisibleCharRefsOutsideFormValues = invisibleCharRefsOutsideFormValues

// invisibleCharRefsOutsideFormValues describes each character reference in body to a character hiddenByRef reports,
// other than in a posted form value or among the tags of a valid subdivision flag.
func invisibleCharRefsOutsideFormValues(body string) []string {
	var formValues [][2]int
	for _, re := range []*regexp.Regexp{valueAttr, textareaText} {
		for _, m := range re.FindAllStringSubmatchIndex(body, -1) {
			formValues = append(formValues, [2]int{m[2], m[3]})
		}
	}
	inFormValue := func(i int) bool {
		for _, v := range formValues {
			if i >= v[0] && i < v[1] {
				return true
			}
		}
		return false
	}

	var chars []pageChar
	for i := 0; i < len(body); {
		if body[i] == '&' {
			if ref := charRefAt.FindString(body[i:]); ref != "" {
				if dec := html.UnescapeString(ref); dec != ref {
					chars = append(chars, pageChar{runes: []rune(dec), ref: ref, start: i, end: i + len(ref)})
					i += len(ref)
					continue
				}
			}
		}
		r, size := utf8.DecodeRuneInString(body[i:])
		chars = append(chars, pageChar{runes: []rune{r}, start: i, end: i + size})
		i += size
	}

	single := func(c pageChar) rune {
		if len(c.runes) == 1 {
			return c.runes[0]
		}
		return -1
	}
	inFlag := map[int]bool{}
	for i, c := range chars {
		if single(c) != blackFlagRune {
			continue
		}
		j := i + 1
		for j < len(chars) && j-i-1 <= 7 {
			r := single(chars[j])
			if !(r >= 0xe0061 && r <= 0xe007a || r >= 0xe0030 && r <= 0xe0039) {
				break
			}
			j++
		}
		if n := j - i - 1; n >= 2 && n <= 7 && j < len(chars) && single(chars[j]) == cancelTagRune {
			for k := i + 1; k <= j; k++ {
				inFlag[k] = true
			}
		}
	}

	var found []string
	for i, c := range chars {
		if c.ref == "" || inFlag[i] || inFormValue(c.start) {
			continue
		}
		for _, r := range c.runes {
			if hiddenByRef(r) {
				found = append(found, fmt.Sprintf("U+%04X as %s at byte %d: %q", r, c.ref, c.start, quoteContext(body, c.start, c.end)))
				break
			}
		}
	}
	return found
}

func quoteContext(body string, start, end int) string {
	return body[max(0, start-50):min(len(body), end+20)]
}

// runEscapeOutput runs escapeOutput whether it reports refusals or not, so these tests build before it does.
func runEscapeOutput(t *template.Template) error {
	switch f := any(escapeOutput).(type) {
	case func(*template.Template) error:
		return f(t)
	case func(*template.Template):
		f(t)
		return nil
	}
	panic("escapeOutput has an unexpected signature")
}

// renderPage parses src as a page's layout, rewrites it as parseTemplates does and renders it through the server's
// render with data as the page's data ({{.Page}}), so the bytes are those a browser would get.
func renderPage(t *testing.T, src string, data any) *httptest.ResponseRecorder {
	t.Helper()
	tmpl, err := template.New("layout.html").Funcs(funcs).Funcs(displayFuncs).Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := runEscapeOutput(tmpl); err != nil {
		t.Fatal(err)
	}
	s := &server{log: slog.New(slog.DiscardHandler), pages: map[string]*template.Template{"probe": tmpl}}
	rec := httptest.NewRecorder()
	s.render(rec, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusOK, "probe", "", "", data)
	return rec
}

func TestInvisibleCharRefGuard(t *testing.T) {
	rlo := string(rune(0x202e))
	flag := string(rune(blackFlagRune))
	for _, tag := range "gbwls" {
		flag += string(tag - 'a' + 0xe0061)
	}
	flag += string(rune(cancelTagRune))

	t.Run("flags references in text", func(t *testing.T) {
		for _, body := range []string{
			"<p>a&#x202e;b</p>", "<p>a&#X202E;b</p>", "<p>a&#8238;b</p>", "<p>a&#x202e b</p>", "<p>&rlm;</p>",
			"<p>&ZeroWidthSpace;</p>", "<p>a&#xfe0e;</p>", "<p>a&#xE0041;</p>", `<p title="&#x200b;">x</p>`,
			`<a href="/x" data-value="&#x202e;">x</a>`, "<p>&#x81;</p>",
		} {
			if got := invisibleCharRefsOutsideFormValues(body); len(got) != 1 {
				t.Errorf("%q: found %q, want one reference", body, got)
			}
		}
	})
	t.Run("leaves form values, flags and visible references", func(t *testing.T) {
		for _, body := range []string{
			`<input type="hidden" name="n" value="a&#x202e;b">`, `<option value="a&#x200b;">a\u200b</option>`,
			"<textarea name=\"d\">a&#x202e;\nb</textarea>", "<p>" + flag + "</p>",
			"<p>&#x1f3f4;&#xe0067;&#xe0062;&#xe0073;&#xe0063;&#xe0074;&#xe007f;</p>", "<p>&amp; &lt; &#39; &#x200d; &#xfe0f;</p>",
		} {
			if got := invisibleCharRefsOutsideFormValues(body); len(got) != 0 {
				t.Errorf("%q: found %q, want none", body, got)
			}
		}
	})
	t.Run("a tag outside a valid flag is flagged", func(t *testing.T) {
		for _, body := range []string{
			"<p>a&#xe0067;&#xe0062;&#xe007f;</p>", "<p>" + string(rune(blackFlagRune)) + "&#xe0067;&#xe007f;</p>",
			"<p>" + string(rune(blackFlagRune)) + "&#xe0067;&#xe0062;</p>",
		} {
			if got := invisibleCharRefsOutsideFormValues(body); len(got) == 0 {
				t.Errorf("%q: found none, want the stray tags", body)
			}
		}
	})
	// The guard must catch what it is for: a value printed with verbatim outside a form value reaches the page as a
	// reference once render has written its invisible characters that way.
	t.Run("bites on verbatim in text", func(t *testing.T) {
		rec := renderPage(t, `<p>{{verbatim .Page}}</p>`, "a"+rlo+"b")
		if strings.Contains(rec.Body.String(), rlo) {
			t.Fatalf("render wrote the bidi override raw: %q", rec.Body.String())
		}
		if got := invisibleCharRefsOutsideFormValues(rec.Body.String()); len(got) == 0 {
			t.Errorf("guard found nothing in %q", rec.Body.String())
		}
	})
	t.Run("passes form values and shown flags as rendered", func(t *testing.T) {
		rec := renderPage(t, `<form><input type="hidden" name="n" value="{{verbatim .Page}}"><textarea name="d">{{verbatim .Page}}</textarea></form><p>{{.Page}}</p>`,
			"a"+rlo+"b"+flag)
		if got := invisibleCharRefsOutsideFormValues(rec.Body.String()); len(got) != 0 {
			t.Errorf("found %q in %q", got, rec.Body.String())
		}
	})
}
