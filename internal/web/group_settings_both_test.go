package web_test

// A group settings form that changes the client mode and the file access override together (owner decision,
// 2026-11-23): one store change, so one audit entry with the CLI's combined summary. The client mode's confirmation
// still guards the whole form: refused, neither setting is written.

import (
	"net/http"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

func TestGroupSettings_BothChangedIsOneAuditEntry(t *testing.T) {
	s := groupFAAStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)

	// staff has neither setting. LOCKDOWN without its name typed refuses the whole form.
	form := faaForm("staff", groupByName(t, s, "staff"), "DISABLE")
	form.Set("ClientMode", string(store.ClientModeLockdown))
	if rec := a.post(t, "/admin/groups/config", form, admin, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed LOCKDOWN: status %d, want 400", rec.Code)
	}
	if g := groupByName(t, s, "staff"); g.ClientMode != "" || g.FileAccessOverride != "" || len(auditOf(t, s)) != 0 {
		t.Fatalf("staff %+v after a refused form, want nothing written or audited", g)
	}

	form.Set("confirm_mode", string(store.ClientModeLockdown))
	see303(t, a.post(t, "/admin/groups/config", form, admin, true), "/groups/staff")
	if g := groupByName(t, s, "staff"); g.ClientMode != store.ClientModeLockdown || g.FileAccessOverride != "DISABLE" {
		t.Errorf("staff %+v, want LOCKDOWN and DISABLE", g)
	}
	entries := auditOf(t, s)
	if len(entries) != 1 {
		t.Fatalf("audit %+v, want one entry for the one form", entries)
	}
	if got, want := entries[0].Action+" "+entries[0].Target+" "+entries[0].Summary, "config.update group:staff ClientMode=LOCKDOWN, OverrideFileAccessAction=DISABLE"; got != want {
		t.Errorf("audit entry %q, want %q", got, want)
	}
}
