package web_test

// File access override per device group in the web view (Rudolph 019bf42, docs/groups.md "File access override per
// group" and docs/deploy.md step 29). Rudolph stores it on the group's registry entry (OverrideFileAccessAction) and
// sends a Mac, in order: its own configuration's override, the most relaxed override of its groups (DISABLE, then
// AUDIT_ONLY, then NONE; between equal ones the group whose name sorts first; rudolph:ungrouped for a Mac in no
// group), then the global one.
//   - store.Group gains FileAccessOverride ("NONE", "AUDIT_ONLY", "DISABLE", or "" when the group has none).
//   - A group's page (ungrouped included) shows "Override file access action" in its <dl> with the override, or "not
//     set" when its Macs inherit one, for everyone.
//   - Admins with editing on get it in the folded settings form (<details class="edit config-edit">) beside the
//     client mode: a row <tr data-edit-setting="OverrideFileAccessAction"> with a select name="OverrideFileAccessAction"
//     ("" not set, NONE, AUDIT_ONLY, DISABLE; the current one selected) and a hidden orig_OverrideFileAccessAction. It
//     posts to POST /admin/groups/config, which now takes either setting, or both (a form without ClientMode changes
//     only the override), and returns to the group's page (303).
//   - The write is store.Writer.SetGroupFileAccessOverride(ctx, actor, group, action) ("" removes it), recording the
//     CLI's config.update on group:<name> or ungrouped with summary "OverrideFileAccessAction=<ACTION>" or
//     "OverrideFileAccessAction inherited". No confirmation is needed (the CLI asks for none). Left as the form showed
//     it: nothing written. Refused with nothing written or audited: a value that is not one of the four (400), a stale
//     form whose orig_OverrideFileAccessAction is no longer the stored override (409), an unregistered group (404).
//     Viewers 403; editing off 404; cross-site 403; GET 405.
//   - The group's page offers the CLI commands (docs/cli.md: Rudolph's cli-examples.txt has no line for them):
//     `rudolph config update --group '<name>'|--ungrouped --override-file-access-action <none|audit_only|disable>
//     --yes` for each value but the current one, and `--inherit override-file-access-action --yes` when the group has
//     one.
//   - A Mac's Configuration table (data-setting="OverrideFileAccessAction") marks an override from a group
//     "(group <name>)", worked out as Rudolph does from the one ListGroups read the page already makes.

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

const overrideMac = "EEEEEEEE-5555-4555-8555-EEEEEEEEEEEE"

func groupFAAStore() *fake.Store {
	s := fake.New()
	s.SetGlobalConfig(store.Config{Exists: true, ClientMode: store.ClientModeMonitor, OverrideFileAccessAction: "NONE"})
	s.AddGroup(store.Group{Name: "pilot", Description: "FAA pilot", AcceptReported: true, ClientMode: store.ClientModeLockdown, FileAccessOverride: "AUDIT_ONLY"})
	s.AddGroup(store.Group{Name: "lab", AcceptReported: true, FileAccessOverride: "DISABLE"})
	s.AddGroup(store.Group{Name: "staff", Description: "Staff", AcceptReported: true})
	s.AddGroup(store.Group{Name: store.UngroupedGroup})
	s.AddMachine(store.Machine{MachineID: overrideMac, SerialNum: "C02FAA", Hostname: "faa-mac", ClientMode: store.ClientModeMonitor, LastSeen: fixedNow})
	s.SetMachineGroups(overrideMac, []string{"pilot", "staff"}, nil, nil)
	return s
}

// faaForm is the group settings form as a browser sends it, with the client mode left as shown.
func faaForm(group string, g *store.Group, action string) url.Values {
	return url.Values{
		"name":                          {group},
		"ClientMode":                    {string(g.ClientMode)},
		"orig_ClientMode":               {string(g.ClientMode)},
		"OverrideFileAccessAction":      {action},
		"orig_OverrideFileAccessAction": {g.FileAccessOverride},
		"confirm_mode":                  {""},
	}
}

var faaTerm = regexp.MustCompile(`(?s)<dt>Override file access action</dt>\s*<dd>(.*?)</dd>`)

func faaShown(t *testing.T, body string) string {
	t.Helper()
	m := faaTerm.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no <dt>Override file access action</dt> on the group's page")
	}
	return m[1]
}

