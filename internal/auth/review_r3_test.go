package auth_test

// Review of the input handling fixes, round 3: the redirect to sign-in carries return_to only when the login handler
// would keep it, and the login handler keeps only a same-site path outside the auth routes.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
)

// A request URI the login handler would replace with / is left out of the redirect rather than carried to the login
// page, where it would be dropped anyway: one that leads off the site, and one that resolves into the auth routes.
// ServeMux cleans such paths with a redirect of its own before any handler sees them, so Unauthenticated is called
// directly, as a front end or another router could. Pinned because only keepsReturnTo's safety check leaves them out.
func TestUnauthenticated_ReviewR3_UnsafeRequestURIsAreNotCarried(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"//evil.example/x", "/x/../auth/login"} {
		req := httptest.NewRequest(http.MethodGet, "https://rudolph-web.example.com/", nil)
		req.URL.Path = p
		if got := req.URL.RequestURI(); got != p {
			t.Fatalf("the request URI is %q, want %q", got, p)
		}
		rec := httptest.NewRecorder()
		e.auth.Unauthenticated(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("%s signed out: status %d, want 302", p, rec.Code)
		}
		loc := rec.Header().Get("Location")
		u, err := url.Parse(loc)
		if err != nil || u.Path != auth.LoginPath {
			t.Fatalf("%s signed out: redirected to %q, want %s", p, loc, auth.LoginPath)
		}
		if u.Query().Has("return_to") {
			t.Errorf("%s signed out: redirected to %q; want no return_to, which the login handler would not keep", p, loc)
		}
	}
}
