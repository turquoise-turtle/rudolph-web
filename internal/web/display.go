package web

import (
	"fmt"
	"html/template"
	"reflect"
	"text/template/parse"

	"github.com/turquoise-turtle/rudolph-web/internal/textsafe"
)

// Text from Macs can carry control, bidi and zero-width characters that html/template writes as they are: a file named
// "invoice<U+202E>fdp.exe" reads as "invoiceexe.pdf", and a host name can hide or reorder what it says (stored-XSS
// review, 2026-11-18). Rather than relying on every template remembering a function on every value, parseTemplates
// rewrites each printing action to end in show, which writes those characters as visible escapes (textsafe.String),
// so a template added later is covered without anyone thinking of it.
//
// The rewrite cannot see the HTML context, so the few actions whose value must keep its exact bytes opt out
// explicitly with verbatim: form values and textareas that are posted back (filters, group names, rule identifiers,
// configuration values and descriptions an admin edits) and query values html/template percent-encodes anyway. A
// verbatim action that is left out shows escapes in a form, which is wrong but harmless; the reverse cannot happen.
// lines is show for text displayed with its line breaks.
const (
	showFunc     = "show"
	verbatimFunc = "verbatim"
	linesFunc    = "lines"
)

var displayFuncs = template.FuncMap{
	showFunc:     func(v any) any { return display(v, textsafe.String) },
	linesFunc:    func(v any) any { return display(v, textsafe.Lines) },
	verbatimFunc: func(v any) any { return v },
}

// display escapes the text of v as html/template would print it. Typed template content (the layout's script) is
// already trusted markup, and nil is left for html/template to print as before.
func display(v any, escape func(string) string) any {
	switch v := v.(type) {
	case nil, template.HTML, template.HTMLAttr, template.JS, template.JSStr, template.CSS, template.URL, template.Srcset:
		return v
	case string:
		return escape(v)
	}
	return escape(fmt.Sprint(indirectToStringer(v)))
}

var (
	errorType       = reflect.TypeFor[error]()
	fmtStringerType = reflect.TypeFor[fmt.Stringer]()
)

// indirectToStringer dereferences pointers until a Stringer, an error or a non-pointer, as html/template does before
// printing a value, so that show prints what html/template would have.
func indirectToStringer(a any) any {
	v := reflect.ValueOf(a)
	for !v.Type().Implements(fmtStringerType) && !v.Type().Implements(errorType) && v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	return v.Interface()
}

// escapeOutput makes every printing action of t's templates end in show, unless it already ends in show, verbatim or
// lines. It must run before the templates are first executed, when html/template adds its own escapers after ours.
func escapeOutput(t *template.Template) {
	for _, tt := range t.Templates() {
		if tt.Tree != nil && tt.Tree.Root != nil {
			escapeNode(tt.Tree.Root)
		}
	}
}

func escapeNode(n parse.Node) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			escapeNode(c)
		}
	case *parse.ActionNode:
		// An action declaring or assigning variables prints nothing.
		if len(n.Pipe.Decl) > 0 || endsInDisplayFunc(n.Pipe) {
			return
		}
		n.Pipe.Cmds = append(n.Pipe.Cmds, &parse.CommandNode{
			NodeType: parse.NodeCommand,
			Pos:      n.Pos,
			Args:     []parse.Node{parse.NewIdentifier(showFunc).SetPos(n.Pos)},
		})
	case *parse.IfNode:
		escapeNode(n.List)
		escapeNode(n.ElseList)
	case *parse.RangeNode:
		escapeNode(n.List)
		escapeNode(n.ElseList)
	case *parse.WithNode:
		escapeNode(n.List)
		escapeNode(n.ElseList)
	}
}

func endsInDisplayFunc(p *parse.PipeNode) bool {
	if len(p.Cmds) == 0 {
		return false
	}
	last := p.Cmds[len(p.Cmds)-1]
	if len(last.Args) == 0 {
		return false
	}
	id, ok := last.Args[0].(*parse.IdentifierNode)
	return ok && (id.Ident == showFunc || id.Ident == verbatimFunc || id.Ident == linesFunc)
}
