package auth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/fakeidp"
)

const (
	tenantID     = "11111111-2222-3333-4444-555555555555"
	otherTenant  = "99999999-8888-7777-6666-555555555555"
	clientID     = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	clientSecret = "s3cret-value"
	redirectURL  = "https://rudolph-web.example.com/auth/callback"
	postLogout   = "https://rudolph-web.example.com/"
)

var cookieKey = []byte("0123456789abcdef0123456789abcdef") // 32 bytes

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	idp     *fakeidp.IDP
	auth    *auth.Authenticator
	handler http.Handler
	clock   *testClock
}

// newEnv wires an Authenticator against a fake Entra-like provider and mounts
// it next to two protected routes:
//
//	GET /viewer      requires Rudolph.Viewer, echoes the session's name
//	GET /admin-only  requires Rudolph.Admin
func newEnv(t *testing.T) *env {
	t.Helper()
	idp := fakeidp.New(t, tenantID, clientID, clientSecret)
	clock := &testClock{t: time.Now()}
	a, err := auth.New(auth.Config{
		Issuer:                idp.Issuer,
		TenantID:              tenantID,
		ClientID:              clientID,
		ClientSecret:          clientSecret,
		RedirectURL:           redirectURL,
		PostLogoutRedirectURL: postLogout,
		CookieKey:             cookieKey,
		SessionTTL:            time.Hour,
		Now:                   clock.Now,
	})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	mux := http.NewServeMux()
	a.Mount(mux)
	mux.Handle("GET /viewer", a.Require(auth.RoleViewer, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := auth.SessionFromContext(r.Context())
		if !ok {
			http.Error(w, "no session in context", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("viewer page for " + s.Name + " <" + s.Username + ">"))
	})))
	mux.Handle("GET /admin-only", a.Require(auth.RoleAdmin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("admin page"))
	})))
	return &env{idp: idp, auth: a, handler: mux, clock: clock}
}

func (e *env) do(t *testing.T, method, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "https://rudolph-web.example.com"+target, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func responseCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// authorizeRequest is what the app sent the browser to the IdP with.
type authorizeRequest struct {
	flowCookie *http.Cookie
	params     url.Values
}

// startLogin hits /auth/login and returns the authorize redirect.
func (e *env) startLogin(t *testing.T, returnTo string) authorizeRequest {
	t.Helper()
	target := "/auth/login"
	if returnTo != "" {
		target += "?return_to=" + url.QueryEscape(returnTo)
	}
	rec := e.do(t, http.MethodGet, target)
	if rec.Code != http.StatusFound {
		t.Fatalf("login: status %d, want 302; body %q", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("login: bad Location: %v", err)
	}
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != e.idp.AuthorizationEndpoint() {
		t.Fatalf("login redirected to %q, want authorize endpoint %q", got, e.idp.AuthorizationEndpoint())
	}
	flow := responseCookie(rec, auth.FlowCookieName)
	if flow == nil || flow.Value == "" {
		t.Fatalf("login did not set the %s cookie", auth.FlowCookieName)
	}
	return authorizeRequest{flowCookie: flow, params: loc.Query()}
}

// callback simulates the IdP redirecting back with a code for idToken.
func (e *env) callback(t *testing.T, ar authorizeRequest, idToken string) *httptest.ResponseRecorder {
	t.Helper()
	code := e.idp.IssueCode(idToken, ar.params.Get("code_challenge"), ar.params.Get("redirect_uri"))
	q := url.Values{"code": {code}, "state": {ar.params.Get("state")}}
	return e.do(t, http.MethodGet, "/auth/callback?"+q.Encode(), ar.flowCookie)
}

// login performs the whole flow with the given roles and returns the session cookie.
func (e *env) login(t *testing.T, roles ...string) *http.Cookie {
	t.Helper()
	ar := e.startLogin(t, "/viewer")
	rec := e.callback(t, ar, e.idp.SignIDToken(e.idp.Claims(ar.params.Get("nonce"), roles...)))
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: status %d, want 302; body %q", rec.Code, rec.Body.String())
	}
	c := responseCookie(rec, auth.SessionCookieName)
	if c == nil || c.Value == "" {
		t.Fatal("callback did not set a session cookie")
	}
	return c
}

func assertRedirectsToLogin(t *testing.T, rec *httptest.ResponseRecorder, returnTo string) {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d, want 302 to login; body %q", rec.Code, rec.Body.String())
	}
	want := "/auth/login?return_to=" + url.QueryEscape(returnTo)
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location %q, want %q", got, want)
	}
}

