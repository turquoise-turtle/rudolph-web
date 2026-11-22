package fake_test

// A change to both of a group's settings (owner decision, 2026-11-23): one write and one audit entry with the
// combined summary, exactly as one `rudolph config update --group <name> -c <mode> --override-file-access-action
// <action>` is. A setting the group already has as asked is left out of the entry. The DynamoDB store is held to the
// same cases in internal/store/dynamo/group_settings_both_integration_test.go.

import (
	"errors"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func TestFakeGroupSettingsBothInOneEntry(t *testing.T) {
	s := fake.New()
	s.AddGroup(store.Group{Name: "pilot", Description: "Pilot", AcceptReported: true})
	monitor, lockdown, disable, none, unset := store.ClientModeMonitor, store.ClientModeLockdown, "DISABLE", "NONE", ""

	if err := s.UpdateGroupSettings(t.Context(), groupActor, "pilot", store.GroupSettingsChange{ClientMode: &monitor, FileAccessOverride: &disable}); err != nil {
		t.Fatal(err)
	}
	if g := groupNamed(t, s, "pilot"); g.ClientMode != monitor || g.FileAccessOverride != disable || g.Description != "Pilot" || !g.AcceptReported {
		t.Errorf("pilot %+v, want MONITOR and DISABLE with the rest kept", g)
	}
	// The mode is already MONITOR: only the override changes and is recorded.
	if err := s.UpdateGroupSettings(t.Context(), groupActor, "pilot", store.GroupSettingsChange{ClientMode: &monitor, FileAccessOverride: &none}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateGroupSettings(t.Context(), groupActor, "pilot", store.GroupSettingsChange{ClientMode: &lockdown, FileAccessOverride: &unset}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateGroupSettings(t.Context(), groupActor, "pilot", store.GroupSettingsChange{ClientMode: &lockdown, FileAccessOverride: &unset}); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("both already as asked: %v, want ErrNothingToChange", err)
	}
	if err := s.UpdateGroupSettings(t.Context(), groupActor, "pilot", store.GroupSettingsChange{}); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("an empty change: %v, want ErrNothingToChange", err)
	}
	bad := "PERMISSIVE"
	if err := s.UpdateGroupSettings(t.Context(), groupActor, "pilot", store.GroupSettingsChange{ClientMode: &monitor, FileAccessOverride: &bad}); !errors.Is(err, store.ErrInvalidArgument) || errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("a bad override beside a good mode: %v, want ErrInvalidArgument", err)
	}
	if g := groupNamed(t, s, "pilot"); g.ClientMode != lockdown || g.FileAccessOverride != "" {
		t.Errorf("pilot %+v, want LOCKDOWN and no override: a refused change must write neither setting", g)
	}

	want := []string{
		"config.update group:pilot ClientMode=MONITOR, OverrideFileAccessAction=DISABLE",
		"config.update group:pilot OverrideFileAccessAction=NONE",
		"config.update group:pilot ClientMode=LOCKDOWN, OverrideFileAccessAction inherited",
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
}
