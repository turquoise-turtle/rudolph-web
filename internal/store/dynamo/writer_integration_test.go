//go:build integration

package dynamo_test

// The DynamoDB writer (plan F2): *dynamo.Store implements store.Writer through Rudolph's own Go packages
// (github.com/airbnb/rudolph pkg/model/...), so a rule written here is the row the CLI would write, Macs receive it
// at their next sync, and every write records an audit entry (Rudolph's pkg/model/audit) naming the web actor.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

var (
	writerSHA   = strings.Repeat("ab", 32)
	writerActor = store.Actor{Username: "admin@example.com", Name: "Ada Admin", Subject: "00000000-0000-4000-8000-000000000001"}
)

func TestWriter_GlobalRuleAddRemoveAndAudit(t *testing.T) {
	st := newStore(t)
	before, err := st.ListGlobalRules(ctx, store.RuleFilter{}, store.PageRequest{Limit: store.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	rule := store.NewRule{RuleType: store.RuleTypeBinary, Identifier: writerSHA, Policy: store.PolicyBlocklist, CustomMessage: "Blocked by IT", Description: "web test"}
	if err := st.AddRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetGlobal}, rule); err != nil {
		t.Fatal(err)
	}
	after, err := st.ListGlobalRules(ctx, store.RuleFilter{RuleType: store.RuleTypeBinary}, store.PageRequest{Limit: store.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	var got *store.Rule
	for i := range after.Items {
		if after.Items[i].Identifier == writerSHA {
			got = &after.Items[i]
		}
	}
	if got == nil || got.Policy != store.PolicyBlocklist || got.CustomMessage != "Blocked by IT" || got.State != store.RuleStateInForce {
		t.Fatalf("written rule %+v (had %d rules before)", got, len(before.Items))
	}

	entries, err := st.ListAudit(ctx, time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Action != "rule.add" || entries[0].Target != "global" ||
		!strings.HasPrefix(entries[0].Actor, "web:") || !strings.Contains(entries[0].Actor, "admin@example.com") ||
		!strings.Contains(entries[0].Summary, writerSHA) {
		t.Errorf("audit %+v", entries)
	}

	if err := st.RemoveRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetGlobal}, store.RuleTypeBinary, writerSHA); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetGlobal}, store.RuleTypeBinary, strings.Repeat("cd", 32)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("removing a missing rule: %v, want ErrNotFound", err)
	}
	entries, _ = st.ListAudit(ctx, time.Now().Add(-time.Hour), 10)
	if len(entries) != 2 || entries[0].Action != "rule.remove" {
		t.Errorf("audit after removal %+v", entries)
	}
}

func TestWriter_RejectsWhatTheCLIRejects(t *testing.T) {
	st := newStore(t)
	for name, c := range map[string]struct {
		target store.RuleTarget
		rule   store.NewRule
	}{
		"bad sha":         {store.RuleTarget{Kind: store.TargetGlobal}, store.NewRule{RuleType: store.RuleTypeBinary, Identifier: "abc", Policy: store.PolicyBlocklist}},
		"remove policy":   {store.RuleTarget{Kind: store.TargetGlobal}, store.NewRule{RuleType: store.RuleTypeBinary, Identifier: writerSHA, Policy: store.PolicyRemove}},
		"bad machine":     {store.RuleTarget{Kind: store.TargetMachine, MachineID: "nope"}, store.NewRule{RuleType: store.RuleTypeBinary, Identifier: writerSHA, Policy: store.PolicyBlocklist}},
		"unknown group":   {store.RuleTarget{Kind: store.TargetGroup, Group: "no-such-group"}, store.NewRule{RuleType: store.RuleTypeBinary, Identifier: writerSHA, Policy: store.PolicyBlocklist}},
		"global expiry":   {store.RuleTarget{Kind: store.TargetGlobal}, store.NewRule{RuleType: store.RuleTypeBinary, Identifier: writerSHA, Policy: store.PolicyBlocklist, ExpiresAt: time.Now().Add(time.Hour)}},
		"bad team ID":     {store.RuleTarget{Kind: store.TargetGlobal}, store.NewRule{RuleType: store.RuleTypeTeamID, Identifier: "short", Policy: store.PolicyAllowlist}},
		"anonymous actor": {store.RuleTarget{Kind: store.TargetGlobal}, store.NewRule{RuleType: store.RuleTypeBinary, Identifier: writerSHA, Policy: store.PolicyBlocklist}},
	} {
		actor := writerActor
		if name == "anonymous actor" {
			actor = store.Actor{}
		}
		if err := st.AddRule(ctx, actor, c.target, c.rule); !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("%s: %v, want ErrInvalidArgument", name, err)
		}
	}
	if entries, _ := st.ListAudit(ctx, time.Now().Add(-time.Hour), 10); len(entries) != 0 {
		t.Errorf("rejected writes were audited: %+v", entries)
	}
}

func TestWriter_TemporaryMode(t *testing.T) {
	st := newStore(t)
	machines, err := st.ListMachines(ctx, store.MachineFilter{}, store.PageRequest{Limit: 1})
	if err != nil || len(machines.Items) == 0 {
		t.Fatalf("no machine in the fixture rows: %v", err)
	}
	id := machines.Items[0].MachineID
	until := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	if err := st.SetTemporaryMode(ctx, writerActor, id, store.ClientModeLockdown, until); err != nil {
		t.Fatal(err)
	}
	d, err := st.GetMachine(ctx, id)
	if err != nil || d.TemporaryClientMode != store.ClientModeLockdown || !d.TemporaryClientModeUntil.Equal(until) {
		t.Fatalf("after set: %+v, %v", d, err)
	}
	if err := st.SetTemporaryMode(ctx, writerActor, id, store.ClientModeLockdown, time.Now().Add(31*24*time.Hour)); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("31 days: %v, want ErrInvalidArgument", err)
	}
	if err := st.EndTemporaryMode(ctx, writerActor, id); err != nil {
		t.Fatal(err)
	}
	if d, _ := st.GetMachine(ctx, id); d.TemporaryClientMode != "" {
		t.Errorf("after end: %+v", d)
	}
	entries, _ := st.ListAudit(ctx, time.Now().Add(-time.Hour), 10)
	if len(entries) != 2 || entries[0].Action != "config.end-temporary-mode" || entries[1].Target != "machine:"+id {
		t.Errorf("audit %+v", entries)
	}
}