func assertSecureCookieAttributes(t *testing.T, c *http.Cookie) {
	t.Helper()
	if !strings.HasPrefix(c.Name, "__Host-") {
		t.Errorf("cookie %q should use the __Host- prefix", c.Name)
	}
	if !c.Secure {
		t.Errorf("cookie %q is not Secure", c.Name)
	}
	if !c.HttpOnly {
		t.Errorf("cookie %q is not HttpOnly", c.Name)
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie %q SameSite = %v, want Lax", c.Name, c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("cookie %q Path = %q, want /", c.Name, c.Path)
	}
	if c.Domain != "" {
		t.Errorf("cookie %q must not set Domain, got %q", c.Name, c.Domain)
	}
}

func TestLogin_RedirectCarriesStateNoncePKCE(t *testing.T) {
	e := newEnv(t)
	ar := e.startLogin(t, "/viewer")
	p := ar.params

	checks := map[string]string{
		"client_id":             clientID,
		"redirect_uri":          redirectURL,
		"response_type":         "code",
		"code_challenge_method": "S256",
	}
	for k, want := range checks {
		if got := p.Get(k); got != want {
			t.Errorf("authorize param %s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(" "+p.Get("scope")+" ", " openid ") {
		t.Errorf("scope %q must include openid", p.Get("scope"))
	}
	for _, k := range []string{"state", "nonce", "code_challenge"} {
		if len(p.Get(k)) < 20 {
			t.Errorf("authorize param %s = %q, want a long random value", k, p.Get(k))
		}
	}
	if p.Get("state") == p.Get("nonce") {
		t.Error("state and nonce must be independent values")
	}
	if strings.Contains(ar.flowCookie.Value, p.Get("state")) || strings.Contains(ar.flowCookie.Value, p.Get("nonce")) {
		t.Error("flow cookie must be encrypted, but contains state/nonce in clear")
	}

	// A second login must use fresh values.
	ar2 := e.startLogin(t, "/viewer")
	if ar2.params.Get("state") == p.Get("state") || ar2.params.Get("nonce") == p.Get("nonce") || ar2.params.Get("code_challenge") == p.Get("code_challenge") {
		t.Error("state/nonce/code_challenge reused across logins")
	}
}

func TestLogin_FullRoundTripLandsOnOriginalPage(t *testing.T) {
	e := newEnv(t)

	// 1. Unauthenticated request to a protected page redirects to login,
	//    remembering where the user wanted to go.
	original := "/viewer?serial=C02&cursor=abc"
	rec := e.do(t, http.MethodGet, original)
	assertRedirectsToLogin(t, rec, original)
	if strings.Contains(rec.Body.String(), "viewer page") {
		t.Fatal("protected content leaked to unauthenticated request")
	}

	// 2. Login redirects to the IdP.
	ar := e.startLogin(t, original)

	// 3. IdP redirects back with a code; app exchanges it and sets a session.
	idToken := e.idp.SignIDToken(e.idp.Claims(ar.params.Get("nonce"), auth.RoleViewer))
	rec = e.callback(t, ar, idToken)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status %d, want 302; body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != original {
		t.Fatalf("callback redirected to %q, want original page %q", got, original)
	}
	session := responseCookie(rec, auth.SessionCookieName)
	if session == nil || session.Value == "" {
		t.Fatal("no session cookie set")
	}
	if strings.Contains(session.Value, "alice@example.com") || strings.Contains(session.Value, auth.RoleViewer) {
		t.Error("session cookie must be encrypted, but contains claims in clear")
	}
	// The one-time flow cookie is cleared.
	if flow := responseCookie(rec, auth.FlowCookieName); flow == nil || flow.MaxAge >= 0 {
		t.Errorf("flow cookie not cleared after callback: %+v", flow)
	}

	// The code was redeemed with the PKCE verifier matching the challenge.
	reqs := e.idp.TokenRequests()
	if len(reqs) != 1 {
		t.Fatalf("token endpoint called %d times, want 1", len(reqs))
	}
	if fakeidp.S256Challenge(reqs[0].CodeVerifier) != ar.params.Get("code_challenge") {
		t.Error("code_verifier does not match code_challenge")
	}
	if reqs[0].RedirectURI != redirectURL {
		t.Errorf("token request redirect_uri %q, want %q", reqs[0].RedirectURI, redirectURL)
	}

	// 4. The session grants access to the original page.
	rec = e.do(t, http.MethodGet, original, session)
	if rec.Code != http.StatusOK {
		t.Fatalf("with session: status %d, want 200; body %q", rec.Code, rec.Body.String())
	}
	if want := "viewer page for Alice Admin <alice@example.com>"; rec.Body.String() != want {
		t.Fatalf("body %q, want %q", rec.Body.String(), want)
	}
}

func TestLogin_ReturnToMustBeLocalPath(t *testing.T) {
	for _, returnTo := range []string{
		"https://evil.example.com/",
		"//evil.example.com/path",
		"/\\evil.example.com",
		"javascript:alert(1)",
		"viewer",
	} {
		t.Run(returnTo, func(t *testing.T) {
			e := newEnv(t)
			ar := e.startLogin(t, returnTo)
			rec := e.callback(t, ar, e.idp.SignIDToken(e.idp.Claims(ar.params.Get("nonce"), auth.RoleViewer)))
			if rec.Code != http.StatusFound {
				t.Fatalf("status %d, want 302", rec.Code)
			}
			if got := rec.Header().Get("Location"); got != "/" {
				t.Fatalf("redirected to %q after login, want / (open redirect)", got)
			}
		})
	}
}

func TestCallback_RejectsInvalidIDTokens(t *testing.T) {
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		// token builds the ID token given the provider and the nonce the app sent.
		token func(p *fakeidp.IDP, nonce string) string
	}{
		{"wrong tenant tid", func(p *fakeidp.IDP, nonce string) string {
			c := p.Claims(nonce, auth.RoleViewer)
			c["tid"] = otherTenant
			return p.SignIDToken(c)
		}},
		{"missing tid", func(p *fakeidp.IDP, nonce string) string {
			c := p.Claims(nonce, auth.RoleViewer)
			delete(c, "tid")
			return p.SignIDToken(c)
		}},
		{"wrong audience", func(p *fakeidp.IDP, nonce string) string {
			c := p.Claims(nonce, auth.RoleViewer)
			c["aud"] = "some-other-client-id"
			return p.SignIDToken(c)
		}},
		{"wrong issuer", func(p *fakeidp.IDP, nonce string) string {
			c := p.Claims(nonce, auth.RoleViewer)
			c["iss"] = "https://login.microsoftonline.com/" + otherTenant + "/v2.0"
			return p.SignIDToken(c)
		}},
		{"expired", func(p *fakeidp.IDP, nonce string) string {
			c := p.Claims(nonce, auth.RoleViewer)
			c["iat"] = time.Now().Add(-3 * time.Hour).Unix()
			c["nbf"] = time.Now().Add(-3 * time.Hour).Unix()
			c["exp"] = time.Now().Add(-2 * time.Hour).Unix()
			return p.SignIDToken(c)
		}},
		{"bad nonce", func(p *fakeidp.IDP, nonce string) string {
			return p.SignIDToken(p.Claims("not-the-nonce-we-sent", auth.RoleViewer))
		}},
		{"missing nonce", func(p *fakeidp.IDP, nonce string) string {
			c := p.Claims(nonce, auth.RoleViewer)
			delete(c, "nonce")
			return p.SignIDToken(c)
		}},
		{"signed by unknown key", func(p *fakeidp.IDP, nonce string) string {
			return fakeidp.SignRS256(otherKey, p.KID, p.Claims(nonce, auth.RoleViewer))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ar := e.startLogin(t, "/viewer")
			rec := e.callback(t, ar, tc.token(e.idp, ar.params.Get("nonce")))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401; body %q", rec.Code, rec.Body.String())
			}
			if c := responseCookie(rec, auth.SessionCookieName); c != nil && c.Value != "" {
				t.Fatal("session cookie set for an invalid ID token")
			}
		})
	}
}

