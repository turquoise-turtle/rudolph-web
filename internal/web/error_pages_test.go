package web_test

// Owner feedback from the production walkthrough (step 10.1): a viewer opening an admin page got a bare plain-text
// 403 with none of the site around it, and error pages named the status without its number.

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func requireSitePage(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, heading string, subs ...string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status %d, want %d", rec.Code, wantStatus)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type %q, want the site's HTML page", ct)
	}
	body := rec.Body.String()
	mustContain(t, body, `<header class="top">`, `href="/static/style.css"`, `<a href="/machines"`, "Sign out", "<h1>"+heading+"</h1>")
	mustContain(t, body, subs...)
}

func TestErrorPages_ForbiddenUsesTheSiteLayout(t *testing.T) {
	a := newApp(t, fake.New(), 10)

	// A viewer on an admin page.
	rec := a.get(t, "/admin", a.cookie(t, auth.RoleViewer))
	requireSitePage(t, rec, http.StatusForbidden, "403 Forbidden", "Rudolph.Admin")
	if regexp.MustCompile(`aria-current="page"`).MatchString(rec.Body.String()) {
		t.Error("the 403 page marks a navigation entry as the current page")
	}

	// Signed in, but with no Rudolph role at all.
	rec = a.get(t, "/machines", a.cookie(t))
	requireSitePage(t, rec, http.StatusForbidden, "403 Forbidden", "Rudolph.Viewer")
}

func TestErrorPages_ForbiddenAdminPostUsesTheSiteLayout(t *testing.T) {
	a := newEditApp(t, fake.New(), true)
	rec := a.post(t, "/admin/rules", globalRule(), a.cookie(t, auth.RoleViewer), true)
	requireSitePage(t, rec, http.StatusForbidden, "403 Forbidden", "Rudolph.Admin")
}

func TestErrorPages_HeadingShowsTheStatusCode(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	rec := a.get(t, "/machines?cursor=not-a-cursor", a.cookie(t, auth.RoleViewer))
	requireSitePage(t, rec, http.StatusBadRequest, "400 Bad Request")
	mustContain(t, rec.Body.String(), "<title>400 Bad Request · Rudolph</title>")

	rec = a.get(t, "/machines/no-such-machine", a.cookie(t, auth.RoleViewer))
	requireSitePage(t, rec, http.StatusNotFound, "404 Not Found")
}

// Requests that are not signed in still get the login redirect or a plain 401, never the layout: there is no session
// to show, and a 401 to a POST is not for a person to read.
func TestErrorPages_UnauthenticatedIsUnchanged(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	if rec := a.get(t, "/admin", nil); rec.Code != http.StatusFound {
		t.Errorf("unauthenticated GET: status %d, want 302 to the login page", rec.Code)
	}
}
