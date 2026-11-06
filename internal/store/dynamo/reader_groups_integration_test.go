//go:build integration

package dynamo_test

// Device groups (Rudolph's docs/groups.md; rows in testdata/webcontract/README.md): the registry
// `GroupRegistry`/`Current` (Groups: name → Description, AcceptReported, RulesVersion, CreatedAt), group rules
// `GroupRules#<group>`/`<RuleType>#<identifier>`, and a Mac's `Machine#<id>`/`Groups` (Assigned string set, Reported
// list) and `Machine#<id>`/`ReportedGroups` (PrimaryUserGroups as Santa sent them).
//
// GetMachine returns the Mac's groups from the same single Query (the rows sort between Config and SyncState).
// ListGroups reads the registry with one GetItem, sorted by name; no registry means no groups. ListGroupRules pages a
// group's rules with one Query per page, like the global rules, with cursors bound to the group.

import (
	"errors"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

func TestGetMachine_Groups(t *testing.T) {
	s := newStore(t)
	got, err := s.GetMachine(ctx, machineA)
	if err != nil {
		t.Fatal(err)
	}
	queriesOnly(t, 1)
	if strings.Join(got.AssignedGroups, ",") != "eng" || strings.Join(got.ReportedGroups, ",") != "eng" {
		t.Errorf("assigned %v, reported (counted) %v; want eng and eng", got.AssignedGroups, got.ReportedGroups)
	}
	if strings.Join(got.ReportedAsSent, ",") != "Eng,staff" {
		t.Errorf("reported as sent %v, want Eng,staff", got.ReportedAsSent)
	}
	b, err := s.GetMachine(ctx, machineB)
	if err == nil && (len(b.AssignedGroups) != 0 || len(b.ReportedGroups) != 0) {
		t.Errorf("machine B has groups %v %v", b.AssignedGroups, b.ReportedGroups)
	}
}

func TestListGroups(t *testing.T) {
	s := newStore(t)
	groups, err := s.ListGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Name != "eng" || groups[0].Description != "Engineering" || !groups[0].AcceptReported {
		t.Errorf("groups %+v, want eng (Engineering, accepts reported membership)", groups)
	}
	if c := ddblocal.Counts(); c["GetItem"] != 1 || c["Query"] != 0 || c["Scan"] != 0 {
		t.Errorf("calls %v, want one GetItem", c)
	}

	ddblocal.Reset(t)
	none, err := s.ListGroups(ctx)
	if err != nil || len(none) != 0 {
		t.Errorf("no registry: %+v, %v; want no groups", none, err)
	}
}

func TestListGroupRules(t *testing.T) {
	s := newStore(t)
	page, err := s.ListGroupRules(ctx, "eng", store.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	queriesOnly(t, 1)
	if len(page.Items) != 1 {
		t.Fatalf("rules %+v, want the one eng rule", page.Items)
	}
	r := page.Items[0]
	if r.RuleType != store.RuleTypeSigningID || r.Policy != store.PolicyBlocklist || r.Identifier != "EQHXZ8M8AV:com.google.GeminiMacOS" || r.CustomMessage != "Not for Eng" {
		t.Errorf("rule %+v", r)
	}

	empty, err := s.ListGroupRules(ctx, "nobody", store.PageRequest{Limit: 10})
	if err != nil || len(empty.Items) != 0 {
		t.Errorf("unknown group: %+v, %v; want an empty page", empty, err)
	}
	if _, err := s.ListGroupRules(ctx, "eng", store.PageRequest{Limit: 0}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("limit 0: %v", err)
	}
}

func TestListGroupRules_CursorBoundToTheGroup(t *testing.T) {
	s := newStore(t)
	// The fixture has one eng rule; use global rule paging to obtain a cursor of another query, which must not be
	// accepted here.
	global, err := s.ListGlobalRules(ctx, store.RuleFilter{}, store.PageRequest{Limit: 1})
	if err != nil || global.NextCursor == "" {
		t.Fatalf("precondition: %v", err)
	}
	if _, err := s.ListGroupRules(ctx, "eng", store.PageRequest{Limit: 1, Cursor: global.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("a global rules cursor for a group: %v, want ErrInvalidCursor", err)
	}
}
