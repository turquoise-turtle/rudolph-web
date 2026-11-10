//go:build integration

package dynamo_test

// Group management from the web view (owner request, editing on, admins only): create, update and delete groups and
// assign or unassign a group on a Mac, through Rudolph's pkg/model/groups exactly as `rudolph group create|update|
// delete|add|remove` do, each recording the CLI's audit entry with the web actor. Names are normalised as the CLI
// does (lower case, trimmed), so a name reported as "Eng Team " creates "eng team".

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

func findGroup(t *testing.T, groups []store.Group, name string) *store.Group {
	t.Helper()
	for i := range groups {
		if groups[i].Name == name {
			return &groups[i]
		}
	}
	return nil
}

func TestWriter_GroupLifecycle(t *testing.T) {
	st := newStore(t)

	name, err := st.CreateGroup(ctx, writerActor, "  Lab Macs ", "Teaching labs", true)
	if err != nil {
		t.Fatal(err)
	}
	if name != "lab macs" {
		t.Errorf("created %q, want the normalised name \"lab macs\"", name)
	}
	groups, err := st.ListGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g := findGroup(t, groups, "lab macs"); g == nil || g.Description != "Teaching labs" || !g.AcceptReported {
		t.Fatalf("groups %+v; want lab macs, Teaching labs, accepting reported membership", groups)
	}

	desc := "Teaching labs, building B"
	no := false
	if err := st.UpdateGroup(ctx, writerActor, "lab macs", &desc, &no); err != nil {
		t.Fatal(err)
	}
	groups, _ = st.ListGroups(ctx)
	if g := findGroup(t, groups, "lab macs"); g == nil || g.Description != desc || g.AcceptReported {
		t.Errorf("after update %+v", g)
	}

	if err := st.AssignGroup(ctx, writerActor, machineA, "lab macs"); err != nil {
		t.Fatal(err)
	}
	m, err := st.GetMachine(ctx, machineA)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.AssignedGroups, ",") != "eng,lab macs" {
		t.Errorf("assigned %v, want eng and lab macs", m.AssignedGroups)
	}
	if err := st.UnassignGroup(ctx, writerActor, machineA, "lab macs"); err != nil {
		t.Fatal(err)
	}
	m, _ = st.GetMachine(ctx, machineA)
	if strings.Join(m.AssignedGroups, ",") != "eng" {
		t.Errorf("after unassigning, assigned %v, want eng", m.AssignedGroups)
	}

	if err := st.AddRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetGroup, Group: "lab macs"},
		store.NewRule{RuleType: store.RuleTypeBinary, Identifier: writerSHA, Policy: store.PolicyBlocklist}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteGroup(ctx, writerActor, "lab macs", false); !errors.Is(err, store.ErrInvalidArgument) || !strings.Contains(err.Error(), "rules") {
		t.Errorf("deleting a group with rules without force: %v; want ErrInvalidArgument mentioning its rules", err)
	}
	if err := st.DeleteGroup(ctx, writerActor, "lab macs", true); err != nil {
		t.Fatal(err)
	}
	groups, _ = st.ListGroups(ctx)
	if findGroup(t, groups, "lab macs") != nil {
		t.Errorf("lab macs still registered after delete: %+v", groups)
	}
	if rules, err := st.ListGroupRules(ctx, "lab macs", store.PageRequest{Limit: 10}); err != nil || len(rules.Items) != 0 {
		t.Errorf("group rules after a forced delete: %+v, %v; want none", rules.Items, err)
	}

	entries, err := st.ListAudit(ctx, time.Now().Add(-time.Hour), 20)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if !strings.HasPrefix(e.Actor, "web:") || !strings.Contains(e.Actor, "admin@example.com") {
			t.Errorf("audit actor %q, want the web actor", e.Actor)
		}
		got = append(got, fmt.Sprintf("%s|%s|%s", e.Action, e.Target, e.Summary))
	}
	want := []string{
		`group.create|group:lab macs|created group lab macs, description "Teaching labs", Macs that report it count as members`,
		`group.update|group:lab macs|updated group lab macs: description "Teaching labs, building B", Macs that report it are ignored (only assigned Macs are members)`,
		`group.add|machine:` + machineA + `|assigned group lab macs`,
		`group.remove|machine:` + machineA + `|removed assigned group lab macs`,
	}
	if len(got) < len(want)+2 {
		t.Fatalf("audit entries %q", got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("audit entry %d: %q, want %q", i, got[i], w)
		}
	}
	if last := got[len(got)-1]; last != `group.delete|group:lab macs|deleted group lab macs and its rules` {
		t.Errorf("last audit entry %q, want the forced delete", last)
	}
}

func TestWriter_GroupUpdateRecordsOnlyWhatChanged(t *testing.T) {
	st := newStore(t)
	desc := "Engineering team"
	if err := st.UpdateGroup(ctx, writerActor, "eng", &desc, nil); err != nil {
		t.Fatal(err)
	}
	entries, _ := st.ListAudit(ctx, time.Now().Add(-time.Hour), 5)
	if len(entries) != 1 || entries[0].Summary != `updated group eng: description "Engineering team"` {
		t.Errorf("audit %+v", entries)
	}
}

func TestWriter_GroupRefusals(t *testing.T) {
	st := newStore(t)
	for name, err := range map[string]error{
		"create empty":           func() error { _, e := st.CreateGroup(ctx, writerActor, "  ", "", true); return e }(),
		"create reserved":        func() error { _, e := st.CreateGroup(ctx, writerActor, "rudolph:mine", "", true); return e }(),
		"create too long":        func() error { _, e := st.CreateGroup(ctx, writerActor, strings.Repeat("x", 129), "", true); return e }(),
		"create existing":        func() error { _, e := st.CreateGroup(ctx, writerActor, "ENG", "", true); return e }(),
		"delete ungrouped":       st.DeleteGroup(ctx, writerActor, store.UngroupedGroup, true),
		"assign unregistered":    st.AssignGroup(ctx, writerActor, machineA, "no-such-group"),
		"assign ungrouped":       st.AssignGroup(ctx, writerActor, machineA, store.UngroupedGroup),
		"update nothing changed": st.UpdateGroup(ctx, writerActor, "eng", nil, nil),
	} {
		if !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("%s: %v, want ErrInvalidArgument", name, err)
		}
	}
	if err := st.UpdateGroup(ctx, writerActor, "no-such-group", new(string), nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("update a missing group: %v, want ErrNotFound", err)
	}
	if err := st.DeleteGroup(ctx, writerActor, "no-such-group", false); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("delete a missing group: %v, want ErrNotFound", err)
	}
	if entries, _ := st.ListAudit(ctx, time.Now().Add(-time.Hour), 5); len(entries) != 0 {
		t.Errorf("refused writes were audited: %+v", entries)
	}
}

func TestWriter_GroupLimit(t *testing.T) {
	st := newStore(t) // the fixture registers eng
	for i := 1; i < 64; i++ {
		if _, err := st.CreateGroup(ctx, writerActor, fmt.Sprintf("g%02d", i), "", true); err != nil {
			t.Fatalf("group %d: %v", i, err)
		}
	}
	_, err := st.CreateGroup(ctx, writerActor, "one-too-many", "", true)
	if !errors.Is(err, store.ErrInvalidArgument) || !strings.Contains(err.Error(), "64") {
		t.Errorf("65th group: %v, want ErrInvalidArgument naming the limit of 64", err)
	}
}