func TestGroupFileAccess_ShownOnGroupPages(t *testing.T) {
	a := newApp(t, groupFAAStore(), 10)
	for name, want := range map[string]string{"pilot": "AUDIT_ONLY", "lab": "DISABLE", "staff": "not set", store.UngroupedGroup: "not set"} {
		_, body := a.viewerGet(t, groupURLFor(name))
		mustContain(t, faaShown(t, body), want)
		mustNotContain(t, body, `name="OverrideFileAccessAction"`)
	}
	// Unregistered, ungrouped still has a page, with no override.
	s := fake.New()
	_, body := newApp(t, s, 10).viewerGet(t, groupURLFor(store.UngroupedGroup))
	mustContain(t, faaShown(t, body), "not set")
}

func TestGroupFileAccess_AdminForm(t *testing.T) {
	s := groupFAAStore()
	a := newEditApp(t, s, true)
	for name, current := range map[string]string{"pilot": "AUDIT_ONLY", "staff": "", store.UngroupedGroup: ""} {
		_, body := adminGet(t, a, groupURLFor(name))
		m := configEditDetails.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("%s: no folded Change settings form", name)
		}
		form := m[1]
		mustContain(t, form, `action="/admin/groups/config"`, `<tr data-edit-setting="OverrideFileAccessAction">`,
			`name="OverrideFileAccessAction"`, `name="orig_OverrideFileAccessAction" value="`+current+`"`,
			`<option value="NONE"`, `<option value="AUDIT_ONLY"`, `<option value="DISABLE"`,
			// The client mode stays in the same form.
			`name="ClientMode"`)
		rm := regexp.MustCompile(`(?s)<tr data-edit-setting="OverrideFileAccessAction">(.*?)</tr>`).FindStringSubmatch(form)
		if rm == nil {
			continue
		}
		row := rm[1]
		mustContain(t, row, `<option value=""`)
		if current != "" {
			mustContain(t, row, `<option value="`+current+`" selected>`)
		} else {
			mustContain(t, row, `<option value="" selected>`)
		}
	}
}

func TestGroupFileAccess_SetAndClear(t *testing.T) {
	s := groupFAAStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)

	for _, action := range []string{"DISABLE", "NONE", "AUDIT_ONLY"} {
		see303(t, a.post(t, "/admin/groups/config", faaForm("staff", groupByName(t, s, "staff"), action), admin, true), "/groups/staff")
		if g := groupByName(t, s, "staff"); g.FileAccessOverride != action {
			t.Errorf("staff override %q, want %s", g.FileAccessOverride, action)
		}
	}
	// Clearing pilot's override leaves its LOCKDOWN mode alone, with no confirmation asked.
	see303(t, a.post(t, "/admin/groups/config", faaForm("pilot", groupByName(t, s, "pilot"), ""), admin, true), "/groups/pilot")
	if g := groupByName(t, s, "pilot"); g.FileAccessOverride != "" || g.ClientMode != store.ClientModeLockdown {
		t.Errorf("pilot %+v after clearing, want no override and LOCKDOWN kept", g)
	}
	see303(t, a.post(t, "/admin/groups/config", faaForm(store.UngroupedGroup, groupByName(t, s, store.UngroupedGroup), "AUDIT_ONLY"), admin, true), groupURLFor(store.UngroupedGroup))
	if g := groupByName(t, s, store.UngroupedGroup); g.FileAccessOverride != "AUDIT_ONLY" {
		t.Errorf("ungrouped override %q, want AUDIT_ONLY", g.FileAccessOverride)
	}
	// A form that sends only the override changes only the override.
	see303(t, a.post(t, "/admin/groups/config", url.Values{"name": {"lab"}, "OverrideFileAccessAction": {"NONE"}, "orig_OverrideFileAccessAction": {"DISABLE"}}, admin, true), "/groups/lab")
	if g := groupByName(t, s, "lab"); g.FileAccessOverride != "NONE" || g.ClientMode != "" {
		t.Errorf("lab %+v, want NONE and still no client mode", g)
	}

	want := []string{
		"config.update group:staff OverrideFileAccessAction=DISABLE",
		"config.update group:staff OverrideFileAccessAction=NONE",
		"config.update group:staff OverrideFileAccessAction=AUDIT_ONLY",
		"config.update group:pilot OverrideFileAccessAction inherited",
		"config.update ungrouped OverrideFileAccessAction=AUDIT_ONLY",
		"config.update group:lab OverrideFileAccessAction=NONE",
	}
	entries := auditOf(t, s)
	if len(entries) != len(want) {
		t.Fatalf("audit %+v, want %d entries", entries, len(want))
	}
	for i, e := range entries {
		// auditOf is newest first.
		if got := e.Action + " " + e.Target + " " + e.Summary; got != want[len(want)-1-i] {
			t.Errorf("audit entry %q, want %q", got, want[len(want)-1-i])
		}
		if !strings.HasPrefix(e.Actor, "web:") {
			t.Errorf("audit actor %q, want the web actor", e.Actor)
		}
	}
}

