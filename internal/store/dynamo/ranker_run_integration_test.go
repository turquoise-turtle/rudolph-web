//go:build integration

package dynamo_test

// The rerun guard of "Run the ranker now" in DynamoDB (owner request, 2026-11-25), as the fake implements it
// (internal/store/fake/ranker_run_test.go) and as the fleet job's works (fleet_run_integration_test.go):
//
//	RequestRankerRun(ctx, actor store.Actor, now time.Time, start func(context.Context) error) error
//
// The marker is one store-table item beside the fleet job's, in the FleetHealth partition. The top lists' own
// partitions (Top#*, TopSnapshot#*) are on the events table, which the web role only reads, and are the ranker's; a
// new store-table partition would need a new leading key in the role's write statement. FleetHealth is already
// granted exactly (iam.tf, WriteRudolphRules), and Rudolph reads it by key only (FleetHealth / Config, the fleet job's
// setting), so neither Rudolph nor the fleet job ever sees the marker:
//
//	PK: FleetHealth, SK: RankerRunRequested
//	RequestedAt   S  the request's time, RFC 3339 in UTC to the second
//	ExpiresAfter  N  Unix seconds, 15 minutes to a day after RequestedAt (the store table's TTL attribute)
//
// The current top lists' time is GeneratedAt on the pointer Top#Blocked#1 / Current in the events table (one
// GetItem; the ranker writes the same GeneratedAt on all 20 pointers of a run and moves Top#Blocked#1 first). Lists
// younger than 15 minutes refuse the request before the marker is written. Otherwise one conditional write of the
// marker decides between concurrent requests. The winner calls start; its error is returned wrapped with
// store.ErrRankerRunNotStarted, the marker kept and nothing audited; success records ranker.run on ranker. The fleet
// job's marker (FleetHealth / RunRequested) and FleetHealth / Config are never touched, and the fleet job's marker never
// refuses the ranker. No Scan or Query.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

