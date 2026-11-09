package web_test

// Regression tests for security review findings: response headers on every
// kind of response, strict validation of filter
// lengths before any store query, sign-out behind a CDN/custom domain,
// authenticated cursors, and no static directory listing.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

// ---------------------------------------------------------------------------
// Security headers

// parseCSP splits a Content-Security-Policy into directive -> source tokens.
func parseCSP(t *testing.T, csp string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, part := range strings.Split(csp, ";") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		name := strings.ToLower(fields[0])
		if _, dup := out[name]; dup {
			t.Errorf("CSP %q repeats directive %s", csp, name)
		}
		out[name] = fields[1:]
	}
	return out
}

// assertSecurityHeaders checks the full intended header set. idpOrigin is the
// identity provider's scheme://host, the only non-self form-action target.
func assertSecurityHeaders(t *testing.T, label string, h http.Header, idpOrigin string) {
	t.Helper()
	csp := h.Get("Content-Security-Policy")
	if csp == "" {
		t.Errorf("%s: missing Content-Security-Policy", label)
	} else {
		d := parseCSP(t, csp)
		for _, name := range []string{"default-src", "frame-ancestors", "base-uri"} {
			if got := d[name]; !slices.Equal(got, []string{"'none'"}) {
				t.Errorf("%s: CSP %s = %q, want 'none' (CSP %q)", label, name, got, csp)
			}
		}
		// Only the inline script that shows times in the browser's zone, by its hash (browser_time_test.go).
		if got := d["script-src"]; len(got) != 1 || !strings.HasPrefix(got[0], "'sha256-") {
			t.Errorf("%s: CSP script-src = %q, want a single 'sha256-…' hash (CSP %q)", label, got, csp)
		}
		fa, ok := d["form-action"]
		if !ok {
			t.Errorf("%s: CSP %q has no form-action", label, csp)
		}
		got := slices.Clone(fa)
		slices.Sort(got)
		want := []string{"'self'", idpOrigin}
		slices.Sort(want)
		if ok && !slices.Equal(got, want) {
			t.Errorf("%s: CSP form-action = %q, want exactly 'self' and %s", label, fa, idpOrigin)
		}
		for name, srcs := range d {
			for _, src := range srcs {
				if src == "*" || src == "https:" || src == "http:" || src == "data:" {
					t.Errorf("%s: CSP %s allows %q", label, name, src)
				}
			}
		}
	}

	hsts := h.Get("Strict-Transport-Security")
	maxAge := -1
	for _, part := range strings.Split(hsts, ";") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(strings.ToLower(part)), "max-age="); ok {
			maxAge, _ = strconv.Atoi(v)
		}
	}
	if maxAge < 31536000 {
		t.Errorf("%s: Strict-Transport-Security %q, want max-age of at least one year", label, hsts)
	}
	if rp := h.Get("Referrer-Policy"); rp != "same-origin" && rp != "no-referrer" {
		t.Errorf("%s: Referrer-Policy %q, want same-origin or no-referrer", label, rp)
	}
	if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("%s: X-Content-Type-Options %q, want nosniff", label, got)
	}
	if cc := h.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("%s: Cache-Control %q, want no-store", label, cc)
	}
}

func (a *app) idpOrigin(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(a.idp.Issuer)
	if err != nil {
		t.Fatal(err)
	}
	return u.Scheme + "://" + u.Host
}

func TestSecurityHeaders_OnEveryKindOfResponse(t *testing.T) {
	s := fake.New()
	seedMachines(s, 2)
	a := newApp(t, s, 10)
	viewer := a.cookie(t, auth.RoleViewer)

	crossSiteLogout := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, origin+"/auth/logout", nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", "https://evil.example.net")
		req.AddCookie(viewer)
		rec := httptest.NewRecorder()
		a.h.ServeHTTP(rec, req)
		return rec
	}

	cases := []struct {
		name string
		want int
		do   func() *httptest.ResponseRecorder
	}{
		{"200 page", http.StatusOK, func() *httptest.ResponseRecorder { return a.get(t, "/machines", viewer) }},
		{"302 login redirect", http.StatusFound, func() *httptest.ResponseRecorder { return a.get(t, "/machines", nil) }},
		{"403 without role", http.StatusForbidden, func() *httptest.ResponseRecorder { return a.get(t, "/machines", a.cookie(t)) }},
		{"403 viewer on admin", http.StatusForbidden, func() *httptest.ResponseRecorder { return a.get(t, "/admin", viewer) }},
		{"404 unknown route", http.StatusNotFound, func() *httptest.ResponseRecorder { return a.get(t, "/no-such-page", viewer) }},
		{"404 unknown machine", http.StatusNotFound, func() *httptest.ResponseRecorder { return a.get(t, "/machines/nope", viewer) }},
		{"403 cross-origin protection", http.StatusForbidden, crossSiteLogout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.do()
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d; body %q", rec.Code, tc.want, rec.Body.String())
			}
			assertSecurityHeaders(t, tc.name, rec.Header(), a.idpOrigin(t))
		})
	}
}

