package fake_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

var ctx = context.Background()

// The fake must satisfy every read interface the web layer depends on.
var (
	_ store.Reader        = (*fake.Store)(nil)
	_ store.MachineReader = (*fake.Store)(nil)
	_ store.RuleReader    = (*fake.Store)(nil)
	_ store.EventReader   = (*fake.Store)(nil)
)

func TestListMachines_CursorWalksAllPagesWithoutOverlap(t *testing.T) {
	s := fake.New()
	for i := 0; i < 23; i++ {
		s.AddMachine(store.Machine{MachineID: fmt.Sprintf("m-%02d", i), Hostname: fmt.Sprintf("host-%02d", i), SerialNum: fmt.Sprintf("SN%02d", i)})
	}
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		page, err := s.ListMachines(ctx, store.MachineFilter{}, store.PageRequest{Limit: 10, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		pages++
		if len(page.Items) > 10 {
			t.Fatalf("page has %d items, limit was 10", len(page.Items))
		}
		for _, m := range page.Items {
			if seen[m.MachineID] {
				t.Fatalf("machine %s returned twice", m.MachineID)
			}
			seen[m.MachineID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if pages != 3 || len(seen) != 23 {
		t.Fatalf("got %d pages and %d machines, want 3 and 23", pages, len(seen))
	}
}

func TestListMachines_ExactMultipleHasNoEmptyTrailingPage(t *testing.T) {
	s := fake.New()
	for i := 0; i < 4; i++ {
		s.AddMachine(store.Machine{MachineID: fmt.Sprintf("m-%d", i)})
	}
	page, err := s.ListMachines(ctx, store.MachineFilter{}, store.PageRequest{Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 4 || page.NextCursor != "" {
		t.Fatalf("got %d items, cursor %q; want 4 items and no cursor", len(page.Items), page.NextCursor)
	}
}

func TestListMachines_PrefixFilters(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "1", Hostname: "eng-mac-1", SerialNum: "C02AAA"})
	s.AddMachine(store.Machine{MachineID: "2", Hostname: "eng-mac-2", SerialNum: "FVFBBB"})
	s.AddMachine(store.Machine{MachineID: "3", Hostname: "ops-mac-1", SerialNum: "C02CCC"})

	ids := func(p store.Page[store.Machine]) []string {
		var out []string
		for _, m := range p.Items {
			out = append(out, m.MachineID)
		}
		return out
	}
	page, err := s.ListMachines(ctx, store.MachineFilter{HostnamePrefix: "eng-"}, store.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(ids(page)); got != "[1 2]" {
		t.Errorf("hostname prefix eng-: got %s", got)
	}
	page, err = s.ListMachines(ctx, store.MachineFilter{SerialPrefix: "C02"}, store.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(ids(page)); got != "[1 3]" {
		t.Errorf("serial prefix C02: got %s", got)
	}
	_, err = s.ListMachines(ctx, store.MachineFilter{SerialPrefix: "C02", HostnamePrefix: "eng"}, store.PageRequest{Limit: 10})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("both prefixes: err %v, want ErrInvalidArgument (each maps to a different index)", err)
	}
}

func TestPaging_RejectsBadLimitsAndCursors(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "a"})
	s.AddMachine(store.Machine{MachineID: "b"})

	for _, limit := range []int{0, -1, store.MaxPageSize + 1} {
		if _, err := s.ListMachines(ctx, store.MachineFilter{}, store.PageRequest{Limit: limit}); !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("limit %d: err %v, want ErrInvalidArgument", limit, err)
		}
	}
	if _, err := s.ListMachines(ctx, store.MachineFilter{}, store.PageRequest{Limit: 1, Cursor: "garbage!"}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("garbage cursor: err %v, want ErrInvalidCursor", err)
	}

	// A cursor is bound to the query that produced it.
	page, err := s.ListMachines(ctx, store.MachineFilter{}, store.PageRequest{Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("setup: %v %q", err, page.NextCursor)
	}
	if _, err := s.ListMachines(ctx, store.MachineFilter{HostnamePrefix: "x"}, store.PageRequest{Limit: 1, Cursor: page.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("cursor reused with other filter: err %v, want ErrInvalidCursor", err)
	}
	if _, err := s.ListGlobalRules(ctx, store.RuleFilter{}, store.PageRequest{Limit: 1, Cursor: page.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("machine cursor used for rules: err %v, want ErrInvalidCursor", err)
	}
}

func TestGetMachine(t *testing.T) {
	s := fake.New()
	pre := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	s.AddMachine(store.Machine{MachineID: "m1", Hostname: "h1", ClientMode: store.ClientModeLockdown})
	s.SetSyncState("m1", store.SyncState{PreflightAt: pre, PostflightAt: pre.Add(time.Minute), LastSyncType: "CLEAN", RulesProcessed: 7})
	s.AddMachine(store.Machine{MachineID: "m2", Hostname: "h2"})

	d, err := s.GetMachine(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Hostname != "h1" || d.ClientMode != store.ClientModeLockdown || d.Sync == nil || !d.Sync.PreflightAt.Equal(pre) || d.Sync.RulesProcessed != 7 {
		t.Errorf("unexpected detail: %+v sync=%+v", d.Machine, d.Sync)
	}
	d, err = s.GetMachine(ctx, "m2")
	if err != nil || d.Sync != nil {
		t.Errorf("machine without sync state: err=%v sync=%+v", err, d.Sync)
	}
	if _, err := s.GetMachine(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown machine: err %v, want ErrNotFound", err)
	}
}

func TestListMachineRules_ScopedToMachine(t *testing.T) {
	s := fake.New()
	s.AddMachineRule("m1", store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: "aaa"})
	s.AddMachineRule("m2", store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyBlocklist, Identifier: "bbb"})
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeTeamID, Policy: store.PolicyAllowlist, Identifier: "EQHXZ8M8AV"})

	page, err := s.ListMachineRules(ctx, "m1", store.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Identifier != "aaa" {
		t.Fatalf("got %+v, want only m1's rule", page.Items)
	}
}

func TestListGlobalRules_Filters(t *testing.T) {
	s := fake.New()
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: "b1"})
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyBlocklist, Identifier: "b2"})
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeTeamID, Policy: store.PolicyBlocklist, Identifier: "t1"})
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeCDHash, Policy: store.PolicyCEL, Identifier: "c1", CELExpr: "true"})

	cases := []struct {
		f    store.RuleFilter
		want string
	}{
		{store.RuleFilter{}, "[b1 b2 c1 t1]"},
		{store.RuleFilter{RuleType: store.RuleTypeBinary}, "[b1 b2]"},
		{store.RuleFilter{Policy: store.PolicyBlocklist}, "[b2 t1]"},
		{store.RuleFilter{RuleType: store.RuleTypeTeamID, Policy: store.PolicyBlocklist}, "[t1]"},
		{store.RuleFilter{RuleType: store.RuleTypeTeamID, Policy: store.PolicyAllowlist}, "[]"},
	}
	for _, tc := range cases {
		page, err := s.ListGlobalRules(ctx, tc.f, store.PageRequest{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range page.Items {
			got = append(got, r.Identifier)
		}
		if fmt.Sprint(got) != tc.want && !(tc.want == "[]" && len(got) == 0) {
			t.Errorf("filter %+v: got %v, want %s", tc.f, got, tc.want)
		}
	}
}

func TestListExecutionEvents_NewestFirstAndFilters(t *testing.T) {
	s := fake.New()
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	s.AddExecutionEvent(store.ExecutionEvent{MachineID: "m1", FileName: "old", Decision: store.DecisionBlockUnknown, TeamID: "T1", ExecutionTime: t0})
	s.AddExecutionEvent(store.ExecutionEvent{MachineID: "m1", FileName: "new", Decision: store.DecisionAllowBinary, TeamID: "T2", ExecutionTime: t0.Add(time.Hour)})
	s.AddExecutionEvent(store.ExecutionEvent{MachineID: "m2", FileName: "mid", Decision: store.DecisionBlockUnknown, TeamID: "T1", ExecutionTime: t0.Add(30 * time.Minute)})

	names := func(f store.EventFilter) string {
		page, err := s.ListExecutionEvents(ctx, f, store.PageRequest{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range page.Items {
			out = append(out, e.FileName)
		}
		return fmt.Sprint(out)
	}
	if got := names(store.EventFilter{}); got != "[new mid old]" {
		t.Errorf("all: %s", got)
	}
	if got := names(store.EventFilter{MachineID: "m1"}); got != "[new old]" {
		t.Errorf("machine m1: %s", got)
	}
	if got := names(store.EventFilter{Decision: store.DecisionBlockUnknown}); got != "[mid old]" {
		t.Errorf("decision: %s", got)
	}
	if got := names(store.EventFilter{TeamID: "T1", MachineID: "m2"}); got != "[mid]" {
		t.Errorf("team+machine: %s", got)
	}
}

func TestTopBlockedBinaries_AggregatesBlocksInWindow(t *testing.T) {
	s := fake.New()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	add := func(machine, sha string, d store.Decision, age time.Duration) {
		s.AddExecutionEvent(store.ExecutionEvent{MachineID: machine, FileSHA256: sha, FileName: "bin-" + sha, Decision: d, ExecutionTime: now.Add(-age)})
	}
	add("m1", "aaa", store.DecisionBlockUnknown, time.Hour)
	add("m2", "aaa", store.DecisionBlockBinary, 2*time.Hour)
	add("m2", "aaa", store.DecisionBlockUnknown, 3*time.Hour)
	add("m1", "bbb", store.DecisionBlockUnknown, time.Hour)
	add("m1", "bbb", store.DecisionAllowBinary, time.Hour)        // allowed: not counted
	add("m1", "ccc", store.DecisionBlockUnknown, 10*24*time.Hour) // outside window
	add("m1", "ccc", store.DecisionBlockUnknown, 11*24*time.Hour)

	page, err := s.TopBlockedBinaries(ctx, store.TopBlockedFilter{Since: now.Add(-7 * 24 * time.Hour)}, store.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("got %d binaries, want 2: %+v", len(page.Items), page.Items)
	}
	a, b := page.Items[0], page.Items[1]
	if a.FileSHA256 != "aaa" || a.BlockCount != 3 || a.MachineCount != 2 || !a.LastBlocked.Equal(now.Add(-time.Hour)) {
		t.Errorf("first = %+v, want aaa blocked 3 times on 2 machines", a)
	}
	if b.FileSHA256 != "bbb" || b.BlockCount != 1 || b.MachineCount != 1 {
		t.Errorf("second = %+v, want bbb blocked once", b)
	}
}

func TestListFileAccessEvents(t *testing.T) {
	s := fake.New()
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	s.AddFileAccessEvent(store.FileAccessEvent{MachineID: "m1", RuleName: "ChromeCookies", Target: "/a", Decision: store.FileAccessDecisionDenied, AccessTime: t0,
		ProcessChain: []store.Process{{FilePath: "/usr/bin/curl", TeamID: ""}}})
	s.AddFileAccessEvent(store.FileAccessEvent{MachineID: "m2", RuleName: "ChromeCookies", Target: "/b", Decision: store.FileAccessDecisionAuditOnly, AccessTime: t0})

	page, err := s.ListFileAccessEvents(ctx, store.FileAccessEventFilter{MachineID: "m1"}, store.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Target != "/a" || len(page.Items[0].ProcessChain) != 1 {
		t.Fatalf("got %+v", page.Items)
	}
	page, err = s.ListFileAccessEvents(ctx, store.FileAccessEventFilter{Decision: store.FileAccessDecisionAuditOnly}, store.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Target != "/b" {
		t.Fatalf("decision filter: %v %+v", err, page.Items)
	}
}

func TestCalls_AreRecordedAndResettable(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "m1"})
	_, _ = s.ListMachines(ctx, store.MachineFilter{HostnamePrefix: "h"}, store.PageRequest{Limit: 5, Cursor: ""})
	_, _ = s.GetMachine(ctx, "m1")
	_, _ = s.ListMachineRules(ctx, "m1", store.PageRequest{Limit: 7})

	calls := s.Calls()
	if len(calls) != 3 {
		t.Fatalf("recorded %d calls, want 3: %+v", len(calls), calls)
	}
	if calls[0].Method != "ListMachines" || calls[0].Page.Limit != 5 || calls[0].Filter != (store.MachineFilter{HostnamePrefix: "h"}) {
		t.Errorf("call 0 = %+v", calls[0])
	}
	if calls[1].Method != "GetMachine" || calls[1].MachineID != "m1" || calls[1].Paginated() {
		t.Errorf("call 1 = %+v", calls[1])
	}
	if calls[2].Method != "ListMachineRules" || calls[2].MachineID != "m1" || calls[2].Page.Limit != 7 || !calls[2].Paginated() {
		t.Errorf("call 2 = %+v", calls[2])
	}
	s.ResetCalls()
	if len(s.Calls()) != 0 {
		t.Error("ResetCalls did not clear")
	}
}

func TestSetError_FailsEveryRead(t *testing.T) {
	s := fake.New()
	boom := errors.New("boom")
	s.SetError(boom)
	if _, err := s.ListMachines(ctx, store.MachineFilter{}, store.PageRequest{Limit: 1}); !errors.Is(err, boom) {
		t.Errorf("ListMachines err %v", err)
	}
	if _, err := s.GetMachine(ctx, "x"); !errors.Is(err, boom) {
		t.Errorf("GetMachine err %v", err)
	}
	if _, err := s.ListExecutionEvents(ctx, store.EventFilter{}, store.PageRequest{Limit: 1}); !errors.Is(err, boom) {
		t.Errorf("ListExecutionEvents err %v", err)
	}
}
