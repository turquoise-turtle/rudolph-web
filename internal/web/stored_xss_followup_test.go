package web

import (
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/textsafe"
)

// Second review of the stored-XSS fixes (2026-11-19).

// timedRender renders page through render (and so through its character-reference pass) up to three times, and returns
// the fastest run and its body, as the fastest run is the one least disturbed by other work on the machine. It stops at
// the first run within limit, or above a second, which no retry brings down to a linear time.
func timedRender(t *testing.T, page string, limit time.Duration) (time.Duration, string) {
	t.Helper()
	best := time.Duration(-1)
	var body string
	for range 3 {
		start := time.Now()
		rec := renderPage(t, `{{.Page}}`, template.HTML(page))
		if d := time.Since(start); best < 0 || d < best {
			best = d
		}
		body = rec.Body.String()
		if best <= limit || best > time.Second {
			break
		}
	}
	return best, body
}

// Each comment used to search for "--!>" to the end of the page, so a page of many short comments took time quadratic
// in its length: 20,000 of them (200 KB, which a long list of rows with a comment each reaches) took about 3 s. The
// page is compared with one of the same length made of short elements, so a slow or busy machine slows both alike.
func TestCharRefs_ManyCommentsTakeLinearTime(t *testing.T) {
	const n = 20000
	tail := "<p>a" + rlo + "b</p>"
	for name, unit := range map[string][2]string{
		"closed with -->":  {"<!-- x -->", "<b>x</b>  "},
		"closed with --!>": {"<!-- x --!>", "<b>x</b>   "},
	} {
		t.Run(name, func(t *testing.T) {
			if len(unit[0]) != len(unit[1]) {
				t.Fatalf("baseline unit %q is not as long as %q", unit[1], unit[0])
			}
			base, baseBody := timedRender(t, strings.Repeat(unit[1], n)+tail, 0)
			limit := 20*base + 100*time.Millisecond
			took, body := timedRender(t, strings.Repeat(unit[0], n)+tail, limit)
			for what, b := range map[string]string{"comments": body, "baseline": baseBody} {
				if strings.Contains(b, rlo) || !strings.HasSuffix(b, "<p>a&#x202e;b</p>") {
					t.Errorf("%s page: bidi override after the comments not written as a reference: ...%q", what, b[max(0, len(b)-60):])
				}
			}
			if took > limit {
				t.Errorf("%d comments took %v, against %v for a page of the same length without them", n, took, base)
			}
		})
	}
}

// scriptTag is a string type with its own JSON form, which html/template writes into a script in place of the string.
type scriptTag string

func (s scriptTag) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"tag": string(s)})
}

// scriptErr is an error that is also a struct: html/template prints its Error text, but writes it into a script as the
// JSON of its exported fields, as an error is not a fmt.Stringer.
type scriptErr struct {
	Code int
	Msg  string
}

func (e scriptErr) Error() string { return fmt.Sprintf("error %d: %s", e.Code, e.Msg) }

func rawJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

var scriptValueAt = regexp.MustCompile(`(?s)^<script>var a = (.*);</script>$`)

// decodedInvisible JSON-decodes the value a script was given and reports each of its strings (object keys included)
// holding a character show escapes: the browser's script would hand that string on with the character live.
func decodedInvisible(t *testing.T, script string) []string {
	t.Helper()
	m := scriptValueAt.FindStringSubmatch(script)
	if m == nil {
		t.Fatalf("not a single-value script: %q", script)
	}
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &v); err != nil {
		t.Fatalf("script value %q is not JSON: %v", m[1], err)
	}
	var bad []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			if strings.IndexFunc(v, textsafe.Invisible) >= 0 {
				bad = append(bad, v)
			}
		case []any:
			for _, e := range v {
				walk(e)
			}
		case map[string]any:
			for k, e := range v {
				walk(k)
				walk(e)
			}
		}
	}
	walk(v)
	return bad
}

