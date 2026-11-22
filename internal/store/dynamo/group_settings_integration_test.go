//go:build integration

package dynamo_test

// A device group's file access override and the collection of its rules' allowed executions (Rudolph 019bf42),
// against DynamoDB Local, held to the same cases as the fake (internal/store/fake/group_settings_test.go).
//
// File access override per group (Rudolph's docs/groups.md "File access override per group", docs/deploy.md step 29):
//   - ListGroups returns the registry entry's OverrideFileAccessAction as store.Group.FileAccessOverride, as Santa
//     names it (legacy spellings such as "AuditOnly" normalised, as groups.StoredFileAccessAction.Action does), and ""
//     when it is absent or cannot be decoded (a number, or a string that is no action, must not break the list).
//   - SetGroupFileAccessOverride writes it through Rudolph's groups.SetFileAccessOverride (one conditional update that
//     keeps the rest of the entry, client mode included, and bumps MembershipVersion, not the group's RulesVersion: the
//     override is configuration, not rules) and records config.update with the web actor, summary
//     "OverrideFileAccessAction=<ACTION>" or "OverrideFileAccessAction inherited". "" removes it, and also removes a
//     stored value that cannot be decoded. rudolph:ungrouped is registered first when it isn't. Unknown group:
//     store.ErrNotFound; an action that is not NONE, AUDIT_ONLY or DISABLE: store.ErrInvalidArgument; the override it
//     already has: store.ErrNothingToChange; nothing written or audited for any of them.
//
// Collecting a group rule's allowed executions (docs/groups.md "Collecting what a group rule allows", docs/deploy.md
// step 30): SetRuleCollection with a group target goes through Rudolph's groups.SetCollectAllowedEvents, so the rule
// row's CollectAllowedEvents and the collection index item (EventCollection / Rules) attribute Group#<group> change
// in one transaction, and the group's RulesVersion does not move. It records rule.update on group:<name> or
// ungrouped, summary ending "collect allowed events true|false". Flagging a block (groups.ErrNotAnAllowRule) is
// store.ErrInvalidArgument; a missing rule (ErrRuleNotFound) or an unregistered group (ErrGroupNotFound) is
// store.ErrNotFound; the flag as it already is, store.ErrNothingToChange. Writing the rule again (AddRule) clears the
// flag and takes the rule out of the index, as groups.PutRule does.

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	awstypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

