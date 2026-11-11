package web_test

// Markup contract relied on by these tests (kept deliberately small):
//   - every data row in a list table is rendered as <tr data-row ...>
//   - the link to the next page is <a rel="next" href="...">
//   - all timestamps are rendered as "2006-01-02 15:04:05 UTC"

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/fakeidp"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

const (
	tenantID = "11111111-2222-3333-4444-555555555555"
	clientID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	origin   = "https://rudolph-web.example.com"
)

var fixedNow = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

type app struct {
	h     http.Handler
	auth  *auth.Authenticator
	store *fake.Store
	idp   *fakeidp.IDP
}

func newApp(t *testing.T, s *fake.Store, pageSize int) *app {
	t.Helper()
	idp := fakeidp.New(t, tenantID, clientID, "secret")
	a, err := auth.New(auth.Config{
		Issuer:       idp.Issuer,
		TenantID:     tenantID,
		ClientID:     clientID,
		ClientSecret: "secret",
		RedirectURL:  origin + "/auth/callback",
		CookieKey:    []byte("0123456789abcdef0123456789abcdef"),
		SessionTTL:   time.Hour,
		Now:          func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	h, err := web.New(web.Options{Auth: a, Store: s, PageSize: pageSize, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	return &app{h: h, auth: a, store: s, idp: idp}
}

func (a *app) cookie(t *testing.T, roles ...string) *http.Cookie {
	t.Helper()
	c, err := a.auth.NewSessionCookie(auth.Session{
		Subject: "oid-vera", Name: "Vera Viewer", Username: "vera@example.com", TenantID: tenantID, Roles: roles,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (a *app) get(t *testing.T, target string, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, origin+target, nil)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

func (a *app) viewerGet(t *testing.T, target string) (int, string) {
	t.Helper()
	rec := a.get(t, target, a.cookie(t, auth.RoleViewer))
	return rec.Code, rec.Body.String()
}

func rows(body string) int { return strings.Count(body, "<tr data-row") }

var nextRe = regexp.MustCompile(`<a rel="next" href="([^"]+)"`)

// nextLink returns the unescaped next-page URL, or "" if there is none.
func nextLink(t *testing.T, body string) string {
	t.Helper()
	m := nextRe.FindAllStringSubmatch(body, -1)
	if len(m) > 1 {
		t.Fatalf("found %d next links, want at most 1", len(m))
	}
	if len(m) == 0 {
		return ""
	}
	return html.UnescapeString(m[0][1])
}

func mustContain(t *testing.T, body string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(body, s) {
			t.Errorf("body missing %q", s)
		}
	}
}

func mustNotContain(t *testing.T, body string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(body, s) {
			t.Errorf("body unexpectedly contains %q", s)
		}
	}
}

func paginatedCalls(s *fake.Store) []fake.Call {
	var out []fake.Call
	for _, c := range s.Calls() {
		if c.Paginated() {
			out = append(out, c)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Authentication and authorization at the page level

func TestPages_UnauthenticatedRedirectToLoginWithoutTouchingData(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "m1", Hostname: "secret-host"})
	a := newApp(t, s, 10)

	for _, target := range []string{
		"/", "/machines", "/machines?hostname=secret", "/machines/m1", "/rules?type=BINARY",
		"/events", "/events?decision=BLOCK_UNKNOWN", "/events/top-blocked", "/events/top-allowed", "/admin",
	} {
		t.Run(target, func(t *testing.T) {
			s.ResetCalls()
			rec := a.get(t, target, nil)
			if rec.Code != http.StatusFound {
				t.Fatalf("status %d, want 302", rec.Code)
			}
			want := "/auth/login?return_to=" + url.QueryEscape(target)
			if got := rec.Header().Get("Location"); got != want {
				t.Errorf("Location %q, want %q", got, want)
			}
			mustNotContain(t, rec.Body.String(), "secret-host")
			if n := len(s.Calls()); n != 0 {
				t.Errorf("%d data-layer calls before authentication, want 0", n)
			}
		})
	}
}

func TestPages_RequireViewerRole(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "m1", Hostname: "secret-host"})
	a := newApp(t, s, 10)

	rec := a.get(t, "/machines", a.cookie(t)) // authenticated, no roles
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no roles: status %d, want 403", rec.Code)
	}
	mustNotContain(t, rec.Body.String(), "secret-host")
	if n := len(s.Calls()); n != 0 {
		t.Errorf("%d data-layer calls for a forbidden request, want 0", n)
	}

	rec = a.get(t, "/machines", a.cookie(t, "Some.Other.Role"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("other role: status %d, want 403", rec.Code)
	}
}

func TestAdminRoute_ViewerForbiddenAdminAllowed(t *testing.T) {
	a := newApp(t, fake.New(), 10)

	rec := a.get(t, "/admin", a.cookie(t, auth.RoleViewer))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer on /admin: status %d, want 403", rec.Code)
	}
	mustNotContain(t, rec.Body.String(), "Admin actions")

	rec = a.get(t, "/admin", a.cookie(t, auth.RoleAdmin))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin on /admin: status %d, want 200", rec.Code)
	}
	mustContain(t, rec.Body.String(), "Admin actions")

	// Admin is a superset of Viewer.
	if rec := a.get(t, "/machines", a.cookie(t, auth.RoleAdmin)); rec.Code != http.StatusOK {
		t.Errorf("admin on /machines: status %d, want 200", rec.Code)
	}
}

func TestLogout_CrossOriginPostRejected(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	c := a.cookie(t, auth.RoleViewer)

	req := httptest.NewRequest(http.MethodPost, origin+"/auth/logout", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://evil.example.net")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site logout: status %d, want 403", rec.Code)
	}
	for _, sc := range rec.Result().Cookies() {
		if sc.Name == auth.SessionCookieName {
			t.Error("cross-site logout touched the session cookie")
		}
	}

	req = httptest.NewRequest(http.MethodPost, origin+"/auth/logout", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("same-origin logout: status %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, a.idp.EndSessionEndpoint()) {
		t.Errorf("logout Location %q, want end-session endpoint", loc)
	}
}

func TestSecurityHeadersAndStaticAssets(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	rec := a.get(t, "/machines", a.cookie(t, auth.RoleViewer))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	h := rec.Header()
	csp := h.Get("Content-Security-Policy")
	// script-src allows only the hash of the inline script that shows times in the browser's zone (browser_time_test.go).
	for _, d := range []string{"default-src 'none'", "frame-ancestors 'none'", "script-src 'sha256-"} {
		if !strings.Contains(csp, d) {
			t.Errorf("CSP %q missing %q", csp, d)
		}
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options: nosniff")
	}
	if !strings.Contains(h.Get("Cache-Control"), "no-store") {
		t.Errorf("Cache-Control %q, want no-store for authenticated pages", h.Get("Cache-Control"))
	}
	if !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type %q", h.Get("Content-Type"))
	}
	mustContain(t, rec.Body.String(), "Vera Viewer", `action="/auth/logout"`, `method="post"`)

	// The stylesheet is embedded and public.
	css := a.get(t, "/static/style.css", nil)
	if css.Code != http.StatusOK || !strings.HasPrefix(css.Header().Get("Content-Type"), "text/css") {
		t.Errorf("/static/style.css: status %d type %q", css.Code, css.Header().Get("Content-Type"))
	}

	// Root sends viewers to the machines list.
	root := a.get(t, "/", a.cookie(t, auth.RoleViewer))
	if root.Code != http.StatusFound || root.Header().Get("Location") != "/machines" {
		t.Errorf("/: status %d Location %q", root.Code, root.Header().Get("Location"))
	}
}

// ---------------------------------------------------------------------------
// Machines

func seedMachines(s *fake.Store, n int) {
	for i := 1; i <= n; i++ {
		s.AddMachine(store.Machine{
			MachineID:    fmt.Sprintf("machine-%03d", i),
			SerialNum:    fmt.Sprintf("SER%03d", i),
			Hostname:     fmt.Sprintf("host-%03d", i),
			OSVersion:    "15.6",
			SantaVersion: "2026.8",
			ClientMode:   store.ClientModeMonitor,
			LastSeen:     fixedNow.Add(-time.Duration(i) * time.Minute),
		})
	}
}

func TestMachinesList_PaginatesWithCursor(t *testing.T) {
	s := fake.New()
	seedMachines(s, 7)
	a := newApp(t, s, 3)

	code, body := a.viewerGet(t, "/machines")
	if code != http.StatusOK {
		t.Fatalf("page 1 status %d", code)
	}
	if rows(body) != 3 {
		t.Fatalf("page 1 has %d rows, want 3", rows(body))
	}
	mustContain(t, body, "host-001", "host-002", "host-003", "SER001")
	mustNotContain(t, body, "host-004", "host-007")

	next := nextLink(t, body)
	if next == "" {
		t.Fatal("page 1 has no next link")
	}
	u, err := url.Parse(next)
	if err != nil || u.Path != "/machines" || u.Query().Get("cursor") == "" {
		t.Fatalf("next link %q should be /machines?cursor=...", next)
	}

	code, body = a.viewerGet(t, next)
	if code != http.StatusOK || rows(body) != 3 {
		t.Fatalf("page 2: status %d rows %d", code, rows(body))
	}
	mustContain(t, body, "host-004", "host-005", "host-006")
	mustNotContain(t, body, "host-003", "host-007")

	code, body = a.viewerGet(t, nextLink(t, body))
	if code != http.StatusOK || rows(body) != 1 {
		t.Fatalf("page 3: status %d rows %d", code, rows(body))
	}
	mustContain(t, body, "host-007")
	mustNotContain(t, body, "host-006")
	if nextLink(t, body) != "" {
		t.Error("last page must not have a next link")
	}
}

func TestMachinesList_FiltersByHostnameAndSerialPrefix(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "id-1", Hostname: "eng-mbp-1", SerialNum: "C02XAAA"})
	s.AddMachine(store.Machine{MachineID: "id-2", Hostname: "eng-mbp-2", SerialNum: "FVFXBBB"})
	s.AddMachine(store.Machine{MachineID: "id-3", Hostname: "eng-mbp-3", SerialNum: "C02XCCC"})
	s.AddMachine(store.Machine{MachineID: "id-4", Hostname: "ops-mbp-1", SerialNum: "C02XDDD"})
	a := newApp(t, s, 2)

	code, body := a.viewerGet(t, "/machines?hostname=eng-")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "eng-mbp-1", "eng-mbp-2")
	mustNotContain(t, body, "eng-mbp-3", "ops-mbp-1")
	if calls := s.Calls(); len(calls) != 1 || calls[0].Filter != (store.MachineFilter{HostnamePrefix: "eng-"}) {
		t.Errorf("store calls %+v, want one ListMachines with HostnamePrefix eng-", calls)
	}
	// Filter value is echoed back into the form.
	mustContain(t, body, `value="eng-"`)

	// The next link keeps the filter.
	next := nextLink(t, body)
	u, _ := url.Parse(next)
	if u.Query().Get("hostname") != "eng-" || u.Query().Get("cursor") == "" {
		t.Fatalf("next link %q lost the filter or cursor", next)
	}
	code, body = a.viewerGet(t, next)
	if code != http.StatusOK {
		t.Fatalf("page 2 status %d", code)
	}
	mustContain(t, body, "eng-mbp-3")
	mustNotContain(t, body, "eng-mbp-1", "eng-mbp-2", "ops-mbp-1")

	s.ResetCalls()
	code, body = a.viewerGet(t, "/machines?serial=C02X")
	if code != http.StatusOK {
		t.Fatalf("serial status %d", code)
	}
	mustContain(t, body, "C02XAAA", "C02XCCC")
	mustNotContain(t, body, "FVFXBBB", "C02XDDD") // DDD is on page 2
	if calls := s.Calls(); len(calls) != 1 || calls[0].Filter != (store.MachineFilter{SerialPrefix: "C02X"}) {
		t.Errorf("store calls %+v, want one ListMachines with SerialPrefix C02X", calls)
	}

	s.ResetCalls()
	if code, _ := a.viewerGet(t, "/machines?serial=C02&hostname=eng"); code != http.StatusBadRequest {
		t.Errorf("both filters: status %d, want 400", code)
	}
	if n := len(s.Calls()); n != 0 {
		t.Errorf("invalid filter still queried the store %d times", n)
	}
}