func TestCallback_RejectsBadState(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, e *env, ar authorizeRequest, code string) *httptest.ResponseRecorder
	}{
		{"state mismatch", func(t *testing.T, e *env, ar authorizeRequest, code string) *httptest.ResponseRecorder {
			q := url.Values{"code": {code}, "state": {"attacker-chosen-state-value-xxxxxxxx"}}
			return e.do(t, http.MethodGet, "/auth/callback?"+q.Encode(), ar.flowCookie)
		}},
		{"missing state param", func(t *testing.T, e *env, ar authorizeRequest, code string) *httptest.ResponseRecorder {
			q := url.Values{"code": {code}}
			return e.do(t, http.MethodGet, "/auth/callback?"+q.Encode(), ar.flowCookie)
		}},
		{"missing flow cookie", func(t *testing.T, e *env, ar authorizeRequest, code string) *httptest.ResponseRecorder {
			q := url.Values{"code": {code}, "state": {ar.params.Get("state")}}
			return e.do(t, http.MethodGet, "/auth/callback?"+q.Encode())
		}},
		{"tampered flow cookie", func(t *testing.T, e *env, ar authorizeRequest, code string) *httptest.ResponseRecorder {
			q := url.Values{"code": {code}, "state": {ar.params.Get("state")}}
			bad := *ar.flowCookie
			bad.Value = flipChar(bad.Value, len(bad.Value)/2)
			return e.do(t, http.MethodGet, "/auth/callback?"+q.Encode(), &bad)
		}},
		{"expired flow cookie", func(t *testing.T, e *env, ar authorizeRequest, code string) *httptest.ResponseRecorder {
			e.clock.Advance(16 * time.Minute)
			q := url.Values{"code": {code}, "state": {ar.params.Get("state")}}
			return e.do(t, http.MethodGet, "/auth/callback?"+q.Encode(), ar.flowCookie)
		}},
		{"flow cookie from a different login", func(t *testing.T, e *env, ar authorizeRequest, code string) *httptest.ResponseRecorder {
			other := e.startLogin(t, "/viewer")
			q := url.Values{"code": {code}, "state": {ar.params.Get("state")}}
			return e.do(t, http.MethodGet, "/auth/callback?"+q.Encode(), other.flowCookie)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ar := e.startLogin(t, "/viewer")
			idToken := e.idp.SignIDToken(e.idp.Claims(ar.params.Get("nonce"), auth.RoleViewer))
			code := e.idp.IssueCode(idToken, ar.params.Get("code_challenge"), ar.params.Get("redirect_uri"))
			rec := tc.build(t, e, ar, code)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401; body %q", rec.Code, rec.Body.String())
			}
			if c := responseCookie(rec, auth.SessionCookieName); c != nil && c.Value != "" {
				t.Fatal("session cookie set despite bad state")
			}
			if n := len(e.idp.TokenRequests()); n != 0 {
				t.Errorf("code was redeemed (%d token requests) before state was validated", n)
			}
		})
	}
}

