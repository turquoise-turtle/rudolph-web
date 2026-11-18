package web

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"reflect"
	"regexp"
	"slices"
	"text/template/parse"
	"unicode/utf8"

	"github.com/turquoise-turtle/rudolph-web/internal/textsafe"
)

// Text from Macs can carry control, bidi and zero-width characters that html/template writes as they are: a file named
// "invoice<U+202E>fdp.exe" reads as "invoiceexe.pdf", and a host name can hide or reorder what it says (stored-XSS
// review, 2026-11-18). Rather than relying on every template remembering a function on every value, parseTemplates
// rewrites each printing action to end in show, which writes those characters as visible escapes (textsafe.String),
// so a template added later is covered without anyone thinking of it.
//
// Two kinds of value must keep their exact bytes instead:
//   - Values html/template percent-encodes: URL-valued attributes (href, src, data-href, xlink:href, an unquoted
//     href, a style's url() and the like) and urlquery. Percent-encoding makes invisible characters visible as
//     %E2%80%AE anyway, and a link built from an escaped value would no longer find what it names. These end in
//     asURL, which leaves the value to html/template.
//   - Form values and textareas that are posted back (filters, group names, rule identifiers, configuration values
//     and descriptions an admin edits). The template opts out explicitly with verbatim; one that is left out shows
//     escapes in a form, which is wrong but harmless, and a test keeps verbatim to those places. Such a value still
//     reaches the page without a raw invisible character: render writes each one as a character reference
//     (invisibleAsCharRefs), which the browser reads back as the same character.
//
// Which actions html/template percent-encodes is not guessed from the template text: urlActions runs html/template's
// own escaper over a throwaway copy of the templates and reads the escapers it puts in each action. A pattern over the
// text cannot follow html/template's attribute typing (data- and namespace prefixes, on* handlers, srcdoc and srclang,
// which contain src without being URLs), quoting, CSS inside a style attribute, href= in prose or inside another
// attribute's value, or a {{template}} call placed inside an attribute, so it disagreed with html/template in exactly
// the places a link breaks or a character is written raw (review, 2026-11-19).
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

var displayFuncNames = map[string]bool{showFunc: true, verbatimFunc: true, linesFunc: true, asURLFunc: true}

// predefinedEscapers escape their argument for one context: html/template's html and urlquery, which it allows only at
// the end of a pipeline, and text/template's js. The display function goes before them, or round their arguments when
// one is called directly, so they escape the visible escapes rather than the characters.
var predefinedEscapers = map[string]bool{"html": true, "urlquery": true, "js": true}

// percentEncoders are the escapers html/template puts in a pipeline whose value it percent-encodes: every non-ASCII and
// control byte then shows as %XX, so the value can keep its exact bytes.
var percentEncoders = map[string]bool{
	"_html_template_urlescaper":    true,
	"_html_template_urlnormalizer": true,
	"_html_template_srcsetescaper": true,
	"urlquery":                     true,
}

// display escapes the text of v as html/template would print it. Typed template content (the layout's script) is
// already trusted markup, and nil is left for html/template to print as before. Plain numbers and booleans cannot hold
// such characters, so they are passed on as they are: html/template then writes them exactly as it would have, in a
// script as a number or boolean with its own padding.
func display(v any, escape func(string) string) any {
	switch v := v.(type) {
	case nil, template.HTML, template.HTMLAttr, template.JS, template.JSStr, template.CSS, template.URL, template.Srcset:
		return v
	case string:
		return escape(v)
	}
	p := indirectToStringer(v)
	if plainScalar(p) {
		return v
	}
	return escape(fmt.Sprint(p))
}

// plainScalar reports whether v is a number or boolean without its own String or Error method, whose text can be
// anything.
func plainScalar(v any) bool {
	if _, ok := v.(fmt.Stringer); ok {
		return false
	}
	if _, ok := v.(error); ok {
		return false
	}
	switch reflect.ValueOf(v).Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8,
		reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr, reflect.Float32, reflect.Float64, reflect.Complex64,
		reflect.Complex128:
		return true
	}
	return false
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

// urlAttrOpen matches template text that ends inside a quoted or unquoted URL-valued attribute. The rewrite does not
// use it, as html/template's own escaper decides; the coverage test checks each asURL in the templates against it, so
// an action left to html/template where the text shows no URL is noticed.
var urlAttrOpen = regexp.MustCompile(`(?is)\s(?:data-|[a-z]+:)?(?:action|archive|background|cite|classid|codebase|data|formaction|href|icon|longdesc|manifest|poster|profile|src|usemap|xmlns|[a-z-]*(?:src|uri|url)[a-z-]*)\s*=\s*(?:"[^"]*|'[^']*|[^\s"'<>=` + "`" + `]*)$`)