func TestMachineDetail_ShowsSyncStateCountsAndOnlyItsRules(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{
		MachineID: "m-alpha", SerialNum: "C02ALPHA", Hostname: "alpha.example.com", ModelIdentifier: "Mac15,3",
		OSVersion: "15.6.1", OSBuild: "24G90", SantaVersion: "2026.8", ClientMode: store.ClientModeLockdown,
		PrimaryUser: "alice", SIPStatus: "enabled",
		RuleCounts: store.RuleCounts{Binary: 1201, Certificate: 1302, TeamID: 1403, SigningID: 1504, CDHash: 1605},
		LastSeen:   time.Date(2026, 9, 16, 8, 29, 0, 0, time.UTC),
	})
	s.SetSyncState("m-alpha", store.SyncState{
		PreflightAt:    time.Date(2026, 9, 16, 8, 30, 0, 0, time.UTC),
		PostflightAt:   time.Date(2026, 9, 16, 8, 31, 15, 0, time.UTC),
		LastSyncType:   "CLEAN",
		RulesProcessed: 4242,
	})
	s.AddMachine(store.Machine{MachineID: "m-beta", Hostname: "beta.example.com"})
	s.AddMachineRule("m-alpha", store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyBlocklist, Identifier: "alpha-rule-1", CustomMessage: "no thanks"})
	// Rules are listed in key order (rule type, then identifier), like Rudolph's SK.
	s.AddMachineRule("m-alpha", store.Rule{RuleType: store.RuleTypeCertificate, Policy: store.PolicyAllowlist, Identifier: "alpha-rule-2"})
	s.AddMachineRule("m-alpha", store.Rule{RuleType: store.RuleTypeTeamID, Policy: store.PolicyAllowlist, Identifier: "alpha-rule-3"})
	s.AddMachineRule("m-beta", store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: "beta-rule-1"})
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: "global-rule-1"})
	a := newApp(t, s, 2)

	code, body := a.viewerGet(t, "/machines/m-alpha")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body,
		"alpha.example.com", "C02ALPHA", "Mac15,3", "15.6.1", "24G90", "2026.8", "LOCKDOWN", "alice", "enabled",
		"1201", "1302", "1403", "1504", "1605",
		"2026-09-16 08:30:00 UTC", "2026-09-16 08:31:15 UTC", "CLEAN", "4242",
		"alpha-rule-1", "alpha-rule-2", "no thanks", "BLOCKLIST", "ALLOWLIST",
	)
	mustNotContain(t, body, "beta-rule-1", "global-rule-1", "beta.example.com", "alpha-rule-3")
	if rows(body) != 2 {
		t.Errorf("rules table has %d rows, want 2", rows(body))
	}

	next := nextLink(t, body)
	u, _ := url.Parse(next)
	if u == nil || u.Path != "/machines/m-alpha" || u.Query().Get("cursor") == "" {
		t.Fatalf("rules next link %q", next)
	}
	code, body = a.viewerGet(t, next)
	if code != http.StatusOK {
		t.Fatalf("page 2 status %d", code)
	}
	mustContain(t, body, "alpha-rule-3", "alpha.example.com")
	mustNotContain(t, body, "alpha-rule-1", "alpha-rule-2", "beta-rule-1")

	// Never-synced machine renders without sync state.
	code, body = a.viewerGet(t, "/machines/m-beta")
	if code != http.StatusOK {
		t.Fatalf("m-beta status %d", code)
	}
	mustContain(t, body, "beta.example.com", "beta-rule-1")
	mustNotContain(t, body, "alpha-rule-1")

	code, body = a.viewerGet(t, "/machines/does-not-exist")
	if code != http.StatusNotFound {
		t.Fatalf("unknown machine: status %d, want 404", code)
	}
	if !strings.Contains(strings.ToLower(body), "not found") {
		t.Error("404 page should say not found")
	}
}

