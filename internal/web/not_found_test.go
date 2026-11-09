package web_test

// Owner feedback from the production walkthrough (step 10.1): /admin/audit with editing off, like any unknown path,
// answered with the mux's bare "404 page not found". Signed-in users now get the site's 404 page, like the 403.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func TestNotFound_SignedInUsersGetTheSitePage(t *testing.T) {
	a := newApp(t, fake.New(), 10) // editing off: /admin/audit does not exist
	for _, c := range []struct {
		name   string
		target string
		cookie *http.Cookie
	}{
		{"viewer, unknown path", "/no/such/page", a.cookie(t, auth.RoleViewer)},
		{"admin, audit with editing off", "/admin/audit", a.cookie(t, auth.RoleAdmin)},
		{"admin, unknown admin path", "/admin/nothing", a.cookie(t, auth.RoleAdmin)},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := a.get(t, c.target, c.cookie)
			requireSitePage(t, rec, http.StatusNotFound, "404 Not Found")
			if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
				t.Errorf("Cache-Control %q, want no-store", cc)
			}
			assertSecurityHeaders(t, c.name, rec.Header(), a.idpOrigin(t))
		})
	}
}

// Owner: "I don't want randoms to be able to see our site structure". Without a session, every path answers the way a
// real page does (sign-in redirect for GET/HEAD, 401 otherwise), so a signed-out visitor cannot tell which addresses
// exist; only after sign-in does an unknown address get the 404. /auth/ and /static/ are the exceptions.
func TestNotFound_SignedOutCannotTellRoutesApart(t *testing.T) {
	a := newApp(t, fake.New(), 10) // editing off: /admin/audit and the write routes do not exist
	signedOut := func(method, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, origin+target, nil)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		rec := httptest.NewRecorder()
		a.h.ServeHTTP(rec, req)
		return rec
	}
	wantLogin := func(target string) string { return auth.LoginPath + "?return_to=" + url.QueryEscape(target) }

	known := signedOut(http.MethodGet, "/machines")
	if known.Code != http.StatusFound || known.Header().Get("Location") != wantLogin("/machines") {
		t.Fatalf("GET /machines signed out: %d %q, want 302 to sign-in", known.Code, known.Header().Get("Location"))
	}
	for _, target := range []string{"/admin/audit", "/nonsense", "/admin/nothing", "/machines/x/y/z", "/nonsense?a=1"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := signedOut(method, target)
			if rec.Code != known.Code || rec.Header().Get("Location") != wantLogin(target) {
				t.Errorf("%s %s signed out: %d %q, want %d to %q like a real page", method, target, rec.Code, rec.Header().Get("Location"), known.Code, wantLogin(target))
			}
			if got, want := headerNames(rec.Header()), headerNames(known.Header()); got != want {
				t.Errorf("%s %s signed out: headers %s, a real page's %s", method, target, got, want)
			}
		}
	}

	// Other methods: a real route, a route that exists only with editing on, an unknown path, and a real path with a
	// method it does not take all get the same 401 and body.
	base := signedOut(http.MethodDelete, "/nonsense")
	if base.Code != http.StatusUnauthorized {
		t.Fatalf("DELETE /nonsense signed out: status %d, want 401", base.Code)
	}
	for _, c := range []struct{ method, target string }{
		{http.MethodPost, "/admin/rules"},
		{http.MethodPost, "/nonsense"},
		{http.MethodDelete, "/machines"},
		{http.MethodPut, "/events"},
	} {
		rec := signedOut(c.method, c.target)
		if rec.Code != base.Code || rec.Body.String() != base.Body.String() {
			t.Errorf("%s %s signed out: %d %q, want %d %q", c.method, c.target, rec.Code, rec.Body.String(), base.Code, base.Body.String())
		}
	}
}

func headerNames(h http.Header) string {
	var names []string
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// The sign-in redirect's return_to stays a same-origin relative path whatever the address: the login handler keeps
// only safe values, so an unknown address cannot become an open redirect.
func TestNotFound_SignedOutReturnToIsSafe(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	req := httptest.NewRequest(http.MethodGet, origin+"//evil.example/x", nil)
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "://") || strings.HasPrefix(loc, "//") {
		t.Errorf("signed-out //evil.example/x: Location %q leaves the site", loc)
	}
}

// Unknown static files are not pages: they stay a plain 404 even for a signed-in user.
func TestNotFound_StaticStaysPlain(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	rec := a.get(t, "/static/nope.css", a.cookie(t, auth.RoleViewer))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `<header class="top">`) {
		t.Error("an unknown static file renders the site's page")
	}
}

// A known path with the wrong method keeps its 405.
func TestNotFound_WrongMethodKeeps405(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	req := httptest.NewRequest(http.MethodDelete, origin+"/machines", nil)
	req.AddCookie(a.cookie(t, auth.RoleViewer))
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /machines: status %d, want 405", rec.Code)
	}
}
