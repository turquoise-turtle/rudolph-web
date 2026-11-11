package web_test

// Editing configuration from the web view (steps S3 and S4 of the admin plan, owner approved 2026-11-11; admins,
// editing on, as every other write; builds on the configuration pages of config_page_test.go):
//   - The Mac's Configuration table and /admin/config are forms: one input per setting, named by its attribute
//     (ClientMode and OverrideFileAccessAction selects; booleans a select of "", yes and no; the others text inputs),
//     filled with the value the config stores. The Mac's form also has an inherit checkbox per setting
//     (name="inherit", value=<attribute>). Machine form: POST /admin/machines/{id}/config; global form: POST
//     /admin/config. Both return to their page (303).
//   - Only what changed is written: a setting missing from the POST, equal to the stored value, or blank when the
//     config doesn't store it, is left alone. If nothing changed: 303, nothing written or audited.
//   - The writes are store.Writer.UpdateMachineConfig(ctx, actor, id, store.ConfigChange) and
//     UpdateGlobalConfig(ctx, actor, store.ConfigChange), through Rudolph's machine configuration service (its
//     validation, lockdown confirmation and audit entry, config.update, summary as the CLI's: "BatchSize=100",
//     "BatchSize inherited").
//   - Changing the client mode to anything but MONITOR needs the new mode's name typed in confirm_mode (owner: typing
//     the mode name is enough); otherwise 400 and nothing written. On the global form this is what moves the fleet.
//   - The Mac's page has "Request a clean sync" (POST /admin/machines/{id}/clean-sync, action=request) or, while one is
//     requested, "Cancel the clean sync request" (action=cancel). Global clean syncs aren't offered.
//   - Refused with 400: values Rudolph refuses (batch size outside 1–500, full sync interval under 60 seconds), a
//     number that isn't one, an unknown mode or file access action, inheriting on the global form, a setting both set
//     and inherited, and USB settings (left out for now). Unknown Mac: 404. Viewers: 403.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

const cfgMac = "CCCCCCCC-3333-4333-8333-CCCCCCCCCCCC"

func configEditStore() *fake.Store {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: cfgMac, SerialNum: "C02CFG", Hostname: "cfg-mac", ClientMode: store.ClientModeMonitor, LastSeen: fixedNow})
	s.SetGlobalConfig(store.Config{Exists: true, ClientMode: store.ClientModeMonitor, BatchSize: ptr(50), FullSyncInterval: ptr(600)})
	s.SetMachineConfig(cfgMac, store.Config{Exists: true, BatchSize: ptr(25), EnableBundles: ptr(true)})
	return s
}

func machineConfigOf(t *testing.T, s *fake.Store) store.Config {
	t.Helper()
	m, err := s.GetMachine(t.Context(), cfgMac)
	if err != nil {
		t.Fatal(err)
	}
	return m.Config
}

func globalConfigOf(t *testing.T, s *fake.Store) store.Config {
	t.Helper()
	c, err := s.GlobalConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConfigEditing_FormsForAdminsWithEditingOn(t *testing.T) {
	s := configEditStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)

	body := a.get(t, "/machines/"+cfgMac, admin).Body.String()
	mustContain(t, body, `action="/admin/machines/`+cfgMac+`/config"`, `name="BatchSize" value="25"`, `name="inherit" value="BatchSize"`,
		`name="confirm_mode"`, `action="/admin/machines/`+cfgMac+`/clean-sync"`, "Request a clean sync")
	mustNotContain(t, body, `name="BlockUsbMount"`, `name="RemountUsbMode"`)

	body = a.get(t, "/admin/config", admin).Body.String()
	mustContain(t, body, `action="/admin/config"`, `name="BatchSize" value="50"`, `name="confirm_mode"`)
	mustNotContain(t, body, `name="inherit"`, "clean-sync", `name="BlockUsbMount"`)

	ro := newEditApp(t, s, false)
	body = ro.get(t, "/admin/config", ro.cookie(t, auth.RoleAdmin)).Body.String()
	mustNotContain(t, body, `method="post" action="/admin/config"`)
	_, body = a.viewerGet(t, "/machines/"+cfgMac)
	mustNotContain(t, body, "/config\"", "clean-sync")
}

