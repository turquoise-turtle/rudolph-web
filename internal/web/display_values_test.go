package web

import (
	"html/template"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/textsafe"
)

// Review of the stored-XSS hardening (2026-11-19).

type scriptRecord struct {
	Name string
	N    int
}

// html/template writes a slice, map or struct into a script as JSON. show must leave such a value as html/template
// writes it, not turn it into the quoted text of fmt.Sprint.
func TestShow_NonScalarValuesInScripts(t *testing.T) {
	src := `<script>var a = {{.}};</script>`
	for name, v := range map[string]any{
		"slice":  []string{"x", "y"},
		"map":    map[string]int{"x": 1},
		"struct": scriptRecord{Name: "x", N: 1},
		"ptr":    &scriptRecord{Name: "x", N: 1},
	} {
		t.Run(name, func(t *testing.T) {
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
	// Strings inside such a value must still not reach the script with a raw bidi override.
	rlo := string(rune(0x202e))
	for name, v := range map[string]any{
		"slice":  []string{"a" + rlo + "b"},
		"map":    map[string]string{"k" + rlo: "v" + rlo},
		"struct": scriptRecord{Name: "a" + rlo + "b"},
	} {
		t.Run(name+" with bidi", func(t *testing.T) {
			got, err := renderRewritten(src, v)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(got, rlo) {
				t.Errorf("bidi override written raw into the script: %q", got)
			}
		})
	}
}

// html/template escapes a called template in the context of each call, so one template cannot keep a URL's exact bytes
// in one call and be shown escaped in another: the rewrite must refuse it when the templates are parsed.
func TestEscapeOutput_RefusesTemplateCalledInAndOutsideURL(t *testing.T) {
	for name, src := range map[string]string{
		"href then text": `{{define "v"}}{{.}}{{end}}<a href="/x/{{template "v" .}}">a</a><p>{{template "v" .}}</p>`,
		"text then src":  `{{define "v"}}{{.}}{{end}}<p>{{template "v" .}}</p><img src="/i/{{template "v" .}}">`,
		"nested":         `{{define "v"}}{{.}}{{end}}{{define "w"}}{{template "v" .}}{{end}}<a href="/{{template "w" .}}">a</a><p>{{template "v" .}}</p>`,
	} {
		t.Run(name, func(t *testing.T) {
			tmpl, err := template.New("x").Funcs(funcs).Funcs(displayFuncs).Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			if err := runEscapeOutput(tmpl); err == nil {
				t.Errorf("template called inside and outside a URL attribute accepted")
			}
		})
	}
	// The same template called twice in the same kind of place is fine.
	tmpl, err := template.New("x").Funcs(funcs).Funcs(displayFuncs).Parse(
		`{{define "v"}}{{.}}{{end}}<p>{{template "v" .}}</p><p>{{template "v" .}}</p>`)
	if err != nil {
		t.Fatal(err)
	}
	if err := runEscapeOutput(tmpl); err != nil {
		t.Errorf("template called twice in text refused: %v", err)
	}
}

type treeNode struct {
	Name string
	Kids []treeNode
}

func escapedTree(n treeNode) treeNode {
	out := treeNode{Name: textsafe.String(n.Name)}
	for _, k := range n.Kids {
		out.Kids = append(out.Kids, escapedTree(k))
	}
	return out
}

// A recursive template is called with its own body still being probed; its values must be shown escaped at every
// depth in text, and keep their exact bytes at every depth in a URL.
func TestShow_RecursiveTemplates(t *testing.T) {
	rlo := string(rune(0x202e))
	tree := treeNode{Name: "root" + rlo, Kids: []treeNode{
		{Name: "a" + rlo, Kids: []treeNode{{Name: "a1" + rlo}, {Name: "a2" + rlo, Kids: []treeNode{{Name: "deep" + rlo}}}}},
		{Name: "b" + rlo},
	}}
	t.Run("text", func(t *testing.T) {
		src := `{{define "t"}}<li>{{.Name}}{{if .Kids}}<ul>{{range .Kids}}{{template "t" .}}{{end}}</ul>{{end}}</li>{{end}}<ul>{{template "t" .}}</ul>`
		got, err := renderRewritten(src, tree)
		if err != nil {
			t.Fatal(err)
		}
		if want := renderPlain(t, src, escapedTree(tree)); got != want {
			t.Errorf("recursive template\ngot  %q\nwant %q", got, want)
		}
	})
	t.Run("url", func(t *testing.T) {
		src := `{{define "p"}}{{.Name}}{{range .Kids}}/{{template "p" .}}{{end}}{{end}}<a href="/x/{{template "p" .}}">x</a>`
		got, err := renderRewritten(src, tree)
		if err != nil {
			t.Fatal(err)
		}
		if want := renderPlain(t, src, tree); got != want {
			t.Errorf("recursive template in a URL\ngot  %q\nwant %q", got, want)
		}
	})
}
