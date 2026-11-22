package fake_test

// The fake's fleet health and readiness, which the web tests use as they use its top lists. Test setup:
//
//	fake.FleetSnapshot{Summary store.FleetSummary; Health map[store.FleetCategory][]store.FleetHealthRow;
//	    Readiness map[string][]store.ReadinessRow}   // Readiness[""] is the fleet's list (Ready#All), others a group's
//	(*Store).SetFleetSnapshot(fake.FleetSnapshot)   // keeps it under Summary.Snapshot and points the pointer at it;
//	                                                // earlier snapshots stay readable through their cursors
//	(*Store).ExpireFleetSnapshot(id string)         // its rows are gone, as after DynamoDB's TTL
//	(*Store).SetFleetHealthConfig(store.FleetHealthConfig)
//	(*Store).SetMachineUnknown(machineID string, store.UnknownCounts)   // what MachineUnknown answers for the Mac
//	(*Store).AddUnknownBinary(machineID string, store.UnknownBinary)     // a row of ListMachineUnknown, in SHA order
//
// Calls are recorded as Method "FleetSummary", "FleetHealthConfig", "MachineUnknown" (point reads: Paginated() is
// false) and "ListFleetHealth" (Filter: the category), "ListReadiness" (Filter: the ReadinessFilter),
// "ListMachineUnknown" (MachineID). SetError fails them all.
//
// Like the DynamoDB store: lists are served in their stored order, readiness rows sorted by bucket, then Pending30,
// then machine ID; a cursor pins the snapshot it was issued for, and is bound to the method and the category or
// filter; a pinned snapshot that has expired is ErrInvalidCursor. The writes validate as Rudolph does and use the real
// clock, as the DynamoDB writer does: SetFleetHoliday with fleethealth.Config.ValidateHoliday, AssignCohort with the
// 26-hour limit on the time in the snapshot ID.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

var fleetActor = store.Actor{Username: "admin@example.com", Subject: "00000000-0000-4000-8000-000000000001"}

func snapshotAt(at time.Time) string {
	return at.UTC().Format("20060102T150405.000000000Z") + "-0a1b2c3d"
}

func staleRows(prefix string, n int) []store.FleetHealthRow {
	var rows []store.FleetHealthRow
	for i := 1; i <= n; i++ {
		rows = append(rows, store.FleetHealthRow{MachineID: fmt.Sprintf("%s-%d", prefix, i), Hostname: fmt.Sprintf("%s-%d", prefix, i)})
	}
	return rows
}

func healthNames(rows []store.FleetHealthRow) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Hostname)
	}
	return strings.Join(out, ",")
}

func readyNames(rows []store.ReadinessRow) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.MachineID)
	}
	return strings.Join(out, ",")
}

func TestFleet_NotComputedYet(t *testing.T) {
	s := fake.New()
	sum, err := s.FleetSummary(ctx)
	if err != nil || !sum.GeneratedAt.IsZero() || sum.Snapshot != "" {
		t.Errorf("summary %+v, %v; want the zero summary", sum, err)
	}
	list, err := s.ListFleetHealth(ctx, store.FleetCategoryStale, store.PageRequest{Limit: 10})
	if err != nil || len(list.Items) != 0 || !list.GeneratedAt.IsZero() {
		t.Errorf("health %+v, %v; want an empty list", list, err)
	}
	ready, err := s.ListReadiness(ctx, store.ReadinessFilter{}, store.PageRequest{Limit: 10})
	if err != nil || len(ready.Items) != 0 || !ready.GeneratedAt.IsZero() {
		t.Errorf("readiness %+v, %v; want an empty list", ready, err)
	}
	cfg, err := s.FleetHealthConfig(ctx)
	if err != nil || cfg != (store.FleetHealthConfig{StaleDays: 10}) {
		t.Errorf("config %+v, %v; want the normal 10 days", cfg, err)
	}
}

