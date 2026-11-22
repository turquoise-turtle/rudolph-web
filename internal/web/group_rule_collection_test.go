package web_test

// Collecting the allowed executions of a device group's rules (Rudolph 019bf42, docs/groups.md "Collecting what a
// group rule allows", docs/deploy.md step 30), as the global and machine rule lists already do (rule_collection_test.go):
//   - A group's rule list (ungrouped included) marks a flagged rule "collecting allowed executions", for everyone.
//   - With editing on, admins get the same "collectrule" form on each allowing rule in force (ALLOWLIST,
//     ALLOWLIST_COMPILER), or on any flagged rule: "Collect allowed executions" (collect=on) or "Stop collecting"
//     (collect=off), with hidden target=group and group=<name>. It posts to POST /admin/rules/collect and returns to
//     the group's page (303). Blocking rules get no button.
//   - The write is store.Writer.SetRuleCollection with a group target, recording the CLI's rule.update on group:<name>
//     or ungrouped, summary "<TYPE> rule <identifier> <POLICY>, collect allowed events true|false". Already as asked:
//     303, nothing recorded. Flagging a block is 400; a missing rule, or a group that is not registered, 404; nothing
//     written or audited. Viewers 403, editing off 404, cross-site 403, GET 405.
//   - Every rule that can be flagged offers the CLI command `rudolph rule update -t <type> -i '<id>' --group '<name>'
//     --collect-allowed-events=true|false --yes` (`--ungrouped` for rudolph:ungrouped), as in Rudolph's
//     cli-examples.txt lines 16 to 18.
//   - As on the global rules page, admins with editing on are told when the global configuration does not turn on
//     EnableAllEventUpload; a viewer's page does not read the global configuration for it.
// The group's RulesVersion is not moved (store tests: the flag is never sent to Santa).

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

const (
	groupAllowTeam  = "ABCDE12345"
	groupBlockSigID = "EQHXZ8M8AV:com.google.GeminiMacOS"
)

var groupCompilerSHA = strings.Repeat("c3", 32)

func groupCollectStore() *fake.Store {
	s := fake.New()
	s.AddGroup(store.Group{Name: "eng", AcceptReported: true})
	s.AddGroup(store.Group{Name: store.UngroupedGroup})
	s.AddGroupRule("eng", store.Rule{RuleType: store.RuleTypeTeamID, Policy: store.PolicyAllowlist, Identifier: groupAllowTeam, State: store.RuleStateInForce})
	s.AddGroupRule("eng", store.Rule{RuleType: store.RuleTypeSigningID, Policy: store.PolicyBlocklist, Identifier: groupBlockSigID, State: store.RuleStateInForce})
	s.AddGroupRule("eng", store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlistCompiler, Identifier: groupCompilerSHA, State: store.RuleStateInForce, CollectAllowedEvents: true})
	s.AddGroupRule(store.UngroupedGroup, store.Rule{RuleType: store.RuleTypeTeamID, Policy: store.PolicyAllowlist, Identifier: groupAllowTeam, State: store.RuleStateInForce})
	return s
}

func groupCollectPost(group string, ruleType store.RuleType, identifier, collect string) url.Values {
	return url.Values{"target": {"group"}, "group": {group}, "type": {string(ruleType)}, "identifier": {identifier}, "collect": {collect}}
}