// ---------------------------------------------------------------------------
// Global rules

func TestGlobalRules_FiltersAndPagination(t *testing.T) {
	s := fake.New()
	for i := 1; i <= 3; i++ {
		s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyBlocklist, Identifier: fmt.Sprintf("bin-block-%d", i), Description: "desc"})
	}
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: "bin-allow-1"})
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeTeamID, Policy: store.PolicyAllowlist, Identifier: "EQHXZ8M8AV"})
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeTeamID, Policy: store.PolicySilentBlocklist, Identifier: "BADTEAM001"})
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeCertificate, Policy: store.PolicyAllowlistCompiler, Identifier: "cert-compiler"})
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeCDHash, Policy: store.PolicyCEL, Identifier: "cdhash-cel", CELExpr: "target.signing_time >= timestamp('2025-01-01T00:00:00Z')"})
	a := newApp(t, s, 2)

	code, body := a.viewerGet(t, "/rules?type=TEAMID")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "EQHXZ8M8AV", "BADTEAM001", "SILENT_BLOCKLIST")
	mustNotContain(t, body, "bin-block-1", "bin-allow-1", "cert-compiler", "cdhash-cel")
	if nextLink(t, body) != "" {
		t.Error("two TEAMID rules fit on one page; no next link expected")
	}

	code, body = a.viewerGet(t, "/rules?type=BINARY&policy=BLOCKLIST")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "bin-block-1", "bin-block-2")
	mustNotContain(t, body, "bin-block-3", "bin-allow-1", "EQHXZ8M8AV")
	next := nextLink(t, body)
	u, _ := url.Parse(next)
	if u == nil || u.Query().Get("type") != "BINARY" || u.Query().Get("policy") != "BLOCKLIST" {
		t.Fatalf("next link %q lost filters", next)
	}
	code, body = a.viewerGet(t, next)
	if code != http.StatusOK {
		t.Fatalf("page 2 status %d", code)
	}
	mustContain(t, body, "bin-block-3")
	mustNotContain(t, body, "bin-block-1", "bin-block-2", "bin-allow-1")

	code, body = a.viewerGet(t, "/rules?policy=CEL")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "cdhash-cel", "CDHASH")
	mustNotContain(t, body, "EQHXZ8M8AV")

	s.ResetCalls()
	for _, bad := range []string{"/rules?type=HASH", "/rules?policy=ALLOW", "/rules?type=binary"} {
		if code, _ := a.viewerGet(t, bad); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", bad, code)
		}
	}
	if n := len(s.Calls()); n != 0 {
		t.Errorf("invalid filters still queried the store %d times", n)
	}
}

