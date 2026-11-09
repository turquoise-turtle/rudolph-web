package web_test

// Owner: "can you add audit to the navbar for admins?". The audit page exists only with editing on, so the link shows
// only then, and only to admins: viewers get a 403 for everything under /admin.

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

var navAudit = regexp.MustCompile(`<nav>(?s:.*?)<a href="/admin/audit"[^>]*>Audit</a>(?s:.*?)</nav>`)

func TestAuditNav_AdminsWithEditingSeeIt(t *testing.T) {
	a := newEditApp(t, fake.New(), true)
	rec := a.get(t, "/machines", a.cookie(t, auth.RoleAdmin))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !navAudit.MatchString(rec.Body.String()) {
		t.Error("an admin with editing on has no Audit link in the navigation")
	}

	rec = a.get(t, "/admin/audit", a.cookie(t, auth.RoleAdmin))
	if rec.Code != http.StatusOK {
		t.Fatalf("/admin/audit: status %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, `<a href="/admin/audit" aria-current="page">Audit</a>`)
	mustNotContain(t, body, `<a href="/admin" aria-current="page">`)
}

func TestAuditNav_HiddenOtherwise(t *testing.T) {
	for _, c := range []struct {
		name    string
		editing bool
		role    string
	}{
		{"admin, editing off", false, auth.RoleAdmin},
		{"viewer, editing on", true, auth.RoleViewer},
		{"viewer, editing off", false, auth.RoleViewer},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newEditApp(t, fake.New(), c.editing)
			rec := a.get(t, "/machines", a.cookie(t, c.role))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			mustNotContain(t, rec.Body.String(), `href="/admin/audit"`)
		})
	}
}