// ---------------------------------------------------------------------------
// Filter length validation

func TestFilters_OverlongValueIsBadRequestWithoutQuery(t *testing.T) {
	const maxFilterLen = 256 // web.maxFilterLen
	a := newApp(t, fake.New(), 10)
	long := strings.Repeat("a", maxFilterLen+1)
	limit := strings.Repeat("a", maxFilterLen)

	for _, tc := range []struct{ path, key string }{
		{"/machines", "serial"},
		{"/machines", "hostname"},
		{"/events", "machine"},
		{"/events", "team_id"},
	} {
		t.Run(tc.path+"?"+tc.key, func(t *testing.T) {
			a.store.ResetCalls()
			code, _ := a.viewerGet(t, tc.path+"?"+url.Values{tc.key: {long}}.Encode())
			if code != http.StatusBadRequest {
				t.Errorf("%d-char %s: status %d, want 400", len(long), tc.key, code)
			}
			if calls := a.store.Calls(); len(calls) != 0 {
				t.Errorf("over-long filter still queried the store: %d calls", len(calls))
			}

			if code, _ := a.viewerGet(t, tc.path+"?"+url.Values{tc.key: {limit}}.Encode()); code != http.StatusOK {
				t.Errorf("%d-char %s: status %d, want 200", len(limit), tc.key, code)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Sign-out behind CloudFront / a custom domain

// Behind CloudFront the Lambda sees the Function URL host, while the browser
// sends the custom domain as Origin. Browsers that omit Sec-Fetch-Site fall
// back to the Origin check, which must trust the configured redirect URL's
// origin, and only that origin.
func TestLogout_BehindCustomDomainTrustsRedirectOrigin(t *testing.T) {
	const functionURLHost = "abc123.lambda-url.us-east-1.on.aws"
	a := newApp(t, fake.New(), 10)
	c := a.cookie(t, auth.RoleViewer)

	post := func(originHeader, fetchSite string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://"+functionURLHost+"/auth/logout", nil)
		if req.Host != functionURLHost {
			t.Fatalf("test setup: Host %q", req.Host)
		}
		req.Header.Set("Origin", originHeader)
		if fetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		a.h.ServeHTTP(rec, req)
		return rec
	}

	rec := post(origin, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout from the configured origin: status %d, want 303; body %q", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, a.idp.EndSessionEndpoint()) {
		t.Errorf("logout Location %q, want end-session endpoint", loc)
	}

	for _, foreign := range []string{
		"https://evil.example.net",
		"http://rudolph-web.example.com", // scheme differs
		"https://rudolph-web.example.com.evil.example.net",
		"https://sub.rudolph-web.example.com",
		"https://rudolph-web.example.com:8443",
		"null",
	} {
		t.Run(foreign, func(t *testing.T) {
			for _, fetchSite := range []string{"", "cross-site"} {
				rec := post(foreign, fetchSite)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("Origin %q Sec-Fetch-Site %q: status %d, want 403", foreign, fetchSite, rec.Code)
				}
				for _, sc := range rec.Result().Cookies() {
					if sc.Name == auth.SessionCookieName {
						t.Error("rejected logout touched the session cookie")
					}
				}
				assertSecurityHeaders(t, "rejected logout", rec.Header(), a.idpOrigin(t))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Forged cursors

// forgeCursor mints a cursor by hand in the fake's unauthenticated encoding
// (base64url JSON of the query fingerprint and the last key), for the same
// query a real next link would carry. Cursors must be authenticated, so a
// syntactically valid but unauthenticated cursor is rejected.
func forgeCursor(method, machineID string, filter any, afterKey string) string {
	raw, _ := json.Marshal(map[string]string{
		"q": fmt.Sprintf("%s|%s|%#v", method, machineID, filter),
		"k": afterKey,
	})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func TestForgedCursorIsBadRequest(t *testing.T) {
	s := fake.New()
	seedMachines(s, 7)
	for i := 1; i <= 7; i++ {
		s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: fmt.Sprintf("rule-%d", i)})
		s.AddMachineRule("machine-001", store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyBlocklist, Identifier: fmt.Sprintf("mrule-%d", i)})
		s.AddExecutionEvent(store.ExecutionEvent{MachineID: "machine-001", FileSHA256: fmt.Sprintf("sha-%d", i), FileName: "f",
			Decision: store.DecisionBlockUnknown, ExecutionTime: fixedNow.Add(-time.Duration(i) * time.Minute)})
	}
	var top []store.TopBinary
	for i := 1; i <= 7; i++ {
		top = append(top, store.TopBinary{FileSHA256: fmt.Sprintf("sha-%d", i), Count: 10 - i})
	}
	s.SetTopList(store.TopKindBlocked, 7, fixedNow, top)
	a := newApp(t, s, 2)

	cases := map[string]string{
		"machines skip ahead": "/machines?cursor=" +
			forgeCursor("ListMachines", "", store.MachineFilter{}, "machine-004"),
		"machines hostname filter": "/machines?hostname=host-&cursor=" +
			forgeCursor("ListMachines", "", store.MachineFilter{HostnamePrefix: "host-"}, "host-005\x00machine-005"),
		"global rules": "/rules?cursor=" +
			forgeCursor("ListGlobalRules", "", store.RuleFilter{}, "BINARY#rule-3"),
		"machine rules": "/machines/machine-001?cursor=" +
			forgeCursor("ListMachineRules", "machine-001", nil, "BINARY#mrule-3"),
		"events arbitrary offset": "/events?cursor=" +
			forgeCursor("ListExecutionEvents", "", store.EventFilter{}, fmt.Sprintf("%020d|%08d", 0, 0)),
		"top blocked": "/events/top-blocked?cursor=" +
			forgeCursor("TopBinaries", "", store.TopFilter{Kind: store.TopKindBlocked, WindowDays: 7}, "3"),
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			code, body := a.viewerGet(t, target)
			if code != http.StatusBadRequest {
				t.Errorf("forged cursor %s: status %d (%d rows), want 400", target, code, rows(body))
			}
		})
	}

	// Genuine cursors keep working.
	_, body := a.viewerGet(t, "/machines")
	if code, _ := a.viewerGet(t, nextLink(t, body)); code != http.StatusOK {
		t.Errorf("genuine next link: status %d, want 200", code)
	}
}

// Tampering with a genuine cursor must be detected whatever the encoding: the
// key it carries cannot be edited, and no single-character change is accepted.
func TestTamperedGenuineCursorIsBadRequest(t *testing.T) {
	s := fake.New()
	seedMachines(s, 9)
	a := newApp(t, s, 2)

	_, body := a.viewerGet(t, "/machines")
	u, err := url.Parse(nextLink(t, body))
	if err != nil {
		t.Fatal(err)
	}
	genuine := u.Query().Get("cursor")
	if genuine == "" {
		t.Fatal("no cursor on page 1")
	}
	get := func(cursor string) int {
		code, _ := a.viewerGet(t, "/machines?"+url.Values{"cursor": {cursor}}.Encode())
		return code
	}
	if code := get(genuine); code != http.StatusOK {
		t.Fatalf("genuine cursor: status %d, want 200", code)
	}

	// If the last key is visible in the decoded cursor, move it forward.
	if raw, err := base64.RawURLEncoding.DecodeString(genuine); err == nil && bytes.Contains(raw, []byte("machine-002")) {
		edited := base64.RawURLEncoding.EncodeToString(bytes.ReplaceAll(raw, []byte("machine-002"), []byte("machine-006")))
		if code := get(edited); code != http.StatusBadRequest {
			t.Errorf("cursor with its key edited to skip ahead: status %d, want 400", code)
		}
	} else {
		t.Logf("cursor key not visible in plaintext; skipping key-edit check")
	}

	// Every single-character change (except the final character, whose low
	// bits may be non-significant in unpadded base64) must be rejected.
	for i := 0; i < len(genuine)-1; i++ {
		b := []byte(genuine)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		if code := get(string(b)); code != http.StatusBadRequest {
			t.Errorf("cursor with character %d changed: status %d, want 400", i, code)
		}
	}
}

// ---------------------------------------------------------------------------
// Static assets

func TestStatic_NoDirectoryListing(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	for _, target := range []string{"/static/", "/static/./", "/static//"} {
		rec := a.get(t, target, nil)
		// A redirect produced by path cleaning must lead to a 404 as well.
		if rec.Code >= 300 && rec.Code < 400 {
			rec = a.get(t, rec.Header().Get("Location"), nil)
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", target, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "style.css") {
			t.Errorf("GET %s lists directory contents: %q", target, rec.Body.String())
		}
	}

	css := a.get(t, "/static/style.css", nil)
	if css.Code != http.StatusOK || !strings.HasPrefix(css.Header().Get("Content-Type"), "text/css") {
		t.Errorf("/static/style.css: status %d type %q", css.Code, css.Header().Get("Content-Type"))
	}
	if missing := a.get(t, "/static/nope.css", nil); missing.Code != http.StatusNotFound {
		t.Errorf("/static/nope.css: status %d, want 404", missing.Code)
	}
}
