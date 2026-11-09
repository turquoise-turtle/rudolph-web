package web_test

// Owner: "I don't want randoms to be able to see our site structure" (security review follow-up). Anyone in the
// tenant can sign in, so a signed-in user without a Rudolph role gets the same 403 at every address; and a viewer gets
// the same 403 at every address under /admin/, so they cannot tell which admin routes exist or whether editing is on.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

type uniformCase struct{ method, target string }

func send(a *app, c uniformCase, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, origin+c.target, nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", origin)
	if c.method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

// requireAllSame checks that every case gets the same status, headers (by name) and body as the first, and returns it.
func requireAllSame(t *testing.T, label string, recs map[uniformCase]*httptest.ResponseRecorder, first uniformCase) *httptest.ResponseRecorder {
	t.Helper()
	ref := recs[first]
	for c, rec := range recs {
		if rec.Code != ref.Code || rec.Body.String() != ref.Body.String() || headerNames(rec.Header()) != headerNames(ref.Header()) {
			t.Errorf("%s: %s %s gives %d (headers %s), %s %s gives %d (headers %s); want the same response",
				label, c.method, c.target, rec.Code, headerNames(rec.Header()), first.method, first.target, ref.Code, headerNames(ref.Header()))
		}
	}
	return ref
}

func TestRoleUniform_NoRoleGetsOne403Everywhere(t *testing.T) {
	cases := []uniformCase{
		{http.MethodGet, "/machines"},
		{http.MethodGet, "/nonsense"},
		{http.MethodGet, "/admin"},
		{http.MethodGet, "/admin/audit"},
		{http.MethodGet, "/admin/nothing"},
		{http.MethodHead, "/events"},
		{http.MethodDelete, "/machines"},
		{http.MethodPost, "/admin/rules"},
		{http.MethodPost, "/nonsense"},
	}
	for _, editing := range []bool{false, true} {
		a := newEditApp(t, fake.New(), editing)
		recs := map[uniformCase]*httptest.ResponseRecorder{}
		for _, c := range cases {
			recs[c] = send(a, c, a.cookie(t))
		}
		label := "no role, editing " + map[bool]string{false: "off", true: "on"}[editing]
		ref := requireAllSame(t, label, recs, cases[0])
		requireSitePage(t, ref, http.StatusForbidden, "403 Forbidden", "Rudolph.Viewer")
	}
}

// The response does not depend on editing either: the same 403 body with editing off and on.
func TestRoleUniform_NoRoleCannotTellEditing(t *testing.T) {
	a, b := newEditApp(t, fake.New(), false), newEditApp(t, fake.New(), true)
	x := send(a, uniformCase{http.MethodPost, "/admin/rules"}, a.cookie(t))
	y := send(b, uniformCase{http.MethodPost, "/admin/rules"}, b.cookie(t))
	if x.Code != y.Code || x.Body.String() != y.Body.String() {
		t.Errorf("no role, POST /admin/rules: editing off gives %d, on gives %d; want the same", x.Code, y.Code)
	}
}

func TestRoleUniform_ViewerGetsOne403UnderAdmin(t *testing.T) {
	cases := []uniformCase{
		{http.MethodGet, "/admin"},
		{http.MethodGet, "/admin/audit"},
		{http.MethodGet, "/admin/nothing"},
		{http.MethodGet, "/admin/rules"},
		{http.MethodPost, "/admin/rules"},
		{http.MethodPost, "/admin/rules/remove"},
		{http.MethodPost, "/admin/nothing"},
		{http.MethodDelete, "/admin/audit"},
	}
	var bodies []string
	for _, editing := range []bool{false, true} {
		a := newEditApp(t, fake.New(), editing)
		recs := map[uniformCase]*httptest.ResponseRecorder{}
		for _, c := range cases {
			recs[c] = send(a, c, a.cookie(t, auth.RoleViewer))
		}
		label := "viewer, editing " + map[bool]string{false: "off", true: "on"}[editing]
		ref := requireAllSame(t, label, recs, cases[0])
		requireSitePage(t, ref, http.StatusForbidden, "403 Forbidden", "Rudolph.Admin")
		bodies = append(bodies, ref.Body.String())
	}
	if bodies[0] != bodies[1] {
		t.Error("a viewer's 403 under /admin/ differs between editing off and on")
	}
}

// Outside /admin/ a viewer still gets the ordinary 404 and 405: they may see every viewer page anyway.
func TestRoleUniform_ViewerOutsideAdminUnchanged(t *testing.T) {
	a := newEditApp(t, fake.New(), true)
	if rec := send(a, uniformCase{http.MethodGet, "/nonsense"}, a.cookie(t, auth.RoleViewer)); rec.Code != http.StatusNotFound {
		t.Errorf("viewer GET /nonsense: status %d, want 404", rec.Code)
	}
	if rec := send(a, uniformCase{http.MethodDelete, "/machines"}, a.cookie(t, auth.RoleViewer)); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("viewer DELETE /machines: status %d, want 405", rec.Code)
	}
}

// An admin still sees the truth: with editing off the write routes do not exist.
func TestRoleUniform_AdminUnchanged(t *testing.T) {
	a := newEditApp(t, fake.New(), false)
	admin := a.cookie(t, auth.RoleAdmin)
	if rec := send(a, uniformCase{http.MethodGet, "/admin/nothing"}, admin); rec.Code != http.StatusNotFound {
		t.Errorf("admin GET /admin/nothing: status %d, want 404", rec.Code)
	}
	if rec := send(a, uniformCase{http.MethodPost, "/admin/rules"}, admin); rec.Code != http.StatusNotFound {
		t.Errorf("admin POST /admin/rules with editing off: status %d, want 404", rec.Code)
	}
	if rec := send(a, uniformCase{http.MethodGet, "/admin"}, admin); rec.Code != http.StatusOK {
		t.Errorf("admin GET /admin: status %d, want 200", rec.Code)
	}
	b := newEditApp(t, fake.New(), true)
	if rec := send(b, uniformCase{http.MethodPost, "/admin/rules"}, b.cookie(t, auth.RoleAdmin)); rec.Code != http.StatusBadRequest {
		t.Errorf("admin POST /admin/rules with an empty form, editing on: status %d, want 400", rec.Code)
	}
}