func TestConfigEditing_MachineSettingAndInherit(t *testing.T) {
	s := configEditStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)

	rec := a.post(t, "/admin/machines/"+cfgMac+"/config", url.Values{"BatchSize": {"100"}, "EnableBundles": {"yes"}, "FullSyncInterval": {""}}, admin, true)
	see303(t, rec, "/machines/"+cfgMac)
	if c := machineConfigOf(t, s); c.BatchSize == nil || *c.BatchSize != 100 || c.FullSyncInterval != nil {
		t.Errorf("machine config %+v; want BatchSize 100 and FullSyncInterval still inherited", c)
	}

	rec = a.post(t, "/admin/machines/"+cfgMac+"/config", url.Values{"inherit": {"BatchSize", "EnableBundles"}}, admin, true)
	see303(t, rec, "/machines/"+cfgMac)
	if c := machineConfigOf(t, s); c.BatchSize != nil || c.EnableBundles != nil {
		t.Errorf("machine config %+v; want BatchSize and EnableBundles inherited", c)
	}

	entries := auditOf(t, s)
	if len(entries) != 2 {
		t.Fatalf("audit %+v; want two config.update entries (the unchanged EnableBundles and blank interval not written)", entries)
	}
	if e := entries[1]; e.Action != "config.update" || e.Target != "machine:"+cfgMac || e.Summary != "BatchSize=100" {
		t.Errorf("first entry %+v; want config.update machine:%s BatchSize=100", e, cfgMac)
	}
	if e := entries[0]; !strings.Contains(e.Summary, "BatchSize inherited") || !strings.Contains(e.Summary, "EnableBundles inherited") {
		t.Errorf("second entry %+v", e)
	}

	// Nothing changed: no write, no audit.
	rec = a.post(t, "/admin/machines/"+cfgMac+"/config", url.Values{"BatchSize": {""}, "EnableBundles": {""}}, admin, true)
	see303(t, rec, "/machines/"+cfgMac)
	if len(auditOf(t, s)) != 2 {
		t.Error("an unchanged form was audited")
	}
}

func TestConfigEditing_ModeNeedsItsNameTyped(t *testing.T) {
	for _, c := range []struct {
		path, back string
		read       func(*testing.T, *fake.Store) store.Config
	}{
		{"/admin/machines/" + cfgMac + "/config", "/machines/" + cfgMac, machineConfigOf},
		{"/admin/config", "/admin/config", globalConfigOf},
	} {
		s := configEditStore()
		a := newEditApp(t, s, true)
		admin := a.cookie(t, auth.RoleAdmin)
		for _, confirm := range []string{"", "lockdown please", "MONITOR"} {
			if rec := a.post(t, c.path, url.Values{"ClientMode": {"LOCKDOWN"}, "confirm_mode": {confirm}}, admin, true); rec.Code != http.StatusBadRequest {
				t.Errorf("%s with confirm %q: status %d, want 400", c.path, confirm, rec.Code)
			}
		}
		if len(auditOf(t, s)) != 0 {
			t.Fatalf("%s: unconfirmed mode change was written", c.path)
		}
		rec := a.post(t, c.path, url.Values{"ClientMode": {"LOCKDOWN"}, "confirm_mode": {" lockdown "}}, admin, true)
		see303(t, rec, c.back)
		if got := c.read(t, s).ClientMode; got != store.ClientModeLockdown {
			t.Errorf("%s: mode %q after a confirmed change", c.path, got)
		}
		// Back to MONITOR needs no confirmation.
		rec = a.post(t, c.path, url.Values{"ClientMode": {"MONITOR"}}, admin, true)
		see303(t, rec, c.back)
	}
}

func TestConfigEditing_GlobalChangeIsAuditedAsGlobal(t *testing.T) {
	s := configEditStore()
	a := newEditApp(t, s, true)
	rec := a.post(t, "/admin/config", url.Values{"BatchSize": {"75"}, "EnableAllEventUpload": {"yes"}}, a.cookie(t, auth.RoleAdmin), true)
	see303(t, rec, "/admin/config")
	c := globalConfigOf(t, s)
	if c.BatchSize == nil || *c.BatchSize != 75 || c.EnableAllEventUpload == nil || !*c.EnableAllEventUpload {
		t.Errorf("global config %+v", c)
	}
	entries := auditOf(t, s)
	if len(entries) != 1 || entries[0].Action != "config.update" || entries[0].Target != "global" ||
		!strings.Contains(entries[0].Summary, "BatchSize=75") || !strings.Contains(entries[0].Summary, "EnableAllEventUpload=true") {
		t.Errorf("audit %+v", entries)
	}
}