func TestCallback_ProviderErrorIsRejectedAndEscaped(t *testing.T) {
	e := newEnv(t)
	ar := e.startLogin(t, "/viewer")
	q := url.Values{
		"error":             {"access_denied"},
		"error_description": {"<script>alert(1)</script>"},
		"state":             {ar.params.Get("state")},
	}
	rec := e.do(t, http.MethodGet, "/auth/callback?"+q.Encode(), ar.flowCookie)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<script>") {
		t.Fatal("provider error_description reflected unescaped")
	}
	if c := responseCookie(rec, auth.SessionCookieName); c != nil && c.Value != "" {
		t.Fatal("session cookie set on provider error")
	}
}

func TestCookies_HaveRequiredAttributes(t *testing.T) {
	e := newEnv(t)
	ar := e.startLogin(t, "/viewer")
	assertSecureCookieAttributes(t, ar.flowCookie)
	if ar.flowCookie.MaxAge <= 0 || ar.flowCookie.MaxAge > 15*60 {
		t.Errorf("flow cookie MaxAge = %d, want short-lived (1..900s)", ar.flowCookie.MaxAge)
	}

	rec := e.callback(t, ar, e.idp.SignIDToken(e.idp.Claims(ar.params.Get("nonce"), auth.RoleViewer)))
	session := responseCookie(rec, auth.SessionCookieName)
	if session == nil {
		t.Fatal("no session cookie")
	}
	assertSecureCookieAttributes(t, session)
	if session.MaxAge <= 0 || session.MaxAge > int(time.Hour/time.Second) {
		t.Errorf("session cookie MaxAge = %d, want 1..3600 (SessionTTL)", session.MaxAge)
	}
}

