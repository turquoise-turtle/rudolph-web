package web_test

// v2 (plan F2, 2026-11-07): admins (Rudolph.Admin) edit rules and temporary modes from the web view. Writes go through
// a store.Writer (implemented over Rudolph's own Go packages, so validation and writes match the CLI), every write
// records an audit entry naming the signed-in Entra user, every POST is same-origin only, and without a Writer the
// view stays read-only (no forms, POSTs 404). Forms post back and redirect (303) to the page they came from.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/fakeidp"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

const editMachine = "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA"

var editSHA = strings.Repeat("ab", 32)

func newEditApp(t *testing.T, s *fake.Store, writable bool) *app {
	t.Helper()
	idp := fakeidp.New(t, tenantID, clientID, "secret")
	a, err := auth.New(auth.Config{
		Issuer: idp.Issuer, TenantID: tenantID, ClientID: clientID, ClientSecret: "secret",
		RedirectURL: origin + "/auth/callback", CookieKey: []byte("0123456789abcdef0123456789abcdef"),
		SessionTTL: time.Hour, Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := web.Options{Auth: a, Store: s, PageSize: 10, Now: func() time.Time { return fixedNow }}
	if writable {
		opts.Writer = s
	}
	h, err := web.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return &app{h: h, auth: a, store: s, idp: idp}
}

func (a *app) post(t *testing.T, target string, form url.Values, c *http.Cookie, sameOrigin bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, origin+target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if sameOrigin {
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Origin", origin)
	} else {
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", "https://evil.example.net")
	}
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

func globalRule() url.Values {
	return url.Values{"target": {"global"}, "type": {"BINARY"}, "identifier": {editSHA}, "policy": {"BLOCKLIST"},
		"custom_message": {"Blocked by IT"}, "description": {"test"}}
}

func auditOf(t *testing.T, s *fake.Store) []store.AuditEntry {
	t.Helper()
	got, err := s.ListAudit(context.Background(), fixedNow.Add(-24*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func globalRules(t *testing.T, s *fake.Store) []store.Rule {
	t.Helper()
	p, err := s.ListGlobalRules(context.Background(), store.RuleFilter{}, store.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return p.Items
}

func TestEditing_AdminAddsAGlobalRule(t *testing.T) {
	s := fake.New()
	a := newEditApp(t, s, true)
	rec := a.post(t, "/admin/rules", globalRule(), a.cookie(t, auth.RoleAdmin), true)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/rules" {
		t.Fatalf("status %d, Location %q; want 303 to /rules\n%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	rules := globalRules(t, s)
	if len(rules) != 1 || rules[0].Identifier != editSHA || rules[0].Policy != store.PolicyBlocklist || rules[0].CustomMessage != "Blocked by IT" {
		t.Fatalf("global rules %+v", rules)
	}
	entries := auditOf(t, s)
	if len(entries) != 1 {
		t.Fatalf("audit %+v", entries)
	}
	e := entries[0]
	if e.Action != "rule.add" || e.Target != "global" || !strings.HasPrefix(e.Actor, "web:") || !strings.Contains(e.Actor, "vera@example.com") {
		t.Errorf("audit entry %+v; want rule.add on global by web:vera@example.com", e)
	}
}

func TestEditing_ViewerCannotWrite(t *testing.T) {
	s := fake.New()
	a := newEditApp(t, s, true)
	rec := a.post(t, "/admin/rules", globalRule(), a.cookie(t, auth.RoleViewer), true)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer POST status %d, want 403", rec.Code)
	}
	if len(globalRules(t, s)) != 0 || len(auditOf(t, s)) != 0 {
		t.Error("a viewer's POST wrote something")
	}
}

func TestEditing_CrossOriginPostRejected(t *testing.T) {
	s := fake.New()
	a := newEditApp(t, s, true)
	rec := a.post(t, "/admin/rules", globalRule(), a.cookie(t, auth.RoleAdmin), false)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST status %d, want 403", rec.Code)
	}
	if len(globalRules(t, s)) != 0 {
		t.Error("a cross-origin POST wrote a rule")
	}
}

func TestEditing_InvalidRuleIsABadRequest(t *testing.T) {
	s := fake.New()
	a := newEditApp(t, s, true)
	form := globalRule()
	form.Set("identifier", "not-a-sha")
	rec := a.post(t, "/admin/rules", form, a.cookie(t, auth.RoleAdmin), true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "identifier") {
		t.Errorf("the error page does not say what is wrong:\n%s", rec.Body.String())
	}
	for _, bad := range []url.Values{
		{"target": {"global"}, "type": {"BINARY"}, "identifier": {editSHA}, "policy": {"REMOVE"}},
		{"target": {"global"}, "type": {"NOPE"}, "identifier": {editSHA}, "policy": {"BLOCKLIST"}},
		{"target": {"machine"}, "machine": {"not-a-uuid"}, "type": {"BINARY"}, "identifier": {editSHA}, "policy": {"BLOCKLIST"}},
		{"target": {"elsewhere"}, "type": {"BINARY"}, "identifier": {editSHA}, "policy": {"BLOCKLIST"}},
	} {
		if rec := a.post(t, "/admin/rules", bad, a.cookie(t, auth.RoleAdmin), true); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: status %d, want 400", bad, rec.Code)
		}
	}
	if len(globalRules(t, s)) != 0 || len(auditOf(t, s)) != 0 {
		t.Error("an invalid POST wrote something")
	}
}

func TestEditing_MachineRuleAddAndRemove(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: editMachine, Hostname: "lab-mac-01", SerialNum: "C02ABC123"})
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	form := url.Values{"target": {"machine"}, "machine": {editMachine}, "type": {"BINARY"}, "identifier": {editSHA},
		"policy": {"ALLOWLIST"}, "expires": {"2h"}}
	rec := a.post(t, "/admin/rules", form, admin, true)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/machines/"+editMachine {
		t.Fatalf("add: status %d, Location %q\n%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	p, err := s.ListMachineRules(context.Background(), editMachine, store.PageRequest{Limit: 10})
	if err != nil || len(p.Items) != 1 || p.Items[0].Policy != store.PolicyAllowlist || !p.Items[0].ExpiresAt.Equal(fixedNow.Add(2*time.Hour)) {
		t.Fatalf("machine rules %+v, %v; want one allowlist rule expiring in 2 hours", p.Items, err)
	}

	rec = a.post(t, "/admin/rules/remove", url.Values{"target": {"machine"}, "machine": {editMachine}, "type": {"BINARY"}, "identifier": {editSHA}}, admin, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("remove: status %d\n%s", rec.Code, rec.Body.String())
	}
	p, _ = s.ListMachineRules(context.Background(), editMachine, store.PageRequest{Limit: 10})
	for _, r := range p.Items {
		if r.State == store.RuleStateInForce {
			t.Errorf("rule still in force after removal: %+v", r)
		}
	}
	entries := auditOf(t, s)
	if len(entries) != 2 || entries[0].Action != "rule.remove" || entries[1].Action != "rule.add" || entries[0].Target != "machine:"+editMachine {
		t.Errorf("audit %+v", entries)
	}

	// Removing a rule that does not exist is a 404, and records nothing.
	rec = a.post(t, "/admin/rules/remove", url.Values{"target": {"global"}, "type": {"BINARY"}, "identifier": {strings.Repeat("cd", 32)}}, admin, true)
	if rec.Code != http.StatusNotFound || len(auditOf(t, s)) != 2 {
		t.Errorf("removing a missing rule: status %d, %d audit entries", rec.Code, len(auditOf(t, s)))
	}
}

func TestEditing_TemporaryMode(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: editMachine, Hostname: "lab-mac-01", SerialNum: "C02ABC123"})
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	rec := a.post(t, "/admin/machines/"+editMachine+"/temporary-mode", url.Values{"mode": {"LOCKDOWN"}, "for": {"2h"}}, admin, true)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/machines/"+editMachine {
		t.Fatalf("set: status %d, Location %q\n%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	d, err := s.GetMachine(context.Background(), editMachine)
	if err != nil || d.TemporaryClientMode != store.ClientModeLockdown || !d.TemporaryClientModeUntil.Equal(fixedNow.Add(2*time.Hour)) {
		t.Fatalf("machine %+v, %v; want LOCKDOWN until now+2h", d, err)
	}
	for _, bad := range []url.Values{{"mode": {"LOCKDOWN"}, "for": {"31d"}}, {"mode": {"LOCKDOWN"}, "for": {"30s"}}, {"mode": {"PARTY"}, "for": {"1h"}}} {
		if rec := a.post(t, "/admin/machines/"+editMachine+"/temporary-mode", bad, admin, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: status %d, want 400", bad, rec.Code)
		}
	}
	rec = a.post(t, "/admin/machines/"+editMachine+"/temporary-mode/end", url.Values{}, admin, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("end: status %d", rec.Code)
	}
	if d, _ := s.GetMachine(context.Background(), editMachine); d.TemporaryClientMode != "" {
		t.Errorf("temporary mode still set: %+v", d)
	}
	entries := auditOf(t, s)
	if len(entries) != 2 || entries[0].Action != "config.end-temporary-mode" || entries[1].Action != "config.temporary-mode" {
		t.Errorf("audit %+v", entries)
	}
}

func TestEditing_FormsOnlyForAdminsAndOnlyWithAWriter(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: editMachine, Hostname: "lab-mac-01", SerialNum: "C02ABC123"})
	a := newEditApp(t, s, true)
	for _, page := range []string{"/rules", "/machines/" + editMachine} {
		admin := a.get(t, page, a.cookie(t, auth.RoleAdmin)).Body.String()
		if !strings.Contains(admin, `action="/admin/`) {
			t.Errorf("%s: no edit form for an admin", page)
		}
		viewer := a.get(t, page, a.cookie(t, auth.RoleViewer)).Body.String()
		if strings.Contains(viewer, `action="/admin/`) {
			t.Errorf("%s: an edit form is shown to a viewer", page)
		}
	}

	ro := newEditApp(t, fake.New(), false)
	if body := ro.get(t, "/rules", ro.cookie(t, auth.RoleAdmin)).Body.String(); strings.Contains(body, `action="/admin/`) {
		t.Error("read-only deployment shows edit forms")
	}
	if rec := ro.post(t, "/admin/rules", globalRule(), ro.cookie(t, auth.RoleAdmin), true); rec.Code != http.StatusNotFound {
		t.Errorf("read-only deployment: POST status %d, want 404", rec.Code)
	}
}

func TestAuditPage(t *testing.T) {
	s := fake.New()
	a := newEditApp(t, s, true)
	a.post(t, "/admin/rules", globalRule(), a.cookie(t, auth.RoleAdmin), true)
	rec := a.get(t, "/admin/audit", a.cookie(t, auth.RoleAdmin))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	mustContain(t, rec.Body.String(), "rule.add", "vera@example.com", editSHA[:16])
	if rec := a.get(t, "/admin/audit", a.cookie(t, auth.RoleViewer)); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status %d, want 403", rec.Code)
	}
}