// ---------------------------------------------------------------------------
// Events

func TestEvents_FiltersAndPagination(t *testing.T) {
	s := fake.New()
	ev := func(machine, name string, d store.Decision, team string, minutesAgo int) {
		s.AddExecutionEvent(store.ExecutionEvent{
			MachineID: machine, FileName: name, FilePath: "/Applications/" + name + ".app", FileSHA256: "sha-" + name,
			ExecutingUser: "user-" + machine, Decision: d, TeamID: team, SigningID: team + ":com.example." + name,
			ExecutionTime: fixedNow.Add(-time.Duration(minutesAgo) * time.Minute),
		})
	}
	ev("m1", "evt-a", store.DecisionBlockUnknown, "TEAMAAAAAA", 1)
	ev("m1", "evt-b", store.DecisionAllowBinary, "TEAMBBBBBB", 2)
	ev("m2", "evt-c", store.DecisionBlockUnknown, "TEAMAAAAAA", 3)
	ev("m2", "evt-d", store.DecisionBlockBinaryMismatch, "TEAMBBBBBB", 4)
	ev("m1", "evt-e", store.DecisionBlockUnknown, "TEAMAAAAAA", 5)
	a := newApp(t, s, 2)

	code, body := a.viewerGet(t, "/events")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "evt-a", "evt-b", "BLOCK_UNKNOWN", "ALLOW_BINARY", "sha-evt-a", "user-m1", "TEAMAAAAAA",
		fixedNow.Add(-time.Minute).Format("2006-01-02 15:04:05")+" UTC")
	mustNotContain(t, body, "evt-c", "evt-e")

	code, body = a.viewerGet(t, "/events?machine=m1")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "evt-a", "evt-b")
	mustNotContain(t, body, "evt-c", "evt-d", "evt-e")
	next := nextLink(t, body)
	code, body = a.viewerGet(t, next)
	if code != http.StatusOK {
		t.Fatalf("page 2 status %d", code)
	}
	mustContain(t, body, "evt-e")
	mustNotContain(t, body, "evt-a", "evt-b", "evt-c", "evt-d")

	// The events table has one index per filter, so filters are used one at a time; several at once are refused
	// with a message rather than read with a filter that would return short pages.
	code, body = a.viewerGet(t, "/events?decision=BLOCK_UNKNOWN&team_id=TEAMAAAAAA&machine=m2")
	if code != http.StatusBadRequest {
		t.Fatalf("combined filters: status %d, want 400", code)
	}
	mustContain(t, body, "one")
	code, body = a.viewerGet(t, "/events?team_id=TEAMAAAAAA")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if calls := s.Calls(); calls[len(calls)-1].Filter != (store.EventFilter{TeamID: "TEAMAAAAAA"}) {
		t.Errorf("last call filter %+v", calls[len(calls)-1].Filter)
	}

	code, body = a.viewerGet(t, "/events?decision=BLOCK_BINARY_MISMATCH")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "evt-d")
	mustNotContain(t, body, "evt-a", "evt-b", "evt-c", "evt-e")

	s.ResetCalls()
	if code, _ := a.viewerGet(t, "/events?decision=PLEASE_ALLOW"); code != http.StatusBadRequest {
		t.Errorf("bad decision: status %d, want 400", code)
	}
	if n := len(s.Calls()); n != 0 {
		t.Errorf("invalid filter still queried the store %d times", n)
	}
}