func groupRuleFlag(t *testing.T, s *fake.Store, group string, ruleType store.RuleType, identifier string) bool {
	t.Helper()
	p, err := s.ListGroupRules(t.Context(), group, store.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range p.Items {
		if r.RuleType == ruleType && r.Identifier == identifier {
			return r.CollectAllowedEvents
		}
	}
	t.Fatalf("group %s has no %s rule %s", group, ruleType, identifier)
	return false
}

func TestGroupRuleCollection_MarkedForEveryone(t *testing.T) {
	s := groupCollectStore()
	a := newEditApp(t, s, true)
	_, body := a.viewerGet(t, "/groups/eng")
	mustContain(t, ruleRow(t, body, groupCompilerSHA), "collecting allowed executions")
	mustNotContain(t, ruleRow(t, body, groupAllowTeam), "collecting allowed executions")
	mustNotContain(t, body, `action="/admin/rules/collect"`)

	s.AddGroupRule(store.UngroupedGroup, store.Rule{RuleType: store.RuleTypeTeamID, Policy: store.PolicyAllowlist, Identifier: groupAllowTeam, State: store.RuleStateInForce, CollectAllowedEvents: true})
	_, body = a.viewerGet(t, groupURLFor(store.UngroupedGroup))
	mustContain(t, ruleRow(t, body, groupAllowTeam), "collecting allowed executions")
}

func TestGroupRuleCollection_ButtonsOnAllowingRulesOnly(t *testing.T) {
	a := newEditApp(t, groupCollectStore(), true)
	_, body := adminGet(t, a, "/groups/eng")
	mustContain(t, ruleRow(t, body, groupAllowTeam), `action="/admin/rules/collect"`, `name="target" value="group"`, `name="group" value="eng"`,
		`name="collect" value="on"`, "Collect allowed executions")
	mustContain(t, ruleRow(t, body, groupCompilerSHA), `action="/admin/rules/collect"`, `name="collect" value="off"`, "Stop collecting")
	mustNotContain(t, ruleRow(t, body, groupBlockSigID), `action="/admin/rules/collect"`)

	_, body = adminGet(t, a, groupURLFor(store.UngroupedGroup))
	mustContain(t, ruleRow(t, body, groupAllowTeam), `action="/admin/rules/collect"`, `name="group" value="`+store.UngroupedGroup+`"`, `name="collect" value="on"`)
}

func TestGroupRuleCollection_SetAndClear(t *testing.T) {
	s := groupCollectStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)

	see303(t, a.post(t, "/admin/rules/collect", groupCollectPost("eng", store.RuleTypeTeamID, groupAllowTeam, "on"), admin, true), "/groups/eng")
	if !groupRuleFlag(t, s, "eng", store.RuleTypeTeamID, groupAllowTeam) {
		t.Error("eng's allow not flagged")
	}
	_, body := adminGet(t, a, "/groups/eng")
	mustContain(t, ruleRow(t, body, groupAllowTeam), "collecting allowed executions", "Stop collecting")

	see303(t, a.post(t, "/admin/rules/collect", groupCollectPost(store.UngroupedGroup, store.RuleTypeTeamID, groupAllowTeam, "on"), admin, true), groupURLFor(store.UngroupedGroup))
	if !groupRuleFlag(t, s, store.UngroupedGroup, store.RuleTypeTeamID, groupAllowTeam) {
		t.Error("ungrouped's allow not flagged")
	}

	see303(t, a.post(t, "/admin/rules/collect", groupCollectPost("eng", store.RuleTypeBinary, groupCompilerSHA, "off"), admin, true), "/groups/eng")
	if groupRuleFlag(t, s, "eng", store.RuleTypeBinary, groupCompilerSHA) {
		t.Error("eng's compiler rule still flagged")
	}
	// Already off: nothing to change, nothing recorded.
	see303(t, a.post(t, "/admin/rules/collect", groupCollectPost("eng", store.RuleTypeBinary, groupCompilerSHA, "off"), admin, true), "/groups/eng")

	want := []string{
		"rule.update group:eng TEAMID rule " + groupAllowTeam + " ALLOWLIST, collect allowed events true",
		"rule.update ungrouped TEAMID rule " + groupAllowTeam + " ALLOWLIST, collect allowed events true",
		"rule.update group:eng BINARY rule " + groupCompilerSHA + " ALLOWLIST_COMPILER, collect allowed events false",
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

func TestGroupRuleCollection_Refusals(t *testing.T) {
	s := groupCollectStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	for name, c := range map[string]struct {
		form url.Values
		code int
	}{
		"block rule":         {groupCollectPost("eng", store.RuleTypeSigningID, groupBlockSigID, "on"), http.StatusBadRequest},
		"missing rule":       {groupCollectPost("eng", store.RuleTypeTeamID, "ZZZZZ99999", "on"), http.StatusNotFound},
		"unregistered group": {groupCollectPost("nope", store.RuleTypeTeamID, groupAllowTeam, "on"), http.StatusNotFound},
		"bad collect":        {groupCollectPost("eng", store.RuleTypeTeamID, groupAllowTeam, "maybe"), http.StatusBadRequest},
		"bad type":           {groupCollectPost("eng", "TEAM", groupAllowTeam, "on"), http.StatusBadRequest},
	} {
		if rec := a.post(t, "/admin/rules/collect", c.form, admin, true); rec.Code != c.code {
			t.Errorf("%s: status %d, want %d", name, rec.Code, c.code)
		}
	}
	if rec := a.post(t, "/admin/rules/collect", groupCollectPost("eng", store.RuleTypeSigningID, groupBlockSigID, "on"), admin, true); !strings.Contains(rec.Body.String(), "ALLOWLIST") {
		t.Error("flagging a block: the refusal should say only an allow (ALLOWLIST or ALLOWLIST_COMPILER) can be flagged")
	}
	if groupRuleFlag(t, s, "eng", store.RuleTypeSigningID, groupBlockSigID) || groupRuleFlag(t, s, "eng", store.RuleTypeTeamID, groupAllowTeam) {
		t.Error("a refused POST flagged a rule")
	}
	if len(auditOf(t, s)) != 0 {
		t.Error("refusals were audited")
	}
}

func TestGroupRuleCollection_OnlyAdminsWithEditing(t *testing.T) {
	s := groupCollectStore()
	a := newEditApp(t, s, true)
	form := groupCollectPost("eng", store.RuleTypeTeamID, groupAllowTeam, "on")
	if rec := a.post(t, "/admin/rules/collect", form, a.cookie(t, auth.RoleViewer), true); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status %d, want 403", rec.Code)
	}
	if rec := a.post(t, "/admin/rules/collect", form, a.cookie(t, auth.RoleAdmin), false); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: status %d, want 403", rec.Code)
	}
	if rec := a.get(t, "/admin/rules/collect", a.cookie(t, auth.RoleAdmin)); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d, want 405", rec.Code)
	}
	off := newEditApp(t, s, false)
	_, body := adminGet(t, off, "/groups/eng")
	mustNotContain(t, body, `action="/admin/rules/collect"`)
	if rec := off.post(t, "/admin/rules/collect", form, off.cookie(t, auth.RoleAdmin), true); rec.Code != http.StatusNotFound {
		t.Errorf("editing off: status %d, want 404", rec.Code)
	}
	if groupRuleFlag(t, s, "eng", store.RuleTypeTeamID, groupAllowTeam) || len(auditOf(t, s)) != 0 {
		t.Error("a refused POST changed or recorded something")
	}
}

