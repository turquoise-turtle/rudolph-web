package web

import (
	"bytes"
	"encoding"
	"encoding/json"
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
// script as a number or boolean with its own padding. A value html/template writes into a script as JSON other than
// its printed text (a json.Marshaler such as time.Time, a slice, map or struct, an error, or a type with MarshalText)
// becomes a shownValue, so a script still gets that JSON.
func display(v any, escape func(string) string) any {
	switch v := v.(type) {
	case nil, template.HTML, template.HTMLAttr, template.JS, template.JSStr, template.CSS, template.URL, template.Srcset:
		return v
	case string:
		return escape(v)
	}
	// html/template finds the text and the script form of a value by dereferencing pointers to different methods.
	text := indirectToStringer(v)
	js := indirectToJSONMarshaler(v)
	switch j := js.(type) {
	case json.Marshaler:
		return shownValue{v: js, text: text, escape: escape}
	case fmt.Stringer:
		// html/template writes the String text into a script, which is the printed text unless Error, preferred by
		// fmt, says something else.
		printed := fmt.Sprint(text)
		if s, ok := stringOf(j); ok && s != printed {
			return shownValue{v: s, text: text, escape: escape}
		}
		return escape(printed)
	case encoding.TextMarshaler:
		return shownValue{v: js, text: text, escape: escape}
	}
	if plainScalar(js) {
		return v
	}
	if _, isErr := text.(error); isErr || composite(js) {
		return shownValue{v: js, text: text, escape: escape}
	}
	return escape(fmt.Sprint(text))
}

// stringOf returns s.String(), or false if it panics (a nil pointer), which fmt prints rather than failing.
func stringOf(s fmt.Stringer) (str string, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return s.String(), true
}

// shownValue is a value html/template writes into a script as JSON other than its printed text. html/template writes
// it into a script as JSON (json.Marshaler) and everywhere else as its fmt.Sprint text (fmt.Stringer), so it answers
// both: the text with the characters escaped as for any other value, and exactly html/template's JSON of v (the value
// as html/template's script escaper sees it), except that each JSON string holding such a character raw is that
// string escaped. A script is skipped by render's pass over the page, so nothing else would stop them reaching the
// browser raw: html/template's JSON writes a bidi override as it is.
type shownValue struct {
	v, text any
	escape  func(string) string
}

func (s shownValue) String() string { return s.escape(fmt.Sprint(s.text)) }

func (s shownValue) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(s.v)
	if err != nil || bytes.IndexFunc(b, textsafe.Invisible) < 0 {
		return b, err
	}
	var out bytes.Buffer
	for i := 0; i < len(b); {
		if b[i] != '"' {
			out.WriteByte(b[i])
			i++
			continue
		}
		end := jsonStringEnd(b, i)
		lit := b[i:end]
		if bytes.IndexFunc(lit, textsafe.Invisible) >= 0 {
			var str string
			if err := json.Unmarshal(lit, &str); err != nil {
				return nil, err
			}
			if lit, err = json.Marshal(s.escape(str)); err != nil {
				return nil, err
			}
		}
		out.Write(lit)
		i = end
	}
	return out.Bytes(), nil
}

// jsonStringEnd returns the index just past the JSON string literal that begins at b[i], which json.Marshal wrote.
func jsonStringEnd(b []byte, i int) int {
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(b)
}

// composite reports whether v, as html/template's script escaper sees it, is a value it writes into a script as a JSON
// array or object (or null, for a nil pointer).
func composite(v any) bool {
	switch reflect.ValueOf(v).Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct, reflect.Pointer:
		return true
	}
	return false
}

