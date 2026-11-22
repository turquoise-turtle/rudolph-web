package fake_test

// The fake's device group settings and group rule collection (Rudolph 019bf42), which the web tests rely on. The
// DynamoDB store is held to the same cases in internal/store/dynamo/group_settings_integration_test.go.
//
// File access override per group (Rudolph's docs/groups.md "File access override per group"):
//   - store.Group.FileAccessOverride is the group's override as Santa names it ("NONE", "AUDIT_ONLY", "DISABLE"), or
//     "" when it has none; (*fake.Store).AddGroup takes it as given.
//   - store.Writer.SetGroupFileAccessOverride(ctx, actor, group, action) sets it, or removes it when action is "", as
//     `rudolph config update --group <name>|--ungrouped --override-file-access-action <v>` and `--inherit
//     override-file-access-action` do. The group's client mode and the rest of its entry are kept. It records the CLI's
//     audit entry: config.update on group:<name> (ungrouped for rudolph:ungrouped), summary
//     "OverrideFileAccessAction=<ACTION>" or "OverrideFileAccessAction inherited".
//   - The group name is normalised as for the client mode (GroupConfigName). rudolph:ungrouped is registered first
//     when it is not, as Rudolph does.
//   - Refusals, with nothing written or audited: an action that is not NONE, AUDIT_ONLY or DISABLE is
//     store.ErrInvalidArgument; an unregistered group is store.ErrNotFound; the action the group already has (or none,
//     when removing) is store.ErrNothingToChange.
//
// Collection of a group rule's allowed executions: store.Writer.SetRuleCollection now takes a group target
// (store.TargetGroup, rudolph:ungrouped included), as `rudolph rule update ... --group <name>|--ungrouped
// --collect-allowed-events=true|false` does through groups.SetCollectAllowedEvents. The flag is read back as
// store.Rule.CollectAllowedEvents from ListGroupRules, and the audit entry is rule.update on group:<name> or
// ungrouped with the summary "<TYPE> rule <identifier> <POLICY>, collect allowed events true|false".
//   - Only an allow (ALLOWLIST, ALLOWLIST_COMPILER) can be flagged: flagging a block is store.ErrInvalidArgument.
//     Clearing is accepted whatever the policy.
//   - A rule the group does not have, and a group that is not registered, are store.ErrNotFound; the flag as it already
//     is, store.ErrNothingToChange. Nothing is written or audited for any of them.
//   - Writing the rule again (AddRule on the group) ends its collection, as groups.PutRule does.

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

var groupActor = store.Actor{Username: "admin@example.com", Subject: "00000000-0000-4000-8000-000000000001"}

