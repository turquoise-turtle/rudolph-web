package web_test

// Usability of the CEL help page's try loop, as a Mac admin uses it:
//   - the answer says which of three things went wrong: the sample could not be read ("Not tried", 400), Santa could
//     not compile the expression ("Santa would not apply this"), or it compiles and failed on this sample ("Santa can
//     compile this, but it failed on this sample execution"), which on a Mac decides nothing rather than being skipped;
//   - a compile error shows the line it names with a caret under the column, since the expression box has no line or
//     column numbers;
//   - an example's "Try it" link carries a sample execution the example blocks, so the first try shows the case the
//     rule exists for and how the sample is filled in;
//   - the links at the top of the page reach every section, the try form among them;
//   - the expression box grows with a long expression after a try;
//   - admins with editing on get links to the existing add-rule forms, pre-filled with the identifier where the
//     add-rule page already takes one. The page itself still writes nothing and has only the GET try form.

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/santacel"
)

func celAnswer(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<p class="cel-answer" data-outcome="[a-z]+">.*?</p>`).FindString(body)
	if m == "" {
		t.Fatalf("no answer on the page:\n%s", body)
	}
	return html.UnescapeString(m)
}

func TestCELUsability_TheAnswerSaysWhatKindOfFailureItIs(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	for name, c := range map[string]struct {
		q       url.Values
		status  int
		outcome string
		want    string
		not     string
	}{
		"allowed":        {url.Values{"expr": {"true"}}, 200, "allow", "Santa would apply", "failed"},
		"blocked":        {url.Values{"expr": {"euid == 0 ? SILENT_BLOCKLIST : ALLOWLIST"}}, 200, "block", "Santa would apply", "failed"},
		"compile error":  {url.Values{"expr": {"args.exists(a,"}}, 200, "error", "Santa would not apply this", "Santa can compile this"},
		"Workshop only":  {url.Values{"expr": {"AUDIT"}}, 200, "error", "Santa would not apply this", "Santa can compile this"},
		"fails on args":  {url.Values{"expr": {"args[5] == 'x'"}, "args": {"ls"}}, 200, "error", "Santa can compile this, but it failed on this sample execution", "Santa would not apply this"},
		"fails on envs":  {url.Values{"expr": {"envs['HOME'] == '/'"}}, 200, "error", "Santa can compile this, but it failed on this sample execution", "Santa would not apply this"},
		"unreadable":     {url.Values{"expr": {"true"}, "euid": {"root"}}, 400, "error", "Not tried", "Santa"},
		"unreadable env": {url.Values{"expr": {"true"}, "envs": {"HOME"}}, 400, "error", "Not tried", "Santa"},
	} {
		code, body := a.viewerGet(t, "/rules/cel?"+c.q.Encode())
		if code != c.status {
			t.Errorf("%s: status %d, want %d", name, code, c.status)
		}
		answer := celAnswer(t, body)
		if !strings.Contains(answer, `data-outcome="`+c.outcome+`"`) || !strings.Contains(answer, c.want) || strings.Contains(answer, c.not) {
			t.Errorf("%s: the answer is %q; want outcome %q, %q and not %q", name, answer, c.outcome, c.want, c.not)
		}
		// What a run-time failure means on a Mac is said where it happens, and only there.
		if failed := strings.Contains(answer, "Santa can compile this"); failed != strings.Contains(body, "An expression that fails while it runs on a Mac decides nothing") {
			t.Errorf("%s: the note about run-time failures on a Mac is shown: %v, want %v", name, !failed, failed)
		}
	}
}

func TestCELUsability_TheCacheNoteSaysWhy(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	for expr, want := range map[string]string{
		"target.team_id == 'EQHXZ8M8AV'": "cached</output>: the expression reads only <code>target</code>",
		"euid == 0":                      "not cached</output>: the expression reads <code>args</code>, <code>envs</code>, <code>euid</code>, <code>cwd</code> or <code>path</code>",
	} {
		_, body := a.viewerGet(t, "/rules/cel?"+url.Values{"expr": {expr}}.Encode())
		mustContain(t, body, want, `href="#caching"`)
	}
}

func TestCELUsability_ACompileErrorPointsAtItsPlace(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	where := func(expr string) []string {
		t.Helper()
		_, body := a.viewerGet(t, "/rules/cel?"+url.Values{"expr": {expr}}.Encode())
		var out []string
		for _, m := range regexp.MustCompile(`(?s)<pre class="cel-where">(.*?)</pre>`).FindAllStringSubmatch(body, -1) {
			out = append(out, html.UnescapeString(m[1]))
		}
		return out
	}
	// The caret stands under the character the message's column names.
	under := func(excerpt string) string {
		line, caret, ok := strings.Cut(excerpt, "\n")
		if !ok || !strings.HasSuffix(caret, "^") || strings.Trim(caret, " ") != "^" {
			t.Fatalf("excerpt %q is not a line and a caret", excerpt)
		}
		r := []rune(line)
		if i := len([]rune(caret)) - 1; i < len(r) {
			return string(r[i:])
		}
		return ""
	}

	// Line 2 of an expression sent as a browser sends a textarea, with CRLF.
	got := where("args.exists(a,\r\n  a in ['x']) ? BLOCKLIST : foo")
	if len(got) != 1 || !strings.HasPrefix(got[0], "  a in ['x']) ? BLOCKLIST : foo\n") || under(got[0]) != "foo" {
		t.Errorf("an undeclared name on line 2: %q; want that line with the caret under foo", got)
	}
	// Each error gets its place.
	got = where("foo == 1 && bar == 2")
	if len(got) != 2 || under(got[0]) != "foo == 1 && bar == 2" || under(got[1]) != "bar == 2" {
		t.Errorf("two undeclared names: %q; want the caret under each", got)
	}
	// An error at the end of the input points just past the last character.
	got = where("euid == ")
	if len(got) != 1 || under(got[0]) != "" || !strings.HasPrefix(got[0], "euid == \n") {
		t.Errorf("an unfinished expression: %q; want the caret after the last character", got)
	}
	// A long line is cut round the place, so the caret stays in view.
	long := "args.exists(a, a in [" + strings.Repeat("'--flag', ", 60) + "'x']) ? BLOCKLIST : nope ? ALLOWLIST : (" + strings.Repeat("true || ", 20) + "false ? ALLOWLIST : BLOCKLIST)"
	got = where(long)
	if len(got) != 1 || !strings.HasPrefix(under(got[0]), "nope ?") || !strings.HasPrefix(got[0], "...") || !strings.Contains(got[0], "...\n") {
		t.Errorf("a long line: %q; want it cut with ... on both sides and the caret under nope", got)
	} else if line, _, _ := strings.Cut(got[0], "\n"); len(line) > 120 {
		t.Errorf("a long line's excerpt is %d characters; want it short enough to stay in view", len(line))
	}
	// Hidden characters are shown escaped, and the caret counts what is shown.
	got = where("path == '\u202e' && foo")
	if len(got) != 1 || !strings.Contains(got[0], `\u202e`) || under(got[0]) != "foo" {
		t.Errorf("an expression with a bidi override: %q (caret under %q); want it escaped and the caret under foo", got, under(got[0]))
	}
	// Nothing to point at: no excerpt.
	for _, expr := range []string{"true", "AUDIT", "args[5] == 'x'"} {
		if got := where(expr); len(got) != 0 {
			t.Errorf("%q shows %q; want no excerpt", expr, got)
		}
	}
}

func TestCELUsability_ExamplesAreTriedOnASampleTheyBlock(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	_, body := a.viewerGet(t, "/rules/cel")
	links := regexp.MustCompile(`<a href="(/rules/cel\?expr=[^"]*)">Try it</a>`).FindAllStringSubmatch(body, -1)
	if len(links) != len(santacel.Examples) {
		t.Fatalf("%d Try it links, want %d", len(links), len(santacel.Examples))
	}
	for i, l := range links {
		e := santacel.Examples[i]
		link := html.UnescapeString(l[1])
		u, err := url.Parse(link)
		if err != nil || u.Fragment != "try" || u.Query().Get("expr") != e.Expression {
			t.Errorf("%s: link %s; want the example's expression and #try (%v)", e.Name, link, err)
			continue
		}
		code, tried := a.viewerGet(t, strings.TrimSuffix(link, "#try"))
		if code != http.StatusOK {
			t.Errorf("%s: trying it is a %d", e.Name, code)
			continue
		}
		if got := celOutput(t, tried, "cel-result"); !strings.Contains(got, "BLOCKLIST") {
			t.Errorf("%s: its sample execution gives %s; want one it blocks, as the link says", e.Name, got)
		}
		// The sample is in the form, to change and try again.
		if len(e.Sample.Args) > 0 && !strings.Contains(html.UnescapeString(tried), strings.Join(e.Sample.Args, "\n")+"</textarea>") {
			t.Errorf("%s: the form does not hold the sample's arguments, one per line", e.Name)
		}
		if id := e.Sample.SigningID; id != "" {
			mustContain(t, tried, `name="signing_id" value="`+id+`"`)
		}
	}
	mustContain(t, body, "on a sample execution it blocks")
}

