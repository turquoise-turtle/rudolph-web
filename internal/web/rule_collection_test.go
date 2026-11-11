package web_test

// Collecting a rule's allowed executions from the web view (step S2 of the admin plan): Santa uploads allowed
// executions only with EnableAllEventUpload, and Rudolph stores those of rules flagged CollectAllowedEvents.
//   - Every global and machine rule list marks a collecting rule "collecting allowed executions", for everyone.
//   - With editing on, admins get a button on each allowing rule (ALLOWLIST, ALLOWLIST_COMPILER) that is in force:
//     "Collect allowed executions", or "Stop collecting" on a collecting rule. It posts target, machine, type,
//     identifier and collect=on|off to /admin/rules/collect and returns to the list (303), as removing does. Blocking
//     rules get no button: blocked executions are always stored. Group rules get none: Rudolph can't collect them.
//   - Turning collection on while the global config doesn't set EnableAllEventUpload to yes says so on the rules page
//     (nothing would be uploaded from Macs that don't upload allowed events).
//   - A change that changes nothing is still a 303, with nothing audited; an unknown rule is 404; group or unknown
//     targets are 400.

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

var (
	collectAllow = strings.Repeat("a1", 32)
	collectBlock = strings.Repeat("b2", 32)
)

func collectStore() *fake.Store {
	s := fake.New()
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: collectAllow, State: store.RuleStateInForce})
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyBlocklist, Identifier: collectBlock, State: store.RuleStateInForce})
	s.AddMachine(store.Machine{MachineID: editMachine, SerialNum: "SER1", Hostname: "lab-mac-01", LastSeen: fixedNow})
	s.AddMachineRule(editMachine, store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: collectAllow, State: store.RuleStateInForce, CollectAllowedEvents: true})
	s.AddGroup(store.Group{Name: "eng", AcceptReported: true})
	s.AddGroupRule("eng", store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: collectAllow, State: store.RuleStateInForce})
	return s
}

// ruleRow returns the table row of the rule with this identifier.
func ruleRow(t *testing.T, body, identifier string) string {
	t.Helper()
	for _, m := range regexp.MustCompile(`(?s)<tr data-row[^>]*>.*?</tr>`).FindAllString(body, -1) {
		if strings.Contains(m, identifier) {
			return m
		}
	}
	t.Fatalf("no row for %s", identifier)
	return ""
}

func TestRuleCollection_ButtonsOnAllowingRulesOnly(t *testing.T) {
	s := collectStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)

	body := a.get(t, "/rules", admin).Body.String()
	allow := ruleRow(t, body, collectAllow)
	mustContain(t, allow, `action="/admin/rules/collect"`, `name="collect" value="on"`, "Collect allowed executions")
	mustNotContain(t, ruleRow(t, body, collectBlock), `action="/admin/rules/collect"`)

	body = a.get(t, "/machines/"+editMachine, admin).Body.String()
	row := ruleRow(t, body, collectAllow)
	mustContain(t, row, "collecting allowed executions", `name="collect" value="off"`, "Stop collecting")

	body = a.get(t, "/groups/eng", admin).Body.String()
	mustNotContain(t, ruleRow(t, body, collectAllow), `action="/admin/rules/collect"`)

	// Viewers see the mark but no button.
	_, body = a.viewerGet(t, "/machines/"+editMachine)
	mustContain(t, ruleRow(t, body, collectAllow), "collecting allowed executions")
	mustNotContain(t, body, `action="/admin/rules/collect"`)
}

func TestRuleCollection_TurnOnAndOff(t *testing.T) {
	s := collectStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)

	rec := a.post(t, "/admin/rules/collect", url.Values{"target": {"global"}, "type": {"BINARY"}, "identifier": {collectAllow}, "collect": {"on"}}, admin, true)
	see303(t, rec, "/rules")
	body := a.get(t, "/rules", admin).Body.String()
	mustContain(t, ruleRow(t, body, collectAllow), "collecting allowed executions", "Stop collecting")

	rec = a.post(t, "/admin/rules/collect", url.Values{"target": {"machine"}, "machine": {editMachine}, "type": {"BINARY"}, "identifier": {collectAllow}, "collect": {"off"}}, admin, true)
	see303(t, rec, "/machines/"+editMachine)

	// Already off: nothing to change, nothing recorded.
	rec = a.post(t, "/admin/rules/collect", url.Values{"target": {"machine"}, "machine": {editMachine}, "type": {"BINARY"}, "identifier": {collectAllow}, "collect": {"off"}}, admin, true)
	see303(t, rec, "/machines/"+editMachine)

	entries := auditOf(t, s)
	if len(entries) != 2 {
		t.Fatalf("audit %+v; want two entries", entries)
	}
	if e := entries[1]; e.Action != "rule.update" || e.Target != "global" || !strings.HasSuffix(e.Summary, "collect allowed events true") {
		t.Errorf("first entry %+v", e)
	}
	if e := entries[0]; e.Action != "rule.update" || e.Target != "machine:"+editMachine || !strings.HasSuffix(e.Summary, "collect allowed events false") {
		t.Errorf("second entry %+v", e)
	}
}

func TestRuleCollection_Refusals(t *testing.T) {
	s := collectStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	for name, c := range map[string]struct {
		form url.Values
		code int
	}{
		"unknown rule":   {url.Values{"target": {"global"}, "type": {"BINARY"}, "identifier": {strings.Repeat("cd", 32)}, "collect": {"on"}}, http.StatusNotFound},
		"group rule":     {url.Values{"target": {"group"}, "group": {"eng"}, "type": {"BINARY"}, "identifier": {collectAllow}, "collect": {"on"}}, http.StatusBadRequest},
		"bad collect":    {url.Values{"target": {"global"}, "type": {"BINARY"}, "identifier": {collectAllow}, "collect": {"maybe"}}, http.StatusBadRequest},
		"unknown target": {url.Values{"target": {"fleet"}, "type": {"BINARY"}, "identifier": {collectAllow}, "collect": {"on"}}, http.StatusBadRequest},
	} {
		if rec := a.post(t, "/admin/rules/collect", c.form, admin, true); rec.Code != c.code {
			t.Errorf("%s: status %d, want %d", name, rec.Code, c.code)
		}
	}
	if rec := a.post(t, "/admin/rules/collect", url.Values{"target": {"global"}, "type": {"BINARY"}, "identifier": {collectAllow}, "collect": {"on"}}, a.cookie(t, auth.RoleViewer), true); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status %d, want 403", rec.Code)
	}
	if len(auditOf(t, s)) != 0 {
		t.Error("refusals were audited")
	}
}

func TestRuleCollection_WarnsWhenAllowedEventsAreNotUploaded(t *testing.T) {
	s := collectStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	body := a.get(t, "/rules", admin).Body.String()
	mustContain(t, body, "EnableAllEventUpload")

	yes := true
	s.SetGlobalConfig(store.Config{Exists: true, ClientMode: store.ClientModeMonitor, EnableAllEventUpload: &yes})
	body = a.get(t, "/rules", admin).Body.String()
	mustNotContain(t, body, "EnableAllEventUpload")
}
