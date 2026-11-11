package web_test

// Client mode per device group in the web view (owner approved, 2026-11-11; Rudolph stores it on the group's registry
// entry and sends, in order: a temporary mode, the Mac's own ClientMode, the strictest mode of its groups (LOCKDOWN >
// STANDALONE > MONITOR; rudolph:ungrouped for a Mac in no group), the global one).
//   - store.Group gains ClientMode ("" when the group has none, or one Rudolph cannot decode).
//   - /groups shows each group's client mode in a "Client mode" column ("—" when none).
//   - A group's page (ungrouped included) shows "Client mode" with the mode, or "not set" and that the global mode
//     applies. Admins with editing on also get the settings form folded like the others:
//     <details class="edit config-edit"><summary>Change settings</summary>, a ClientMode select ("" not set, MONITOR,
//     LOCKDOWN, STANDALONE) with orig_ClientMode, and a <div class="form-actions"> with confirm_mode and the save
//     button. It posts to POST /admin/groups/config with name=<group>, and returns to the group's page (303).
//   - The write is store.Writer.SetGroupClientMode(ctx, actor, group, mode) ("" clears it): Rudolph's
//     groups.SetClientMode plus the CLI's audit entry (config.update, target group:<name> or ungrouped, summary
//     "ClientMode=LOCKDOWN" or "ClientMode inherited"). Unchanged: 303, nothing written. LOCKDOWN and STANDALONE need
//     the mode's name typed in confirm_mode (400 otherwise). A stale form (orig_ClientMode no longer the stored mode):
//     409. Unknown group: 404. Unknown mode: 400. Viewers 403; editing off 404.
//   - A Mac's Configuration table marks a client mode that comes from a group "(group <name>)": when the Mac has no own
//     ClientMode, its groups (assigned and reported-and-counted, or ungrouped when none) decide it before the global
//     config. The Mac's page reads the groups with one ListGroups call.

import (
	"net/http"
	"net/url"
	"regexp"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

const pilotMac = "DDDDDDDD-4444-4444-8444-DDDDDDDDDDDD"

func groupModeStore() *fake.Store {
	s := fake.New()
	s.SetGlobalConfig(store.Config{Exists: true, ClientMode: store.ClientModeMonitor})
	s.AddGroup(store.Group{Name: "pilot", Description: "Lockdown pilot", AcceptReported: true, ClientMode: store.ClientModeLockdown})
	s.AddGroup(store.Group{Name: "staff", Description: "Staff", AcceptReported: true})
	s.AddGroup(store.Group{Name: store.UngroupedGroup})
	s.AddMachine(store.Machine{MachineID: pilotMac, SerialNum: "C02PIL", Hostname: "pilot-mac", ClientMode: store.ClientModeMonitor, LastSeen: fixedNow})
	s.SetMachineGroups(pilotMac, nil, []string{"pilot", "staff"}, []string{"Pilot", "Staff"})
	return s
}

func TestGroupMode_ShownOnGroupPages(t *testing.T) {
	s := groupModeStore()
	a := newApp(t, s, 10)
	_, list := a.viewerGet(t, "/groups")
	mustContain(t, list, "Client mode", "LOCKDOWN")
	_, pilot := a.viewerGet(t, "/groups/pilot")
	mustContain(t, pilot, "Client mode", "LOCKDOWN")
	mustNotContain(t, pilot, "config-edit")
	_, staff := a.viewerGet(t, "/groups/staff")
	mustContain(t, staff, "Client mode", "not set")
}

func TestGroupMode_MachinePageMarksTheGroup(t *testing.T) {
	s := groupModeStore()
	a := newApp(t, s, 10)
	s.ResetCalls()
	_, body := a.viewerGet(t, "/machines/"+pilotMac)
	mustContain(t, settingRow(t, body, "ClientMode"), "LOCKDOWN", "(group pilot)")
	mustNotContain(t, settingRow(t, body, "ClientMode"), "(global config)")
	n := 0
	for _, c := range s.Calls() {
		if c.Method == "ListGroups" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d ListGroups calls, want 1", n)
	}

	// The Mac's own mode wins
	s.SetMachineConfig(pilotMac, store.Config{Exists: true, ClientMode: store.ClientModeMonitor})
	_, body = a.viewerGet(t, "/machines/"+pilotMac)
	mustContain(t, settingRow(t, body, "ClientMode"), "MONITOR", "(this Mac)")

	// A Mac in no group gets ungrouped's mode
	s2 := groupModeStore()
	s2.AddGroup(store.Group{Name: store.UngroupedGroup, ClientMode: store.ClientModeStandalone})
	s2.SetMachineGroups(pilotMac, nil, nil, nil)
	_, body = newApp(t, s2, 10).viewerGet(t, "/machines/"+pilotMac)
	mustContain(t, settingRow(t, body, "ClientMode"), "STANDALONE", "(group "+store.UngroupedGroup+")")

	// A Mac whose groups have no mode follows the global config
	s3 := groupModeStore()
	s3.SetMachineGroups(pilotMac, []string{"staff"}, nil, nil)
	_, body = newApp(t, s3, 10).viewerGet(t, "/machines/"+pilotMac)
	mustContain(t, settingRow(t, body, "ClientMode"), "MONITOR", "(global config)")
}

func TestGroupMode_StrictestGroupShown(t *testing.T) {
	s := groupModeStore()
	s.AddGroup(store.Group{Name: "a-monitor", AcceptReported: true, ClientMode: store.ClientModeMonitor})
	s.AddGroup(store.Group{Name: "b-standalone", AcceptReported: true, ClientMode: store.ClientModeStandalone})
	s.SetMachineGroups(pilotMac, []string{"a-monitor", "b-standalone"}, nil, nil)
	_, body := newApp(t, s, 10).viewerGet(t, "/machines/"+pilotMac)
	mustContain(t, settingRow(t, body, "ClientMode"), "STANDALONE", "(group b-standalone)")
}

func TestGroupMode_AdminForm(t *testing.T) {
	s := groupModeStore()
	a := newEditApp(t, s, true)
	for _, name := range []string{"pilot", "staff", store.UngroupedGroup} {
		_, body := adminGet(t, a, groupURLFor(name))
		m := configEditDetails.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("%s: no folded Change settings form", name)
		}
		mustContain(t, m[1], `action="/admin/groups/config"`, `name="ClientMode"`, `name="orig_ClientMode"`, `name="confirm_mode"`, `<div class="form-actions">`)
	}
}

