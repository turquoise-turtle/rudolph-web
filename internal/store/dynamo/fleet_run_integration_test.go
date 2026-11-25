//go:build integration

package dynamo_test

// The rerun guard of "Run the fleet job now" in DynamoDB (owner request, 2026-11-25), as the fake implements it
// (internal/store/fake/fleet_run_test.go):
//
//	RequestFleetRun(ctx, actor store.Actor, now time.Time, start func(context.Context) error) error
//
// The marker is one store-table item in the FleetHealth partition, which the web role can already write (iam.tf,
// WriteRudolphRules, leading key "FleetHealth" exactly):
//
//	PK: FleetHealth, SK: RunRequested
//	RequestedAt   S  the request's time, RFC 3339 in UTC to the second (now.UTC().Format(time.RFC3339))
//	ExpiresAfter  N  Unix seconds, at least 15 minutes and at most a day after RequestedAt: the store table's TTL
//	                 attribute, so the marker cleans itself up. TTL deletes late, so the guard reads RequestedAt, never
//	                 whether the item is still there.
//
// The pointer (FleetPointer / Current on the events table, through FleetSummary) younger than 15 minutes refuses the
// request before the marker is written. Otherwise one conditional write of the marker (absent, or RequestedAt at
// least 15 minutes before now) decides between concurrent requests: the loser gets a *store.FleetRunTooSoonError
// with RetryAt from the stored RequestedAt and calls no start. The winner calls start; its error is returned wrapped
// with store.ErrFleetRunNotStarted, the marker kept and nothing audited; success records fleet.run on fleet through
// Rudolph's audit package. FleetHealth / Config, the fleet health setting (the stale threshold, the holiday and the
// minimum Santa version), is never touched: the item seeded from the web contract is the same after the request. No
// Scan or Query.

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

var fleetRunNow = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

