package web_test

// Review of the configuration editing and rule moving (before release):
//   - A form saved after the stored config changed must not quietly undo that change. Every setting input has a hidden
//     orig_<attribute> holding the value the form showed. A setting counts as changed only when the posted value
//     differs from orig_<attribute> (without orig_<attribute>, from the stored value, as before). If it was changed
//     on the form and the stored value is no longer what the form showed, the save is refused with 409 Conflict
//     ("changed since this page was loaded; reload it") and nothing is written.
//   - Inheriting the client mode moves the Mac to the global mode, so when that is not MONITOR it needs the global
//     mode's name typed in confirm_mode, as choosing that mode would.
//   - A move treats a rule the new Mac already has as already there only when it is the same rule as stored: same
//     policy, expiry, custom message and URL, and CEL expression. Any other difference is a conflict (400, named), so
//     removing the old Mac's copy can't lose what the new Mac's copy lacks.

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

func TestConfigEditing_FormCarriesWhatItShowed(t *testing.T) {
	s := configEditStore()
	a := newEditApp(t, s, true)
	body := a.get(t, "/machines/"+cfgMac, a.cookie(t, auth.RoleAdmin)).Body.String()
	mustContain(t, body, `name="orig_BatchSize" value="25"`, `name="orig_EnableBundles" value="yes"`, `name="orig_FullSyncInterval" value=""`)
	body = a.get(t, "/admin/config", a.cookie(t, auth.RoleAdmin)).Body.String()
	mustContain(t, body, `name="orig_ClientMode" value="MONITOR"`, `name="orig_BatchSize" value="50"`)
}

func TestConfigEditing_StaleFormDoesNotRevertOtherChanges(t *testing.T) {
	s := configEditStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)

	// Someone else (the CLI) puts the Mac into LOCKDOWN after the form was drawn with no mode of its own.
	lockdown := store.Config{Exists: true, ClientMode: store.ClientModeLockdown, BatchSize: ptr(25), EnableBundles: ptr(true)}
	s.SetMachineConfig(cfgMac, lockdown)

	form := url.Values{
		"ClientMode": {""}, "orig_ClientMode": {""},
		"BatchSize": {"100"}, "orig_BatchSize": {"25"},
		"EnableBundles": {"yes"}, "orig_EnableBundles": {"yes"},
	}
	see303(t, a.post(t, "/admin/machines/"+cfgMac+"/config", form, admin, true), "/machines/"+cfgMac)
	c := machineConfigOf(t, s)
	if c.ClientMode != store.ClientModeLockdown || c.BatchSize == nil || *c.BatchSize != 100 {
		t.Errorf("config %+v; want LOCKDOWN kept and BatchSize 100", c)
	}

	// The admin changes the batch size on a form that showed 25, but it is 100 now: conflict, nothing written.
	before := len(auditOf(t, s))
	rec := a.post(t, "/admin/machines/"+cfgMac+"/config", url.Values{"BatchSize": {"30"}, "orig_BatchSize": {"25"}}, admin, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale change: status %d, want 409", rec.Code)
	}
	mustContain(t, rec.Body.String(), "reload")
	if c := machineConfigOf(t, s); *c.BatchSize != 100 || len(auditOf(t, s)) != before {
		t.Errorf("a conflicting save wrote: %+v", c)
	}

	// The same holds on the global form.
	s.SetGlobalConfig(store.Config{Exists: true, ClientMode: store.ClientModeLockdown, BatchSize: ptr(50)})
	rec = a.post(t, "/admin/config", url.Values{"ClientMode": {"MONITOR"}, "orig_ClientMode": {"MONITOR"}, "BatchSize": {"60"}, "orig_BatchSize": {"50"}}, admin, true)
	see303(t, rec, "/admin/config")
	if g := globalConfigOf(t, s); g.ClientMode != store.ClientModeLockdown || *g.BatchSize != 60 {
		t.Errorf("global %+v; want LOCKDOWN kept and BatchSize 60", g)
	}
}

func TestConfigEditing_InheritingANonMonitorModeNeedsItsName(t *testing.T) {
	s := configEditStore()
	s.SetGlobalConfig(store.Config{Exists: true, ClientMode: store.ClientModeLockdown})
	s.SetMachineConfig(cfgMac, store.Config{Exists: true, ClientMode: store.ClientModeMonitor})
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	path := "/admin/machines/" + cfgMac + "/config"

	if rec := a.post(t, path, url.Values{"inherit": {"ClientMode"}}, admin, true); rec.Code != http.StatusBadRequest {
		t.Errorf("inheriting LOCKDOWN unconfirmed: status %d, want 400", rec.Code)
	}
	if c := machineConfigOf(t, s); c.ClientMode != store.ClientModeMonitor {
		t.Fatalf("unconfirmed inherit wrote: %+v", c)
	}
	see303(t, a.post(t, path, url.Values{"inherit": {"ClientMode"}, "confirm_mode": {"LOCKDOWN"}}, admin, true), "/machines/"+cfgMac)
	if c := machineConfigOf(t, s); c.ClientMode != "" {
		t.Errorf("confirmed inherit: %+v, want no mode of its own", c)
	}
}

func TestMoveRules_DifferentCopyOnTheNewMacIsAConflict(t *testing.T) {
	for name, held := range map[string]store.Rule{
		"expiry":  {RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: moveA, State: store.RuleStateInForce, CustomMessage: "Lab tool", ExpiresAt: fixedNow.Add(time.Hour)},
		"message": {RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: moveA, State: store.RuleStateInForce, CustomMessage: "Other"},
	} {
		s := moveStore()
		s.AddMachineRule(newMac, held)
		a := newEditApp(t, s, true)
		rec := a.post(t, "/admin/machines/"+oldMac+"/rules/move", url.Values{"to": {"C02NEW"}, "remove": {"on"}}, a.cookie(t, auth.RoleAdmin), true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s differs: status %d, want 400", name, rec.Code)
			continue
		}
		mustContain(t, rec.Body.String(), moveA)
		if len(auditOf(t, s)) != 0 {
			t.Errorf("%s differs: something was written", name)
		}
	}
}
