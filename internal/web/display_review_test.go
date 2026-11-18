package web

import (
	"html/template"
	"strings"
	"testing"
)

// Review of the parse-time rewrite (2026-11-18): show must not break what html/template does after it.

func render(t *testing.T, src string, data any) string {
	t.Helper()
	tmpl, err := template.New("x").Funcs(funcs).Funcs(displayFuncs).Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	escapeOutput(tmpl)
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return b.String()
}

// A pipeline ending in one of html/template's predefined escapers must still render.
func TestShow_PipelinesEndingInPredefinedEscapers(t *testing.T) {
	bidi := "a" + string(rune(0x202e)) + "b"
	got := render(t, `<p>{{. | urlquery}}</p><p>{{. | html}}</p>`, bidi)
	if strings.ContainsRune(got, 0x202e) {
		t.Errorf("bidi override written as it is: %q", got)
	}
}

// In a script, numbers and booleans keep their JavaScript types.
func TestShow_KeepsNumbersAndBooleansInScripts(t *testing.T) {
	got := render(t, `<script>var n = {{.N}}; var b = {{.B}};</script>`, struct {
		N int
		B bool
	}{3, true})
	if !strings.Contains(got, "var n = 3;") || !strings.Contains(got, "var b = true;") {
		t.Errorf("got %q", got)
	}
}

// In a URL attribute the value is percent-encoded with its exact bytes (which makes invisible characters visible
// anyway), so a link built from a raw value still works; the same value as text shows the escape.
func TestShow_URLAttributesKeepExactBytes(t *testing.T) {
	bidi := "a" + string(rune(0x202e)) + "b"
	got := render(t, `<a href="/f?p={{.}}">{{.}}</a><img src="/i/{{.}}"><form action="/x/{{.}}"></form>`, bidi)
	for _, want := range []string{`href="/f?p=a%e2%80%aeb"`, `src="/i/a%e2%80%aeb"`, `action="/x/a%e2%80%aeb"`, `>a\u202eb</a>`} {
		if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
			t.Errorf("missing %s in %q", want, got)
		}
	}
	if strings.ContainsRune(got, 0x202e) {
		t.Errorf("bidi override written as it is: %q", got)
	}
}
