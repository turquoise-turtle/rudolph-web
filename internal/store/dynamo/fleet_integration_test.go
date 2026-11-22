//go:build integration

package dynamo_test

// The fleet health and readiness lists (Rudolph's docs/fleet-health.md), read as the top lists are (top.go):
//
//	FleetSummary(ctx) (store.FleetSummary, error)
//	    one GetItem of FleetPointer / Current on the events table. No pointer: a zero FleetSummary (zero GeneratedAt,
//	    "not computed yet") and no error.
//	ListFleetHealth(ctx, category, store.PageRequest) (store.FleetList[store.FleetHealthRow], error)
//	    first page: the pointer (one GetItem) and one Query of FleetSnapshot#<snapshot>#Health#<category>, Limit = page
//	    size; later pages: one Query of the snapshot the cursor pins. The cursor is bound to the method and category,
//	    and pins the snapshot and its GeneratedAt; a key outside that list's partition is refused; a pinned snapshot
//	    whose rows are gone (they expire 3 days after the run) is ErrInvalidCursor. An unknown category is
//	    ErrInvalidArgument. Without an events table: ErrUnavailable.
//	ListReadiness(ctx, store.ReadinessFilter{Group, Bucket}, store.PageRequest) (store.FleetList[store.ReadinessRow], error)
//	    the same over FleetSnapshot#<snapshot>#Ready#All (Group "") or #Ready#Group#<group>. A bucket is a begins_with
//	    range on the sort key ("<digit>#"), never a filter, so every page is full. A group the pointer's Readiness does
//	    not name is ErrNotFound; an unknown bucket ErrInvalidArgument. The cursor is bound to the group and bucket.
//
// FleetList carries Snapshot (the snapshot ID, which the cohort form posts back) and GeneratedAt of the snapshot read.

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

const fleetSnapshot2 = "20261123T120000.000000000Z-4e5f6a7b"

// putFleetFixture writes every row of fleet-rows.json under the snapshot, the pointer included, to the events table.
func putFleetFixture(t *testing.T, snapshot string, generatedAt time.Time) {
	t.Helper()
	for _, it := range loadFleetRows(t, snapshot) {
		if it["PK"].(*types.AttributeValueMemberS).Value == "FleetPointer" {
			it["GeneratedAt"] = s(generatedAt.Format(time.RFC3339))
		}
		putEventsItem(t, it)
	}
}

func healthRow(snapshot, category, sk, id, host string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": s("FleetSnapshot#" + snapshot + "#Health#" + category), "SK": s(sk), "MachineID": s(id), "Hostname": s(host),
		"LastSeen": s("2026-11-01T00:00:00Z"), "ExpiresAfter": n(1795608000),
	}
}

// putStaleRows adds rows stale-1..count to the Stale list of the snapshot, oldest first.
func putStaleRows(t *testing.T, snapshot, prefix string, count int) {
	t.Helper()
	for i := 1; i <= count; i++ {
		id := fmt.Sprintf("%08d-0000-4000-8000-000000000000", i)
		putEventsItem(t, healthRow(snapshot, "Stale", fmt.Sprintf("202610%02dT000000Z#%s", i, id), id, fmt.Sprintf("%s-%d", prefix, i)))
	}
}

func readinessRow(snapshot, scope string, bucket, pending int, id, host string) map[string]types.AttributeValue {
	it := map[string]types.AttributeValue{
		"PK": s("FleetSnapshot#" + snapshot + "#Ready#" + scope), "SK": s(fmt.Sprintf("%d#%06d#%s", bucket, pending, id)),
		"MachineID": s(id), "Hostname": s(host), "ReportedMode": s("MONITOR"), "IntendedMode": s("MONITOR"),
		"ObservedDays": n(40), "Capped": &types.AttributeValueMemberBOOL{Value: false}, "ExpiresAfter": n(1795608000),
	}
	if bucket != 4 {
		for _, a := range []string{"Unknown30", "Pending30", "Unknown7", "Pending7"} {
			it[a] = n(pending)
		}
	}
	if bucket == 3 {
		it["Why"] = s("observed")
	}
	return it
}