func TestCELUsability_TheSectionLinksHaveTargets(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	_, body := a.viewerGet(t, "/rules/cel")
	links := regexp.MustCompile(`href="#([a-z-]+)"`).FindAllStringSubmatch(body, -1)
	seen := map[string]bool{}
	for _, l := range links {
		seen[l[1]] = true
		if n := strings.Count(body, `id="`+l[1]+`"`); n != 1 {
			t.Errorf("#%s is linked, and %d elements have that id; want 1", l[1], n)
		}
	}
	for _, id := range []string{"use", "workshop", "caching", "try", "examples"} {
		if !seen[id] {
			t.Errorf("no link to #%s at the top of the page", id)
		}
	}
	if first, heading := strings.Index(body, `href="#try"`), strings.Index(body, "<h2"); first < 0 || first > heading {
		t.Error("the link to the try form does not come before the first section, where someone who came to try looks")
	}
}

func TestCELUsability_TheExpressionBoxFitsTheExpression(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	rows := func(expr string) string {
		t.Helper()
		path := "/rules/cel"
		if expr != "" {
			path += "?" + url.Values{"expr": {expr}}.Encode()
		}
		_, body := a.viewerGet(t, path)
		m := regexp.MustCompile(`<textarea name="expr" rows="(\d+)"`).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("no expression box with rows:\n%s", body)
		}
		return m[1]
	}
	for expr, want := range map[string]string{
		"":     "3",
		"true": "3",
		"args.exists(a,\r\n  a == 'x'\r\n) ?\r\n  BLOCKLIST :\r\n  ALLOWLIST": "5",
		strings.Repeat("euid == 0 || ", 40) + "false":                         "7",
		strings.Repeat("true\n", 200) + "true":                                "16",
	} {
		if got := rows(expr); got != want {
			t.Errorf("%.40q: rows %s, want %s", expr, got, want)
		}
	}
}