// escapeOutput makes every printing action of t's templates end in a display function, unless it already does. t is
// the template the set is executed from (a page's layout.html), and escapeOutput must run before the set is first
// executed, when html/template adds its own escapers after ours.
func escapeOutput(t *template.Template) error {
	urls, err := urlActions(t)
	if err != nil {
		return err
	}
	for _, tt := range t.Templates() {
		if tt.Tree != nil {
			rewriteList(tt.Tree.Root, urls)
		}
	}
	return nil
}

// errProbeDone stops the probe's execution at its first write: only html/template's escaping of it is wanted.
var errProbeDone = errors.New("probe done")

type stopWriter struct{}

func (stopWriter) Write([]byte) (int, error) { return 0, errProbeDone }

// urlActions reports which printing actions of t's set html/template percent-encodes when t is executed. It escapes a
// clone of the set, so t itself stays unescaped for the rewrite, with every {{template}} call reached from t replaced
// by a copy of the called template's body: html/template escapes a called template in the context of the call, but
// keeps the copies it makes for other contexts to itself. An action that is percent-encoded in one call and not in
// another cannot be given one display function, and is refused.
//
// Templates t never calls are never executed in this set, and get show.
func urlActions(t *template.Template) (map[*parse.ActionNode]bool, error) {
	if t.Tree == nil {
		return nil, nil
	}
	probe, err := t.Clone()
	if err != nil {
		return nil, err
	}
	c := probeCopier{set: t, origin: map[*parse.ActionNode]*parse.ActionNode{}}
	entry := probe.Lookup(t.Name())
	entry.Tree.Root = c.list(t.Tree.Root)
	// The probe runs without data, which fails at the first value it needs or at the first write. Either is after
	// html/template has escaped it, which is all that is wanted; an escaping error is the template's own.
	var escErr *template.Error
	if err := entry.Execute(stopWriter{}, nil); errors.As(err, &escErr) {
		return nil, err
	}
	urls := map[*parse.ActionNode]bool{}
	for probed, orig := range c.origin {
		u := slices.ContainsFunc(probed.Pipe.Cmds, func(c *parse.CommandNode) bool { return percentEncoders[commandIdent(c)] })
		if was, seen := urls[orig]; seen && was != u {
			return nil, fmt.Errorf("%s: %s is percent-encoded where one {{template}} call prints it and not where another does; give each call its own template", t.Name(), orig)
		}
		urls[orig] = u
	}
	return urls, nil
}

// probeCopier copies a template tree for urlActions, recording which original each copied action came from.
type probeCopier struct {
	set     *template.Template
	origin  map[*parse.ActionNode]*parse.ActionNode
	calling []string
}

func (c *probeCopier) list(l *parse.ListNode) *parse.ListNode {
	if l == nil {
		return nil
	}
	out := l.CopyList()
	for i, n := range l.Nodes {
		out.Nodes[i] = c.node(n)
	}
	return out
}

func (c *probeCopier) node(n parse.Node) parse.Node {
	switch n := n.(type) {
	case *parse.ActionNode:
		cp := n.Copy().(*parse.ActionNode)
		c.origin[cp] = n
		return cp
	case *parse.IfNode:
		cp := n.Copy().(*parse.IfNode)
		cp.List, cp.ElseList = c.list(n.List), c.list(n.ElseList)
		return cp
	case *parse.RangeNode:
		cp := n.Copy().(*parse.RangeNode)
		cp.List, cp.ElseList = c.list(n.List), c.list(n.ElseList)
		return cp
	case *parse.WithNode:
		cp := n.Copy().(*parse.WithNode)
		cp.List, cp.ElseList = c.list(n.List), c.list(n.ElseList)
		return cp
	case *parse.TemplateNode:
		// A recursive call is left as a call: its actions are not followed, and get show. An undefined template is left
		// for html/template to report.
		called := c.set.Lookup(n.Name)
		if called == nil || called.Tree == nil || slices.Contains(c.calling, n.Name) {
			return n.Copy()
		}
		c.calling = append(c.calling, n.Name)
		defer func() { c.calling = c.calling[:len(c.calling)-1] }()
		return c.list(called.Tree.Root)
	}
	return n.Copy()
}

func rewriteList(l *parse.ListNode, urls map[*parse.ActionNode]bool) {
	if l == nil {
		return
	}
	for _, n := range l.Nodes {
		switch n := n.(type) {
		case *parse.ActionNode:
			rewriteAction(n, urls[n])
		case *parse.IfNode:
			rewriteList(n.List, urls)
			rewriteList(n.ElseList, urls)
		case *parse.RangeNode:
			rewriteList(n.List, urls)
			rewriteList(n.ElseList, urls)
		case *parse.WithNode:
			rewriteList(n.List, urls)
			rewriteList(n.ElseList, urls)
		}
	}
}

