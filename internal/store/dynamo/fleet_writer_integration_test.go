//go:build integration

package dynamo_test

// Writes for the fleet pages (owner decisions of 2026-11-22):
//
// Holiday mode, through Rudolph's pkg/model/fleethealth, as `rudolph fleet holiday` does, so the web view and the CLI
// validate and write alike. The setting is one store-table item, PK FleetHealth, SK Config (StaleDays, the normal
// threshold, 10 when absent; HolidayStaleDays and HolidayUntil, a UTC date, inclusive):
//
//	FleetHealthConfig(ctx) (store.FleetHealthConfig{StaleDays, HolidayStaleDays int; HolidayUntil string}, error)
//	    one GetItem. StaleDays is the normal threshold in force: fleethealth's NormalStaleDays (10 when absent). The
//	    holiday fields are zero when none is stored (a lapsed holiday is still returned, as stored).
//	SetFleetHoliday(ctx, actor, staleDays int, until string) error
//	    fleethealth.SetHoliday at the real time; its refusals (days outside 1 to 365 or below the normal threshold, a
//	    date that is not YYYY-MM-DD, in the past, or more than 366 days ahead) are ErrInvalidArgument with Rudolph's
//	    message. Audited as the CLI does: Action fleet.holiday, Target fleet, a summary naming the days and the date.
//	EndFleetHoliday(ctx, actor) error
//	    fleethealth.EndHoliday; ErrNothingToChange (nothing written or audited) when no holiday is stored. Audited
//	    fleet.holiday on fleet, the summary saying the holiday ended.
//
// The cohort action (design, Fix 3, "AssignCohort"):
//
//	AssignCohort(ctx, actor, snapshot, group string, machineIDs []string) (int, error)
//	    refuses, before writing anything: no Macs, or more than store.MaxCohortMacs (ErrInvalidArgument); a snapshot ID
//	    that is not Rudolph's (ErrInvalidArgument) or was generated more than 26 hours ago, by the time in its ID
//	    (store.ErrSnapshotTooOld); a group that is not registered (ErrNotFound), has no LOCKDOWN or STANDALONE client
//	    mode, or is reserved (rudolph:ungrouped) (ErrInvalidArgument); any Mac that is not in bucket 0 of that
//	    snapshot's Ready#All list, checked with one GetItem of FleetSnapshot#<snapshot>#Ready#All / 0#000000#<machine ID>
//	    each (ErrInvalidArgument naming the Mac). Then AssignGroup for each Mac, each audited as AssignGroup is (group.add
//	    on machine:<id>). A failure part-way is a *store.PartialCohortError saying how many were assigned. It returns
//	    how many Macs it assigned.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

func holidayDay(offset int) string {
	return time.Now().UTC().AddDate(0, 0, offset).Format("2006-01-02")
}

