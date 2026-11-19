package auth_test

// Input handling review, 2026-11-18: sign-in keeps return_to, whatever its length, in the sealed flow cookie.
// Browsers refuse a cookie whose name and value together are longer than 4096 bytes (RFC 6265bis section 5.6) and
// drop it without telling the server, so the callback then finds no flow cookie and answers "Sign-in failed" however
// often the person tries. A page link is long enough on its own account: a bookmarked CEL try on /rules/cel carries
// the expression and its sample execution, and a list link carries its cursor and up to four earlier ones.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
)

// maxBrowserCookieBytes is the most a browser stores for a cookie's name and value together.
const maxBrowserCookieBytes = 4096

func TestLogin_FlowCookieFitsABrowserWhateverTheReturnTo(t *testing.T) {
	for name, returnTo := range map[string]string{
		"a CEL try link":     "/rules/cel?expr=" + strings.Repeat("a", 3000),
		"a deep list link":   "/events?cursor=" + strings.Repeat("c", 1600) + "&page=6&back=" + strings.Repeat("b", 4*1024),
		"the longest URL":    "/viewer?x=" + strings.Repeat("a", 8000),
		"a long local path":  "/" + strings.Repeat("p", 5000),
		"an ordinary link":   "/viewer?serial=C02&cursor=abc",
		"a short CEL try":    "/rules/cel?expr=" + strings.Repeat("a", 500),
		"a page 5 list link": "/events?cursor=" + strings.Repeat("c", 402) + "&page=5&back=" + strings.Repeat("b", 3*403),
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			ar := e.startLogin(t, returnTo)
			if n := len(ar.flowCookie.Name) + len(ar.flowCookie.Value); n > maxBrowserCookieBytes {
				t.Fatalf("return_to of %d bytes makes a flow cookie of %d bytes; a browser drops a cookie over %d, and sign-in then fails at the callback",
					len(returnTo), n, maxBrowserCookieBytes)
			}
			// Signing in still works and lands on this site: the page asked for, or the home page when return_to
			// was too long to keep.
			rec := e.callback(t, ar, e.idp.SignIDToken(e.idp.Claims(ar.params.Get("nonce"), auth.RoleViewer)))
			if rec.Code != http.StatusFound {
				t.Fatalf("callback: status %d, want 302; body %q", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Location"); got != returnTo && got != "/" {
				t.Errorf("callback redirected to %.80q, want the page asked for or /", got)
			}
		})
	}
}