func TestGroupRuleCollection_Commands(t *testing.T) {
	s := groupCollectStore()
	a := newAppWithCLIEnv(t, s, "prod")
	_, body := a.viewerGet(t, "/groups/eng")
	mustContainCommand(t, ruleRow(t, body, groupAllowTeam), "ENV=prod rudolph rule update -t teamid -i 'ABCDE12345' --group 'eng' --collect-allowed-events=true --yes")
	mustContainCommand(t, ruleRow(t, body, groupCompilerSHA), "ENV=prod rudolph rule update -t binary -i '"+groupCompilerSHA+"' --group 'eng' --collect-allowed-events=false --yes")
	mustNotContain(t, ruleRow(t, body, groupBlockSigID), "rule update")

	// Flagged, the same rule offers the command that stops collecting.
	if err := s.SetRuleCollection(t.Context(), store.Actor{Username: "cli@example.com"}, store.RuleTarget{Kind: store.TargetGroup, Group: "eng"}, store.RuleTypeTeamID, groupAllowTeam, true); err != nil {
		t.Fatal(err)
	}
	_, body = a.viewerGet(t, "/groups/eng")
	mustContainCommand(t, ruleRow(t, body, groupAllowTeam), "ENV=prod rudolph rule update -t teamid -i 'ABCDE12345' --group 'eng' --collect-allowed-events=false --yes")

	_, body = a.viewerGet(t, groupURLFor(store.UngroupedGroup))
	mustContainCommand(t, ruleRow(t, body, groupAllowTeam), "ENV=prod rudolph rule update -t teamid -i 'ABCDE12345' --ungrouped --collect-allowed-events=true --yes")
}

func TestGroupRuleCollection_WarnsWhenAllowedEventsAreNotUploaded(t *testing.T) {
	s := groupCollectStore()
	a := newEditApp(t, s, true)
	_, body := adminGet(t, a, "/groups/eng")
	mustContain(t, body, "EnableAllEventUpload")

	s.ResetCalls()
	_, body = a.viewerGet(t, "/groups/eng")
	mustNotContain(t, body, "EnableAllEventUpload")
	for _, c := range s.Calls() {
		if c.Method == "GlobalConfig" {
			t.Error("a viewer's group page read the global configuration")
		}
	}

	yes := true
	s.SetGlobalConfig(store.Config{Exists: true, ClientMode: store.ClientModeMonitor, EnableAllEventUpload: &yes})
	_, body = adminGet(t, a, "/groups/eng")
	mustNotContain(t, body, "EnableAllEventUpload")
}