func groupURLFor(name string) string { return "/groups/" + url.PathEscape(name) }

func TestGroupMode_SetAndClear(t *testing.T) {
	s := groupModeStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)

	rec := a.post(t, "/admin/groups/config", url.Values{"name": {"staff"}, "ClientMode": {"STANDALONE"}, "orig_ClientMode": {""}}, admin, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("STANDALONE without confirm_mode: status %d, want 400", rec.Code)
	}
	if g := groupByName(t, s, "staff"); g.ClientMode != "" {
		t.Fatalf("refused change stored %q", g.ClientMode)
	}
	rec = a.post(t, "/admin/groups/config", url.Values{"name": {"staff"}, "ClientMode": {"STANDALONE"}, "orig_ClientMode": {""}, "confirm_mode": {"standalone"}}, admin, true)
	see303(t, rec, "/groups/staff")
	if g := groupByName(t, s, "staff"); g.ClientMode != store.ClientModeStandalone {
		t.Errorf("staff mode %q, want STANDALONE", g.ClientMode)
	}

	rec = a.post(t, "/admin/groups/config", url.Values{"name": {"pilot"}, "ClientMode": {""}, "orig_ClientMode": {"LOCKDOWN"}}, admin, true)
	see303(t, rec, "/groups/pilot")
	if g := groupByName(t, s, "pilot"); g.ClientMode != "" {
		t.Errorf("pilot mode %q after clearing, want none", g.ClientMode)
	}

	rec = a.post(t, "/admin/groups/config", url.Values{"name": {store.UngroupedGroup}, "ClientMode": {"MONITOR"}, "orig_ClientMode": {""}}, admin, true)
	see303(t, rec, groupURLFor(store.UngroupedGroup))

	entries := auditOf(t, s)
	if len(entries) != 3 {
		t.Fatalf("audit %+v, want 3 entries", entries)
	}
	want := map[string]string{"group:staff": "ClientMode=STANDALONE", "group:pilot": "ClientMode inherited", "ungrouped": "ClientMode=MONITOR"}
	for _, e := range entries {
		if e.Action != "config.update" || want[e.Target] == "" || !regexp.MustCompile(regexp.QuoteMeta(want[e.Target])).MatchString(e.Summary) {
			t.Errorf("audit entry %+v", e)
		}
	}
}

func TestGroupMode_Refusals(t *testing.T) {
	s := groupModeStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	for name, c := range map[string]struct {
		form url.Values
		code int
	}{
		"stale form":    {url.Values{"name": {"pilot"}, "ClientMode": {"MONITOR"}, "orig_ClientMode": {"MONITOR"}}, http.StatusConflict},
		"unknown group": {url.Values{"name": {"nope"}, "ClientMode": {"MONITOR"}, "orig_ClientMode": {""}}, http.StatusNotFound},
		"unknown mode":  {url.Values{"name": {"staff"}, "ClientMode": {"PARANOID"}, "orig_ClientMode": {""}, "confirm_mode": {"PARANOID"}}, http.StatusBadRequest},
		"wrong confirm": {url.Values{"name": {"staff"}, "ClientMode": {"LOCKDOWN"}, "orig_ClientMode": {""}, "confirm_mode": {"STANDALONE"}}, http.StatusBadRequest},
		"other setting": {url.Values{"name": {"staff"}, "BatchSize": {"10"}}, http.StatusBadRequest},
	} {
		if rec := a.post(t, "/admin/groups/config", c.form, admin, true); rec.Code != c.code {
			t.Errorf("%s: status %d, want %d", name, rec.Code, c.code)
		}
	}
	if g := groupByName(t, s, "pilot"); g.ClientMode != store.ClientModeLockdown {
		t.Errorf("pilot changed to %q", g.ClientMode)
	}
	if len(auditOf(t, s)) != 0 {
		t.Error("refused changes were audited")
	}

	// Unchanged: nothing written
	rec := a.post(t, "/admin/groups/config", url.Values{"name": {"pilot"}, "ClientMode": {"LOCKDOWN"}, "orig_ClientMode": {"LOCKDOWN"}, "confirm_mode": {"LOCKDOWN"}}, admin, true)
	see303(t, rec, "/groups/pilot")
	if len(auditOf(t, s)) != 0 {
		t.Error("an unchanged form was audited")
	}

	if rec := a.post(t, "/admin/groups/config", url.Values{"name": {"staff"}, "ClientMode": {"MONITOR"}}, a.cookie(t, auth.RoleViewer), true); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status %d, want 403", rec.Code)
	}
	off := newEditApp(t, s, false)
	if rec := off.post(t, "/admin/groups/config", url.Values{"name": {"staff"}, "ClientMode": {"MONITOR"}}, off.cookie(t, auth.RoleAdmin), true); rec.Code != http.StatusNotFound {
		t.Errorf("editing off: status %d, want 404", rec.Code)
	}
}
