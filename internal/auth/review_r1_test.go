package auth_test

// Review of the input handling fixes, round 1: return_to on the way into sign-in and on the way out of it.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
)

// The flow cookie may be this long, name and value together, before the login handler gives up return_to. Browsers
// keep 4096 bytes and some count the attributes as well.
const reviewFlowCookieBytes = 3840

func flowCookieBytes(ar authorizeRequest) int {
	return len(ar.flowCookie.Name) + len(ar.flowCookie.Value)
}

// landing signs in from return_to and returns where the callback sends the browser.
func (e *env) landing(t *testing.T, returnTo string) string {
	t.Helper()
	ar := e.startLogin(t, returnTo)
	rec := e.callback(t, ar, e.idp.SignIDToken(e.idp.Claims(ar.params.Get("nonce"), auth.RoleViewer)))
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: status %d, want 302; body %q", rec.Code, rec.Body.String())
	}
	return rec.Header().Get("Location")
}

// The fallback to the home page is for a return_to too long to keep; an ordinary one still lands where it was, which
// the earlier test allows either way.
func TestLogin_ReviewR1_AnOrdinaryReturnToLandsWhereItWas(t *testing.T) {
	e := newEnv(t)
	for _, returnTo := range []string{
		"/viewer",
		"/viewer?serial=C02&cursor=abc",
		"/events?team_id=EQHXZ8M8AV&page=3&cursor=" + strings.Repeat("c", 400) + "&back=" + strings.Repeat("b", 800),
		"/rules/cel?expr=" + url.QueryEscape("path.startsWith('/Applications/')"),
	} {
		if got := e.landing(t, returnTo); got != returnTo {
			t.Errorf("return_to %.60q landed on %.60q, want it exactly", returnTo, got)
		}
	}
}

// The login handler keeps return_to while the flow cookie fits 3840 bytes and falls back to / past that. Pinned at
// the edge: a return_to one byte longer than the longest kept falls back, and the longest kept makes a cookie within
// a few bytes of the limit (base64 writes three bytes as four), so the limit has not moved down or up.
func TestLogin_ReviewR1_FlowCookieThresholdKeepsReturnToUpToTheLimit(t *testing.T) {
	e := newEnv(t)
	returnTo := func(n int) string { return "/" + strings.Repeat("a", n-1) }
	// A kept return_to makes the cookie grow with it; one given up leaves the cookie as short as for /.
	short := flowCookieBytes(e.startLogin(t, "/"))
	kept := func(n int) bool { return flowCookieBytes(e.startLogin(t, returnTo(n))) > short+n/2 }

	lo, hi := 1, 8000
	if kept(hi) {
		t.Fatalf("a return_to of %d bytes is kept, making a flow cookie of %d bytes; past %d it should fall back to /",
			hi, flowCookieBytes(e.startLogin(t, returnTo(hi))), reviewFlowCookieBytes)
	}
	for hi-lo > 1 { // kept(lo) and !kept(hi)
		mid := (lo + hi) / 2
		if kept(mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	if n := flowCookieBytes(e.startLogin(t, returnTo(lo))); n > reviewFlowCookieBytes || n < reviewFlowCookieBytes-4 {
		t.Errorf("the longest return_to kept is %d bytes, with a flow cookie of %d; want the cookie within 4 bytes under %d",
			lo, n, reviewFlowCookieBytes)
	}
	if got := e.landing(t, returnTo(lo)); got != returnTo(lo) {
		t.Errorf("a return_to of %d bytes, just under the limit, landed on %.40q; want it kept", lo, got)
	}
	if got := e.landing(t, returnTo(hi)); got != "/" {
		t.Errorf("a return_to of %d bytes, just over the limit, landed on %.40q; want /", hi, got)
	}
	t.Logf("return_to is kept up to %d bytes", lo)
}

// A signed-out request is redirected to sign-in with its own URI as return_to, escaped, which can triple it: a
// request near the 8 KB a front end passes on then becomes a redirect of up to 24 KB, past what browsers and front
// ends follow, although the login handler would discard such a return_to anyway (it keeps about 2.5 KB). The
// redirect should leave return_to out when it is longer than the login handler keeps.
func TestUnauthenticated_ReviewR1_RedirectLeavesOutAReturnToTooLongToKeep(t *testing.T) {
	e := newEnv(t)
	for name, uri := range map[string]string{
		"plain":   "/viewer?x=" + strings.Repeat("a", 7000),
		"escaped": "/viewer?x=" + strings.Repeat("%2F", 2300),
	} {
		t.Run(name, func(t *testing.T) {
			rec := e.do(t, http.MethodGet, uri)
			if rec.Code != http.StatusFound {
				t.Fatalf("status %d, want 302", rec.Code)
			}
			loc := rec.Header().Get("Location")
			u, err := url.Parse(loc)
			if err != nil {
				t.Fatal(err)
			}
			if u.Path != auth.LoginPath {
				t.Fatalf("redirected to %.60q, want %s", loc, auth.LoginPath)
			}
			if rt := u.Query().Get("return_to"); len(rt) > reviewFlowCookieBytes {
				t.Errorf("a %d-byte request URI was redirected to sign-in with a %d-byte return_to (a %d-byte Location); the login handler keeps far less, so it should be left out",
					len(uri), len(rt), len(loc))
			}
			if len(loc) > 4096 {
				t.Errorf("the redirect to sign-in is %d bytes long; want it well inside 8 KB", len(loc))
			}
		})
	}
	// A request URI of ordinary length is still carried through.
	rec := e.do(t, http.MethodGet, "/viewer?serial=C02")
	assertRedirectsToLogin(t, rec, "/viewer?serial=C02")
}