func TestSession_RejectsTamperedForeignAndExpiredCookies(t *testing.T) {
	e := newEnv(t)
	good := e.login(t, auth.RoleViewer)

	if rec := e.do(t, http.MethodGet, "/viewer", good); rec.Code != http.StatusOK {
		t.Fatalf("sanity: valid session got %d", rec.Code)
	}

	t.Run("tampered", func(t *testing.T) {
		for _, i := range []int{0, len(good.Value) / 2, len(good.Value) - 1} {
			bad := *good
			bad.Value = flipChar(good.Value, i)
			rec := e.do(t, http.MethodGet, "/viewer", &bad)
			assertRedirectsToLogin(t, rec, "/viewer")
		}
	})

	t.Run("truncated and garbage", func(t *testing.T) {
		for _, v := range []string{"", "x", good.Value[:len(good.Value)/2], "not base64 !!!"} {
			rec := e.do(t, http.MethodGet, "/viewer", &http.Cookie{Name: auth.SessionCookieName, Value: v})
			assertRedirectsToLogin(t, rec, "/viewer")
		}
	})

	t.Run("encrypted with a different key", func(t *testing.T) {
		other, err := auth.New(auth.Config{
			Issuer: e.idp.Issuer, TenantID: tenantID, ClientID: clientID, ClientSecret: clientSecret,
			RedirectURL: redirectURL, CookieKey: []byte("ffffffffffffffffffffffffffffffff"), SessionTTL: time.Hour,
		})
		if err != nil {
			t.Fatal(err)
		}
		forged, err := other.NewSessionCookie(auth.Session{
			Subject: "mallory", Name: "Mallory", TenantID: tenantID, Roles: []string{auth.RoleAdmin},
		})
		if err != nil {
			t.Fatal(err)
		}
		assertRedirectsToLogin(t, e.do(t, http.MethodGet, "/viewer", forged), "/viewer")
	})

	t.Run("flow cookie replayed as session", func(t *testing.T) {
		ar := e.startLogin(t, "/viewer")
		replay := &http.Cookie{Name: auth.SessionCookieName, Value: ar.flowCookie.Value}
		assertRedirectsToLogin(t, e.do(t, http.MethodGet, "/viewer", replay), "/viewer")
	})

	t.Run("explicitly expired", func(t *testing.T) {
		now := e.clock.Now()
		c, err := e.auth.NewSessionCookie(auth.Session{
			Subject: "alice", Name: "Alice", TenantID: tenantID, Roles: []string{auth.RoleViewer},
			IssuedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		assertRedirectsToLogin(t, e.do(t, http.MethodGet, "/viewer", c), "/viewer")
	})

	t.Run("expires after SessionTTL", func(t *testing.T) {
		e.clock.Advance(time.Hour + time.Second)
		assertRedirectsToLogin(t, e.do(t, http.MethodGet, "/viewer", good), "/viewer")
	})
}

func TestSession_RejectedCookieIsCleared(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodGet, "/viewer", &http.Cookie{Name: auth.SessionCookieName, Value: "garbage"})
	assertRedirectsToLogin(t, rec, "/viewer")
	c := responseCookie(rec, auth.SessionCookieName)
	if c == nil || c.MaxAge >= 0 {
		t.Fatalf("invalid session cookie not cleared: %+v", c)
	}
}

func TestRequire_UnauthenticatedNonGETIs401(t *testing.T) {
	e := newEnv(t)
	mux := http.NewServeMux()
	mux.Handle("POST /thing", e.auth.Require(auth.RoleViewer, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not run")
	})))
	req := httptest.NewRequest(http.MethodPost, "https://rudolph-web.example.com/thing", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}