func TestGroupFileAccess_UnchangedWritesNothing(t *testing.T) {
	s := groupFAAStore()
	a := newEditApp(t, s, true)
	see303(t, a.post(t, "/admin/groups/config", faaForm("pilot", groupByName(t, s, "pilot"), "AUDIT_ONLY"), a.cookie(t, auth.RoleAdmin), true), "/groups/pilot")
	if len(auditOf(t, s)) != 0 {
		t.Error("an unchanged form was audited")
	}
}

func TestGroupFileAccess_Refusals(t *testing.T) {
	s := groupFAAStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	pilot := *groupByName(t, s, "pilot")
	stale := faaForm("pilot", &pilot, "DISABLE")
	stale.Set("orig_OverrideFileAccessAction", "")
	twice := faaForm("pilot", &pilot, "DISABLE")
	twice.Add("OverrideFileAccessAction", "NONE")
	for name, c := range map[string]struct {
		form url.Values
		code int
	}{
		"unknown value": {faaForm("pilot", &pilot, "PERMISSIVE"), http.StatusBadRequest},
		"injected":      {faaForm("pilot", &pilot, "AUDIT_ONLY\" onclick=\"x"), http.StatusBadRequest},
		"sent twice":    {twice, http.StatusBadRequest},
		"stale form":    {stale, http.StatusConflict},
		"unknown group": {faaForm("nope", &store.Group{}, "NONE"), http.StatusNotFound},
	} {
		if rec := a.post(t, "/admin/groups/config", c.form, admin, true); rec.Code != c.code {
			t.Errorf("%s: status %d, want %d", name, rec.Code, c.code)
		}
	}
	if g := groupByName(t, s, "pilot"); g.FileAccessOverride != "AUDIT_ONLY" {
		t.Errorf("refusals changed pilot to %q", g.FileAccessOverride)
	}
	if groupByName(t, s, "nope") != nil {
		t.Error("an unknown group was registered")
	}
	if len(auditOf(t, s)) != 0 {
		t.Error("refused changes were audited")
	}
}

func TestGroupFileAccess_OnlyAdminsWithEditing(t *testing.T) {
	s := groupFAAStore()
	a := newEditApp(t, s, true)
	form := faaForm("staff", groupByName(t, s, "staff"), "DISABLE")

	_, body := a.viewerGet(t, "/groups/staff")
	mustNotContain(t, body, `name="OverrideFileAccessAction"`, `action="/admin/groups/config"`)
	if rec := a.post(t, "/admin/groups/config", form, a.cookie(t, auth.RoleViewer), true); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status %d, want 403", rec.Code)
	}
	if rec := a.post(t, "/admin/groups/config", form, a.cookie(t, auth.RoleAdmin), false); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: status %d, want 403", rec.Code)
	}
	if rec := a.get(t, "/admin/groups/config", a.cookie(t, auth.RoleAdmin)); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d, want 405", rec.Code)
	}

	off := newEditApp(t, s, false)
	_, body = adminGet(t, off, "/groups/staff")
	mustNotContain(t, body, `name="OverrideFileAccessAction"`, `action="/admin/groups/config"`)
	if rec := off.post(t, "/admin/groups/config", form, off.cookie(t, auth.RoleAdmin), true); rec.Code != http.StatusNotFound {
		t.Errorf("editing off: status %d, want 404", rec.Code)
	}
	if g := groupByName(t, s, "staff"); g.FileAccessOverride != "" {
		t.Errorf("refused POSTs changed staff to %q", g.FileAccessOverride)
	}
	if len(auditOf(t, s)) != 0 {
		t.Error("refused POSTs were audited")
	}
}