// Top blocked and top allowed binaries (Rudolph's hourly ranker): a window of 1, 7, 30 or 90 days (`days`, default
// 7), the list as stored, when it was generated, and a warning when that is more than 3 hours ago.
func TestTopLists_ShowTheStoredSnapshot(t *testing.T) {
	s := fake.New()
	gen := fixedNow.Add(-40 * time.Minute)
	s.SetTopList(store.TopKindBlocked, 7, gen, []store.TopBinary{
		{FileSHA256: "sha-most", FileName: "most-blocked", TeamID: "TEAMMOST", Count: 50, Machines: 5, MachinesKnown: true},
		{FileSHA256: "sha-some", FileName: "some-blocked", Count: 2},
	})
	s.SetTopList(store.TopKindBlocked, 30, gen, []store.TopBinary{{FileSHA256: "sha-old", FileName: "old-blocked", Count: 70}})
	s.SetTopList(store.TopKindAllowed, 7, gen, []store.TopBinary{{FileSHA256: "sha-gem", FileName: "Gemini", SigningID: "EQHXZ8M8AV:com.google.GeminiMacOS", Count: 12}})
	a := newApp(t, s, 10)

	code, body := a.viewerGet(t, "/events/top-blocked")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "most-blocked", "some-blocked", "sha-most", "TEAMMOST", gen.UTC().Format("2006-01-02 15:04:05 UTC"))
	mustNotContain(t, body, "old-blocked", "Gemini", "Warning")
	if i, j := strings.Index(body, "most-blocked"), strings.Index(body, "some-blocked"); i > j {
		t.Error("the list's order was not kept")
	}
	calls := s.Calls()
	if len(calls) != 1 || calls[0].Method != "TopBinaries" {
		t.Fatalf("calls %+v", calls)
	}
	if f, ok := calls[0].Filter.(store.TopFilter); !ok || f != (store.TopFilter{Kind: store.TopKindBlocked, WindowDays: 7}) {
		t.Errorf("default: filter %+v, want blocked, 7 days", calls[0].Filter)
	}

	code, body = a.viewerGet(t, "/events/top-blocked?days=30")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "old-blocked")

	code, body = a.viewerGet(t, "/events/top-allowed")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "Gemini", "EQHXZ8M8AV:com.google.GeminiMacOS")
	mustNotContain(t, body, "most-blocked")

	for _, bad := range []string{"/events/top-blocked?days=5", "/events/top-blocked?days=seven", "/events/top-allowed?days=-7"} {
		if code, _ := a.viewerGet(t, bad); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", bad, code)
		}
	}
}