// html/template writes a json.Marshaler into a script as its own JSON, never as its String text, and an error struct as
// the JSON of its fields. show must leave those exactly as html/template writes them; it made a quoted string of the
// text instead, so a script given a time read "2026-11-19 01:02:03 +0000 UTC" rather than RFC 3339.
//
// Three rows hold a bidi override that html/template itself writes raw into the script (its JSON escapes only U+2028
// and U+2029): the json.RawMessage, the string type and the error struct with an invisible character. For those the
// script must get, in place of that string, the string shown escaped, which is what html/template writes for the same
// value with textsafe.String applied to the string (its "twin"). Outside a script each value is printed as
// html/template prints it, with the characters in that text shown escaped.
func TestShow_MarshalersInScriptsAsHTMLTemplateWritesThem(t *testing.T) {
	when := time.Date(2026, 11, 19, 1, 2, 3, 0, time.UTC)
	shownRLO := textsafe.String("a" + rlo + "b")
	cases := []struct {
		name     string
		v, twin  any
		plainRaw bool // html/template writes a raw invisible character into the script for v
	}{
		{name: "time.Time", v: when},
		{name: "*time.Time", v: &when},
		{name: "json.RawMessage", v: json.RawMessage(`{"n": "x", "k": [1, 2]}`)},
		{name: "json.RawMessage with bidi", v: json.RawMessage(`{"n":` + rawJSONString("a"+rlo+"b") + `}`),
			twin: json.RawMessage(`{"n":` + rawJSONString(shownRLO) + `}`), plainRaw: true},
		{name: "string type with MarshalJSON", v: scriptTag("x")},
		{name: "string type with MarshalJSON and bidi", v: scriptTag("a" + rlo + "b"), twin: scriptTag(shownRLO), plainRaw: true},
		{name: "error struct", v: scriptErr{Code: 1, Msg: "x"}},
		{name: "*error struct", v: &scriptErr{Code: 1, Msg: "x"}},
		{name: "error struct with bidi", v: scriptErr{Code: 1, Msg: "a" + rlo + "b"}, twin: scriptErr{Code: 1, Msg: shownRLO},
			plainRaw: true},
	}
	const script = `<script>var a = {{.}};</script>`
	const text = `<p>{{.}}</p>`
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := c.v
			if c.twin != nil {
				want = c.twin
			}
			plain := renderPlain(t, script, c.v)
			if hasRawInvisible(plain) != c.plainRaw {
				t.Fatalf("html/template writes a raw invisible character: %v, the table says %v (%q)", !c.plainRaw, c.plainRaw, plain)
			}
			got, err := renderRewritten(script, c.v)
			if err != nil {
				t.Fatal(err)
			}
			if w := renderPlain(t, script, want); got != w {
				t.Errorf("script\ngot  %q\nwant %q", got, w)
			}
			if hasRawInvisible(got) {
				t.Errorf("raw invisible character written into the script: %q", got)
			}
			if bad := decodedInvisible(t, got); len(bad) > 0 {
				t.Errorf("script strings hold invisible characters once decoded: %+q", bad)
			}

			got, err = renderRewritten(text, c.v)
			if err != nil {
				t.Fatal(err)
			}
			plainText := strings.TrimSuffix(strings.TrimPrefix(renderPlain(t, text, c.v), "<p>"), "</p>")
			wantText := renderPlain(t, text, textsafe.String(html.UnescapeString(plainText)))
			if got != wantText {
				t.Errorf("text\ngot  %q\nwant %q", got, wantText)
			}
		})
	}
}

// Number and boolean types with their own JSON form. show passes plain numbers and booleans on untouched, as they
// cannot hold an invisible character, but html/template writes these with their MarshalJSON, which can.
type (
	scriptCount int
	scriptFlag  bool
	scriptRatio float64
	scriptShape int
)

func (c scriptCount) MarshalJSON() ([]byte, error) {
	return json.Marshal(fmt.Sprintf("n=%d%s", int(c), rlo))
}

func (f scriptFlag) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{fmt.Sprint(bool(f)) + rlo: "on" + rlo})
}

func (r scriptRatio) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{fmt.Sprint(float64(r)), zws})
}

// scriptShape's JSON holds nothing invisible, so it must be written exactly as html/template writes it.
func (s scriptShape) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]int{"sides": int(s)})
}

func TestShow_NumberAndBooleanMarshalersInScripts(t *testing.T) {
	const script = `<script>var a = {{.}};</script>`
	for name, v := range map[string]any{
		"int type":   scriptCount(3),
		"bool type":  scriptFlag(true),
		"float type": scriptRatio(0.5),
		"pointer":    new(scriptCount),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := renderRewritten(script, v)
			if err != nil {
				t.Fatal(err)
			}
			if hasRawInvisible(got) {
				t.Errorf("raw invisible character written into the script: %q", got)
			}
			if bad := decodedInvisible(t, got); len(bad) > 0 {
				t.Errorf("script strings hold invisible characters once decoded: %+q", bad)
			}
		})
	}
	t.Run("no invisible character", func(t *testing.T) {
		got, err := renderRewritten(script, scriptShape(4))
		if err != nil {
			t.Fatal(err)
		}
		if plain := renderPlain(t, script, scriptShape(4)); got != plain {
			t.Errorf("script\ngot  %q\nwant %q", got, plain)
		}
	})
	// The JSON string the marshaler wrote must reach the script shown escaped, not dropped or replaced.
	t.Run("string shown escaped", func(t *testing.T) {
		got, err := renderRewritten(script, scriptCount(3))
		if err != nil {
			t.Fatal(err)
		}
		if want := `<script>var a = ` + rawJSONString(textsafe.String("n=3"+rlo)) + `;</script>`; got != want {
			t.Errorf("script\ngot  %q\nwant %q", got, want)
		}
	})
}

// Besides script and style, browsers read the contents of these elements as raw text (xmp, iframe, noembed,
// noframes, plaintext, and noscript where scripting is on) or as foreign markup in which script and style are ordinary
// elements whose contents decode character references (svg, math). render's pass cannot tell what a browser does with
// their contents, so the templates must have none of them.
func TestTemplates_NoRawTextOrForeignElements(t *testing.T) {
	element := regexp.MustCompile(`(?i)<(xmp|iframe|noscript|noembed|noframes|plaintext|svg|math)[\s/>]`)
	for _, s := range []string{"<xmp>", "<IFRAME src=x>", "<noscript>", "<noembed>", "<noframes>", "<plaintext>", "<svg\n", "<math/>"} {
		if !element.MatchString(s) {
			t.Fatalf("guard misses %q", s)
		}
	}
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 10 {
		t.Fatalf("found only %d templates", len(names))
	}
	for _, name := range names {
		b, err := fs.ReadFile(templateFS, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range element.FindAllSubmatch(b, -1) {
			t.Errorf("%s has a <%s element", name, m[1])
		}
	}
}