func TestGroupFileAccess_Commands(t *testing.T) {
	a := newAppWithCLIEnv(t, groupFAAStore(), "prod")
	_, pilot := a.viewerGet(t, "/groups/pilot")
	mustContainCommand(t, pilot, "ENV=prod rudolph config update --group 'pilot' --override-file-access-action none --yes")
	mustContainCommand(t, pilot, "ENV=prod rudolph config update --group 'pilot' --override-file-access-action disable --yes")
	mustContainCommand(t, pilot, "ENV=prod rudolph config update --group 'pilot' --inherit override-file-access-action --yes")
	mustNotContain(t, pilot, "--override-file-access-action audit_only")

	_, staff := a.viewerGet(t, "/groups/staff")
	for _, v := range []string{"none", "audit_only", "disable"} {
		mustContainCommand(t, staff, "ENV=prod rudolph config update --group 'staff' --override-file-access-action "+v+" --yes")
	}
	mustNotContain(t, staff, "--inherit override-file-access-action")

	_, ungrouped := a.viewerGet(t, groupURLFor(store.UngroupedGroup))
	mustContainCommand(t, ungrouped, "ENV=prod rudolph config update --ungrouped --override-file-access-action audit_only --yes")
}

func TestGroupFileAccess_MachinePage(t *testing.T) {
	row := func(t *testing.T, s *fake.Store) string {
		t.Helper()
		_, body := newApp(t, s, 10).viewerGet(t, "/machines/"+overrideMac)
		return settingRow(t, body, "OverrideFileAccessAction")
	}

	// pilot (AUDIT_ONLY) and staff (none): pilot's, ahead of the global NONE.
	s := groupFAAStore()
	s.ResetCalls()
	got := row(t, s)
	mustContain(t, got, "AUDIT_ONLY", "(group pilot)")
	mustNotContain(t, got, "(global config)")
	n := 0
	for _, c := range s.Calls() {
		if c.Method == "ListGroups" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d ListGroups calls, want 1", n)
	}

	// The most relaxed of its groups wins: lab's DISABLE over pilot's AUDIT_ONLY.
	s = groupFAAStore()
	s.SetMachineGroups(overrideMac, []string{"pilot", "lab"}, nil, nil)
	mustContain(t, row(t, s), "DISABLE", "(group lab)")

	// Between equal overrides, the group whose name sorts first.
	s = groupFAAStore()
	s.AddGroup(store.Group{Name: "b-audit", AcceptReported: true, FileAccessOverride: "AUDIT_ONLY"})
	s.AddGroup(store.Group{Name: "a-audit", AcceptReported: true, FileAccessOverride: "AUDIT_ONLY"})
	s.SetMachineGroups(overrideMac, []string{"b-audit", "a-audit"}, nil, nil)
	mustContain(t, row(t, s), "AUDIT_ONLY", "(group a-audit)")

	// A group holding NONE beats a global DISABLE.
	s = groupFAAStore()
	s.SetGlobalConfig(store.Config{Exists: true, ClientMode: store.ClientModeMonitor, OverrideFileAccessAction: "DISABLE"})
	s.AddGroup(store.Group{Name: "strict", AcceptReported: true, FileAccessOverride: "NONE"})
	s.SetMachineGroups(overrideMac, []string{"strict"}, nil, nil)
	mustContain(t, row(t, s), "NONE", "(group strict)")

	// The Mac's own override wins over its groups'.
	s = groupFAAStore()
	s.SetMachineConfig(overrideMac, store.Config{Exists: true, OverrideFileAccessAction: "NONE"})
	mustContain(t, row(t, s), "NONE", "(this Mac)")

	// A Mac in no group gets ungrouped's.
	s = groupFAAStore()
	s.AddGroup(store.Group{Name: store.UngroupedGroup, FileAccessOverride: "DISABLE"})
	s.SetMachineGroups(overrideMac, nil, nil, nil)
	mustContain(t, row(t, s), "DISABLE", "(group "+store.UngroupedGroup+")")

	// No group override: the global one.
	s = groupFAAStore()
	s.SetMachineGroups(overrideMac, []string{"staff"}, nil, nil)
	got = row(t, s)
	mustContain(t, got, "NONE", "(global config)")
	mustNotContain(t, got, "(group ")
}