func fleetRunMarker(t *testing.T) map[string]types.AttributeValue {
	t.Helper()
	out, err := ddblocal.Raw(t).GetItem(ctx, &awsdynamodb.GetItemInput{
		TableName:      aws.String(ddblocal.TableName),
		Key:            map[string]types.AttributeValue{"PK": s("FleetHealth"), "SK": s("RunRequested")},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.Item
}

func putFleetRunMarker(t *testing.T, requestedAt time.Time) {
	t.Helper()
	_, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{
		TableName: aws.String(ddblocal.TableName),
		Item: map[string]types.AttributeValue{
			"PK": s("FleetHealth"), "SK": s("RunRequested"),
			"RequestedAt":  s(requestedAt.UTC().Format(time.RFC3339)),
			"ExpiresAfter": &types.AttributeValueMemberN{Value: strconv.FormatInt(requestedAt.Add(time.Hour).Unix(), 10)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

type runCounter struct {
	mu  sync.Mutex
	n   int
	err error
}

func (c *runCounter) start(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.err
}

func (c *runCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func runEntries(t *testing.T, list func() ([]store.AuditEntry, error)) []store.AuditEntry {
	t.Helper()
	all, err := list()
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEntry
	for _, e := range all {
		if e.Action == "fleet.run" {
			out = append(out, e)
		}
	}
	return out
}

func wantTooSoon(t *testing.T, err error, retryAt time.Time) {
	t.Helper()
	var soon *store.FleetRunTooSoonError
	if !errors.As(err, &soon) {
		t.Fatalf("error %v, want a *store.FleetRunTooSoonError", err)
	}
	if !soon.RetryAt.Equal(retryAt) {
		t.Errorf("RetryAt %v, want %v", soon.RetryAt, retryAt)
	}
}

func noScanOrQuery(t *testing.T) {
	t.Helper()
	if c := ddblocal.Counts(); c["Scan"] != 0 || c["Query"] != 0 {
		t.Errorf("calls %v; the guard reads by key only", c)
	}
}

func TestRequestFleetRun_WritesTheMarkerStartsAndAudits(t *testing.T) {
	st := newStore(t)
	putFleetFixture(t, fleetSnapshot, fleetRunNow.Add(-3*time.Hour))
	setting := fleetHealthSetting(t)
	ddblocal.ResetCounts()
	c := &runCounter{}
	if err := st.RequestFleetRun(ctx, writerActor, fleetRunNow, c.start); err != nil {
		t.Fatal(err)
	}
	noScanOrQuery(t)
	if c.count() != 1 {
		t.Errorf("%d starts, want 1", c.count())
	}
	it := fleetRunMarker(t)
	if it == nil {
		t.Fatal("no FleetHealth / RunRequested item")
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
	if after := fleetHealthItem(t); !reflect.DeepEqual(after, setting) {
		t.Errorf("the request changed FleetHealth / Config, the fleet health setting:\n before %v\n after  %v", setting, after)
	}
	entries := runEntries(t, func() ([]store.AuditEntry, error) { return st.ListAudit(ctx, time.Now().Add(-time.Hour), 100) })
	if len(entries) != 1 || entries[0].Target != "fleet" || !strings.HasPrefix(entries[0].Actor, "web:") ||
		!strings.Contains(entries[0].Actor, "admin@example.com") || !strings.Contains(strings.ToLower(entries[0].Summary), "fleet job") {
		t.Errorf("audit %+v; want one fleet.run on fleet by the web actor naming the fleet job", entries)
	}
}

func TestRequestFleetRun_MarkerRefusesFor15Minutes(t *testing.T) {
	st := newStore(t)
	c := &runCounter{}
	putFleetRunMarker(t, fleetRunNow.Add(-5*time.Minute))
	wantTooSoon(t, st.RequestFleetRun(ctx, writerActor, fleetRunNow, c.start), fleetRunNow.Add(10*time.Minute))
	if c.count() != 0 {
		t.Errorf("a refusal started the job")
	}
	if v := fleetRunMarker(t)["RequestedAt"].(*types.AttributeValueMemberS).Value; v != fleetRunNow.Add(-5*time.Minute).Format(time.RFC3339) {
		t.Errorf("a refusal moved RequestedAt to %s", v)
	}

	// A marker 76 minutes old, its ExpiresAfter (an hour after it) past but the item not yet deleted by TTL, no longer
	// refuses.
	putFleetRunMarker(t, fleetRunNow.Add(-76*time.Minute))
	if err := st.RequestFleetRun(ctx, writerActor, fleetRunNow, c.start); err != nil {
		t.Fatalf("an old marker: %v", err)
	}
	if v := fleetRunMarker(t)["RequestedAt"].(*types.AttributeValueMemberS).Value; v != "2026-11-20T09:00:00Z" {
		t.Errorf("RequestedAt %s after the run, want the new request's time", v)
	}
	wantTooSoon(t, st.RequestFleetRun(ctx, writerActor, fleetRunNow.Add(14*time.Minute), c.start), fleetRunNow.Add(15*time.Minute))
	if c.count() != 1 {
		t.Errorf("%d starts, want 1", c.count())
	}
	if n := len(runEntries(t, func() ([]store.AuditEntry, error) { return st.ListAudit(ctx, time.Now().Add(-time.Hour), 100) })); n != 1 {
		t.Errorf("%d fleet.run entries, want 1: refusals are not audited", n)
	}
}

func TestRequestFleetRun_FreshSnapshotRefusesWithoutAMarker(t *testing.T) {
	st := newStore(t)
	putFleetFixture(t, fleetSnapshot, fleetRunNow.Add(-5*time.Minute))
	c := &runCounter{}
	wantTooSoon(t, st.RequestFleetRun(ctx, writerActor, fleetRunNow, c.start), fleetRunNow.Add(10*time.Minute))
	if c.count() != 0 {
		t.Error("a refusal started the job")
	}
	if fleetRunMarker(t) != nil {
		t.Error("a refusal for a fresh snapshot wrote the marker")
	}
}

func TestRequestFleetRun_StartFailureKeepsTheMarker(t *testing.T) {
	st := newStore(t)
	invokeErr := errors.New("operation error Lambda: Invoke, ResourceNotFoundException")
	c := &runCounter{err: invokeErr}
	err := st.RequestFleetRun(ctx, writerActor, fleetRunNow, c.start)
	if !errors.Is(err, store.ErrFleetRunNotStarted) || !errors.Is(err, invokeErr) {
		t.Errorf("error %v, want one wrapping store.ErrFleetRunNotStarted and the start's error", err)
	}
	if fleetRunMarker(t) == nil {
		t.Error("the marker was removed after a failed start")
	}
	if n := len(runEntries(t, func() ([]store.AuditEntry, error) { return st.ListAudit(ctx, time.Now().Add(-time.Hour), 100) })); n != 0 {
		t.Errorf("a run that never started was audited (%d)", n)
	}
	c.err = nil
	wantTooSoon(t, st.RequestFleetRun(ctx, writerActor, fleetRunNow.Add(time.Minute), c.start), fleetRunNow.Add(15*time.Minute))
}

// Two requests at once: the first is held just before its marker write while the second runs to the end. Only the
// conditional write can then tell them apart; a read of the marker followed by a plain write would start twice.
func TestRequestFleetRun_ConcurrentRequestsStartOnce(t *testing.T) {
	st := newStore(t)
	pause := ddblocal.PauseFirst(t, func(c *ddblocal.Call) bool {
		switch c.Op {
		case "PutItem", "UpdateItem", "TransactWriteItems":
			return strings.Contains(c.InputJSON(), "RunRequested")
		}
		return false
	})
	c := &runCounter{}
	first := make(chan error, 1)
	go func() { first <- st.RequestFleetRun(ctx, writerActor, fleetRunNow, c.start) }()
	select {
	case <-pause.Reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the first request never wrote the marker")
	}
	second := st.RequestFleetRun(ctx, writerActor, fleetRunNow, c.start)
	pause.Release()
	firstErr := <-first

	var soon *store.FleetRunTooSoonError
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
		t.Errorf("%d requests won and the job started %d times; want exactly one of each", wins, c.count())
	}
	if n := len(runEntries(t, func() ([]store.AuditEntry, error) { return st.ListAudit(ctx, time.Now().Add(-time.Hour), 100) })); n != 1 {
		t.Errorf("%d fleet.run entries, want 1", n)
	}
}

func TestRequestFleetRun_RefusesAnUnusableActorBeforeWriting(t *testing.T) {
	st := newStore(t)
	c := &runCounter{}
	if err := st.RequestFleetRun(ctx, store.Actor{}, fleetRunNow, c.start); err == nil {
		t.Error("an actor without a username was accepted")
	}
	if fleetRunMarker(t) != nil || c.count() != 0 {
		t.Error("an unusable actor wrote the marker or started the job")
	}
}

// fleetHealthSetting is the fleet health setting before a test changes anything: the contract rows newStore seeds hold
// one (FleetHealth / Config with MinSantaVersion), so a test of a write that must leave it alone compares it after.
func fleetHealthSetting(t *testing.T) map[string]types.AttributeValue {
	t.Helper()
	it := fleetHealthItem(t)
	if it == nil {
		t.Fatal("the seeded contract rows hold no FleetHealth / Config item to compare against")
	}
	return it
}