func TestConfigEditing_CleanSyncRequestAndCancel(t *testing.T) {
	s := configEditStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	see303(t, a.post(t, "/admin/machines/"+cfgMac+"/clean-sync", url.Values{"action": {"request"}}, admin, true), "/machines/"+cfgMac)
	body := a.get(t, "/machines/"+cfgMac, admin).Body.String()
	mustContain(t, body, "Cancel the clean sync request")
	see303(t, a.post(t, "/admin/machines/"+cfgMac+"/clean-sync", url.Values{"action": {"cancel"}}, admin, true), "/machines/"+cfgMac)
	body = a.get(t, "/machines/"+cfgMac, admin).Body.String()
	mustContain(t, body, "Request a clean sync")
	entries := auditOf(t, s)
	if len(entries) != 2 || entries[1].Summary != "CleanSync=true" || entries[0].Summary != "CleanSync=false" {
		t.Errorf("audit %+v; want CleanSync=true then CleanSync=false", entries)
	}
	if rec := a.post(t, "/admin/machines/"+cfgMac+"/clean-sync", url.Values{"action": {"maybe"}}, admin, true); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown action: status %d", rec.Code)
	}
}

func TestConfigEditing_Refusals(t *testing.T) {
	machine := "/admin/machines/" + cfgMac + "/config"
	for name, c := range map[string]struct {
		path string
		form url.Values
		code int
	}{
		"batch size 0":            {machine, url.Values{"BatchSize": {"0"}}, http.StatusBadRequest},
		"batch size 501":          {"/admin/config", url.Values{"BatchSize": {"501"}}, http.StatusBadRequest},
		"interval 30":             {machine, url.Values{"FullSyncInterval": {"30"}}, http.StatusBadRequest},
		"not a number":            {machine, url.Values{"BatchSize": {"lots"}}, http.StatusBadRequest},
		"unknown mode":            {machine, url.Values{"ClientMode": {"PARANOID"}, "confirm_mode": {"PARANOID"}}, http.StatusBadRequest},
		"unknown FAA action":      {"/admin/config", url.Values{"OverrideFileAccessAction": {"MAYBE"}}, http.StatusBadRequest},
		"bad boolean":             {machine, url.Values{"EnableBundles": {"sometimes"}}, http.StatusBadRequest},
		"global inherit":          {"/admin/config", url.Values{"inherit": {"BatchSize"}}, http.StatusBadRequest},
		"set and inherit":         {machine, url.Values{"BatchSize": {"30"}, "inherit": {"BatchSize"}}, http.StatusBadRequest},
		"unknown inherit":         {machine, url.Values{"inherit": {"Nonsense"}}, http.StatusBadRequest},
		"USB setting":             {machine, url.Values{"BlockUsbMount": {"yes"}}, http.StatusBadRequest},
		"global clean sync field": {"/admin/config", url.Values{"CleanSync": {"yes"}}, http.StatusBadRequest},
		"unknown Mac":             {"/admin/machines/NO-SUCH-MAC/config", url.Values{"BatchSize": {"30"}}, http.StatusNotFound},
	} {
		s := configEditStore()
		a := newEditApp(t, s, true)
		if rec := a.post(t, c.path, c.form, a.cookie(t, auth.RoleAdmin), true); rec.Code != c.code {
			t.Errorf("%s: status %d, want %d", name, rec.Code, c.code)
		}
		if len(auditOf(t, s)) != 0 {
			t.Errorf("%s: written", name)
		}
	}
	s := configEditStore()
	a := newEditApp(t, s, true)
	for _, path := range []string{machine, "/admin/config", "/admin/machines/" + cfgMac + "/clean-sync"} {
		if rec := a.post(t, path, url.Values{"BatchSize": {"30"}, "action": {"request"}}, a.cookie(t, auth.RoleViewer), true); rec.Code != http.StatusForbidden {
			t.Errorf("viewer POST %s: status %d, want 403", path, rec.Code)
		}
	}
}
