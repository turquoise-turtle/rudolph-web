//go:build integration

package dynamo_test

// Editing configuration (steps S3 and S4 of the admin plan): store.Writer.UpdateMachineConfig and UpdateGlobalConfig
// apply a store.ConfigChange through Rudolph's machine configuration service, exactly as `rudolph config update
// (--machine <id>|--global)` does: Rudolph validates it (store.ErrInvalidArgument for what it refuses), writes only
// the given attributes (and removes inherited ones), leaves a running temporary mode alone, and records the CLI's
// audit entry (config.update, target machine:<id> or global, summary as the CLI's updateSummary, URLs redacted).
// The web handler has already checked that a non-MONITOR mode was confirmed, so the store passes Rudolph's lockdown
// confirmation. An empty change is store.ErrNothingToChange. A Mac Rudolph has nothing for is store.ErrNotFound.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

func TestWriter_UpdateMachineConfig(t *testing.T) {
	st := newStore(t)
	mode := store.ClientModeLockdown
	err := st.UpdateMachineConfig(ctx, writerActor, machineA, store.ConfigChange{
		ClientMode: &mode, BatchSize: ptr(100), UploadLogsURL: ptr("https://u:secret@logs.example.com/up?token=abc"),
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.GetMachine(ctx, machineA)
	if err != nil {
		t.Fatal(err)
	}
	if m.Config.ClientMode != store.ClientModeLockdown || m.Config.BatchSize == nil || *m.Config.BatchSize != 100 {
		t.Errorf("machine config after update %+v", m.Config)
	}

	if err := st.UpdateMachineConfig(ctx, writerActor, machineA, store.ConfigChange{Inherit: []string{"BatchSize"}}); err != nil {
		t.Fatal(err)
	}
	m, _ = st.GetMachine(ctx, machineA)
	if m.Config.BatchSize != nil || m.Config.ClientMode != store.ClientModeLockdown {
		t.Errorf("after inheriting BatchSize: %+v", m.Config)
	}

	yes := true
	if err := st.UpdateMachineConfig(ctx, writerActor, machineA, store.ConfigChange{CleanSync: &yes}); err != nil {
		t.Fatal(err)
	}
	if m, _ = st.GetMachine(ctx, machineA); !m.CleanSyncRequested {
		t.Error("clean sync not requested")
	}

	entries, err := st.ListAudit(ctx, time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("audit %+v", entries)
	}
	first := entries[2]
	if first.Action != "config.update" || first.Target != "machine:"+machineA || !strings.Contains(first.Summary, "ClientMode=LOCKDOWN") ||
		!strings.Contains(first.Summary, "BatchSize=100") || strings.Contains(first.Summary, "secret") || strings.Contains(first.Summary, "abc") {
		t.Errorf("first audit entry %+v; want config.update with the CLI's summary and the URL redacted", first)
	}
	if entries[1].Summary != "BatchSize inherited" || entries[0].Summary != "CleanSync=true" {
		t.Errorf("audit summaries %q, %q", entries[1].Summary, entries[0].Summary)
	}
}

func TestWriter_UpdateGlobalConfig(t *testing.T) {
	st := newStore(t)
	mode := store.ClientModeLockdown
	if err := st.UpdateGlobalConfig(ctx, writerActor, store.ConfigChange{ClientMode: &mode, FullSyncInterval: ptr(900)}); err != nil {
		t.Fatal(err)
	}
	c, err := st.GlobalConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.ClientMode != store.ClientModeLockdown || c.FullSyncInterval == nil || *c.FullSyncInterval != 900 {
		t.Errorf("global config %+v", c)
	}
	if got, _ := st.GlobalClientMode(ctx); got != store.ClientModeLockdown {
		t.Errorf("GlobalClientMode %q", got)
	}
	entries, _ := st.ListAudit(ctx, time.Now().Add(-time.Hour), 5)
	if len(entries) != 1 || entries[0].Target != "global" || entries[0].Action != "config.update" {
		t.Errorf("audit %+v", entries)
	}
}

func TestWriter_ConfigRefusals(t *testing.T) {
	st := newStore(t)
	yes := true
	for name, err := range map[string]error{
		"batch size 0":        st.UpdateMachineConfig(ctx, writerActor, machineA, store.ConfigChange{BatchSize: ptr(0)}),
		"interval 30":         st.UpdateGlobalConfig(ctx, writerActor, store.ConfigChange{FullSyncInterval: ptr(30)}),
		"global clean sync":   st.UpdateGlobalConfig(ctx, writerActor, store.ConfigChange{CleanSync: &yes}),
		"global inherit":      st.UpdateGlobalConfig(ctx, writerActor, store.ConfigChange{Inherit: []string{"BatchSize"}}),
		"unknown inherit":     st.UpdateMachineConfig(ctx, writerActor, machineA, store.ConfigChange{Inherit: []string{"Nonsense"}}),
		"set and inherit":     st.UpdateMachineConfig(ctx, writerActor, machineA, store.ConfigChange{BatchSize: ptr(5), Inherit: []string{"BatchSize"}}),
		"bad FAA action":      st.UpdateGlobalConfig(ctx, writerActor, store.ConfigChange{OverrideFileAccessAction: ptr("MAYBE")}),
		"USB inherit refused": st.UpdateMachineConfig(ctx, writerActor, machineA, store.ConfigChange{Inherit: []string{"BlockUsbMount"}}),
	} {
		if !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("%s: %v, want ErrInvalidArgument", name, err)
		}
	}
	if err := st.UpdateMachineConfig(ctx, writerActor, machineA, store.ConfigChange{}); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("empty change: %v, want ErrNothingToChange", err)
	}
	if err := st.UpdateMachineConfig(ctx, writerActor, "CCCCCCCC-3333-4333-8333-CCCCCCCCCCCC", store.ConfigChange{BatchSize: ptr(5)}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown Mac: %v, want ErrNotFound", err)
	}
	if entries, _ := st.ListAudit(ctx, time.Now().Add(-time.Hour), 5); len(entries) != 0 {
		t.Errorf("refusals were audited: %+v", entries)
	}
}
