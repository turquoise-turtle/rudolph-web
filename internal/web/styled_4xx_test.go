package web_test

// Owner: "any other 40x sort of errors should be styled". Every 4xx a signed-in user can meet is the site's page with
// the code in its heading. Signed out, every path gets the sign-in redirect or 401 instead (not_found_test.go).

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func TestStyled4xx_SignedInSweep(t *testing.T) {
	s := fake.New()
	a := newEditApp(t, s, true)
	viewer := a.cookie(t, auth.RoleViewer)
	admin := a.cookie(t, auth.RoleAdmin)
	method := func(m, target string, c *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(m, origin+target, nil)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		a.h.ServeHTTP(rec, req)
		return rec
	}
	badRule := globalRule()
	badRule.Set("type", "nonsense")

	for _, c := range []struct {
		name    string
		status  int
		heading string
		do      func() *httptest.ResponseRecorder
	}{
		{"400 invalid cursor", 400, "400 Bad Request", func() *httptest.ResponseRecorder { return a.get(t, "/machines?cursor=not-a-cursor", viewer) }},
		{"400 bad events filter", 400, "400 Bad Request", func() *httptest.ResponseRecorder { return a.get(t, "/events?decision=NOPE", viewer) }},
		{"400 bad admin form", 400, "400 Bad Request", func() *httptest.ResponseRecorder { return a.post(t, "/admin/rules", badRule, admin, true) }},
		{"403 viewer on admin", 403, "403 Forbidden", func() *httptest.ResponseRecorder { return a.get(t, "/admin/audit", viewer) }},
		{"403 viewer posting", 403, "403 Forbidden", func() *httptest.ResponseRecorder { return a.post(t, "/admin/rules", globalRule(), viewer, true) }},
		{"403 cross-site post", 403, "403 Forbidden", func() *httptest.ResponseRecorder { return a.post(t, "/admin/rules", globalRule(), admin, false) }},
		{"404 unknown path", 404, "404 Not Found", func() *httptest.ResponseRecorder { return a.get(t, "/nonsense", viewer) }},
		{"404 unknown machine", 404, "404 Not Found", func() *httptest.ResponseRecorder { return a.get(t, "/machines/no-such-machine", viewer) }},
		{"405 wrong method", 405, "405 Method Not Allowed", func() *httptest.ResponseRecorder { return method(http.MethodDelete, "/machines", viewer) }},
		{"405 wrong method on a write route", 405, "405 Method Not Allowed", func() *httptest.ResponseRecorder { return method(http.MethodPut, "/admin/rules", admin) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := c.do()
			requireSitePage(t, rec, c.status, c.heading)
			assertSecurityHeaders(t, c.name, rec.Header(), a.idpOrigin(t))
			if c.status == http.StatusMethodNotAllowed && rec.Header().Get("Allow") == "" {
				t.Error("the 405 lost its Allow header")
			}
		})
	}
}

// The cross-site refusal stays plain without a session: there is no one to show the site to, and it is the same for
// every path.
func TestStyled4xx_CrossSiteSignedOutStaysPlain(t *testing.T) {
	a := newEditApp(t, fake.New(), true)
	for _, target := range []string{"/admin/rules", "/nonsense"} {
		rec := a.post(t, target, url.Values{}, nil, false)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", target, rec.Code)
		}
		mustNotContain(t, rec.Body.String(), `<header class="top">`)
	}
}