func TestTopLists_NotComputedYetAndStale(t *testing.T) {
	s := fake.New()
	s.SetTopList(store.TopKindBlocked, 7, fixedNow.Add(-5*time.Hour), []store.TopBinary{{FileSHA256: "sha-a", FileName: "a", Count: 1}})
	a := newApp(t, s, 10)
	code, body := a.viewerGet(t, "/events/top-blocked")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "Warning", "a")
	code, body = a.viewerGet(t, "/events/top-allowed?days=90")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "not been computed yet")
}

// ---------------------------------------------------------------------------
// Cross-cutting: escaping, cursors, errors, query budget

func TestHostileDataIsEscaped(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{
		MachineID:   `evil"onmouseover="alert(1)`,
		Hostname:    `<script>alert("pwned")</script>`,
		SerialNum:   `<b>SERIAL</b>`,
		PrimaryUser: `<img src=x onerror=alert(2)>`,
	})
	s.AddMachineRule(`evil"onmouseover="alert(1)`, store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyBlocklist,
		Identifier: "id", CustomMessage: `<iframe src="javascript:alert(3)">`})
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist,
		Identifier: `<svg onload=alert(4)>`, Description: `</td></tr></table><script>alert(5)</script>`})
	s.AddExecutionEvent(store.ExecutionEvent{MachineID: "m", FileName: `<script>alert(6)</script>`, FilePath: `"><svg onload=alert(7)>`,
		FileSHA256: "abc", Decision: store.DecisionBlockUnknown, ExecutionTime: fixedNow})
	a := newApp(t, s, 10)

	pages := []string{
		"/machines",
		"/machines/" + url.PathEscape(`evil"onmouseover="alert(1)`),
		"/rules",
		"/events",
		"/events/top-blocked",
		"/machines?hostname=" + url.QueryEscape(`"><script>alert(8)</script>`),
		"/events?team_id=" + url.QueryEscape(`"><script>alert(9)</script>`),
		"/events?machine=" + url.QueryEscape(`'><img src=x onerror=alert(10)>`),
	}
	for _, p := range pages {
		t.Run(p, func(t *testing.T) {
			code, body := a.viewerGet(t, p)
			if code != http.StatusOK {
				t.Fatalf("status %d", code)
			}
			// The only script is the page's own inline one (browser_time_test.go); injected markup never appears.
			if n := strings.Count(body, "<script"); n != 1 {
				t.Errorf("want only the page's own script, got %d <script elements", n)
			}
			// The layout has its own logo image; injected ones (src=x onerror=…) must never appear.
			mustNotContain(t, body, "<script>alert", "<img src=x", "<svg", "<iframe", "<b>SERIAL", `"onmouseover="`, "</table><script")
		})
	}
	_, body := a.viewerGet(t, "/machines")
	mustContain(t, body, "&lt;script&gt;alert(&#34;pwned&#34;)&lt;/script&gt;")
}