func groupNamed(t *testing.T, s *fake.Store, name string) *store.Group {
	t.Helper()
	gs, err := s.ListGroups(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := range gs {
		if gs[i].Name == name {
			return &gs[i]
		}
	}
	return nil
}

func auditLines(t *testing.T, s *fake.Store) []store.AuditEntry {
	t.Helper()
	entries, err := s.ListAudit(t.Context(), time.Time{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Oldest first, for reading the tests in order.
	slices.Reverse(entries)
	return entries
}

func TestFakeGroupFileAccessOverride(t *testing.T) {
	s := fake.New()
	s.AddGroup(store.Group{Name: "pilot", Description: "Pilot", AcceptReported: true, ClientMode: store.ClientModeLockdown})
	s.AddGroup(store.Group{Name: "lab", FileAccessOverride: "DISABLE"})

	for _, action := range []string{"NONE", "AUDIT_ONLY", "DISABLE"} {
		if err := s.SetGroupFileAccessOverride(t.Context(), groupActor, "pilot", action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		g := groupNamed(t, s, "pilot")
		if g == nil || g.FileAccessOverride != action || g.ClientMode != store.ClientModeLockdown || g.Description != "Pilot" || !g.AcceptReported {
			t.Errorf("after %s: %+v; want the override set and the rest of the entry kept", action, g)
		}
	}
	if err := s.SetGroupFileAccessOverride(t.Context(), groupActor, "PILOT", "DISABLE"); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("the override it already has (name in another case): %v, want ErrNothingToChange", err)
	}
	if err := s.SetGroupFileAccessOverride(t.Context(), groupActor, "lab", ""); err != nil {
		t.Fatal(err)
	}
	if g := groupNamed(t, s, "lab"); g == nil || g.FileAccessOverride != "" {
		t.Errorf("lab after removing %+v", g)
	}
	if err := s.SetGroupFileAccessOverride(t.Context(), groupActor, "lab", ""); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("removing an override the group lacks: %v, want ErrNothingToChange", err)
	}

	// Ungrouped is registered on demand.
	if err := s.SetGroupFileAccessOverride(t.Context(), groupActor, store.UngroupedGroup, "AUDIT_ONLY"); err != nil {
		t.Fatal(err)
	}
	if g := groupNamed(t, s, store.UngroupedGroup); g == nil || g.FileAccessOverride != "AUDIT_ONLY" {
		t.Errorf("ungrouped %+v, want AUDIT_ONLY", g)
	}

	want := []string{
		"config.update group:pilot OverrideFileAccessAction=NONE",
		"config.update group:pilot OverrideFileAccessAction=AUDIT_ONLY",
		"config.update group:pilot OverrideFileAccessAction=DISABLE",
		"config.update group:lab OverrideFileAccessAction inherited",
		"config.update ungrouped OverrideFileAccessAction=AUDIT_ONLY",
	}
	entries := auditLines(t, s)
	if len(entries) != len(want) {
		t.Fatalf("audit %+v, want %d entries", entries, len(want))
	}
	for i, e := range entries {
		if got := e.Action + " " + e.Target + " " + e.Summary; got != want[i] {
			t.Errorf("audit entry %d %q, want %q", i, got, want[i])
		}
		if e.Actor != "web:admin@example.com (00000000-0000-4000-8000-000000000001)" {
			t.Errorf("audit actor %q", e.Actor)
		}
	}
}

func TestFakeGroupFileAccessOverrideRefusals(t *testing.T) {
	s := fake.New()
	s.AddGroup(store.Group{Name: "pilot", FileAccessOverride: "AUDIT_ONLY"})
	for name, c := range map[string]struct {
		group, action string
		want          error
	}{
		"unknown action":     {"pilot", "PERMISSIVE", store.ErrInvalidArgument},
		"unregistered group": {"nope", "NONE", store.ErrNotFound},
		"reserved name":      {"rudolph:other", "NONE", store.ErrInvalidArgument},
	} {
		if err := s.SetGroupFileAccessOverride(t.Context(), groupActor, c.group, c.action); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if err := s.SetGroupFileAccessOverride(t.Context(), store.Actor{}, "pilot", "NONE"); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("no actor: %v, want ErrInvalidArgument", err)
	}
	if g := groupNamed(t, s, "pilot"); g.FileAccessOverride != "AUDIT_ONLY" {
		t.Errorf("refusals changed pilot to %q", g.FileAccessOverride)
	}
	if groupNamed(t, s, "nope") != nil {
		t.Error("an unregistered group was registered")
	}
	if entries := auditLines(t, s); len(entries) != 0 {
		t.Errorf("refusals were audited: %+v", entries)
	}
}

const (
	groupTeam  = "ABCDE12345"
	groupBlock = "EQHXZ8M8AV:com.google.GeminiMacOS"
)

func groupRuleOf(t *testing.T, s *fake.Store, group string, ruleType store.RuleType, identifier string) store.Rule {
	t.Helper()
	p, err := s.ListGroupRules(t.Context(), group, store.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range p.Items {
		if r.RuleType == ruleType && r.Identifier == identifier {
			return r
		}
	}
	t.Fatalf("group %s has no %s rule %s", group, ruleType, identifier)
	return store.Rule{}
}

func collectGroupStore() *fake.Store {
	s := fake.New()
	s.AddGroup(store.Group{Name: "eng", AcceptReported: true})
	s.AddGroup(store.Group{Name: store.UngroupedGroup})
	s.AddGroupRule("eng", store.Rule{RuleType: store.RuleTypeTeamID, Policy: store.PolicyAllowlist, Identifier: groupTeam, State: store.RuleStateInForce})
	s.AddGroupRule("eng", store.Rule{RuleType: store.RuleTypeSigningID, Policy: store.PolicyBlocklist, Identifier: groupBlock, State: store.RuleStateInForce})
	s.AddGroupRule(store.UngroupedGroup, store.Rule{RuleType: store.RuleTypeTeamID, Policy: store.PolicyAllowlistCompiler, Identifier: groupTeam, State: store.RuleStateInForce})
	return s
}

func TestFakeGroupRuleCollection(t *testing.T) {
	s := collectGroupStore()
	eng := store.RuleTarget{Kind: store.TargetGroup, Group: "eng"}
	ungrouped := store.RuleTarget{Kind: store.TargetGroup, Group: store.UngroupedGroup}

	if err := s.SetRuleCollection(t.Context(), groupActor, eng, store.RuleTypeTeamID, groupTeam, true); err != nil {
		t.Fatal(err)
	}
	if !groupRuleOf(t, s, "eng", store.RuleTypeTeamID, groupTeam).CollectAllowedEvents {
		t.Error("eng's allow not read back as collecting")
	}
	if err := s.SetRuleCollection(t.Context(), groupActor, eng, store.RuleTypeTeamID, groupTeam, true); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("flagging it again: %v, want ErrNothingToChange", err)
	}
	if err := s.SetRuleCollection(t.Context(), groupActor, ungrouped, store.RuleTypeTeamID, groupTeam, true); err != nil {
		t.Fatalf("ungrouped ALLOWLIST_COMPILER: %v", err)
	}
	if !groupRuleOf(t, s, store.UngroupedGroup, store.RuleTypeTeamID, groupTeam).CollectAllowedEvents {
		t.Error("ungrouped's allow not read back as collecting")
	}
	if err := s.SetRuleCollection(t.Context(), groupActor, eng, store.RuleTypeTeamID, groupTeam, false); err != nil {
		t.Fatal(err)
	}
	if groupRuleOf(t, s, "eng", store.RuleTypeTeamID, groupTeam).CollectAllowedEvents {
		t.Error("eng's allow still collecting after it was cleared")
	}
	// Clearing a block that is not flagged changes nothing.
	if err := s.SetRuleCollection(t.Context(), groupActor, eng, store.RuleTypeSigningID, groupBlock, false); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("clearing an unflagged block: %v, want ErrNothingToChange", err)
	}

	want := []string{
		"rule.update group:eng TEAMID rule ABCDE12345 ALLOWLIST, collect allowed events true",
		"rule.update ungrouped TEAMID rule ABCDE12345 ALLOWLIST_COMPILER, collect allowed events true",
		"rule.update group:eng TEAMID rule ABCDE12345 ALLOWLIST, collect allowed events false",
	}
	entries := auditLines(t, s)
	if len(entries) != len(want) {
		t.Fatalf("audit %+v, want %d entries", entries, len(want))
	}
	for i, e := range entries {
		if got := e.Action + " " + e.Target + " " + e.Summary; got != want[i] {
			t.Errorf("audit entry %d %q, want %q", i, got, want[i])
		}
	}

	// Writing the rule again ends its collection.
	if err := s.AddRule(t.Context(), groupActor, ungrouped, store.NewRule{RuleType: store.RuleTypeTeamID, Identifier: groupTeam, Policy: store.PolicyAllowlist}); err != nil {
		t.Fatal(err)
	}
	if groupRuleOf(t, s, store.UngroupedGroup, store.RuleTypeTeamID, groupTeam).CollectAllowedEvents {
		t.Error("a group rule written again is still collecting")
	}
}

func TestFakeGroupRuleCollectionRefusals(t *testing.T) {
	s := collectGroupStore()
	eng := store.RuleTarget{Kind: store.TargetGroup, Group: "eng"}
	for name, c := range map[string]struct {
		target     store.RuleTarget
		ruleType   store.RuleType
		identifier string
		want       error
	}{
		"block rule":         {eng, store.RuleTypeSigningID, groupBlock, store.ErrInvalidArgument},
		"missing rule":       {eng, store.RuleTypeTeamID, "ZZZZZ99999", store.ErrNotFound},
		"unregistered group": {store.RuleTarget{Kind: store.TargetGroup, Group: "nope"}, store.RuleTypeTeamID, groupTeam, store.ErrNotFound},
		"bad identifier":     {eng, store.RuleTypeTeamID, "abc", store.ErrInvalidArgument},
	} {
		if err := s.SetRuleCollection(t.Context(), groupActor, c.target, c.ruleType, c.identifier, true); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		} else if name == "block rule" && !strings.Contains(err.Error(), "ALLOWLIST") {
			// Rudolph's groups.ErrNotAnAllowRule names the policies that can be flagged.
			t.Errorf("block rule: %q, want the refusal to say only an allow (ALLOWLIST or ALLOWLIST_COMPILER) can be flagged", err)
		}
	}
	if groupRuleOf(t, s, "eng", store.RuleTypeSigningID, groupBlock).CollectAllowedEvents {
		t.Error("a block was flagged")
	}
	if entries := auditLines(t, s); len(entries) != 0 {
		t.Errorf("refusals were audited: %+v", entries)
	}
}