func healthHosts(items []store.FleetHealthRow) string {
	var out []string
	for _, it := range items {
		out = append(out, it.Hostname)
	}
	return strings.Join(out, ",")
}

func readyHosts(items []store.ReadinessRow) string {
	var out []string
	for _, it := range items {
		out = append(out, it.Hostname)
	}
	return strings.Join(out, ",")
}

func TestFleetSummary_DecodesTheContractRowWithOneGetItem(t *testing.T) {
	st := newStore(t)
	putFleetFixture(t, fleetSnapshot, fleetGenerated)
	ddblocal.ResetCounts()
	got, err := st.FleetSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Snapshot != fleetSnapshot || !got.GeneratedAt.Equal(fleetGenerated) || got.Macs != 2 || got.Attention != 1 ||
		got.Health[store.FleetCategoryStale] != 1 || got.StaleDays != 10 || got.MinSantaVersion != "2026.9" ||
		got.ReadinessGroups["eng"].Few != 1 || got.ReadinessAll.Macs != 2 {
		t.Errorf("summary %+v", got)
	}
	if c := ddblocal.Counts(); c["GetItem"] != 1 || len(c) != 1 {
		t.Errorf("calls %v, want one GetItem only", c)
	}
}

func TestFleetSummary_NoPointer(t *testing.T) {
	st := newStore(t)
	got, err := st.FleetSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.GeneratedAt.IsZero() || got.Snapshot != "" || got.Macs != 0 {
		t.Errorf("no pointer: %+v, want the zero summary", got)
	}
}

func TestFleet_UnavailableWithoutAnEventsTable(t *testing.T) {
	codec, _ := store.NewCursorCodec(store.RandomCursorKey())
	st, err := dynamo.New(dynamo.Options{Client: ddblocal.Raw(t), Table: ddblocal.TableName, Cursors: codec})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FleetSummary(ctx); !errors.Is(err, store.ErrUnavailable) {
		t.Errorf("FleetSummary: %v, want ErrUnavailable", err)
	}
	if _, err := st.ListFleetHealth(ctx, store.FleetCategoryStale, store.PageRequest{Limit: 10}); !errors.Is(err, store.ErrUnavailable) {
		t.Errorf("ListFleetHealth: %v, want ErrUnavailable", err)
	}
	if _, err := st.ListReadiness(ctx, store.ReadinessFilter{}, store.PageRequest{Limit: 10}); !errors.Is(err, store.ErrUnavailable) {
		t.Errorf("ListReadiness: %v, want ErrUnavailable", err)
	}
}