func TestFleet_CursorPinsTheSnapshotAndExpires(t *testing.T) {
	s := fake.New()
	gen := time.Date(2026, 9, 17, 6, 0, 0, 0, time.UTC)
	s.SetFleetSnapshot(fake.FleetSnapshot{
		Summary: store.FleetSummary{Snapshot: "s1", GeneratedAt: gen, Macs: 3},
		Health:  map[store.FleetCategory][]store.FleetHealthRow{store.FleetCategoryStale: staleRows("old", 3)},
	})
	first, err := s.ListFleetHealth(ctx, store.FleetCategoryStale, store.PageRequest{Limit: 2})
	if err != nil || healthNames(first.Items) != "old-1,old-2" || first.Snapshot != "s1" || !first.GeneratedAt.Equal(gen) || first.NextCursor == "" {
		t.Fatalf("first page %+v, %v", first, err)
	}
	s.SetFleetSnapshot(fake.FleetSnapshot{
		Summary: store.FleetSummary{Snapshot: "s2", GeneratedAt: gen.Add(24 * time.Hour), Macs: 3},
		Health:  map[store.FleetCategory][]store.FleetHealthRow{store.FleetCategoryStale: staleRows("new", 3)},
	})
	second, err := s.ListFleetHealth(ctx, store.FleetCategoryStale, store.PageRequest{Limit: 2, Cursor: first.NextCursor})
	if err != nil || healthNames(second.Items) != "old-3" || second.Snapshot != "s1" {
		t.Errorf("second page %+v, %v; want old-3 from s1", second, err)
	}
	if sum, _ := s.FleetSummary(ctx); sum.Snapshot != "s2" {
		t.Errorf("pointer names %q, want s2", sum.Snapshot)
	}
	if _, err := s.ListFleetHealth(ctx, store.FleetCategoryIncomplete, store.PageRequest{Limit: 2, Cursor: first.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("a Stale cursor on Incomplete: %v, want ErrInvalidCursor", err)
	}
	s.ExpireFleetSnapshot("s1")
	if _, err := s.ListFleetHealth(ctx, store.FleetCategoryStale, store.PageRequest{Limit: 2, Cursor: first.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("a cursor of an expired snapshot: %v, want ErrInvalidCursor", err)
	}
	if _, err := s.ListFleetHealth(ctx, "Sleepy", store.PageRequest{Limit: 2}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("an unknown category: %v, want ErrInvalidArgument", err)
	}
}

func TestFleet_ReadinessOrderScopeAndBucket(t *testing.T) {
	s := fake.New()
	s.SetFleetSnapshot(fake.FleetSnapshot{
		Summary: store.FleetSummary{Snapshot: "s1", GeneratedAt: time.Now(), Macs: 4,
			ReadinessGroups: map[string]store.ReadinessCounts{"eng": {Macs: 1, Ready: 1}}},
		Readiness: map[string][]store.ReadinessRow{
			"": {
				{MachineID: "d", Bucket: store.ReadinessUnmeasured, Counted: true, Why: "observed"},
				{MachineID: "c", Bucket: store.ReadinessFew, Counted: true, Pending30: 5},
				{MachineID: "b", Bucket: store.ReadinessFew, Counted: true, Pending30: 2},
				{MachineID: "a", Bucket: store.ReadinessReady, Counted: true},
			},
			"eng": {{MachineID: "a", Bucket: store.ReadinessReady, Counted: true}},
		},
	})
	all, err := s.ListReadiness(ctx, store.ReadinessFilter{}, store.PageRequest{Limit: 10})
	if err != nil || readyNames(all.Items) != "a,b,c,d" {
		t.Errorf("all: %q, %v; want bucket, then pending order", readyNames(all.Items), err)
	}
	few, err := s.ListReadiness(ctx, store.ReadinessFilter{Bucket: store.ReadinessFew}, store.PageRequest{Limit: 10})
	if err != nil || readyNames(few.Items) != "b,c" {
		t.Errorf("few: %q, %v", readyNames(few.Items), err)
	}
	eng, err := s.ListReadiness(ctx, store.ReadinessFilter{Group: "eng"}, store.PageRequest{Limit: 10})
	if err != nil || readyNames(eng.Items) != "a" {
		t.Errorf("eng: %q, %v", readyNames(eng.Items), err)
	}
	if _, err := s.ListReadiness(ctx, store.ReadinessFilter{Group: "nope"}, store.PageRequest{Limit: 10}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a group the snapshot does not name: %v, want ErrNotFound", err)
	}
	if _, err := s.ListReadiness(ctx, store.ReadinessFilter{Bucket: "soon"}, store.PageRequest{Limit: 10}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("an unknown bucket: %v, want ErrInvalidArgument", err)
	}
	page, err := s.ListReadiness(ctx, store.ReadinessFilter{}, store.PageRequest{Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("page %+v, %v", page, err)
	}
	if _, err := s.ListReadiness(ctx, store.ReadinessFilter{Bucket: store.ReadinessFew}, store.PageRequest{Limit: 1, Cursor: page.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("a cursor of another filter: %v, want ErrInvalidCursor", err)
	}
}

func TestFleet_MachineUnknownAndCallsRecorded(t *testing.T) {
	s := fake.New()
	s.SetMachineUnknown("m1", store.UnknownCounts{Last30: 12, Last7: 3})
	s.AddUnknownBinary("m1", store.UnknownBinary{FileSHA256: strings.Repeat("b", 64), FileName: "b"})
	s.AddUnknownBinary("m1", store.UnknownBinary{FileSHA256: strings.Repeat("a", 64), FileName: "a"})
	s.AddUnknownBinary("m2", store.UnknownBinary{FileSHA256: strings.Repeat("c", 64), FileName: "c"})
	s.ResetCalls()

	c, err := s.MachineUnknown(ctx, "m1")
	if err != nil || c != (store.UnknownCounts{Last30: 12, Last7: 3}) {
		t.Errorf("counts %+v, %v", c, err)
	}
	if c, _ := s.MachineUnknown(ctx, "m3"); c != (store.UnknownCounts{}) {
		t.Errorf("a Mac without markers: %+v", c)
	}
	page, err := s.ListMachineUnknown(ctx, "m1", store.PageRequest{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].FileName != "a" || page.NextCursor == "" {
		t.Fatalf("page %+v, %v; want a first, in SHA order", page, err)
	}
	if _, err := s.ListMachineUnknown(ctx, "m2", store.PageRequest{Limit: 1, Cursor: page.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("m1's cursor on m2: %v, want ErrInvalidCursor", err)
	}
	if _, err := s.FleetSummary(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FleetHealthConfig(ctx); err != nil {
		t.Fatal(err)
	}
	paginated := map[string]bool{}
	for _, call := range s.Calls() {
		paginated[call.Method] = call.Paginated()
	}
	for method, want := range map[string]bool{"MachineUnknown": false, "ListMachineUnknown": true, "FleetSummary": false, "FleetHealthConfig": false} {
		if got, ok := paginated[method]; !ok || got != want {
			t.Errorf("%s recorded %v with Paginated() %v, want %v", method, ok, got, want)
		}
	}

	boom := errors.New("boom")
	s.SetError(boom)
	if _, err := s.MachineUnknown(ctx, "m1"); !errors.Is(err, boom) {
		t.Errorf("MachineUnknown: %v", err)
	}
	if _, err := s.FleetSummary(ctx); !errors.Is(err, boom) {
		t.Errorf("FleetSummary: %v", err)
	}
	if _, err := s.ListReadiness(ctx, store.ReadinessFilter{}, store.PageRequest{Limit: 1}); !errors.Is(err, boom) {
		t.Errorf("ListReadiness: %v", err)
	}
}

func TestFleet_HolidayValidatesAsRudolphAndIsAudited(t *testing.T) {
	s := fake.New()
	day := func(offset int) string { return time.Now().UTC().AddDate(0, 0, offset).Format("2006-01-02") }
	for name, c := range map[string]struct {
		days  int
		until string
	}{
		"0 days":       {0, day(5)},
		"below normal": {5, day(5)},
		"past":         {30, day(-1)},
		"too far":      {30, day(367)},
		"not a date":   {30, "soon"},
	} {
		if err := s.SetFleetHoliday(ctx, fleetActor, c.days, c.until); !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("%s: %v, want ErrInvalidArgument", name, err)
		}
	}
	if err := s.EndFleetHoliday(ctx, fleetActor); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("ending no holiday: %v, want ErrNothingToChange", err)
	}
	until := day(20)
	if err := s.SetFleetHoliday(ctx, fleetActor, 60, until); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := s.FleetHealthConfig(ctx); cfg != (store.FleetHealthConfig{StaleDays: 10, HolidayStaleDays: 60, HolidayUntil: until}) {
		t.Errorf("config %+v", cfg)
	}
	if err := s.EndFleetHoliday(ctx, fleetActor); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := s.FleetHealthConfig(ctx); cfg != (store.FleetHealthConfig{StaleDays: 10}) {
		t.Errorf("config after the end %+v", cfg)
	}
	entries, err := s.ListAudit(ctx, time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Action != "fleet.holiday" || entries[0].Target != "fleet" || !strings.Contains(entries[0].Summary, "ended") ||
		entries[1].Action != "fleet.holiday" || !strings.Contains(entries[1].Summary, until) || !strings.Contains(entries[1].Summary, "60") {
		t.Errorf("audit %+v", entries)
	}
}

const (
	macA = "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA"
	macB = "BBBBBBBB-2222-4222-8222-BBBBBBBBBBBB"
	macC = "CCCCCCCC-3333-4333-8333-CCCCCCCCCCCC"
)

func TestFleet_AssignCohort(t *testing.T) {
	s := fake.New()
	for _, id := range []string{macA, macB, macC} {
		s.AddMachine(store.Machine{MachineID: id})
	}
	s.AddGroup(store.Group{Name: "pilot", ClientMode: store.ClientModeLockdown})
	s.AddGroup(store.Group{Name: "staff"})
	s.AddGroup(store.Group{Name: store.UngroupedGroup, ClientMode: store.ClientModeLockdown})
	snapshot := snapshotAt(time.Now().Add(-2 * time.Hour))
	s.SetFleetSnapshot(fake.FleetSnapshot{
		Summary: store.FleetSummary{Snapshot: snapshot, GeneratedAt: time.Now().Add(-2 * time.Hour)},
		Readiness: map[string][]store.ReadinessRow{"": {
			{MachineID: macA, Bucket: store.ReadinessReady, Counted: true},
			{MachineID: macB, Bucket: store.ReadinessReady, Counted: true},
			{MachineID: macC, Bucket: store.ReadinessFew, Counted: true, Pending30: 2},
		}},
	})
	old := snapshotAt(time.Now().Add(-27 * time.Hour))
	tooMany := make([]string, store.MaxCohortMacs+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("%08X-0000-4000-8000-000000000000", i)
	}
	for name, c := range map[string]struct {
		snapshot, group string
		macs            []string
		want            error
	}{
		"none":            {snapshot, "pilot", nil, store.ErrInvalidArgument},
		"too many":        {snapshot, "pilot", tooMany, store.ErrInvalidArgument},
		"no lockdown":     {snapshot, "staff", []string{macA}, store.ErrInvalidArgument},
		"unknown group":   {snapshot, "nope", []string{macA}, store.ErrNotFound},
		"reserved":        {snapshot, store.UngroupedGroup, []string{macA}, store.ErrInvalidArgument},
		"not ready":       {snapshot, "pilot", []string{macA, macC}, store.ErrInvalidArgument},
		"old snapshot":    {old, "pilot", []string{macA}, store.ErrSnapshotTooOld},
		"not a snapshot":  {"s1", "pilot", []string{macA}, store.ErrInvalidArgument},
		"no actor at all": {snapshot, "pilot", []string{macA}, store.ErrInvalidArgument},
	} {
		actor := fleetActor
		if name == "no actor at all" {
			actor = store.Actor{}
		}
		if n, err := s.AssignCohort(ctx, actor, c.snapshot, c.group, c.macs); !errors.Is(err, c.want) || n != 0 {
			t.Errorf("%s: %d, %v; want 0 and %v", name, n, err, c.want)
		}
	}
	if entries, _ := s.ListAudit(ctx, time.Now().Add(-time.Hour), 10); len(entries) != 0 {
		t.Fatalf("refusals wrote %+v", entries)
	}
	n, err := s.AssignCohort(ctx, fleetActor, snapshot, "pilot", []string{macA, macB})
	if err != nil || n != 2 {
		t.Fatalf("assign: %d, %v", n, err)
	}
	for _, id := range []string{macA, macB} {
		d, _ := s.GetMachine(ctx, id)
		if len(d.AssignedGroups) != 1 || d.AssignedGroups[0] != "pilot" {
			t.Errorf("%s assigned %v", id, d.AssignedGroups)
		}
	}
	entries, _ := s.ListAudit(ctx, time.Now().Add(-time.Hour), 10)
	if len(entries) != 2 || entries[0].Action != "group.add" || entries[1].Action != "group.add" {
		t.Errorf("audit %+v; want group.add for each Mac", entries)
	}
}
