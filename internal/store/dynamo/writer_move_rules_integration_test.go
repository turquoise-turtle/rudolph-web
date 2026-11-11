//go:build integration

package dynamo_test

// Moving a Mac's rules to another Mac (owner, 2026-11-11; see internal/web/move_machine_rules_test.go):
// store.Writer.MoveMachineRules(ctx, actor, from, to, remove) copies every machine rule in force on `from` to `to`,
// as stored (policy, custom message and URL, CEL expression, description, expiry, CollectAllowedEvents, through
// Rudolph's machine rule functions so the new Mac downloads them), recording rule.add on `to` for each; with remove,
// it then removes each from `from` as `rudolph rule remove` does (rule.remove). It returns how many it copied.
//   - Checked before anything is written: `to` != `from` (store.ErrInvalidArgument), `to` exists (store.ErrNotFound),
//     `from` has rules in force (ErrInvalidArgument), and no rule on `to` has the same type and identifier with a
//     different policy (ErrInvalidArgument naming the identifier). A rule `to` already has with the same policy is
//     not copied again (and still removed from `from` with remove).
//   - At most 500 rules are moved in one request (ErrInvalidArgument above that: the CLI is for bulk work).

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

func TestWriter_MoveMachineRules(t *testing.T) {
	st := newStore(t)
	shaX, shaY := strings.Repeat("e1", 32), strings.Repeat("e2", 32)
	expires := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	if err := st.AddRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetMachine, MachineID: machineA},
		store.NewRule{RuleType: store.RuleTypeBinary, Identifier: shaX, Policy: store.PolicyAllowlist, CustomMessage: "Lab tool", Description: "for Ada", ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetMachine, MachineID: machineA},
		store.NewRule{RuleType: store.RuleTypeBinary, Identifier: shaY, Policy: store.PolicyBlocklist}); err != nil {
		t.Fatal(err)
	}
	inForce := func(id string) map[string]store.Rule {
		t.Helper()
		p, err := st.ListMachineRules(ctx, id, store.PageRequest{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]store.Rule{}
		for _, r := range p.Items {
			if r.State == store.RuleStateInForce {
				out[r.Identifier] = r
			}
		}
		return out
	}
	fromBefore := len(inForce(machineA))
	toBefore := inForce(machineB)

	moved, err := st.MoveMachineRules(ctx, writerActor, machineA, machineB, true)
	if err != nil {
		t.Fatal(err)
	}
	if moved != fromBefore {
		t.Errorf("moved %d, want %d (every rule in force on A)", moved, fromBefore)
	}
	after := inForce(machineB)
	if r, ok := after[shaX]; !ok || r.Policy != store.PolicyAllowlist || r.CustomMessage != "Lab tool" || r.Description != "for Ada" || !r.ExpiresAt.Equal(expires) {
		t.Errorf("rule X on B: %+v, %v", r, ok)
	}
	if r, ok := after[shaY]; !ok || r.Policy != store.PolicyBlocklist {
		t.Errorf("rule Y on B: %+v, %v", r, ok)
	}
	for id := range toBefore {
		if _, ok := after[id]; !ok {
			t.Errorf("B lost its own rule %s", id)
		}
	}
	if left := inForce(machineA); len(left) != 0 {
		t.Errorf("%d rules still in force on A after a move", len(left))
	}

	entries, err := st.ListAudit(ctx, time.Now().Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	var adds, removes int
	for _, e := range entries {
		switch {
		case e.Action == "rule.add" && e.Target == "machine:"+machineB:
			adds++
		case e.Action == "rule.remove" && e.Target == "machine:"+machineA:
			removes++
		}
	}
	if adds != moved || removes != fromBefore {
		t.Errorf("audit: %d adds on B, %d removes on A; want %d and %d", adds, removes, moved, fromBefore)
	}
}

func TestWriter_MoveMachineRulesRefusals(t *testing.T) {
	st := newStore(t)
	sha := strings.Repeat("e3", 32)
	if err := st.AddRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetMachine, MachineID: machineA},
		store.NewRule{RuleType: store.RuleTypeBinary, Identifier: sha, Policy: store.PolicyAllowlist}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetMachine, MachineID: machineB},
		store.NewRule{RuleType: store.RuleTypeBinary, Identifier: sha, Policy: store.PolicyBlocklist}); err != nil {
		t.Fatal(err)
	}
	auditBefore, _ := st.ListAudit(ctx, time.Now().Add(-time.Hour), 100)

	if _, err := st.MoveMachineRules(ctx, writerActor, machineA, machineA, true); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("same Mac: %v", err)
	}
	if _, err := st.MoveMachineRules(ctx, writerActor, machineA, "CCCCCCCC-3333-4333-8333-CCCCCCCCCCCC", true); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown target: %v", err)
	}
	if _, err := st.MoveMachineRules(ctx, writerActor, machineA, machineB, true); !errors.Is(err, store.ErrInvalidArgument) || !strings.Contains(err.Error(), sha) {
		t.Errorf("conflicting policy: %v; want ErrInvalidArgument naming %s", err, sha)
	}
	if after, _ := st.ListAudit(ctx, time.Now().Add(-time.Hour), 100); len(after) != len(auditBefore) {
		t.Errorf("refusals wrote %d audit entries", len(after)-len(auditBefore))
	}
}