func rawRegistry(t *testing.T) map[string]awstypes.AttributeValue {
	t.Helper()
	out, err := ddblocal.Raw(t).GetItem(context.Background(), &awsdynamodb.GetItemInput{
		TableName:      aws.String(ddblocal.TableName),
		Key:            map[string]awstypes.AttributeValue{"PK": &awstypes.AttributeValueMemberS{Value: "GroupRegistry"}, "SK": &awstypes.AttributeValueMemberS{Value: "Current"}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.Item
}

func rawNumber(t *testing.T, v awstypes.AttributeValue) int {
	t.Helper()
	n, ok := v.(*awstypes.AttributeValueMemberN)
	if !ok {
		t.Fatalf("not a number: %#v", v)
	}
	i, err := strconv.Atoi(n.Value)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

// rawEntry returns a group's registry entry as stored.
func rawEntry(t *testing.T, group string) map[string]awstypes.AttributeValue {
	t.Helper()
	groups, ok := rawRegistry(t)["Groups"].(*awstypes.AttributeValueMemberM)
	if !ok {
		t.Fatal("registry without Groups")
	}
	entry, ok := groups.Value[group].(*awstypes.AttributeValueMemberM)
	if !ok {
		t.Fatalf("group %s not in the registry", group)
	}
	return entry.Value
}

func membershipVersion(t *testing.T) int {
	t.Helper()
	return rawNumber(t, rawRegistry(t)["MembershipVersion"])
}

func rulesVersionOf(t *testing.T, group string) int {
	t.Helper()
	return rawNumber(t, rawEntry(t, group)["RulesVersion"])
}

func setRawGroupAttribute(t *testing.T, group, attribute string, v awstypes.AttributeValue) {
	t.Helper()
	_, err := ddblocal.Raw(t).UpdateItem(context.Background(), &awsdynamodb.UpdateItemInput{
		TableName:                 aws.String(ddblocal.TableName),
		Key:                       map[string]awstypes.AttributeValue{"PK": &awstypes.AttributeValueMemberS{Value: "GroupRegistry"}, "SK": &awstypes.AttributeValueMemberS{Value: "Current"}},
		UpdateExpression:          aws.String("SET #g.#n.#a = :v"),
		ExpressionAttributeNames:  map[string]string{"#g": "Groups", "#n": group, "#a": attribute},
		ExpressionAttributeValues: map[string]awstypes.AttributeValue{":v": v},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// auditSince returns the audit entries of one action, oldest first, as "<target> <summary>".
func auditSince(t *testing.T, st *dynamo.Store, action string) []string {
	t.Helper()
	entries, err := st.ListAudit(ctx, time.Now().Add(-time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(entries) - 1; i >= 0; i-- {
		if e := entries[i]; e.Action == action {
			if !strings.HasPrefix(e.Actor, "web:admin@example.com") {
				t.Errorf("audit actor %q, want the web actor", e.Actor)
			}
			out = append(out, e.Target+" "+e.Summary)
		}
	}
	return out
}

func TestGroupFileAccess_ListGroupsReadsTheOverride(t *testing.T) {
	st := newStore(t)
	for _, name := range []string{"legacy", "lower", "bad", "num", "plain"} {
		if _, err := st.CreateGroup(ctx, writerActor, name, "", true); err != nil {
			t.Fatal(err)
		}
	}
	setRawGroupAttribute(t, "legacy", "OverrideFileAccessAction", &awstypes.AttributeValueMemberS{Value: "AuditOnly"})
	setRawGroupAttribute(t, "lower", "OverrideFileAccessAction", &awstypes.AttributeValueMemberS{Value: "disable"})
	setRawGroupAttribute(t, "bad", "OverrideFileAccessAction", &awstypes.AttributeValueMemberS{Value: "PERMISSIVE"})
	setRawGroupAttribute(t, "num", "OverrideFileAccessAction", &awstypes.AttributeValueMemberN{Value: "2"})
	groups, err := st.ListGroups(ctx)
	if err != nil {
		t.Fatalf("an undecodable override must not break the list: %v", err)
	}
	for name, want := range map[string]string{
		"eng":    "AUDIT_ONLY", // the web contract fixture's
		"legacy": "AUDIT_ONLY",
		"lower":  "DISABLE",
		"bad":    "",
		"num":    "",
		"plain":  "",
	} {
		if g := findGroup(t, groups, name); g == nil || g.FileAccessOverride != want {
			t.Errorf("%s: %+v, want the override %q", name, g, want)
		}
	}
}

func TestGroupFileAccess_Set(t *testing.T) {
	st := newStore(t)
	if _, err := st.CreateGroup(ctx, writerActor, "pilot", "Pilot", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupClientMode(ctx, writerActor, "pilot", store.ClientModeLockdown); err != nil {
		t.Fatal(err)
	}
	rulesBefore, membershipBefore := rulesVersionOf(t, "pilot"), membershipVersion(t)

	for _, action := range []string{"NONE", "AUDIT_ONLY", "DISABLE"} {
		if err := st.SetGroupFileAccessOverride(ctx, writerActor, "pilot", action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if got, ok := rawEntry(t, "pilot")["OverrideFileAccessAction"].(*awstypes.AttributeValueMemberS); !ok || got.Value != action {
			t.Errorf("stored override %#v, want the string %s", rawEntry(t, "pilot")["OverrideFileAccessAction"], action)
		}
	}
	groups, _ := st.ListGroups(ctx)
	if g := findGroup(t, groups, "pilot"); g == nil || g.FileAccessOverride != "DISABLE" || g.ClientMode != store.ClientModeLockdown || g.Description != "Pilot" {
		t.Errorf("pilot %+v, want DISABLE with its client mode and description kept", g)
	}
	if got := rulesVersionOf(t, "pilot"); got != rulesBefore {
		t.Errorf("RulesVersion %d, want %d: the override is not a rule change", got, rulesBefore)
	}
	if got := membershipVersion(t); got != membershipBefore+3 {
		t.Errorf("MembershipVersion %d, want %d (bumped by each change, as Rudolph's ChangeSettings does)", got, membershipBefore+3)
	}

	if err := st.SetGroupFileAccessOverride(ctx, writerActor, "Pilot", "DISABLE"); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("the same override again: %v, want ErrNothingToChange", err)
	}
	if err := st.SetGroupFileAccessOverride(ctx, writerActor, "pilot", ""); err != nil {
		t.Fatal(err)
	}
	if _, has := rawEntry(t, "pilot")["OverrideFileAccessAction"]; has {
		t.Error("the override is still stored after removing it")
	}
	if err := st.SetGroupFileAccessOverride(ctx, writerActor, "pilot", ""); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("removing an override the group lacks: %v, want ErrNothingToChange", err)
	}

	// A value Rudolph cannot decode counts as none, but is still worth removing.
	setRawGroupAttribute(t, "pilot", "OverrideFileAccessAction", &awstypes.AttributeValueMemberS{Value: "PERMISSIVE"})
	if err := st.SetGroupFileAccessOverride(ctx, writerActor, "pilot", ""); err != nil {
		t.Fatalf("removing an undecodable override: %v", err)
	}
	if _, has := rawEntry(t, "pilot")["OverrideFileAccessAction"]; has {
		t.Error("the undecodable override is still stored")
	}

	if err := st.SetGroupFileAccessOverride(ctx, writerActor, store.UngroupedGroup, "AUDIT_ONLY"); err != nil {
		t.Fatalf("ungrouped: %v", err)
	}
	groups, _ = st.ListGroups(ctx)
	if g := findGroup(t, groups, store.UngroupedGroup); g == nil || g.FileAccessOverride != "AUDIT_ONLY" {
		t.Errorf("ungrouped %+v, want AUDIT_ONLY (registered on demand)", g)
	}

	want := []string{
		"group:pilot ClientMode=LOCKDOWN",
		"group:pilot OverrideFileAccessAction=NONE",
		"group:pilot OverrideFileAccessAction=AUDIT_ONLY",
		"group:pilot OverrideFileAccessAction=DISABLE",
		"group:pilot OverrideFileAccessAction inherited",
		"group:pilot OverrideFileAccessAction inherited",
		"ungrouped OverrideFileAccessAction=AUDIT_ONLY",
	}
	if got := auditSince(t, st, "config.update"); !slices.Equal(got, want) {
		t.Errorf("config.update entries\n got %q\nwant %q", got, want)
	}
}

func TestGroupFileAccess_Refusals(t *testing.T) {
	st := newStore(t)
	membershipBefore := membershipVersion(t)
	for name, c := range map[string]struct {
		group, action string
		want          error
	}{
		"unknown action":     {"eng", "PERMISSIVE", store.ErrInvalidArgument},
		"unregistered group": {"nope", "NONE", store.ErrNotFound},
		"reserved name":      {"rudolph:other", "NONE", store.ErrInvalidArgument},
		"already set":        {"eng", "AUDIT_ONLY", store.ErrNothingToChange},
	} {
		if err := st.SetGroupFileAccessOverride(ctx, writerActor, c.group, c.action); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if got, ok := rawEntry(t, "eng")["OverrideFileAccessAction"].(*awstypes.AttributeValueMemberS); !ok || got.Value != "AUDIT_ONLY" {
		t.Errorf("refusals changed eng's override to %#v", rawEntry(t, "eng")["OverrideFileAccessAction"])
	}
	if got := membershipVersion(t); got != membershipBefore {
		t.Errorf("MembershipVersion moved from %d to %d on refusals", membershipBefore, got)
	}
	if entries, _ := st.ListAudit(ctx, time.Now().Add(-time.Hour), 5); len(entries) != 0 {
		t.Errorf("refusals were audited: %+v", entries)
	}
}

func groupRuleCollecting(t *testing.T, st *dynamo.Store, group string, ruleType store.RuleType, identifier string) bool {
	t.Helper()
	p, err := st.ListGroupRules(ctx, group, store.PageRequest{Limit: 100})
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

func TestGroupCollection_SetAndClear(t *testing.T) {
	st := newStore(t)
	eng := store.RuleTarget{Kind: store.TargetGroup, Group: "eng"}
	ungrouped := store.RuleTarget{Kind: store.TargetGroup, Group: store.UngroupedGroup}
	if err := st.AddRule(ctx, writerActor, eng, store.NewRule{RuleType: store.RuleTypeBinary, Identifier: writerSHA, Policy: store.PolicyAllowlist}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddRule(ctx, writerActor, ungrouped, store.NewRule{RuleType: store.RuleTypeBinary, Identifier: writerSHA, Policy: store.PolicyAllowlistCompiler}); err != nil {
		t.Fatal(err)
	}
	engVersion := rulesVersionOf(t, "eng")
	key := "Binary#" + writerSHA

	if err := st.SetRuleCollection(ctx, writerActor, eng, store.RuleTypeBinary, writerSHA, true); err != nil {
		t.Fatal(err)
	}
	if !groupRuleCollecting(t, st, "eng", store.RuleTypeBinary, writerSHA) {
		t.Error("eng's rule not read back as collecting")
	}
	if got := collectionIndex(t, "Group#eng"); !slices.Contains(got, key) {
		t.Errorf("index Group#eng %v, want %s", got, key)
	}
	if err := st.SetRuleCollection(ctx, writerActor, eng, store.RuleTypeBinary, writerSHA, true); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("flagging it again: %v, want ErrNothingToChange", err)
	}
	if err := st.SetRuleCollection(ctx, writerActor, ungrouped, store.RuleTypeBinary, writerSHA, true); err != nil {
		t.Fatalf("ungrouped: %v", err)
	}
	if got := collectionIndex(t, "Group#"+store.UngroupedGroup); !slices.Contains(got, key) {
		t.Errorf("index Group#%s %v, want %s", store.UngroupedGroup, got, key)
	}
	if got := rulesVersionOf(t, "eng"); got != engVersion {
		t.Errorf("eng's RulesVersion moved from %d to %d: the flag is never sent to Santa", engVersion, got)
	}

	if err := st.SetRuleCollection(ctx, writerActor, eng, store.RuleTypeBinary, writerSHA, false); err != nil {
		t.Fatal(err)
	}
	if groupRuleCollecting(t, st, "eng", store.RuleTypeBinary, writerSHA) || slices.Contains(collectionIndex(t, "Group#eng"), key) {
		t.Error("eng's rule still collecting, or still in the index, after it was cleared")
	}
	if !groupRuleCollecting(t, st, store.UngroupedGroup, store.RuleTypeBinary, writerSHA) {
		t.Error("clearing eng's flag cleared ungrouped's")
	}
	if got := rulesVersionOf(t, "eng"); got != engVersion {
		t.Errorf("eng's RulesVersion moved from %d to %d on clearing", engVersion, got)
	}

	want := []string{
		"group:eng BINARY rule " + writerSHA + " ALLOWLIST, collect allowed events true",
		"ungrouped BINARY rule " + writerSHA + " ALLOWLIST_COMPILER, collect allowed events true",
		"group:eng BINARY rule " + writerSHA + " ALLOWLIST, collect allowed events false",
	}
	if got := auditSince(t, st, "rule.update"); !slices.Equal(got, want) {
		t.Errorf("rule.update entries\n got %q\nwant %q", got, want)
	}

	// Writing the rule again ends its collection and takes it out of the index.
	if err := st.AddRule(ctx, writerActor, ungrouped, store.NewRule{RuleType: store.RuleTypeBinary, Identifier: writerSHA, Policy: store.PolicyAllowlist}); err != nil {
		t.Fatal(err)
	}
	if groupRuleCollecting(t, st, store.UngroupedGroup, store.RuleTypeBinary, writerSHA) || slices.Contains(collectionIndex(t, "Group#"+store.UngroupedGroup), key) {
		t.Error("a group rule written again is still collecting, or still in the index")
	}
}

func TestGroupCollection_Refusals(t *testing.T) {
	st := newStore(t)
	eng := store.RuleTarget{Kind: store.TargetGroup, Group: "eng"}
	for name, c := range map[string]struct {
		target     store.RuleTarget
		ruleType   store.RuleType
		identifier string
		want       error
	}{
		// The fixture's eng rules: a SIGNINGID block, and a TEAMID allow already flagged.
		"block rule":         {eng, store.RuleTypeSigningID, "EQHXZ8M8AV:com.google.GeminiMacOS", store.ErrInvalidArgument},
		"missing rule":       {eng, store.RuleTypeBinary, strings.Repeat("cd", 32), store.ErrNotFound},
		"unregistered group": {store.RuleTarget{Kind: store.TargetGroup, Group: "nope"}, store.RuleTypeTeamID, "ABCDE12345", store.ErrNotFound},
		"already collecting": {eng, store.RuleTypeTeamID, "ABCDE12345", store.ErrNothingToChange},
	} {
		if err := st.SetRuleCollection(ctx, writerActor, c.target, c.ruleType, c.identifier, true); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		} else if name == "block rule" && !strings.Contains(err.Error(), "ALLOWLIST") {
			// Rudolph's groups.ErrNotAnAllowRule names the policies that can be flagged.
			t.Errorf("block rule: %q, want the refusal to say only an allow (ALLOWLIST or ALLOWLIST_COMPILER) can be flagged", err)
		}
	}
	if groupRuleCollecting(t, st, "eng", store.RuleTypeSigningID, "EQHXZ8M8AV:com.google.GeminiMacOS") {
		t.Error("a block was flagged")
	}
	if got := collectionIndex(t, "Group#eng"); len(got) != 0 {
		t.Errorf("refusals wrote the index: Group#eng %v", got)
	}
	if entries, _ := st.ListAudit(ctx, time.Now().Add(-time.Hour), 5); len(entries) != 0 {
		t.Errorf("refusals were audited: %+v", entries)
	}
}