func rewriteAction(n *parse.ActionNode, url bool) {
	// An action declaring or assigning variables prints nothing.
	if len(n.Pipe.Decl) > 0 || endsInDisplayFunc(n.Pipe) {
		return
	}
	fn := showFunc
	if url {
		fn = asURLFunc
	}
	cmds := n.Pipe.Cmds
	k := trailingEscapers(cmds)
	// html/template allows its predefined escapers only at the end of a pipeline, so the display function goes round
	// the arguments of one called directly ({{html .X}}) and just before one the value is piped into.
	for _, cmd := range cmds[k:] {
		for i, arg := range cmd.Args[1:] {
			cmd.Args[1+i] = wrapIn(fn, arg)
		}
	}
	if k > 0 {
		n.Pipe.Cmds = slices.Insert(cmds, k, identCmd(fn, n.Pos))
	}
}

// trailingEscapers returns the index of the first of the predefined escapers that end cmds, or len(cmds) if none do.
func trailingEscapers(cmds []*parse.CommandNode) int {
	k := len(cmds)
	for k > 0 && predefinedEscapers[commandIdent(cmds[k-1])] {
		k--
	}
	return k
}

func identCmd(fn string, pos parse.Pos) *parse.CommandNode {
	return &parse.CommandNode{NodeType: parse.NodeCommand, Pos: pos, Args: []parse.Node{parse.NewIdentifier(fn).SetPos(pos)}}
}

// wrapIn returns the argument (fn arg).
func wrapIn(fn string, arg parse.Node) parse.Node {
	cmd := identCmd(fn, arg.Position())
	cmd.Args = append(cmd.Args, arg)
	return &parse.PipeNode{NodeType: parse.NodePipe, Pos: arg.Position(), Cmds: []*parse.CommandNode{cmd}}
}

// endsInDisplayFunc reports whether p's last command, or the one before the predefined escapers that end it, is a
// display function, or p is a predefined escaper called directly whose every argument goes through one.
func endsInDisplayFunc(p *parse.PipeNode) bool {
	cmds := p.Cmds
	k := trailingEscapers(cmds)
	if k > 0 {
		return displayFuncNames[commandIdent(cmds[k-1])]
	}
	if len(cmds) == 0 || len(cmds[0].Args) < 2 {
		return false
	}
	for _, arg := range cmds[0].Args[1:] {
		pipe, ok := arg.(*parse.PipeNode)
		if !ok || len(pipe.Cmds) != 1 || !displayFuncNames[commandIdent(pipe.Cmds[0])] {
			return false
		}
	}
	return true
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

// invisibleAsCharRefs returns page with each invisible character (textsafe.Invisible) written as a numeric character
// reference, which the browser reads back as the same character in text, attribute values and textareas. show has
// already escaped every displayed value, so what is left is posted form values (verbatim), whose bytes must survive,
// and a raw character no longer reaches the page whatever printed it.
//
// Scripts and style sheets are left alone, as character references are not decoded there. Line breaks and tabs are
// ordinary page text. Most C1 controls stay as they are: HTML reads a reference to most of U+0080-U+009F as a
// Windows-1252 character instead (&#x85; is an ellipsis), which would change a posted value. The five that Windows-1252
// leaves undefined are read back as themselves, so they are written as references too.
func invisibleAsCharRefs(page []byte) []byte {
	if bytes.IndexFunc(page, needsCharRef) < 0 {
		return page
	}
	var b bytes.Buffer
	b.Grow(len(page) + 64)
	for i := 0; i < len(page); {
		if end := rawTextEnd(page, i); end > i {
			b.Write(page[i:end])
			i = end
			continue
		}
		r, size := utf8.DecodeRune(page[i:])
		if needsCharRef(r) {
			fmt.Fprintf(&b, "&#x%x;", r)
		} else {
			b.Write(page[i : i+size])
		}
		i += size
	}
	return b.Bytes()
}

func needsCharRef(r rune) bool {
	switch {
	case r == 0x81, r == 0x8d, r == 0x8f, r == 0x90, r == 0x9d:
		return true
	case r == '\n', r == '\r', r == '\t', r >= 0x80 && r <= 0x9f:
		return false
	}
	return textsafe.Invisible(r)
}

var rawTextElements = [][]byte{[]byte("script"), []byte("style")}

// rawTextEnd returns where the content of the script or style element whose start tag begins at page[i] ends (at its
// end tag, or the end of the page), or i if none begins there.
func rawTextEnd(page []byte, i int) int {
	if page[i] != '<' {
		return i
	}
	for _, name := range rawTextElements {
		rest := page[i+1:]
		if len(rest) <= len(name) || !bytes.EqualFold(rest[:len(name)], name) {
			continue
		}
		if c := rest[len(name)]; c != '>' && c != '/' && c != ' ' && c != '\t' && c != '\n' && c != '\r' && c != '\f' {
			continue
		}
		body := i + 1 + len(name)
		closing := append([]byte("</"), name...)
		for j := body; j+len(closing) <= len(page); j++ {
			if page[j] == '<' && bytes.EqualFold(page[j:j+len(closing)], closing) {
				return j
			}
		}
		return len(page)
	}
	return i
}