func TestListFleetHealth_PagesStayOnOneSnapshot(t *testing.T) {
	st := newStore(t)
	putFleetFixture(t, fleetSnapshot, fleetGenerated)
	putStaleRows(t, fleetSnapshot, "old", 3)
	ddblocal.ResetCounts()
	limits := limitsUsed(t)

	first, err := st.ListFleetHealth(ctx, store.FleetCategoryStale, store.PageRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	// Oldest first: the three October rows, then the fixture's November one.
	if healthHosts(first.Items) != "old-1,old-2" || first.NextCursor == "" || first.Snapshot != fleetSnapshot || !first.GeneratedAt.Equal(fleetGenerated) {
		t.Fatalf("first page %+v", first)
	}
	if c := ddblocal.Counts(); c["GetItem"] != 1 || c["Query"] != 1 || c["Scan"] != 0 {
		t.Errorf("first page calls %v, want one GetItem and one Query", c)
	}
	if len(*limits) != 1 || (*limits)[0] != 2 {
		t.Errorf("Query limits %v, want [2]", *limits)
	}

	// The next day's run writes a new snapshot and moves the pointer between the two pages.
	putFleetFixture(t, fleetSnapshot2, fleetGenerated.Add(24*time.Hour))
	putStaleRows(t, fleetSnapshot2, "new", 3)
	ddblocal.ResetCounts()
	second, err := st.ListFleetHealth(ctx, store.FleetCategoryStale, store.PageRequest{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if healthHosts(second.Items) != "old-3,contract-stale" || second.Snapshot != fleetSnapshot || !second.GeneratedAt.Equal(fleetGenerated) {
		t.Errorf("second page %+v; want old-3 and contract-stale from the first snapshot", second)
	}
	if c := ddblocal.Counts(); c["GetItem"] != 0 || c["Query"] < 1 {
		t.Errorf("second page calls %v, want no pointer read", c)
	}

	fresh, err := st.ListFleetHealth(ctx, store.FleetCategoryStale, store.PageRequest{Limit: 2})
	if err != nil || healthHosts(fresh.Items) != "new-1,new-2" || fresh.Snapshot != fleetSnapshot2 {
		t.Errorf("a new first page %+v, %v; want the new snapshot", fresh, err)
	}
}

func TestListFleetHealth_ExpiredSnapshotIsInvalidCursor(t *testing.T) {
	st := newStore(t)
	putFleetFixture(t, fleetSnapshot, fleetGenerated)
	putStaleRows(t, fleetSnapshot, "old", 3)
	first, err := st.ListFleetHealth(ctx, store.FleetCategoryStale, store.PageRequest{Limit: 2})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page %+v, %v", first, err)
	}
	// TTL deletes the snapshot's rows; the job has written a newer one.
	out, err := ddblocal.Raw(t).Query(ctx, &awsdynamodb.QueryInput{
		TableName:                 aws.String(ddblocal.EventsTableName),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": s("FleetSnapshot#" + fleetSnapshot + "#Health#Stale")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range out.Items {
		if _, err := ddblocal.Raw(t).DeleteItem(ctx, &awsdynamodb.DeleteItemInput{TableName: aws.String(ddblocal.EventsTableName),
			Key: map[string]types.AttributeValue{"PK": it["PK"], "SK": it["SK"]}}); err != nil {
			t.Fatal(err)
		}
	}
	putFleetFixture(t, fleetSnapshot2, fleetGenerated.Add(72*time.Hour))
	if page, err := st.ListFleetHealth(ctx, store.FleetCategoryStale, store.PageRequest{Limit: 2, Cursor: first.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("page of an expired snapshot: %+v, %v; want ErrInvalidCursor", page, err)
	}
}

func TestListFleetHealth_CursorBoundToMethodAndCategory(t *testing.T) {
	st := newStore(t)
	putFleetFixture(t, fleetSnapshot, fleetGenerated)
	putStaleRows(t, fleetSnapshot, "old", 3)
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("%08d-0000-4000-8000-000000000000", i)
		putEventsItem(t, healthRow(fleetSnapshot, "Incomplete", fmt.Sprintf("202611%02dT000000Z#%s", i, id), id, fmt.Sprintf("inc-%d", i)))
		putEventsItem(t, readinessRow(fleetSnapshot, "All", 0, 0, id, fmt.Sprintf("ready-%d", i)))
	}
	stale, err := st.ListFleetHealth(ctx, store.FleetCategoryStale, store.PageRequest{Limit: 1})
	if err != nil || stale.NextCursor == "" {
		t.Fatalf("stale page %+v, %v", stale, err)
	}
	if _, err := st.ListFleetHealth(ctx, store.FleetCategoryIncomplete, store.PageRequest{Limit: 1, Cursor: stale.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("a Stale cursor on the Incomplete list: %v, want ErrInvalidCursor", err)
	}
	if _, err := st.ListReadiness(ctx, store.ReadinessFilter{}, store.PageRequest{Limit: 1, Cursor: stale.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("a health cursor on a readiness list: %v, want ErrInvalidCursor", err)
	}
	if _, err := st.TopBinaries(ctx, store.TopFilter{Kind: store.TopKindBlocked, WindowDays: 7}, store.PageRequest{Limit: 1, Cursor: stale.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("a health cursor on a top list: %v, want ErrInvalidCursor", err)
	}
	ready, err := st.ListReadiness(ctx, store.ReadinessFilter{}, store.PageRequest{Limit: 1})
	if err != nil || ready.NextCursor == "" {
		t.Fatalf("readiness page %+v, %v", ready, err)
	}
	if _, err := st.ListFleetHealth(ctx, store.FleetCategoryStale, store.PageRequest{Limit: 1, Cursor: ready.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("a readiness cursor on a health list: %v, want ErrInvalidCursor", err)
	}
}

func TestListFleetHealth_NotComputedYetAndInvalidArguments(t *testing.T) {
	st := newStore(t)
	got, err := st.ListFleetHealth(ctx, store.FleetCategorySIP, store.PageRequest{Limit: 10})
	if err != nil || len(got.Items) != 0 || got.NextCursor != "" || !got.GeneratedAt.IsZero() || got.Snapshot != "" {
		t.Errorf("no pointer: %+v, %v; want an empty list", got, err)
	}
	for name, c := range map[string]struct {
		category store.FleetCategory
		limit    int
	}{
		"unknown category": {"Sleepy", 10},
		"lower case":       {"stale", 10},
		"limit 0":          {store.FleetCategoryStale, 0},
		"limit too big":    {store.FleetCategoryStale, store.MaxPageSize + 1},
	} {
		if _, err := st.ListFleetHealth(ctx, c.category, store.PageRequest{Limit: c.limit}); !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("%s: %v, want ErrInvalidArgument", name, err)
		}
	}
}

func TestListFleetHealth_EveryCategoryReadsItsOwnPartition(t *testing.T) {
	st := newStore(t)
	putFleetFixture(t, fleetSnapshot, fleetGenerated)
	for _, c := range store.FleetCategories {
		putEventsItem(t, healthRow(fleetSnapshot, string(c), "x#"+machineA, machineA, "in-"+string(c)))
	}
	for _, c := range store.FleetCategories {
		got, err := st.ListFleetHealth(ctx, c, store.PageRequest{Limit: 10})
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		if !strings.Contains(healthHosts(got.Items), "in-"+string(c)) || strings.Count(healthHosts(got.Items), "in-") != 1 {
			t.Errorf("%s: rows %s", c, healthHosts(got.Items))
		}
	}
}

func TestListReadiness_ScopesAndOrder(t *testing.T) {
	st := newStore(t)
	putFleetFixture(t, fleetSnapshot, fleetGenerated)
	all, err := st.ListReadiness(ctx, store.ReadinessFilter{}, store.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	// Bucket, then pending, then machine ID: the few Mac (bucket 1) before the unmeasured one (bucket 3).
	if readyHosts(all.Items) != "contract-few,contract-stale" || all.Snapshot != fleetSnapshot || !all.GeneratedAt.Equal(fleetGenerated) {
		t.Errorf("all: %+v", all)
	}
	eng, err := st.ListReadiness(ctx, store.ReadinessFilter{Group: "eng"}, store.PageRequest{Limit: 10})
	if err != nil || readyHosts(eng.Items) != "contract-few" {
		t.Errorf("eng: %+v, %v", eng, err)
	}
	ungrouped, err := st.ListReadiness(ctx, store.ReadinessFilter{Group: store.UngroupedGroup}, store.PageRequest{Limit: 10})
	if err != nil || readyHosts(ungrouped.Items) != "contract-stale" || ungrouped.Items[0].Why != "stale" {
		t.Errorf("ungrouped: %+v, %v", ungrouped, err)
	}
	if _, err := st.ListReadiness(ctx, store.ReadinessFilter{Group: "no-such-group"}, store.PageRequest{Limit: 10}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a group the snapshot does not name: %v, want ErrNotFound", err)
	}
	if _, err := st.ListReadiness(ctx, store.ReadinessFilter{Bucket: "soon"}, store.PageRequest{Limit: 10}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("an unknown bucket: %v, want ErrInvalidArgument", err)
	}
}

// A bucket filter that were a FilterExpression would read the Ready rows first (bucket 0 sorts first) and return
// short or empty pages; a begins_with range returns full ones.
func TestListReadiness_BucketIsARangeNotAFilter(t *testing.T) {
	st := newStore(t)
	putFleetFixture(t, fleetSnapshot, fleetGenerated)
	for i := 1; i <= 5; i++ {
		putEventsItem(t, readinessRow(fleetSnapshot, "All", 0, 0, fmt.Sprintf("%08d-0000-4000-8000-00000000000a", i), fmt.Sprintf("ready-%d", i)))
		putEventsItem(t, readinessRow(fleetSnapshot, "All", 2, 10+i, fmt.Sprintf("%08d-0000-4000-8000-00000000000b", i), fmt.Sprintf("many-%d", i)))
	}
	putEventsItem(t, readinessRow(fleetSnapshot, "All", 4, 0, "00000009-0000-4000-8000-00000000000c", "locked-1"))
	ddblocal.ResetCounts()
	limits := limitsUsed(t)

	var pages []string
	cursor := ""
	for range 5 {
		got, err := st.ListReadiness(ctx, store.ReadinessFilter{Bucket: store.ReadinessMany}, store.PageRequest{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, readyHosts(got.Items))
		if got.NextCursor == "" {
			break
		}
		cursor = got.NextCursor
	}
	if j := strings.Join(pages, "|"); j != "many-1,many-2|many-3,many-4|many-5" && j != "many-1,many-2|many-3,many-4|many-5|" {
		t.Errorf("pages %q; want full pages of the many bucket in pending order", pages)
	}
	if c := ddblocal.Counts(); c["Scan"] != 0 || c["GetItem"] != 1 {
		t.Errorf("calls %v; want the pointer read once, on the first page", c)
	}
	for _, l := range *limits {
		if l != 2 && l != 1 {
			t.Errorf("Query limits %v, want the page size", *limits)
			break
		}
	}

	locked, err := st.ListReadiness(ctx, store.ReadinessFilter{Bucket: store.ReadinessLocked}, store.PageRequest{Limit: 10})
	if err != nil || readyHosts(locked.Items) != "locked-1" || locked.Items[0].Counted || locked.Items[0].Bucket != store.ReadinessLocked {
		t.Errorf("locked: %+v, %v", locked, err)
	}
}

func TestListReadiness_CursorBoundToGroupAndBucket(t *testing.T) {
	st := newStore(t)
	putFleetFixture(t, fleetSnapshot, fleetGenerated)
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("%08d-0000-4000-8000-00000000000a", i)
		putEventsItem(t, readinessRow(fleetSnapshot, "All", 0, 0, id, fmt.Sprintf("ready-%d", i)))
		putEventsItem(t, readinessRow(fleetSnapshot, "Group#eng", 0, 0, id, fmt.Sprintf("eng-ready-%d", i)))
	}
	ready, err := st.ListReadiness(ctx, store.ReadinessFilter{Bucket: store.ReadinessReady}, store.PageRequest{Limit: 1})
	if err != nil || ready.NextCursor == "" {
		t.Fatalf("ready page %+v, %v", ready, err)
	}
	for name, f := range map[string]store.ReadinessFilter{
		"another bucket":       {Bucket: store.ReadinessFew},
		"no bucket":            {},
		"a group, same bucket": {Group: "eng", Bucket: store.ReadinessReady},
	} {
		if _, err := st.ListReadiness(ctx, f, store.PageRequest{Limit: 1, Cursor: ready.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("%s: %v, want ErrInvalidCursor", name, err)
		}
	}
	second, err := st.ListReadiness(ctx, store.ReadinessFilter{Bucket: store.ReadinessReady}, store.PageRequest{Limit: 1, Cursor: ready.NextCursor})
	if err != nil || readyHosts(second.Items) != "ready-2" {
		t.Errorf("second page %+v, %v", second, err)
	}
}
