package web

import (
	"html/template"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"text/template/parse"

	"github.com/turquoise-turtle/rudolph-web/internal/clicmd"
)

// Every value a page prints goes through a display function, so a template added later cannot write a Mac's bidi or
// control characters as they are. The tree is walked by reflection over every node field, not by the node types the
// rewrite knows, so an action inside a node kind it does not handle is reported.
func TestEveryPrintedValueGoesThroughADisplayFunc(t *testing.T) {
	pages, err := parseTemplates(cliFuncs(clicmd.New("")))
	if err != nil {
		t.Fatal(err)
	}
	actions := 0
	for page, tmpl := range pages {
		for _, tt := range tmpl.Templates() {
			if tt.Tree == nil {
				continue
			}
			walkAll(tt.Tree.Root, new(string), func(a *parse.ActionNode, before string) {
				if len(a.Pipe.Decl) > 0 {
					return
				}
				actions++
				if !endsInDisplayFunc(a.Pipe) {
					t.Errorf("%s/%s: %s does not end in a display function", page, tt.Name(), a)
				}
				if id := commandIdent(a.Pipe.Cmds[len(a.Pipe.Cmds)-1]); id == asURLFunc && !urlAttrOpen.MatchString(before) {
					t.Errorf("%s/%s: %s is left to html/template outside a URL attribute (after %q)", page, tt.Name(), a, tail(before))
				}
			})
		}
	}
	if actions < 100 {
		t.Errorf("found only %d printing actions; the walk is missing some", actions)
	}
}

// verbatim is for values that must keep their exact bytes because they are posted back: the value of an input that is
// not a button, of an option, or a textarea's text. Anywhere else (a button's label, text, a query in plain text) it
// would show a Mac's invisible characters as they are.
func TestVerbatimOnlyInPostedFormValues(t *testing.T) {
	inValue := regexp.MustCompile(`(?is)<(?:input|option)\b[^<>]*\svalue="$`)
	button := regexp.MustCompile(`(?is)<input\b[^<>]*\stype="(?:submit|button|reset|image)"[^<>]*$`)
	inTextarea := regexp.MustCompile(`(?is)<textarea\b[^<>]*>$`)
	pages, err := parseTemplates(cliFuncs(clicmd.New("")))
	if err != nil {
		t.Fatal(err)
	}
	for page, tmpl := range pages {
		for _, tt := range tmpl.Templates() {
			if tt.Tree == nil {
				continue
			}
			walkAll(tt.Tree.Root, new(string), func(a *parse.ActionNode, before string) {
				if commandIdent(a.Pipe.Cmds[len(a.Pipe.Cmds)-1]) != verbatimFunc {
					return
				}
				posted := (inValue.MatchString(before) && !button.MatchString(before)) || inTextarea.MatchString(before)
				if !posted {
					t.Errorf("%s/%s: %s is verbatim after %q, not in a posted form value", page, tt.Name(), a, tail(before))
				}
			})
		}
	}
}

func tail(s string) string { return s[max(0, len(s)-80):] }

// walkAll calls f with every action under n, found through every exported field of every node, and the template text
// before it (each earlier action standing in as "x").
func walkAll(n parse.Node, before *string, f func(a *parse.ActionNode, before string)) {
	walkValue(reflect.ValueOf(n), before, f)
}

func walkValue(v reflect.Value, before *string, f func(a *parse.ActionNode, before string)) {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return
		}
		if v.Kind() == reflect.Pointer {
			switch n := v.Interface().(type) {
			case *parse.TextNode:
				*before += string(n.Text)
				return
			case *parse.ActionNode:
				f(n, *before)
				*before += "x"
				return
			}
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				walkValue(v.Field(i), before, f)
			}
		}
	case reflect.Slice:
		for i := range v.Len() {
			walkValue(v.Index(i), before, f)
		}
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
