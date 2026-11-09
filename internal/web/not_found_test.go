package web_test

// Owner feedback from the production walkthrough (step 10.1): /admin/audit with editing off, like any unknown path,
// answered with the mux's bare "404 page not found". Signed-in users now get the site's 404 page, like the 403. Requests
// without a session still get the plain 404, so nothing of the site shows before sign-in.

import (
	"net/http"
	"net/http/httptest"
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
		{"viewer, audit with editing off", "/admin/audit", a.cookie(t, auth.RoleViewer)},
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

func TestNotFound_WithoutASessionStaysPlain(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	for _, target := range []string{"/no/such/page", "/admin/audit"} {
		rec := a.get(t, target, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", target, rec.Code)
		}
		if strings.Contains(rec.Body.String(), `<header class="top">`) || strings.Contains(rec.Body.String(), "<nav") {
			t.Errorf("%s: the site's layout shows without a session:\n%s", target, rec.Body.String())
		}
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
