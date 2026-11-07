//go:build integration

package dynamo_test

// From the review of v2-editing: the shortest temporary mode the form offers works, and machine rules go to the
// machine as Rudolph stores it (any case), never to a machine that has never synced.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

func TestWriter_OneMinuteTemporaryModeIsAccepted(t *testing.T) {
	st := newStore(t)
	machines, err := st.ListMachines(ctx, store.MachineFilter{}, store.PageRequest{Limit: 1})
	if err != nil || len(machines.Items) == 0 {
		t.Fatalf("no machine: %v", err)
	}
	until := time.Now().Add(time.Minute)
	time.Sleep(200 * time.Millisecond) // the handler computed until a moment before the store writes
	if err := st.SetTemporaryMode(ctx, writerActor, machines.Items[0].MachineID, store.ClientModeLockdown, until); err != nil {
		t.Errorf("a one-minute temporary mode: %v", err)
	}
}

func TestWriter_MachineRuleUsesTheStoredMachineID(t *testing.T) {
	st := newStore(t)
	machines, err := st.ListMachines(ctx, store.MachineFilter{}, store.PageRequest{Limit: 1})
	if err != nil || len(machines.Items) == 0 {
		t.Fatalf("no machine: %v", err)
	}
	stored := machines.Items[0].MachineID
	typed := strings.ToLower(stored)
	if typed == stored {
		typed = strings.ToUpper(stored)
	}
	rule := store.NewRule{RuleType: store.RuleTypeBinary, Identifier: writerSHA, Policy: store.PolicyAllowlist, ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.AddRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetMachine, MachineID: typed}, rule); err != nil {
		t.Fatal(err)
	}
	p, err := st.ListMachineRules(ctx, stored, store.PageRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range p.Items {
		if r.Identifier == writerSHA {
			found = true
		}
	}
	if !found {
		t.Errorf("rule typed for %s is not under the stored ID %s", typed, stored)
	}
	entries, _ := st.ListAudit(ctx, time.Now().Add(-time.Hour), 5)
	if len(entries) == 0 || entries[0].Target != "machine:"+stored {
		t.Errorf("audit target %+v, want machine:%s", entries, stored)
	}

	never := "FFFFFFFF-9999-4999-8999-FFFFFFFFFFFF"
	if err := st.AddRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetMachine, MachineID: never}, rule); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("rule for a machine that never synced: %v, want ErrNotFound", err)
	}
}