func TestRoles_FromIDTokenGateAccess(t *testing.T) {
	cases := []struct {
		name       string
		roles      []string
		wantViewer int
		wantAdmin  int
	}{
		{"no roles claim", nil, http.StatusForbidden, http.StatusForbidden},
		{"empty roles", []string{}, http.StatusForbidden, http.StatusForbidden},
		{"unrelated role", []string{"Other.App.Role"}, http.StatusForbidden, http.StatusForbidden},
		{"role name is case sensitive", []string{"rudolph.viewer"}, http.StatusForbidden, http.StatusForbidden},
		{"viewer", []string{auth.RoleViewer}, http.StatusOK, http.StatusForbidden},
		{"admin implies viewer", []string{auth.RoleAdmin}, http.StatusOK, http.StatusOK},
		{"viewer and admin", []string{"Other", auth.RoleViewer, auth.RoleAdmin}, http.StatusOK, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			// A nil slice omits the roles claim; an empty slice sends "roles": [].
			session := e.login(t, tc.roles...)
			rec := e.do(t, http.MethodGet, "/viewer", session)
			if rec.Code != tc.wantViewer {
				t.Errorf("/viewer status %d, want %d", rec.Code, tc.wantViewer)
			}
			if tc.wantViewer == http.StatusForbidden && strings.Contains(rec.Body.String(), "viewer page") {
				t.Error("forbidden response leaked viewer content")
			}
			rec = e.do(t, http.MethodGet, "/admin-only", session)
			if rec.Code != tc.wantAdmin {
				t.Errorf("/admin-only status %d, want %d", rec.Code, tc.wantAdmin)
			}
			if tc.wantAdmin == http.StatusForbidden && strings.Contains(rec.Body.String(), "admin page") {
				t.Error("forbidden response leaked admin content")
			}
		})
	}
}

func TestLogout_ClearsCookieAndRedirectsToEndSession(t *testing.T) {
	e := newEnv(t)
	session := e.login(t, auth.RoleViewer)

	rec := e.do(t, http.MethodPost, "/auth/logout", session)
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound {
		t.Fatalf("status %d, want 303 or 302", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != e.idp.EndSessionEndpoint() {
		t.Fatalf("logout redirected to %q, want end-session endpoint %q", got, e.idp.EndSessionEndpoint())
	}
	if got := loc.Query().Get("post_logout_redirect_uri"); got != postLogout {
		t.Errorf("post_logout_redirect_uri = %q, want %q", got, postLogout)
	}
	if got := loc.Query().Get("client_id"); got != clientID {
		t.Errorf("client_id = %q, want %q", got, clientID)
	}

	cleared := responseCookie(rec, auth.SessionCookieName)
	if cleared == nil {
		t.Fatal("logout did not clear the session cookie")
	}
	if cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Errorf("session cookie not expired: MaxAge=%d Value=%q", cleared.MaxAge, cleared.Value)
	}
	assertSecureCookieAttributes(t, cleared)

	// Logout is POST-only so it cannot be triggered by a link or image.
	if rec := e.do(t, http.MethodGet, "/auth/logout", session); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /auth/logout status %d, want 405", rec.Code)
	}
}

func TestNew_ValidatesConfig(t *testing.T) {
	base := auth.Config{
		Issuer: "https://login.microsoftonline.com/" + tenantID + "/v2.0", TenantID: tenantID,
		ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
		CookieKey: cookieKey, SessionTTL: time.Hour,
	}
	if _, err := auth.New(base); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	mutations := map[string]func(c *auth.Config){
		"short cookie key": func(c *auth.Config) { c.CookieKey = []byte("too-short") },
		"no tenant":        func(c *auth.Config) { c.TenantID = "" },
		"no client id":     func(c *auth.Config) { c.ClientID = "" },
		"no issuer":        func(c *auth.Config) { c.Issuer = "" },
		"no redirect":      func(c *auth.Config) { c.RedirectURL = "" },
		"zero ttl":         func(c *auth.Config) { c.SessionTTL = 0 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			c := base
			mutate(&c)
			if _, err := auth.New(c); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// flipChar changes one character of a base64url string to another valid one.
func flipChar(s string, i int) string {
	b := []byte(s)
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	return string(b)
}
