package web

import (
	"html/template"
	"regexp"
	"strings"
	"testing"
	"text/template/parse"

	"github.com/turquoise-turtle/rudolph-web/internal/clicmd"
)

// Every value a page prints is shown through show unless it opts out, so a template added later cannot write a
// Mac's bidi or control characters as they are.
func TestEveryPrintedValueGoesThroughADisplayFunc(t *testing.T) {
	pages, err := parseTemplates(cliFuncs(clicmd.New("")))
	if err != nil {
		t.Fatal(err)
	}
	for page, tmpl := range pages {
		for _, tt := range tmpl.Templates() {
			if tt.Tree == nil {
				continue
			}
			walkActions(tt.Tree.Root, func(a *parse.ActionNode, before string) {
				if len(a.Pipe.Decl) > 0 {
					return
				}
				if !endsInDisplayFunc(a.Pipe) {
					t.Errorf("%s/%s: %s does not end in show, lines or verbatim", page, tt.Name(), a)
				}
			})
		}
	}
}

// verbatim is for values that must keep their exact bytes: form values and textareas posted back, and query values
// html/template percent-encodes. Anywhere else it would show a Mac's invisible characters as they are.
func TestVerbatimOnlyInFormValuesAndQueries(t *testing.T) {
	allowed := regexp.MustCompile(`(value="|<textarea[^>]*>|\?[a-z_]+=)$`)
	pages, err := parseTemplates(cliFuncs(clicmd.New("")))
	if err != nil {
		t.Fatal(err)
	}
	for page, tmpl := range pages {
		for _, tt := range tmpl.Templates() {
			if tt.Tree == nil {
				continue
			}
			walkActions(tt.Tree.Root, func(a *parse.ActionNode, before string) {
				cmds := a.Pipe.Cmds
				if id, ok := cmds[len(cmds)-1].Args[0].(*parse.IdentifierNode); ok && id.Ident == verbatimFunc && !allowed.MatchString(before) {
					t.Errorf("%s/%s: %s is verbatim after %q, not in a form value or query", page, tt.Name(), a, before)
				}
			})
		}
	}
}

// walkActions calls f with each action and the text just before it in the same list.
func walkActions(n parse.Node, f func(a *parse.ActionNode, before string)) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		before := ""
		for _, c := range n.Nodes {
			if a, ok := c.(*parse.ActionNode); ok {
				f(a, before)
			}
			walkActions(c, f)
			if tx, ok := c.(*parse.TextNode); ok {
				before = string(tx.Text)
			} else {
				before = ""
			}
		}
	case *parse.IfNode:
		walkActions(n.List, f)
		walkActions(n.ElseList, f)
	case *parse.RangeNode:
		walkActions(n.List, f)
		walkActions(n.ElseList, f)
	case *parse.WithNode:
		walkActions(n.List, f)
		walkActions(n.ElseList, f)
	}
}

func TestEscapeOutput(t *testing.T) {
	tmpl := template.Must(template.New("x").Funcs(displayFuncs).Parse(
		`<title>{{.S}}</title><p title="{{.S}}">{{.S}} {{.N}} {{.P}} {{.Nil}}</p>` +
			`<input value="{{verbatim .S}}"><a href="/x?q={{verbatim .S}}">a</a><pre>{{lines .S}}</pre>{{$v := .S}}{{$v}}`))
	escapeOutput(tmpl)
	escapeOutput(tmpl) // the rewrite runs once per page and must not stack
	n := 7
	var b strings.Builder
	data := struct {
		S   string
		N   int
		P   *int
		Nil any
	}{"a\u202eb\nc\x1b", 3, &n, nil}
	if err := tmpl.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	want := `<title>a\u202eb\nc\x1b</title><p title="a\u202eb\nc\x1b">a\u202eb\nc\x1b 3 7 </p>` +
		"<input value=\"a\u202eb\nc\x1b\"><a href=\"/x?q=a%e2%80%aeb%0ac%1b\">a</a><pre>a\\u202eb\nc\\x1b</pre>" + `a\u202eb\nc\x1b`
	if got := b.String(); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