func TestInvalidCursorIsBadRequest(t *testing.T) {
	s := fake.New()
	seedMachines(s, 3)
	s.AddMachineRule("machine-001", store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: "x"})
	a := newApp(t, s, 1)

	// A valid machines cursor must not be accepted by another list.
	_, body := a.viewerGet(t, "/machines")
	machinesNext := nextLink(t, body)
	u, _ := url.Parse(machinesNext)
	machinesCursor := u.Query().Get("cursor")

	for _, target := range []string{
		"/machines?cursor=" + url.QueryEscape("!!bogus"),
		"/machines?cursor=bm90LWEtY3Vyc29y",
		"/rules?cursor=" + url.QueryEscape(machinesCursor),
		"/events?cursor=bm90LWEtY3Vyc29y",
		"/events/top-blocked?cursor=bm90LWEtY3Vyc29y",
		"/machines/machine-001?cursor=bm90LWEtY3Vyc29y",
		"/machines?hostname=host&cursor=" + url.QueryEscape(machinesCursor),
	} {
		if code, _ := a.viewerGet(t, target); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", target, code)
		}
	}
}

func TestStoreErrorsAreNotLeaked(t *testing.T) {
	s := fake.New()
	s.SetError(errors.New("operation error DynamoDB: Query, arn:aws:dynamodb:eu-west-1:123456789012:table/rudolph AccessDenied"))
	a := newApp(t, s, 10)
	for _, target := range []string{"/machines", "/machines/m1", "/rules", "/events", "/events/top-blocked"} {
		code, body := a.viewerGet(t, target)
		if code != http.StatusInternalServerError {
			t.Errorf("%s: status %d, want 500", target, code)
		}
		mustNotContain(t, body, "arn:aws", "123456789012", "AccessDenied")
	}
}

