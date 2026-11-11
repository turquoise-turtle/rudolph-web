package web_test

// CEL rules from the web view (owner, 2026-11-11: "cel in web ... sounds alright"; the end goal is stopping admins
// unenrolling from Jamf). Every add-rule form (rules page, a Mac's page, a group's page and /admin/rules/new) offers
// the CEL policy and a <textarea name="cel_expr"> for its expression, with a link to /rules/cel. An expression is
// checked as Santa's CEL v1 compiles it (internal/santacel) before anything is written: refused expressions are a 400
// that shows why, and an expression with any other policy is refused. The expression is stored trimmed.
// /rules/cel, for viewers too, explains the variables and what needs Workshop, lists santacel.Examples, and has a
// form (GET, no writes) that tries an expression on a sample execution. The answer is in
// <output class="cel-result">POLICY</output> (the policy Santa would apply) or <output class="cel-error">why</output>,
// and for a valid expression <output class="cel-cache">cached</output> or <output class="cel-cache">not cached</output>.

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/santacel"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

const (
	celProfiles = "platform:com.apple.profiles"
	celExpr     = "args.exists(a, a in ['remove', '-R', '-D']) ? BLOCKLIST : ALLOWLIST"
)

func celStore() *fake.Store {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: editMachine, Hostname: "lab-mac-01", SerialNum: "C02ABC123"})
	s.AddGroup(store.Group{Name: "eng", AcceptReported: true})
	return s
}

func celForm(target string, expr string) url.Values {
	f := url.Values{"target": {target}, "type": {"SIGNINGID"}, "identifier": {celProfiles}, "policy": {"CEL"}, "cel_expr": {expr}}
	switch target {
	case "machine":
		f.Set("machine", editMachine)
		f.Set("expires", "never")
	case "group":
		f.Set("group", "eng")
	}
	return f
}

func rulesOf(t *testing.T, s *fake.Store, target string) []store.Rule {
	t.Helper()
	var p store.Page[store.Rule]
	var err error
	switch target {
	case "global":
		p, err = s.ListGlobalRules(context.Background(), store.RuleFilter{}, store.PageRequest{Limit: 100})
	case "machine":
		p, err = s.ListMachineRules(context.Background(), editMachine, store.PageRequest{Limit: 100})
	case "group":
		p, err = s.ListGroupRules(context.Background(), "eng", store.PageRequest{Limit: 100})
	}
	if err != nil {
		t.Fatal(err)
	}
	return p.Items
}

func TestCELRules_EveryAddFormOffersCEL(t *testing.T) {
	a := newEditApp(t, celStore(), true)
	admin := a.cookie(t, auth.RoleAdmin)
	for _, path := range []string{"/rules", "/machines/" + editMachine, "/groups/eng", "/admin/rules/new?signing_id=" + url.QueryEscape(celProfiles)} {
		rec := a.get(t, path, admin)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		mustContain(t, rec.Body.String(), `<option value="CEL"`, `<textarea name="cel_expr"`, `href="/rules/cel"`)
	}
}

func TestCELRules_AdminAddsACELRuleForEachTarget(t *testing.T) {
	for _, target := range []string{"global", "machine", "group"} {
		s := celStore()
		a := newEditApp(t, s, true)
		rec := a.post(t, "/admin/rules", celForm(target, "\r\n  "+celExpr+"  \r\n"), a.cookie(t, auth.RoleAdmin), true)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: status %d, want 303\n%s", target, rec.Code, rec.Body.String())
		}
		rules := rulesOf(t, s, target)
		if len(rules) != 1 || rules[0].Policy != store.PolicyCEL || rules[0].CELExpr != celExpr || rules[0].Identifier != celProfiles {
			t.Errorf("%s rules %+v; want one CEL rule with the expression, trimmed", target, rules)
		}
		if entries := auditOf(t, s); len(entries) != 1 || entries[0].Action != "rule.add" {
			t.Errorf("%s: audit %+v", target, entries)
		}
	}
}

func TestCELRules_RefusedExpressionsWriteNothing(t *testing.T) {
	s := celStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	for expr, want := range map[string]string{
		"":               "expression",
		"   ":            "expression",
		"args.exists(a,": "Syntax error",
		"ancestors.exists(a, a.team_id == 'EQHXZ8M8AV')": "Workshop",
		"AUDIT": "Workshop",
		"policy_for_range(weekdays(), '09:00', '17:00', ALLOWLIST, BLOCKLIST)": "Workshop",
		"foo == 1":       "foo",
		"target.team_id": "ALLOWLIST",
	} {
		for _, target := range []string{"global", "machine", "group"} {
			rec := a.post(t, "/admin/rules", celForm(target, expr), admin, true)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s %q: status %d, want 400", target, expr, rec.Code)
				continue
			}
			if body := html.UnescapeString(rec.Body.String()); !strings.Contains(body, want) {
				t.Errorf("%s %q: the error page does not mention %q:\n%s", target, expr, want, rec.Body.String())
			}
		}
	}

	// An expression only goes with the CEL policy.
	f := celForm("global", "true")
	f.Set("policy", "BLOCKLIST")
	if rec := a.post(t, "/admin/rules", f, admin, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "CEL") {
		t.Errorf("an expression with BLOCKLIST: status %d, want 400 naming CEL\n%s", rec.Code, rec.Body.String())
	}
	// A rule with any other policy and no expression is still added as before.
	f.Del("cel_expr")
	if rec := a.post(t, "/admin/rules", f, admin, true); rec.Code != http.StatusSeeOther {
		t.Errorf("a BLOCKLIST rule without cel_expr: status %d, want 303\n%s", rec.Code, rec.Body.String())
	}

	if n := len(rulesOf(t, s, "machine")) + len(rulesOf(t, s, "group")); n != 0 || len(rulesOf(t, s, "global")) != 1 || len(auditOf(t, s)) != 1 {
		t.Errorf("refused CEL rules wrote something: %d machine and group rules, %d global, %d audit entries", n, len(rulesOf(t, s, "global")), len(auditOf(t, s)))
	}
}

