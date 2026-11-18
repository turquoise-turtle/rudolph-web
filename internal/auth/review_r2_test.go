package auth_test

// Review of the input handling fixes, round 2: the redirect to sign-in measures return_to as the flow cookie holds
// it (JSON), but sends it escaped for a query, and the two differ by up to three times.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
)

// maxSignInRedirectBytes is the longest Location the redirect to sign-in may have: well inside the 8 KB browsers and
// front ends follow, with room for the scheme and host a front end may add.
const maxSignInRedirectBytes = 4096

// signInRedirect is where a signed-out GET of uri is sent, and the return_to it carries, if any.
func (e *env) signInRedirect(t *testing.T, uri string) (loc string, returnTo string, carried bool) {
	t.Helper()
	rec := e.do(t, http.MethodGet, uri)
	if rec.Code != http.StatusFound {
		t.Fatalf("GET %.60q signed out: status %d, want 302", uri, rec.Code)
	}
	loc = rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("GET %.60q signed out: bad Location: %v", uri, err)
	}
	if u.Path != auth.LoginPath {
		t.Fatalf("GET %.60q signed out: redirected to %.60q, want %s", uri, loc, auth.LoginPath)
	}
	q := u.Query()
	return loc, q.Get("return_to"), q.Has("return_to")
}

// The fix leaves return_to out when the flow cookie could not keep it, measured as JSON. "=", "/" and "%" are one
// byte in JSON and three once url.QueryEscape has written them, so the longest request URI of them the redirect
// still carries makes a Location of about 8,000 bytes. Whatever the request URI, the Location must stay within
// 4096 bytes, and a return_to the redirect carries must be one the login handler then keeps, or the person signs
// in and lands on the home page after all. Tested at the edge, where the redirect stops carrying return_to, for
// several kinds of filler.
func TestUnauthenticated_ReviewR2_SignInRedirectFitsAndCarriesOnlyWhatLoginKeeps(t *testing.T) {
	e := newEnv(t)
	for name, filler := range map[string]string{
		"equals sign": "=",
		"slash":       "/",
		"percent":     "%",
		"mixed":       "=/%a",
		"letter":      "a",
	} {
		t.Run(name, func(t *testing.T) {
			uri := func(n int) string { return "/viewer?x=" + strings.Repeat(filler, n) }
			// The longest Location seen, and the request URI that gave it, reported once at the end.
			var longest, longestURI int
			check := func(n int) bool {
				t.Helper()
				loc, returnTo, carried := e.signInRedirect(t, uri(n))
				if len(loc) > longest {
					longest, longestURI = len(loc), len(uri(n))
				}
				return carried && returnTo != ""
			}
			defer func() {
				if longest > maxSignInRedirectBytes {
					t.Errorf("a %d-byte request URI is redirected to sign-in with a %d-byte Location; want at most %d",
						longestURI, longest, maxSignInRedirectBytes)
				}
			}()
			if _, returnTo, _ := e.signInRedirect(t, uri(1)); returnTo != uri(1) {
				t.Fatalf("a short request URI %q is redirected with return_to %q; want it carried", uri(1), returnTo)
			}
			lo, hi := 1, 8000/len(filler)
			if check(hi) {
				t.Fatalf("a %d-byte request URI still carries return_to; no flow cookie keeps that much", len(uri(hi)))
			}
			for hi-lo > 1 { // carried at lo, not at hi
				mid := (lo + hi) / 2
				if check(mid) {
					lo = mid
				} else {
					hi = mid
				}
			}
			for n := max(1, lo-3); n <= hi+3; n++ {
				if !check(n) {
					continue
				}
				_, returnTo, _ := e.signInRedirect(t, uri(n))
				if got := e.landing(t, returnTo); got != returnTo {
					t.Errorf("the redirect to sign-in carries a %d-byte return_to, but signing in lands on %.40q; want what it carried",
						len(returnTo), got)
				}
			}
			loc, _, _ := e.signInRedirect(t, uri(lo))
			check(lo)
			t.Logf("the longest request URI carried is %d bytes, with a %d-byte Location", len(uri(lo)), len(loc))
		})
	}
}