func rankerRunMarker(t *testing.T) map[string]types.AttributeValue {
	t.Helper()
	out, err := ddblocal.Raw(t).GetItem(ctx, &awsdynamodb.GetItemInput{
		TableName:      aws.String(ddblocal.TableName),
		Key:            map[string]types.AttributeValue{"PK": s("FleetHealth"), "SK": s("RankerRunRequested")},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.Item
}

func putRankerRunMarker(t *testing.T, requestedAt time.Time) {
	t.Helper()
	_, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{
		TableName: aws.String(ddblocal.TableName),
		Item: map[string]types.AttributeValue{
			"PK": s("FleetHealth"), "SK": s("RankerRunRequested"),
			"RequestedAt":  s(requestedAt.UTC().Format(time.RFC3339)),
			"ExpiresAfter": &types.AttributeValueMemberN{Value: strconv.FormatInt(requestedAt.Add(time.Hour).Unix(), 10)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// putTopPointer writes the pointer Rudolph's ranker moves first, as it writes it.
func putTopPointer(t *testing.T, generatedAt time.Time) {
	t.Helper()
	putEventsItem(t, map[string]types.AttributeValue{
		"PK": s("Top#Blocked#1"), "SK": s("Current"),
		"Snapshot": s(generatedAt.UTC().Format("20060102T150405Z") + "-a"), "GeneratedAt": s(generatedAt.UTC().Format(time.RFC3339)),
	})
}

func rankerEntries(t *testing.T, st interface {
	ListAudit(context.Context, time.Time, int) ([]store.AuditEntry, error)
}) []store.AuditEntry {
	t.Helper()
	all, err := st.ListAudit(ctx, time.Now().Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEntry
	for _, e := range all {
		if e.Action == "ranker.run" {
			out = append(out, e)
		}
	}
	return out
}

func wantRankerTooSoon(t *testing.T, err error, retryAt time.Time) {
	t.Helper()
	var soon *store.RankerRunTooSoonError
	if !errors.As(err, &soon) {
		t.Fatalf("error %v, want a *store.RankerRunTooSoonError", err)
	}
	if !soon.RetryAt.Equal(retryAt) {
		t.Errorf("RetryAt %v, want %v", soon.RetryAt, retryAt)
	}
}

func TestRequestRankerRun_WritesTheMarkerStartsAndAudits(t *testing.T) {
	st := newStore(t)
	putTopPointer(t, fleetRunNow.Add(-time.Hour))
	ddblocal.ResetCounts()
	c := &runCounter{}
	if err := st.RequestRankerRun(ctx, writerActor, fleetRunNow, c.start); err != nil {
		t.Fatal(err)
	}
	noScanOrQuery(t)
	if c.count() != 1 {
		t.Errorf("%d starts, want 1", c.count())
	}
	it := rankerRunMarker(t)
	if it == nil {
		t.Fatal("no FleetHealth / RankerRunRequested item")
	}
	if v, ok := it["RequestedAt"].(*types.AttributeValueMemberS); !ok || v.Value != "2026-11-20T09:00:00Z" {
		t.Errorf("RequestedAt %#v, want \"2026-11-20T09:00:00Z\"", it["RequestedAt"])
	}
	exp, ok := it["ExpiresAfter"].(*types.AttributeValueMemberN)
	if !ok {
		t.Fatalf("ExpiresAfter %#v, want a number (the store table's TTL attribute)", it["ExpiresAfter"])
	}
	secs, err := strconv.ParseInt(exp.Value, 10, 64)
	if err != nil || secs < fleetRunNow.Add(15*time.Minute).Unix() || secs > fleetRunNow.Add(24*time.Hour).Unix() {
		t.Errorf("ExpiresAfter %s, want Unix seconds 15 minutes to a day after the request", exp.Value)
	}
	if fleetRunMarker(t) != nil {
		t.Error("the ranker request wrote the fleet job's marker, FleetHealth / RunRequested")
	}
	if fleetHealthItem(t) != nil {
		t.Error("the ranker request wrote FleetHealth / Config, the holiday setting")
	}
	entries := rankerEntries(t, st)
	if len(entries) != 1 || entries[0].Target != "ranker" || !strings.HasPrefix(entries[0].Actor, "web:") ||
		!strings.Contains(entries[0].Actor, "admin@example.com") || !strings.Contains(strings.ToLower(entries[0].Summary), "ranker") {
		t.Errorf("audit %+v; want one ranker.run on ranker by the web actor naming the ranker", entries)
	}
}

func TestRequestRankerRun_MarkerRefusesFor15Minutes(t *testing.T) {
	st := newStore(t)
	c := &runCounter{}
	putRankerRunMarker(t, fleetRunNow.Add(-5*time.Minute))
	wantRankerTooSoon(t, st.RequestRankerRun(ctx, writerActor, fleetRunNow, c.start), fleetRunNow.Add(10*time.Minute))
	if c.count() != 0 {
		t.Error("a refusal started the ranker")
	}

	putRankerRunMarker(t, fleetRunNow.Add(-76*time.Minute))
	if err := st.RequestRankerRun(ctx, writerActor, fleetRunNow, c.start); err != nil {
		t.Fatalf("an old marker: %v", err)
	}
	if v := rankerRunMarker(t)["RequestedAt"].(*types.AttributeValueMemberS).Value; v != "2026-11-20T09:00:00Z" {
		t.Errorf("RequestedAt %s after the run, want the new request's time", v)
	}
	wantRankerTooSoon(t, st.RequestRankerRun(ctx, writerActor, fleetRunNow.Add(14*time.Minute), c.start), fleetRunNow.Add(15*time.Minute))
	if c.count() != 1 || len(rankerEntries(t, st)) != 1 {
		t.Errorf("%d starts and %d ranker.run entries, want 1 of each", c.count(), len(rankerEntries(t, st)))
	}
}

func TestRequestRankerRun_FreshTopListsRefuseWithoutAMarker(t *testing.T) {
	st := newStore(t)
	putTopPointer(t, fleetRunNow.Add(-5*time.Minute))
	c := &runCounter{}
	wantRankerTooSoon(t, st.RequestRankerRun(ctx, writerActor, fleetRunNow, c.start), fleetRunNow.Add(10*time.Minute))
	if c.count() != 0 {
		t.Error("a refusal started the ranker")
	}
	if rankerRunMarker(t) != nil {
		t.Error("a refusal for fresh top lists wrote the marker")
	}
}

func TestRequestRankerRun_StartFailureKeepsTheMarker(t *testing.T) {
	st := newStore(t)
	invokeErr := errors.New("operation error Lambda: Invoke, ResourceNotFoundException")
	c := &runCounter{err: invokeErr}
	err := st.RequestRankerRun(ctx, writerActor, fleetRunNow, c.start)
	if !errors.Is(err, store.ErrRankerRunNotStarted) || !errors.Is(err, invokeErr) {
		t.Errorf("error %v, want one wrapping store.ErrRankerRunNotStarted and the start's error", err)
	}
	if rankerRunMarker(t) == nil {
		t.Error("the marker was removed after a failed start")
	}
	if n := len(rankerEntries(t, st)); n != 0 {
		t.Errorf("a run that never started was audited (%d)", n)
	}
	c.err = nil
	wantRankerTooSoon(t, st.RequestRankerRun(ctx, writerActor, fleetRunNow.Add(time.Minute), c.start), fleetRunNow.Add(15*time.Minute))
}

func TestRequestRankerRun_ConcurrentRequestsStartOnce(t *testing.T) {
	st := newStore(t)
	pause := ddblocal.PauseFirst(t, func(c *ddblocal.Call) bool {
		switch c.Op {
		case "PutItem", "UpdateItem", "TransactWriteItems":
			return strings.Contains(c.InputJSON(), "RankerRunRequested")
		}
		return false
	})
	c := &runCounter{}
	first := make(chan error, 1)
	go func() { first <- st.RequestRankerRun(ctx, writerActor, fleetRunNow, c.start) }()
	select {
	case <-pause.Reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the first request never wrote the marker")
	}
	second := st.RequestRankerRun(ctx, writerActor, fleetRunNow, c.start)
	pause.Release()
	firstErr := <-first

	var soon *store.RankerRunTooSoonError
	wins := 0
	for _, err := range []error{firstErr, second} {
		switch {
		case err == nil:
			wins++
		case errors.As(err, &soon):
		default:
			t.Errorf("unexpected error %v", err)
		}
	}
	if wins != 1 || c.count() != 1 {
		t.Errorf("%d requests won and the ranker started %d times; want exactly one of each", wins, c.count())
	}
}

// The two jobs' markers and freshness checks are separate.
func TestRequestRankerRun_IndependentOfTheFleetJob(t *testing.T) {
	st := newStore(t)
	putFleetRunMarker(t, fleetRunNow.Add(-time.Minute))
	putFleetFixture(t, fleetSnapshot, fleetRunNow.Add(-time.Minute))
	c := &runCounter{}
	if err := st.RequestRankerRun(ctx, writerActor, fleetRunNow, c.start); err != nil {
		t.Errorf("a fresh fleet request and snapshot refused the ranker: %v", err)
	}

	st = newStore(t)
	putRankerRunMarker(t, fleetRunNow.Add(-time.Minute))
	putTopPointer(t, fleetRunNow.Add(-time.Minute))
	if err := st.RequestFleetRun(ctx, writerActor, fleetRunNow, c.start); err != nil {
		t.Errorf("a fresh ranker request and top lists refused the fleet job: %v", err)
	}
	if v := rankerRunMarker(t)["RequestedAt"].(*types.AttributeValueMemberS).Value; v != fleetRunNow.Add(-time.Minute).Format(time.RFC3339) {
		t.Errorf("the fleet run moved the ranker's marker to %s", v)
	}
}

func TestRequestRankerRun_RefusesAnUnusableActorBeforeWriting(t *testing.T) {
	st := newStore(t)
	c := &runCounter{}
	if err := st.RequestRankerRun(ctx, store.Actor{}, fleetRunNow, c.start); err == nil {
		t.Error("an actor without a username was accepted")
	}
	if rankerRunMarker(t) != nil || c.count() != 0 {
		t.Error("an unusable actor wrote the marker or started the ranker")
	}
}
