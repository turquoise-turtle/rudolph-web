package web

import (
	"html/template"
	"strings"
	"testing"
)

// Review of the parse-time rewrite (2026-11-19). The rewrite guesses from the template text whether an action sits in a
// URL-valued attribute, while html/template decides the same thing from its own attribute typing; where the two
// disagree a link loses its exact bytes, or an invisible character is written raw into a script or a page. These
// tests take html/template itself as the oracle rather than the rewrite's own regular expression, so a guess that
// merely agrees with itself cannot pass.

var (
	rlo = string(rune(0x202e))
	zws = string(rune(0x200b))
	// oracleValue holds a bidi override and a zero-width space between letters, as a Mac's file name might.
	oracleValue = "a" + rlo + "b" + zws + "c"
)

func renderPlain(t *testing.T, src string, data any) string {
	t.Helper()
	tmpl, err := template.New("x").Funcs(funcs).Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		t.Fatalf("plain %s: %v", src, err)
	}
	return b.String()
}

// renderRewritten is render without failing the test, for templates the rewrite may break.
func renderRewritten(src string, data any) (string, error) {
	tmpl, err := template.New("x").Funcs(funcs).Funcs(displayFuncs).Parse(src)
	if err != nil {
		return "", err
	}
	escapeOutput(tmpl)
	var b strings.Builder
	err = tmpl.Execute(&b, data)
	return b.String(), err
}

func hasRawInvisible(s string) bool { return strings.Contains(s, rlo) || strings.Contains(s, zws) }

func TestShow_URLAttributeDetectionMatchesHTMLTemplate(t *testing.T) {
	cases := map[string]string{
		"href":                  `<a href="/f?p={{.}}">x</a>`,
		"src":                   `<img src="/i/{{.}}">`,
		"action":                `<form action="/x/{{.}}"></form>`,
		"formaction":            `<button formaction="/x/{{.}}">x</button>`,
		"srcset":                `<img srcset="/i/{{.}} 2x">`,
		"cite":                  `<blockquote cite="/c/{{.}}">x</blockquote>`,
		"poster":                `<video poster="/p/{{.}}"></video>`,
		"object data":           `<object data="/o/{{.}}"></object>`,
		"unquoted href":         `<a href=/f/{{.}}>x</a>`,
		"single-quoted href":    `<a href='/f?p={{.}}'>x</a>`,
		"mixed-case HREF":       `<a HREF="/f?p={{.}}">x</a>`,
		"data-href":             `<a data-href="/f?p={{.}}">x</a>`,
		"xlink:href":            `<svg><a xlink:href="/f?p={{.}}">x</a></svg>`,
		"style url()":           `<div style="background:url({{.}})">x</div>`,
		"srclang":               `<track srclang="{{.}}">`,
		"srcdoc":                `<iframe srcdoc="{{.}}"></iframe>`,
		"onsrcx handler":        `<a onsrcx="f('{{.}}')">x</a>`,
		"data-onurl handler":    `<a data-onurl="f('{{.}}')">x</a>`,
		"title":                 `<p title="{{.}}">x</p>`,
		"href in prose":         `<p>Set href="{{.}}</p>`,
		"href inside title":     `<p title="x href='{{.}}">x</p>`,
		"meta refresh":          `<meta http-equiv="refresh" content="0;url={{.}}">`,
		"template call in href": `{{define "u"}}{{.}}{{end}}<a href="/f?p={{template "u" .}}">x</a>`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			plain := renderPlain(t, src, oracleValue)
			got, err := renderRewritten(src, oracleValue)
			if err != nil {
				t.Fatalf("rewritten template fails: %v", err)
			}
			if !hasRawInvisible(plain) {
				// html/template percent-encoded the value, so it is a URL: a link must keep the value's exact bytes.
				if got != plain {
					t.Errorf("html/template treats this as a URL, but the rewrite changed the bytes\nplain     %q\nrewritten %q", plain, got)
				}
				return
			}
			if hasRawInvisible(got) {
				t.Errorf("invisible character written raw: %q", got)
			}
			if !strings.Contains(got, `\u202e`) || !strings.Contains(got, `\u200b`) {
				t.Errorf("visible escapes missing: %q", got)
			}
		})
	}
}

// html/template's predefined escapers may be written as function calls rather than at the end of a pipe; they must
// still render, and still not write an invisible character raw.
func TestShow_PredefinedEscapersAsFunctionCalls(t *testing.T) {
	data := struct{ X, Y string }{oracleValue, "d" + rlo}
	for name, src := range map[string]string{
		"html":     `<p>{{html .X}}</p>`,
		"urlquery": `<p>{{urlquery .X .Y}}</p>`,
		"js":       `<script>var s = "{{js .X}}";</script>`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := renderRewritten(src, data)
			if err != nil {
				t.Fatalf("%s: %v", src, err)
			}
			if hasRawInvisible(got) {
				t.Errorf("invisible character written raw: %q", got)
			}
		})
	}
}

// severity is a number that prints as text: its String method decides what is shown, and that text can hold anything.
type severity int

func (severity) String() string { return "lo" + rlo + "w" }

func TestShow_NumberWithStringMethodIsEscaped(t *testing.T) {
	got, err := renderRewritten(`<p>{{.}}</p><script>var s = {{.}};</script>`, severity(2))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, rlo) {
		t.Errorf("bidi override from String written raw: %q", got)
	}
	if !strings.Contains(got, `<p>lo\u202ew</p>`) {
		t.Errorf("visible escape missing: %q", got)
	}
}
