package web_test

// Owner: "audit should still appear with editing off, so people can know which machine/user ran the rudolph commands".
// /admin/audit is a read-only admin page whether or not editing is on, listing the CLI's changes as well as the web
// view's.

import (
	"net/http"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func TestAuditAlways_ReadOnlyWithEditingOff(t *testing.T) {
	s := fake.New()
	s.AddAuditEntry(store.AuditEntry{Time: fixedNow.Add(-time.Hour), Actor: "cli:alice@admin-mac", Action: "rule.add",
		Target: "global", Summary: "allow binary " + editSHA})
	a := newEditApp(t, s, false)

	rec := a.get(t, "/admin/audit", a.cookie(t, auth.RoleAdmin))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET /admin/audit with editing off: status %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, "cli:alice@admin-mac", "rule.add", `<a href="/admin/audit" aria-current="page">Audit</a>`)
	mustNotContain(t, body, `action="/admin/`)

	if rec := a.get(t, "/admin/audit", a.cookie(t, auth.RoleViewer)); rec.Code != http.StatusForbidden {
		t.Errorf("viewer GET /admin/audit with editing off: status %d, want 403", rec.Code)
	}
}

// Entries older than the page's window are left out, as with editing on.
func TestAuditAlways_WindowApplies(t *testing.T) {
	s := fake.New()
	s.AddAuditEntry(store.AuditEntry{Time: fixedNow.Add(-40 * 24 * time.Hour), Actor: "cli:old@admin-mac", Action: "rule.add", Target: "global"})
	s.AddAuditEntry(store.AuditEntry{Time: fixedNow.Add(-time.Hour), Actor: "cli:new@admin-mac", Action: "rule.add", Target: "global"})
	a := newEditApp(t, s, false)
	_, body := a.get(t, "/admin/audit", a.cookie(t, auth.RoleAdmin)).Code, a.get(t, "/admin/audit", a.cookie(t, auth.RoleAdmin)).Body.String()
	mustContain(t, body, "cli:new@admin-mac")
	mustNotContain(t, body, "cli:old@admin-mac")
}
