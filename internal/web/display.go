package web

import (
	"fmt"
	"html/template"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"text/template/parse"

	"github.com/turquoise-turtle/rudolph-web/internal/textsafe"
)

// Text from Macs can carry control, bidi and zero-width characters that html/template writes as they are: a file named
// "invoice<U+202E>fdp.exe" reads as "invoiceexe.pdf", and a host name can hide or reorder what it says (stored-XSS
// review, 2026-11-18). Rather than relying on every template remembering a function on every value, parseTemplates
// rewrites each printing action to end in show, which writes those characters as visible escapes (textsafe.String),
// so a template added later is covered without anyone thinking of it.
//
// Two kinds of value must keep their exact bytes instead:
//   - Values in URL-valued attributes (href, src, action and the like). html/template percent-encodes every
//     non-ASCII and control byte there, which makes invisible characters visible as %E2%80%AE anyway, and a link
//     built from an escaped value would no longer find what it names. The rewrite sees these from the template text
//     before the action and ends them in asURL, which leaves the value to html/template.
//   - Form values and textareas that are posted back (filters, group names, rule identifiers, configuration values
//     and descriptions an admin edits). The template opts out explicitly with verbatim; one that is left out shows
//     escapes in a form, which is wrong but harmless, and a test keeps verbatim to those places.
//
// lines is show for text displayed with its line breaks.
const (
	showFunc     = "show"
	verbatimFunc = "verbatim"
	linesFunc    = "lines"
	asURLFunc    = "asURL"
)

var displayFuncs = template.FuncMap{
	showFunc:     func(v any) any { return display(v, textsafe.String) },
	linesFunc:    func(v any) any { return display(v, textsafe.Lines) },
	verbatimFunc: func(v any) any { return v },
	asURLFunc:    func(v any) any { return v },
}

// predefinedEscapers are html/template's escapers a template may name, which must stay last in a pipeline.
var predefinedEscapers = map[string]bool{"html": true, "urlquery": true, "js": true}

// display escapes the text of v as html/template would print it. Typed template content (the layout's script) is
// already trusted markup, and nil is left for html/template to print as before. Numbers and booleans cannot hold such
// characters; they are passed on as their JavaScript literal (template.JS), which other contexts print as plain text,
// so a script writes them as a number or boolean, as it did, and without the spaces html/template puts round a
// value it encodes itself.
func display(v any, escape func(string) string) any {
	switch v := v.(type) {
	case nil, template.HTML, template.HTMLAttr, template.JS, template.JSStr, template.CSS, template.URL, template.Srcset:
		return v
	case string:
		return escape(v)
	}
	if lit, ok := jsLiteral(v); ok {
		return lit
	}
	return escape(fmt.Sprint(indirectToStringer(v)))
}

// jsLiteral is the JavaScript literal of a plain number or boolean (not one with its own String or Error method).
func jsLiteral(v any) (template.JS, bool) {
	if _, ok := v.(fmt.Stringer); ok {
		return "", false
	}
	if _, ok := v.(error); ok {
		return "", false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Bool:
		return template.JS(strconv.FormatBool(rv.Bool())), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return template.JS(strconv.FormatInt(rv.Int(), 10)), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return template.JS(strconv.FormatUint(rv.Uint(), 10)), true
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return "", false
		}
		return template.JS(strconv.FormatFloat(f, 'g', -1, rv.Type().Bits())), true
	}
	return "", false
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

// urlAttrOpen matches template text that ends inside a quoted URL-valued attribute: html/template's URL attributes,
// and any whose name contains src, uri or url, as html/template treats them.
var urlAttrOpen = regexp.MustCompile(`(?is)\s(?:action|archive|background|cite|classid|codebase|data|formaction|href|icon|longdesc|manifest|poster|profile|src|usemap|xmlns|[a-z-]*(?:src|uri|url)[a-z-]*)\s*=\s*(?:"[^"]*|'[^']*)$`)

// contextLimit bounds the template text kept to find an action's attribute; no attribute value in the templates comes
// near it.
const contextLimit = 2048

// escapeOutput makes every printing action of t's templates end in a display function, unless it already does. It must
// run before the templates are first executed, when html/template adds its own escapers after ours.
func escapeOutput(t *template.Template) {
	for _, tt := range t.Templates() {
		if tt.Tree != nil && tt.Tree.Root != nil {
			escapeList(tt.Tree.Root, "")
		}
	}
}

// escapeList rewrites the actions of l, given the template text before it, and returns the text up to its end, with
// each action's value standing in as "x". Text inside a branch is followed through its first branch only: enough to
// tell an attribute value, which the templates never open in one branch and close in another.
func escapeList(l *parse.ListNode, before string) string {
	if l == nil {
		return before
	}
	for _, n := range l.Nodes {
		switch n := n.(type) {
		case *parse.TextNode:
			before += string(n.Text)
		case *parse.ActionNode:
			escapeAction(n, before)
			before += "x"
		case *parse.IfNode:
			escapeList(n.ElseList, before)
			before = escapeList(n.List, before)
		case *parse.RangeNode:
			escapeList(n.ElseList, before)
			before = escapeList(n.List, before)
		case *parse.WithNode:
			escapeList(n.ElseList, before)
			before = escapeList(n.List, before)
		}
		if len(before) > contextLimit {
			before = before[len(before)-contextLimit:]
		}
	}
	return before
}

func escapeAction(n *parse.ActionNode, before string) {
	// An action declaring or assigning variables prints nothing.
	if len(n.Pipe.Decl) > 0 || endsInDisplayFunc(n.Pipe) {
		return
	}
	fn := showFunc
	if urlAttrOpen.MatchString(before) {
		fn = asURLFunc
	}
	cmd := &parse.CommandNode{NodeType: parse.NodeCommand, Pos: n.Pos, Args: []parse.Node{parse.NewIdentifier(fn).SetPos(n.Pos)}}
	cmds := n.Pipe.Cmds
	// html/template allows its predefined escapers only at the end of a pipeline, so show goes just before one.
	if last := len(cmds) - 1; last > 0 && predefinedEscapers[commandIdent(cmds[last])] {
		n.Pipe.Cmds = append(cmds[:last:last], cmd, cmds[last])
		return
	}
	n.Pipe.Cmds = append(cmds, cmd)
}

// endsInDisplayFunc reports whether p's last command, or the one before a predefined escaper, is a display function.
func endsInDisplayFunc(p *parse.PipeNode) bool {
	cmds := p.Cmds
	if len(cmds) > 1 && predefinedEscapers[commandIdent(cmds[len(cmds)-1])] {
		cmds = cmds[:len(cmds)-1]
	}
	if len(cmds) == 0 {
		return false
	}
	switch commandIdent(cmds[len(cmds)-1]) {
	case showFunc, verbatimFunc, linesFunc, asURLFunc:
		return true
	}
	return false
}

func commandIdent(c *parse.CommandNode) string {
	if len(c.Args) == 0 {
		return ""
	}
	if id, ok := c.Args[0].(*parse.IdentifierNode); ok {
		return id.Ident
	}
	return ""
}