// TestQueryBudget_OneBoundedPaginatedQueryPerList guards against handlers
// that fetch everything and slice in memory: each list page must issue exactly
// one paginated query whose limit is the page size, and render only that page.
func TestQueryBudget_OneBoundedPaginatedQueryPerList(t *testing.T) {
	const pageSize = 25
	s := fake.New()
	seedMachines(s, 300)
	for i := 0; i < 300; i++ {
		s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: fmt.Sprintf("rule-%03d", i)})
		s.AddMachineRule("machine-001", store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyBlocklist, Identifier: fmt.Sprintf("mrule-%03d", i)})
		s.AddExecutionEvent(store.ExecutionEvent{MachineID: "machine-001", FileSHA256: fmt.Sprintf("sha-%03d", i), FileName: "f",
			Decision: store.DecisionBlockUnknown, ExecutionTime: fixedNow.Add(-time.Duration(i) * time.Second)})
	}
	var top []store.TopBinary
	for i := 0; i < 300; i++ {
		top = append(top, store.TopBinary{FileSHA256: fmt.Sprintf("sha-%03d", i), FileName: "f", Count: 300 - i})
	}
	s.SetTopList(store.TopKindBlocked, 7, fixedNow, top)
	s.SetTopList(store.TopKindAllowed, 7, fixedNow, top)
	a := newApp(t, s, pageSize)

	cases := []struct {
		target        string
		method        string
		maxOtherCalls int // non-paginated point reads allowed (e.g. GetMachine)
	}{
		{"/machines", "ListMachines", 0},
		{"/machines?hostname=host-", "ListMachines", 0},
		{"/machines?serial=SER", "ListMachines", 0},
		{"/machines/machine-001", "ListMachineRules", 3}, // GetMachine, GlobalConfig and ListGroups (a group may set the client mode)
		{"/rules", "ListGlobalRules", 0},
		{"/rules?type=BINARY&policy=ALLOWLIST", "ListGlobalRules", 0},
		{"/events", "ListExecutionEvents", 0},
		{"/events?machine=machine-001", "ListExecutionEvents", 3}, // GetMachine for the stored spelling: as typed, upper, lower case (first page only)
		{"/events/top-blocked", "TopBinaries", 0},
		{"/events/top-allowed", "TopBinaries", 0},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			// First page, then the second page via its cursor.
			target := tc.target
			for page := 1; page <= 2; page++ {
				s.ResetCalls()
				code, body := a.viewerGet(t, target)
				if code != http.StatusOK {
					t.Fatalf("page %d status %d", page, code)
				}
				pc := paginatedCalls(s)
				if len(pc) != 1 {
					t.Fatalf("page %d: %d paginated queries %+v, want exactly 1", page, len(pc), s.Calls())
				}
				if pc[0].Method != tc.method {
					t.Errorf("page %d: paginated call %s, want %s", page, pc[0].Method, tc.method)
				}
				if pc[0].Page.Limit != pageSize {
					t.Errorf("page %d: limit %d, want page size %d", page, pc[0].Page.Limit, pageSize)
				}
				if other := len(s.Calls()) - len(pc); other > tc.maxOtherCalls {
					t.Errorf("page %d: %d extra non-paginated calls, want <= %d: %+v", page, other, tc.maxOtherCalls, s.Calls())
				}
				if page == 2 && pc[0].Page.Cursor == "" {
					t.Error("page 2 query did not pass the cursor to the store")
				}
				if got := rows(body); got != pageSize {
					t.Errorf("page %d rendered %d rows, want %d", page, got, pageSize)
				}
				target = nextLink(t, body)
				if target == "" {
					t.Fatalf("page %d has no next link", page)
				}
			}
		})
	}
}

func TestNew_ValidatesOptions(t *testing.T) {
	if _, err := web.New(web.Options{Store: fake.New()}); err == nil {
		t.Error("missing Auth should be an error")
	}
	idp := fakeidp.New(t, tenantID, clientID, "secret")
	a, err := auth.New(auth.Config{Issuer: idp.Issuer, TenantID: tenantID, ClientID: clientID, ClientSecret: "secret",
		RedirectURL: origin + "/auth/callback", CookieKey: []byte("0123456789abcdef0123456789abcdef"), SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := web.New(web.Options{Auth: a}); err == nil {
		t.Error("missing Store should be an error")
	}
	if _, err := web.New(web.Options{Auth: a, Store: fake.New(), PageSize: store.MaxPageSize + 1}); err == nil {
		t.Error("PageSize above store.MaxPageSize should be an error")
	}
}