func TestCELRules_HelpPage(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	code, body := a.viewerGet(t, "/rules/cel")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	plain := html.UnescapeString(body)
	mustContain(t, plain, "Workshop", "target.signing_id", "target.team_id", "target.signing_time", "args", "envs", "euid", "cwd", "path", "ancestors")
	for _, e := range santacel.Examples {
		mustContain(t, plain, e.Name, e.Expression)
	}
	mustContain(t, body, `<form`, `method="get"`, `action="/rules/cel"`, `name="expr"`, `name="args"`)
	mustNotContain(t, body, `method="post"`)
}

func celOutput(t *testing.T, body, class string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<output class="` + class + `">(.*?)</output>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no <output class=%q> on the page:\n%s", class, body)
	}
	return html.UnescapeString(strings.TrimSpace(m[1]))
}

func TestCELRules_TryAnExpression(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	try := func(q url.Values) string {
		t.Helper()
		code, body := a.viewerGet(t, "/rules/cel?"+q.Encode())
		if code != http.StatusOK {
			t.Fatalf("%v: status %d", q, code)
		}
		return body
	}
	// args: one per line, as the process gets them.
	body := try(url.Values{"expr": {celExpr}, "args": {"/usr/bin/profiles\r\nremove\r\n-identifier\r\ncom.example.wifi"}, "path": {"/usr/bin/profiles"}, "euid": {"0"}})
	if got := celOutput(t, body, "cel-result"); got != "BLOCKLIST" {
		t.Errorf("profiles remove: %q, want BLOCKLIST", got)
	}
	if got := celOutput(t, body, "cel-cache"); got != "not cached" {
		t.Errorf("an expression reading args: %q, want not cached", got)
	}
	if !strings.Contains(html.UnescapeString(body), celExpr) {
		t.Error("the form does not keep the expression tried")
	}

	body = try(url.Values{"expr": {celExpr}, "args": {"/usr/bin/profiles\nlist"}})
	if got := celOutput(t, body, "cel-result"); got != "ALLOWLIST" {
		t.Errorf("profiles list: %q, want ALLOWLIST", got)
	}

	body = try(url.Values{"expr": {"target.team_id == 'EQHXZ8M8AV' && target.is_platform_binary == false"}, "team_id": {"EQHXZ8M8AV"}})
	if got := celOutput(t, body, "cel-result"); got != "ALLOWLIST" {
		t.Errorf("team ID: %q, want ALLOWLIST", got)
	}
	if got := celOutput(t, body, "cel-cache"); got != "cached" {
		t.Errorf("an expression reading only target: %q, want cached", got)
	}

	body = try(url.Values{"expr": {"euid == 0 ? BLOCKLIST : SILENT_GUI_BLOCKLIST"}, "euid": {"501"}})
	if got := celOutput(t, body, "cel-result"); got != "SILENT_GUI_BLOCKLIST" {
		t.Errorf("euid 501: %q, want SILENT_GUI_BLOCKLIST", got)
	}

	body = try(url.Values{"expr": {"AUDIT"}})
	if got := celOutput(t, body, "cel-error"); !strings.Contains(got, "Workshop") {
		t.Errorf("AUDIT: %q, want it to say Workshop", got)
	}
	if strings.Contains(body, `class="cel-result"`) {
		t.Error("an expression Santa refuses also shows a result")
	}

	body = try(url.Values{"expr": {"args.exists(a, a == '<script>alert(1)</script>')"}, "args": {"<script>alert(1)</script>"}})
	mustNotContain(t, body, "<script>alert(1)</script>")
	if got := celOutput(t, body, "cel-result"); got != "ALLOWLIST" {
		t.Errorf("hostile args: %q, want ALLOWLIST", got)
	}

	// Without an expression the page is just the explanation.
	_, body = a.viewerGet(t, "/rules/cel")
	mustNotContain(t, body, `class="cel-result"`, `class="cel-error"`)

	if code, _ := a.viewerGet(t, "/rules/cel?euid=root&expr=true"); code != http.StatusOK && code != http.StatusBadRequest {
		t.Errorf("a euid that is not a number: status %d, want 200 or 400", code)
	}
}