// plainScalar reports whether v is a number or boolean without its own String or Error method, whose text can be
// anything, or its own JSON or text form (json.Marshaler, encoding.TextMarshaler), which html/template writes into a
// script in place of the number and which can hold anything too.
func plainScalar(v any) bool {
	switch v.(type) {
	case fmt.Stringer, error, json.Marshaler, encoding.TextMarshaler:
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
	errorType         = reflect.TypeFor[error]()
	fmtStringerType   = reflect.TypeFor[fmt.Stringer]()
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
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

// indirectToJSONMarshaler dereferences pointers until a json.Marshaler or a non-pointer, as html/template does before
// writing a value into a script, so that show's JSON is what html/template would have written.
func indirectToJSONMarshaler(a any) any {
	v := reflect.ValueOf(a)
	for !v.Type().Implements(jsonMarshalerType) && v.Kind() == reflect.Pointer && !v.IsNil() {
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
// Scripts and style sheets are left alone, as character references are not decoded there (rawTextSpans). Line breaks
// and tabs are ordinary page text. Most C1 controls stay as they are: HTML reads a reference to most of U+0080-U+009F
// as a Windows-1252 character instead (&#x85; is an ellipsis), which would change a posted value. The five that
// Windows-1252 leaves undefined are read back as themselves, so they are written as references too.
func invisibleAsCharRefs(page []byte) []byte {
	if bytes.IndexFunc(page, needsCharRef) < 0 {
		return page
	}
	var b bytes.Buffer
	b.Grow(len(page) + 64)
	spans := rawTextSpans(page)
	for i := 0; i < len(page); {
		if len(spans) > 0 && i == spans[0][0] {
			b.Write(page[i:spans[0][1]])
			i = spans[0][1]
			spans = spans[1:]
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

// rawTextSpans returns the byte ranges [start, end) of page that are the contents of script and style elements, found
// by following tag boundaries as a browser's tokeniser does. A byte scan for "<script" would also find one in an
// attribute value, a comment or a textarea, and then leave the rest of the page unchecked (review, 2026-11-19).
//
// A script or style that is never closed gets no range: the browser reads the rest of the page as its contents, so
// references written there change only text that is no longer a working script, while leaving it alone would let what
// follows (a posted form value) reach the page with raw invisible characters. Everything else a scan can get wrong
// errs the same way: it may write references inside something the browser treats as raw text, never leave raw
// characters in text it shows. The one exception is a script or style inside SVG or MathML, which is parsed as
// ordinary markup; the templates have no such element (TestTemplates_OnlyTheLayoutScript).
func rawTextSpans(page []byte) [][2]int {
	var spans [][2]int
	for i := 0; i < len(page); {
		lt := bytes.IndexByte(page[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		rest := page[i+1:]
		var end int
		switch {
		case bytes.HasPrefix(rest, []byte("!--")):
			end = commentEnd(page, i+4)
		case len(rest) > 0 && (rest[0] == '!' || rest[0] == '?'), len(rest) > 1 && rest[0] == '/' && !isASCIILetter(rest[1]):
			// A doctype, CDATA section or bogus comment runs to the first '>'.
			end = pastByte(page, i+1, '>')
		case len(rest) > 1 && rest[0] == '/':
			_, end = tagEnd(page, i+2)
		case len(rest) > 0 && isASCIILetter(rest[0]):
			var name []byte
			name, end = tagEnd(page, i+1)
			if end < 0 {
				return spans
			}
			switch {
			case bytes.EqualFold(name, []byte("script")), bytes.EqualFold(name, []byte("style")):
				closing := endTag(page, end, name)
				if closing < 0 {
					return spans
				}
				spans = append(spans, [2]int{end, closing})
				end = closing
			case bytes.EqualFold(name, []byte("textarea")), bytes.EqualFold(name, []byte("title")):
				// Their contents are text, in which references are decoded, but hold no tags.
				end = endTag(page, end, name)
			}
		default:
			end = i + 1
		}
		if end < 0 {
			return spans
		}
		i = end
	}
	return spans
}

// commentEnd returns where the comment whose text begins at page[i] ends (past "-->", "--!>", or the "<!-->" and
// "<!--->" that end one at once), or -1 if it runs to the end of the page.
func commentEnd(page []byte, i int) int {
	rest := page[i:]
	switch {
	case bytes.HasPrefix(rest, []byte(">")):
		return i + 1
	case bytes.HasPrefix(rest, []byte("->")):
		return i + 2
	}
	// Both endings begin with "--", so the first "--" followed by '>' or "!>" is where the comment ends. Searching for
	// each ending to the end of the page instead made a page of many comments take time quadratic in its length.
	for j := 0; ; {
		k := bytes.Index(rest[j:], []byte("--"))
		if k < 0 {
			return -1
		}
		j += k
		switch after := rest[j+2:]; {
		case bytes.HasPrefix(after, []byte(">")):
			return i + j + 3
		case bytes.HasPrefix(after, []byte("!>")):
			return i + j + 4
		}
		j++
	}
}

// pastByte returns the index just past the first c at or after page[i], or -1 if there is none.
func pastByte(page []byte, i int, c byte) int {
	if j := bytes.IndexByte(page[i:], c); j >= 0 {
		return i + j + 1
	}
	return -1
}

// tagEnd reads the tag whose name begins at page[i], and returns its name and the index just past its '>', or -1 if
// the page ends inside it. Attribute values are skipped whole, so a '>' or "<script" inside a quoted one is not markup.
func tagEnd(page []byte, i int) ([]byte, int) {
	j := i
	for j < len(page) && !isHTMLSpace(page[j]) && page[j] != '/' && page[j] != '>' {
		j++
	}
	name := page[i:j]
	for j < len(page) {
		switch c := page[j]; {
		case c == '>':
			return name, j + 1
		case isHTMLSpace(c), c == '/':
			j++
		default:
			// An attribute name; its first character is part of it even when it is '='.
			j++
			for j < len(page) && !isHTMLSpace(page[j]) && page[j] != '/' && page[j] != '>' && page[j] != '=' {
				j++
			}
			k := skipHTMLSpace(page, j)
			if k >= len(page) || page[k] != '=' {
				j = k
				continue
			}
			k = skipHTMLSpace(page, k+1)
			if k < len(page) && (page[k] == '"' || page[k] == '\'') {
				if j = pastByte(page, k+1, page[k]); j < 0 {
					return name, -1
				}
				continue
			}
			for k < len(page) && !isHTMLSpace(page[k]) && page[k] != '>' {
				k++
			}
			j = k
		}
	}
	return name, -1
}

// endTag returns the index of the end tag of the element name whose contents begin at page[i], or -1 if there is none.
// As in a browser, it is "</" and the name in any case, followed by white space, '/' or '>'.
func endTag(page []byte, i int, name []byte) int {
	for j := i; j < len(page); j++ {
		k := j + 2 + len(name)
		if page[j] != '<' || k >= len(page) || page[j+1] != '/' || !bytes.EqualFold(page[j+2:k], name) {
			continue
		}
		if c := page[k]; isHTMLSpace(c) || c == '/' || c == '>' {
			return j
		}
	}
	return -1
}

func skipHTMLSpace(page []byte, i int) int {
	for i < len(page) && isHTMLSpace(page[i]) {
		i++
	}
	return i
}

func isHTMLSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
