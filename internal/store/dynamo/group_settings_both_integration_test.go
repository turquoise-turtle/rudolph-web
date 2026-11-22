//go:build integration

package dynamo_test

// A change to both of a group's settings (owner decision, 2026-11-23), against DynamoDB Local: one registry update
// through Rudolph's groups.ChangeSettings, so MembershipVersion moves once and RulesVersion not at all, and one
// config.update entry with the CLI's combined summary. Held to the same cases as the fake
// (internal/store/fake/group_settings_both_test.go).

import (
	"errors"
	"slices"
	"testing"

	awstypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

func TestGroupSettings_BothInOneWriteAndEntry(t *testing.T) {
	st := newStore(t)
	if _, err := st.CreateGroup(ctx, writerActor, "pilot", "Pilot", true); err != nil {
		t.Fatal(err)
	}
	monitor, lockdown, disable, none, unset := store.ClientModeMonitor, store.ClientModeLockdown, "DISABLE", "NONE", ""
	rulesBefore, membershipBefore := rulesVersionOf(t, "pilot"), membershipVersion(t)

	if err := st.UpdateGroupSettings(ctx, writerActor, "pilot", store.GroupSettingsChange{ClientMode: &monitor, FileAccessOverride: &disable}); err != nil {
		t.Fatal(err)
	}
	if got := membershipVersion(t); got != membershipBefore+1 {
		t.Errorf("MembershipVersion %d, want %d: both settings are one update", got, membershipBefore+1)
	}
	entry := rawEntry(t, "pilot")
	if mode, ok := entry["ClientMode"].(*awstypes.AttributeValueMemberN); !ok || mode.Value != "1" {
		t.Errorf("stored client mode %#v, want the number 1", entry["ClientMode"])
	}
	if action, ok := entry["OverrideFileAccessAction"].(*awstypes.AttributeValueMemberS); !ok || action.Value != "DISABLE" {
		t.Errorf("stored override %#v, want the string DISABLE", entry["OverrideFileAccessAction"])
	}
	// The mode is already MONITOR: only the override changes and is recorded.
	if err := st.UpdateGroupSettings(ctx, writerActor, "pilot", store.GroupSettingsChange{ClientMode: &monitor, FileAccessOverride: &none}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateGroupSettings(ctx, writerActor, "pilot", store.GroupSettingsChange{ClientMode: &lockdown, FileAccessOverride: &unset}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateGroupSettings(ctx, writerActor, "pilot", store.GroupSettingsChange{ClientMode: &lockdown, FileAccessOverride: &unset}); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("both already as asked: %v, want ErrNothingToChange", err)
	}
	bad := "PERMISSIVE"
	if err := st.UpdateGroupSettings(ctx, writerActor, "pilot", store.GroupSettingsChange{ClientMode: &monitor, FileAccessOverride: &bad}); !errors.Is(err, store.ErrInvalidArgument) || errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("a bad override beside a good mode: %v, want ErrInvalidArgument", err)
	}
	groups, _ := st.ListGroups(ctx)
	if g := findGroup(t, groups, "pilot"); g == nil || g.ClientMode != lockdown || g.FileAccessOverride != "" || g.Description != "Pilot" {
		t.Errorf("pilot %+v, want LOCKDOWN and no override, description kept", g)
	}
	if got := membershipVersion(t); got != membershipBefore+3 {
		t.Errorf("MembershipVersion %d, want %d (one for each of the three changes)", got, membershipBefore+3)
	}
	if got := rulesVersionOf(t, "pilot"); got != rulesBefore {
		t.Errorf("RulesVersion %d, want %d: settings are not rules", got, rulesBefore)
	}

	want := []string{
		"group:pilot ClientMode=MONITOR, OverrideFileAccessAction=DISABLE",
		"group:pilot OverrideFileAccessAction=NONE",
		"group:pilot ClientMode=LOCKDOWN, OverrideFileAccessAction inherited",
	}
	if got := auditSince(t, st, "config.update"); !slices.Equal(got, want) {
		t.Errorf("config.update entries\n got %q\nwant %q", got, want)
	}
}