func fleetHealthItem(t *testing.T) map[string]types.AttributeValue {
	t.Helper()
	out, err := ddblocal.Raw(t).GetItem(ctx, &awsdynamodb.GetItemInput{
		TableName:      aws.String(ddblocal.TableName),
		Key:            map[string]types.AttributeValue{"PK": s("FleetHealth"), "SK": s("Config")},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.Item
}

func fleetAudit(t *testing.T, st *dynamo.Store) []store.AuditEntry {
	t.Helper()
	entries, err := st.ListAudit(ctx, time.Now().Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEntry
	for _, e := range entries {
		if e.Action == "fleet.holiday" {
			out = append(out, e)
		}
	}
	return out
}

func TestFleetHealthConfig_DefaultsWithoutTheItem(t *testing.T) {
	st := newStore(t)
	got, err := st.FleetHealthConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != (store.FleetHealthConfig{StaleDays: 10}) {
		t.Errorf("no item: %+v, want the normal threshold of 10 days and no holiday", got)
	}
	if c := ddblocal.Counts(); c["GetItem"] != 1 || len(c) != 1 {
		t.Errorf("calls %v, want one GetItem", c)
	}
}

func TestFleetHoliday_SetAndEndThroughRudolph(t *testing.T) {
	st := newStore(t)
	until := holidayDay(30)
	if err := st.SetFleetHoliday(ctx, writerActor, 60, until); err != nil {
		t.Fatal(err)
	}
	it := fleetHealthItem(t)
	if d, ok := it["HolidayStaleDays"].(*types.AttributeValueMemberN); !ok || d.Value != "60" {
		t.Errorf("HolidayStaleDays %#v, want 60", it["HolidayStaleDays"])
	}
	if u, ok := it["HolidayUntil"].(*types.AttributeValueMemberS); !ok || u.Value != until {
		t.Errorf("HolidayUntil %#v, want %s", it["HolidayUntil"], until)
	}
	if _, ok := it["StaleDays"]; ok {
		t.Error("setting a holiday wrote StaleDays; only the holiday attributes are the web view's to set")
	}
	got, err := st.FleetHealthConfig(ctx)
	if err != nil || got != (store.FleetHealthConfig{StaleDays: 10, HolidayStaleDays: 60, HolidayUntil: until}) {
		t.Errorf("after set: %+v, %v", got, err)
	}
	entries := fleetAudit(t, st)
	if len(entries) != 1 || entries[0].Target != "fleet" || !strings.HasPrefix(entries[0].Actor, "web:") ||
		!strings.Contains(entries[0].Actor, "admin@example.com") || !strings.Contains(entries[0].Summary, "60") ||
		!strings.Contains(entries[0].Summary, until) {
		t.Errorf("audit after set %+v; want fleet.holiday on fleet naming 60 days and %s", entries, until)
	}

	if err := st.EndFleetHoliday(ctx, writerActor); err != nil {
		t.Fatal(err)
	}
	it = fleetHealthItem(t)
	if _, ok := it["HolidayStaleDays"]; ok {
		t.Error("HolidayStaleDays left after ending the holiday")
	}
	if _, ok := it["HolidayUntil"]; ok {
		t.Error("HolidayUntil left after ending the holiday")
	}
	entries = fleetAudit(t, st)
	if len(entries) != 2 || !strings.Contains(entries[0].Summary, "ended") || entries[0].Target != "fleet" {
		t.Errorf("audit after end %+v; want a second fleet.holiday entry saying it ended", entries)
	}

	if err := st.EndFleetHoliday(ctx, writerActor); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("ending again: %v, want ErrNothingToChange", err)
	}
	if len(fleetAudit(t, st)) != 2 {
		t.Error("ending a holiday that was not set was audited")
	}
}

func TestFleetHoliday_EndKeepsTheNormalThreshold(t *testing.T) {
	st := newStore(t)
	putItem(t, map[string]types.AttributeValue{"PK": s("FleetHealth"), "SK": s("Config"), "StaleDays": n(14)})
	if err := st.SetFleetHoliday(ctx, writerActor, 30, holidayDay(5)); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.FleetHealthConfig(ctx); got.StaleDays != 14 || got.HolidayStaleDays != 30 {
		t.Errorf("config %+v, want normal 14 and holiday 30", got)
	}
	if err := st.EndFleetHoliday(ctx, writerActor); err != nil {
		t.Fatal(err)
	}
	if d, ok := fleetHealthItem(t)["StaleDays"].(*types.AttributeValueMemberN); !ok || d.Value != "14" {
		t.Errorf("StaleDays after the end %#v, want 14 kept", fleetHealthItem(t)["StaleDays"])
	}
}

// The refusals are Rudolph's (fleethealth.ValidateHoliday), so the CLI and the web view refuse alike.
func TestFleetHoliday_RefusesWhatTheCLIRefuses(t *testing.T) {
	st := newStore(t)
	putItem(t, map[string]types.AttributeValue{"PK": s("FleetHealth"), "SK": s("Config"), "StaleDays": n(20)})
	for name, c := range map[string]struct {
		days    int
		until   string
		message string
	}{
		"0 days":            {0, holidayDay(10), "between 1 and 365"},
		"366 days":          {366, holidayDay(10), "between 1 and 365"},
		"below normal":      {15, holidayDay(10), "below the normal threshold"},
		"not a date":        {30, "2027-13-01", "YYYY-MM-DD"},
		"yesterday":         {30, holidayDay(-1), "in the past"},
		"367 days ahead":    {30, holidayDay(367), "366 days ahead"},
		"empty date":        {30, "", "YYYY-MM-DD"},
		"date and a suffix": {30, holidayDay(3) + "T00:00:00Z", "YYYY-MM-DD"},
	} {
		err := st.SetFleetHoliday(ctx, writerActor, c.days, c.until)
		if !errors.Is(err, store.ErrInvalidArgument) || !strings.Contains(err.Error(), c.message) {
			t.Errorf("%s: %v; want ErrInvalidArgument with Rudolph's message (%q)", name, err, c.message)
		}
	}
	if err := st.SetFleetHoliday(ctx, store.Actor{}, 30, holidayDay(10)); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("no actor: %v, want ErrInvalidArgument", err)
	}
	if _, ok := fleetHealthItem(t)["HolidayStaleDays"]; ok {
		t.Error("a refused holiday was stored")
	}
	if entries := fleetAudit(t, st); len(entries) != 0 {
		t.Errorf("refused holidays were audited: %+v", entries)
	}
	// Today and 366 days ahead are the bounds, both allowed.
	for _, until := range []string{holidayDay(0), holidayDay(366)} {
		if err := st.SetFleetHoliday(ctx, writerActor, 20, until); err != nil {
			t.Errorf("until %s: %v", until, err)
		}
	}
}

func putItem(t *testing.T, it map[string]types.AttributeValue) {
	t.Helper()
	if _, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{TableName: aws.String(ddblocal.TableName), Item: it}); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// AssignCohort

const machineC = "CCCCCCCC-3333-4333-8333-CCCCCCCCCCCC"

func snapshotAt(at time.Time) string {
	return at.UTC().Format("20060102T150405.000000000Z") + "-0a1b2c3d"
}

func groupAdds(entries []store.AuditEntry) []store.AuditEntry {
	var out []store.AuditEntry
	for _, e := range entries {
		if e.Action == "group.add" {
			out = append(out, e)
		}
	}
	return out
}

func writes() int {
	c := ddblocal.Counts()
	return c["PutItem"] + c["UpdateItem"] + c["DeleteItem"] + c["TransactWriteItems"] + c["BatchWriteItem"]
}

func TestAssignCohort_ChecksTheSnapshotThenWrites(t *testing.T) {
	st := newStore(t)
	for _, g := range []struct {
		name string
		mode store.ClientMode
	}{{"pilot", store.ClientModeLockdown}, {"kiosk", store.ClientModeStandalone}, {"staff", ""}} {
		if _, err := st.CreateGroup(ctx, writerActor, g.name, g.name+" Macs", false); err != nil {
			t.Fatal(err)
		}
		if g.mode != "" {
			if err := st.SetGroupClientMode(ctx, writerActor, g.name, g.mode); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := st.SetGroupClientMode(ctx, writerActor, store.UngroupedGroup, store.ClientModeLockdown); err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotAt(time.Now().Add(-2 * time.Hour))
	putEventsItem(t, readinessRow(snapshot, "All", 0, 0, machineA, "mac-a"))
	putEventsItem(t, readinessRow(snapshot, "All", 0, 0, machineB, "mac-b"))
	putEventsItem(t, readinessRow(snapshot, "All", 1, 3, machineC, "mac-c"))
	// Ready in a group's list only is not ready in the fleet's.
	putEventsItem(t, readinessRow(snapshot, "Group#staff", 0, 0, machineC, "mac-c"))
	old := snapshotAt(time.Now().Add(-27 * time.Hour))
	putEventsItem(t, readinessRow(old, "All", 0, 0, machineA, "mac-a"))
	before, err := st.ListAudit(ctx, time.Now().Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}

	tooMany := make([]string, store.MaxCohortMacs+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("%08X-0000-4000-8000-000000000000", i)
	}
	for name, c := range map[string]struct {
		snapshot, group string
		macs            []string
		want            error
	}{
		"no Macs":                {snapshot, "pilot", nil, store.ErrInvalidArgument},
		"51 Macs":                {snapshot, "pilot", tooMany, store.ErrInvalidArgument},
		"group without a mode":   {snapshot, "staff", []string{machineA}, store.ErrInvalidArgument},
		"unregistered group":     {snapshot, "nope", []string{machineA}, store.ErrNotFound},
		"reserved group":         {snapshot, store.UngroupedGroup, []string{machineA}, store.ErrInvalidArgument},
		"a Mac not ready":        {snapshot, "pilot", []string{machineA, machineC}, store.ErrInvalidArgument},
		"a Mac not in the list":  {snapshot, "pilot", []string{machineA, "DDDDDDDD-4444-4444-8444-DDDDDDDDDDDD"}, store.ErrInvalidArgument},
		"a snapshot of 27 hours": {old, "pilot", []string{machineA}, store.ErrSnapshotTooOld},
		"not a snapshot ID":      {"latest", "pilot", []string{machineA}, store.ErrInvalidArgument},
	} {
		ddblocal.ResetCounts()
		n, err := st.AssignCohort(ctx, writerActor, c.snapshot, c.group, c.macs)
		if !errors.Is(err, c.want) || n != 0 {
			t.Errorf("%s: %d, %v; want 0 and %v", name, n, err, c.want)
		}
		if w := writes(); w != 0 {
			t.Errorf("%s: %d writes before refusing (%v)", name, w, ddblocal.Counts())
		}
		if name == "a Mac not ready" && (err == nil || !strings.Contains(err.Error(), machineC)) {
			t.Errorf("a Mac not ready: %v does not name %s", err, machineC)
		}
	}
	after, _ := st.ListAudit(ctx, time.Now().Add(-time.Hour), 100)
	if len(after) != len(before) {
		t.Errorf("refusals were audited: %+v", after[:len(after)-len(before)])
	}

	// GetMachine answers not found for a Mac without sensor data, and the fixture has none for machine B.
	putSensorData(t, machineB, "C02MACB", "mac-b", nil)
	ddblocal.ResetCounts()
	n, err := st.AssignCohort(ctx, writerActor, snapshot, "pilot", []string{machineA, machineB})
	if err != nil || n != 2 {
		t.Fatalf("assign: %d, %v", n, err)
	}
	if c := ddblocal.Counts(); c["GetItem"] < 2 || c["Scan"] != 0 {
		t.Errorf("calls %v; want a point read of each Mac's readiness row and no Scan", c)
	}
	for _, id := range []string{machineA, machineB} {
		m, err := st.GetMachine(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, g := range m.AssignedGroups {
			found = found || g == "pilot"
		}
		if !found {
			t.Errorf("%s assigned %v, want pilot among them", id, m.AssignedGroups)
		}
	}
	members, err := st.ListGroupMembers(ctx, "pilot", store.GroupMemberFilter{}, store.PageRequest{Limit: 10})
	if err != nil || len(members.Items) != 2 {
		t.Errorf("pilot's member rows %+v, %v; want both Macs", members.Items, err)
	}
	entries, _ := st.ListAudit(ctx, time.Now().Add(-time.Hour), 100)
	adds := groupAdds(entries)
	targets := map[string]bool{}
	for _, e := range adds {
		targets[e.Target] = strings.HasPrefix(e.Actor, "web:")
	}
	if len(adds) != 2 || !targets["machine:"+machineA] || !targets["machine:"+machineB] {
		t.Errorf("group.add audit entries %+v; want one per Mac, by the web actor", adds)
	}

	// STANDALONE is a lockdown mode too.
	if n, err := st.AssignCohort(ctx, writerActor, snapshot, "kiosk", []string{machineA}); err != nil || n != 1 {
		t.Errorf("a STANDALONE group: %d, %v", n, err)
	}
}

func TestAssignCohort_PartialFailureSaysHowMany(t *testing.T) {
	st := newStore(t)
	if _, err := st.CreateGroup(ctx, writerActor, "pilot", "", false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupClientMode(ctx, writerActor, "pilot", store.ClientModeLockdown); err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotAt(time.Now().Add(-time.Hour))
	putEventsItem(t, readinessRow(snapshot, "All", 0, 0, machineA, "mac-a"))
	putEventsItem(t, readinessRow(snapshot, "All", 0, 0, machineB, "mac-b"))
	// The first Mac's assignment goes through; IAM refuses the second's.
	transactions := 0
	ddblocal.AddHook(t, ddblocal.Hook{Before: func(c *ddblocal.Call) *ddblocal.Fault {
		if c.Op != "TransactWriteItems" {
			return nil
		}
		transactions++
		if transactions > 1 {
			f := ddblocal.FaultAccessDenied
			return &f
		}
		return nil
	}})
	n, err := st.AssignCohort(ctx, writerActor, snapshot, "pilot", []string{machineA, machineB})
	var partial *store.PartialCohortError
	if !errors.As(err, &partial) || partial.Assigned != 1 || partial.Total != 2 || n != 1 {
		t.Fatalf("%d, %v; want a PartialCohortError with 1 of 2 assigned", n, err)
	}
}