func TestCELUsability_HintsSayWhatAnEmptyFieldMeans(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	_, body := a.viewerGet(t, "/rules/cel")
	mustContain(t, body, "(empty is 0, root)", `<label>args <span class="hint">`, `<label>envs <span class="hint">`)
	if n := strings.Count(body, "empty for none)"); n != 2 {
		t.Errorf("%d signing-time hints say an empty one means none; want both", n)
	}
	// The hint is true: an empty euid is root's.
	_, body = a.viewerGet(t, "/rules/cel?"+url.Values{"expr": {"euid == 0 ? BLOCKLIST : ALLOWLIST"}}.Encode())
	if got := celOutput(t, body, "cel-result"); got != "BLOCKLIST" {
		t.Errorf("an empty euid: %s; want BLOCKLIST, as for root", got)
	}
}

func TestCELUsability_AdminsAreLinkedToTheAddRuleForms(t *testing.T) {
	tried := "/rules/cel?" + url.Values{"expr": {celExpr}, "signing_id": {celProfiles}, "team_id": {"EQHXZ8M8AV"}}.Encode()
	addLinks := regexp.MustCompile(`href="(/admin/rules/new\?[^"]*)"`)

	a := newEditApp(t, celStore(), true)
	admin := a.cookie(t, auth.RoleAdmin)
	rec := a.get(t, tried, admin)
	body := rec.Body.String()
	mustContain(t, body, `<a href="/rules">the rules page</a>`, `href="/machines"`, `href="/groups"`)
	links := addLinks.FindAllStringSubmatch(body, -1)
	// One for the try, and one for each example whose identifier the add-rule page takes.
	withID := 0
	for _, e := range santacel.Examples {
		if e.Identifier != "" {
			withID++
		}
	}
	if len(links) != 1+withID {
		t.Fatalf("%d links to the add-rule page, want %d (the try and %d examples)", len(links), 1+withID, withID)
	}
	for _, l := range links {
		link := html.UnescapeString(l[1])
		u, err := url.Parse(link)
		if err != nil {
			t.Fatal(err)
		}
		// Only what the add-rule page already takes: no expression is carried, so nothing new is written from a link.
		for name := range u.Query() {
			if name != "signing_id" && name != "team_id" {
				t.Errorf("%s carries %s; the add-rule page takes signing_id, team_id and sha256", link, name)
			}
		}
		page := a.get(t, link, admin)
		if page.Code != http.StatusOK {
			t.Errorf("%s: status %d", link, page.Code)
			continue
		}
		mustContain(t, page.Body.String(), `<option value="CEL"`, `<textarea name="cel_expr"`)
		for _, id := range u.Query() {
			mustContain(t, page.Body.String(), `value="`+id[0]+`"`)
		}
	}
	if q := mustParseQuery(t, html.UnescapeString(links[0][1])); q.Get("signing_id") != celProfiles || q.Get("team_id") != "EQHXZ8M8AV" {
		t.Errorf("the try's link is %s; want the sample's signing ID and team ID", links[0][1])
	}

	// Not for an expression Santa would refuse, nor for identifiers that would make no rule.
	for _, q := range []url.Values{
		{"expr": {"args.exists(a,"}, "signing_id": {celProfiles}},
		{"expr": {celExpr}, "signing_id": {"not a signing id"}},
		{"expr": {celExpr}},
	} {
		if n := len(addLinks.FindAllString(a.get(t, "/rules/cel?"+q.Encode(), admin).Body.String(), -1)); n != withID {
			t.Errorf("%v: %d links to the add-rule page, want only the examples' %d", q, n, withID)
		}
	}

	// Viewers, and admins with editing off, have no add-rule page to go to.
	if _, body := a.viewerGet(t, tried); addLinks.MatchString(body) || !strings.Contains(body, "admins, on the rules page") {
		t.Error("a viewer is linked to the add-rule page, or is not told who adds rules")
	}
	off := newEditApp(t, celStore(), false)
	if body := off.get(t, tried, off.cookie(t, auth.RoleAdmin)).Body.String(); addLinks.MatchString(body) {
		t.Error("with editing off, an admin is linked to the add-rule page, which does not exist then")
	}
}

func mustParseQuery(t *testing.T, link string) url.Values {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

// The page still has one form of its own, the GET try form, and no script but the shared time script.
func TestCELUsability_ThePageStillWritesNothing(t *testing.T) {
	a := newEditApp(t, celStore(), true)
	body := a.get(t, "/rules/cel?expr=true&signing_id="+url.QueryEscape(celProfiles), a.cookie(t, auth.RoleAdmin)).Body.String()
	for _, f := range regexp.MustCompile(`<form[^>]*>`).FindAllString(body, -1) {
		if strings.Contains(f, `action="/auth/logout"`) || strings.Contains(f, `class="cel-try" method="get" action="/rules/cel#try"`) {
			continue
		}
		t.Errorf("unexpected form on the CEL page: %s", f)
	}
	if n := strings.Count(body, "<script"); n != 1 {
		t.Errorf("%d scripts on the page, want only the shared time script", n)
	}
	mustNotContain(t, body, "accesskey", "onclick", "autofocus")
}
