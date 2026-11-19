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

// html/template pads a number or boolean it writes into a script with spaces, so "5-{{.}}" with -1 stays "5- -1 "
// rather than the decrement "5--1". The rewrite must leave such values exactly as html/template writes them.
func TestShow_KeepsNumbersAndBooleansInScripts(t *testing.T) {
	for name, v := range map[string]any{
		"negative int":   -1,
		"positive int":   3,
		"negative int8":  int8(-7),
		"uint":           uint(9),
		"negative float": -2.5,
		"true":           true,
		"false":          false,
	} {
		t.Run(name, func(t *testing.T) {
			src := `<script>var a = 5-{{.}}; var b = [{{.}}];</script>`
			plain := renderPlain(t, src, v)
			got, err := renderRewritten(src, v)
			if err != nil {
				t.Fatal(err)
			}
			if got != plain {
				t.Errorf("rewritten script differs from html/template's\nplain     %q\nrewritten %q", plain, got)
			}
		})
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
