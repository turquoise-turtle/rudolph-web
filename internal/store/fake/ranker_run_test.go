package fake_test

// The fake's rerun guard for "Run the ranker now" (owner request, 2026-11-25), as the fleet job's
// (fleet_run_test.go) and as the DynamoDB store implements it (ranker_run_integration_test.go there):
//
//	(*Store).RequestRankerRun(ctx, actor store.Actor, now time.Time, start func(context.Context) error) error
//
//   - Refused with a *store.RankerRunTooSoonError{RetryAt} while the last ranker request (its own marker,
//     FleetHealth / RankerRunRequested in DynamoDB) is younger than store.RankerRunInterval (15 minutes), RetryAt being
//     the request's time plus 15 minutes; or while the current top lists are, RetryAt being their GeneratedAt plus 15
//     minutes. The top lists' time is the GeneratedAt of the blocked list over 1 day (the pointer Top#Blocked#1, the
//     first the ranker moves; one run writes the same GeneratedAt on every pointer), which the fake holds as set with
//     SetTopList(store.TopKindBlocked, 1, ...). When both apply, RetryAt is the later. No list yet is no refusal. A
//     refusal calls no start, records no audit entry, and a refusal for fresh lists writes no marker.
//   - Otherwise the marker is set to now under the store's lock, released before start is called.
//   - start's error is returned wrapped with store.ErrRankerRunNotStarted; the marker is kept and nothing is audited.
//   - A start that succeeds records ranker.run (store.ActionRankerRun) on target ranker (store.AuditTargetRanker) by
//     the actor, with a short summary naming the ranker.
//   - The ranker's guard and the fleet job's are separate: a fleet run request or a fresh fleet snapshot never refuses
//     the ranker, and a ranker request or fresh top lists never refuse the fleet job.
//   - store.RankerRunTooSoonError is not ErrInvalidArgument, and not a *store.FleetRunTooSoonError.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func rankerAudit(t *testing.T, s *fake.Store) []store.AuditEntry {
	t.Helper()
	all, err := s.ListAudit(context.Background(), time.Now().Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEntry
	for _, e := range all {
		if e.Action == store.ActionRankerRun {
			out = append(out, e)
		}
	}
	return out
}

func rankerTooSoon(t *testing.T, err error, retryAt time.Time) {
	t.Helper()
	var soon *store.RankerRunTooSoonError
	if !errors.As(err, &soon) {
		t.Fatalf("error %v, want a *store.RankerRunTooSoonError", err)
	}
	if !soon.RetryAt.Equal(retryAt) {
		t.Errorf("RetryAt %v, want %v", soon.RetryAt, retryAt)
	}
	if errors.Is(err, store.ErrInvalidArgument) {
		t.Error("a request made too soon is ErrInvalidArgument; it is not bad input")
	}
	var fleet *store.FleetRunTooSoonError
	if errors.As(err, &fleet) {
		t.Error("the ranker's refusal is a FleetRunTooSoonError; the two guards are separate")
	}
}

func topListsGenerated(s *fake.Store, at time.Time) {
	s.SetTopList(store.TopKindBlocked, 1, at, []store.TopBinary{{FileSHA256: strings.Repeat("a", 64), FileName: "blocked", Count: 1}})
}

func TestRequestRankerRun_StartsAndAudits(t *testing.T) {
	s := fake.New()
	topListsGenerated(s, runAt.Add(-time.Hour))
	st := &starts{}
	if err := s.RequestRankerRun(context.Background(), fleetActor, runAt, st.start); err != nil {
		t.Fatal(err)
	}
	if st.count() != 1 {
		t.Errorf("%d starts, want 1", st.count())
	}
	entries := rankerAudit(t, s)
	if len(entries) != 1 || entries[0].Target != store.AuditTargetRanker || !strings.HasPrefix(entries[0].Actor, "web:") ||
		!strings.Contains(entries[0].Actor, "admin@example.com") || !strings.Contains(strings.ToLower(entries[0].Summary), "ranker") ||
		len(entries[0].Summary) > 200 {
		t.Errorf("audit %+v; want one ranker.run on ranker by the web actor, with a short summary naming the ranker", entries)
	}
	if store.ActionRankerRun != "ranker.run" || store.AuditTargetRanker != "ranker" || store.RankerRunInterval != 15*time.Minute {
		t.Errorf("ActionRankerRun %q, AuditTargetRanker %q, RankerRunInterval %v", store.ActionRankerRun, store.AuditTargetRanker, store.RankerRunInterval)
	}
	if n := len(runAudit(t, s)); n != 0 {
		t.Errorf("a ranker run was recorded as %d fleet.run entries", n)
	}
}

func TestRequestRankerRun_WithinFifteenMinutesOfTheLastRequest(t *testing.T) {
	s := fake.New()
	st := &starts{}
	if err := s.RequestRankerRun(context.Background(), fleetActor, runAt, st.start); err != nil {
		t.Fatal(err)
	}
	for _, after := range []time.Duration{0, time.Second, 14*time.Minute + 59*time.Second} {
		rankerTooSoon(t, s.RequestRankerRun(context.Background(), fleetActor, runAt.Add(after), st.start), runAt.Add(15*time.Minute))
	}
	if st.count() != 1 || len(rankerAudit(t, s)) != 1 {
		t.Errorf("refusals started (%d) or audited (%d)", st.count(), len(rankerAudit(t, s)))
	}
	if err := s.RequestRankerRun(context.Background(), fleetActor, runAt.Add(15*time.Minute), st.start); err != nil {
		t.Errorf("15 minutes later: %v", err)
	}
	rankerTooSoon(t, s.RequestRankerRun(context.Background(), fleetActor, runAt.Add(20*time.Minute), st.start), runAt.Add(30*time.Minute))
}

func TestRequestRankerRun_WhileTheTopListsAreFresh(t *testing.T) {
	s := fake.New()
	generated := runAt.Add(-5 * time.Minute)
	topListsGenerated(s, generated)
	st := &starts{}
	rankerTooSoon(t, s.RequestRankerRun(context.Background(), fleetActor, runAt, st.start), generated.Add(15*time.Minute))
	if st.count() != 0 || len(rankerAudit(t, s)) != 0 {
		t.Errorf("a refusal started (%d) or audited (%d)", st.count(), len(rankerAudit(t, s)))
	}
	// No marker was written: once the lists are 15 minutes old the run starts.
	if err := s.RequestRankerRun(context.Background(), fleetActor, generated.Add(15*time.Minute), st.start); err != nil {
		t.Errorf("once the lists are 15 minutes old: %v", err)
	}
}

func TestRequestRankerRun_RetryAtIsTheLaterOfBoth(t *testing.T) {
	s := fake.New()
	st := &starts{}
	if err := s.RequestRankerRun(context.Background(), fleetActor, runAt, st.start); err != nil {
		t.Fatal(err)
	}
	topListsGenerated(s, runAt.Add(10*time.Minute))
	rankerTooSoon(t, s.RequestRankerRun(context.Background(), fleetActor, runAt.Add(16*time.Minute), st.start), runAt.Add(25*time.Minute))
}

func TestRequestRankerRun_StartFailureKeepsTheMarker(t *testing.T) {
	s := fake.New()
	invokeErr := errors.New("operation error Lambda: Invoke, ResourceNotFoundException")
	st := &starts{err: invokeErr}
	err := s.RequestRankerRun(context.Background(), fleetActor, runAt, st.start)
	if !errors.Is(err, store.ErrRankerRunNotStarted) || !errors.Is(err, invokeErr) {
		t.Errorf("error %v, want one wrapping store.ErrRankerRunNotStarted and the start's error", err)
	}
	if errors.Is(err, store.ErrFleetRunNotStarted) {
		t.Error("a ranker start failure is ErrFleetRunNotStarted")
	}
	if n := len(rankerAudit(t, s)); n != 0 {
		t.Errorf("a run that never started was audited (%d)", n)
	}
	st.err = nil
	rankerTooSoon(t, s.RequestRankerRun(context.Background(), fleetActor, runAt.Add(time.Minute), st.start), runAt.Add(15*time.Minute))
	if st.count() != 1 {
		t.Errorf("%d starts, want 1", st.count())
	}
}

func TestRequestRankerRun_ConcurrentRequestsStartOnce(t *testing.T) {
	s := fake.New()
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var n int
	var mu sync.Mutex
	slow := func(context.Context) error {
		mu.Lock()
		n++
		mu.Unlock()
		entered <- struct{}{}
		<-release
		return nil
	}
	first := make(chan error, 1)
	go func() { first <- s.RequestRankerRun(context.Background(), fleetActor, runAt, slow) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("start was never called")
	}
	others := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			others <- s.RequestRankerRun(context.Background(), fleetActor, runAt, slow)
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("requests waited on a start in progress instead of being refused: the store's lock is held across start")
	}
	close(others)
	for err := range others {
		var soon *store.RankerRunTooSoonError
		if !errors.As(err, &soon) {
			t.Errorf("a request during the start: %v, want RankerRunTooSoonError", err)
		}
	}
	close(release)
	if err := <-first; err != nil {
		t.Errorf("the first request: %v", err)
	}
	if n != 1 {
		t.Errorf("start called %d times, want 1", n)
	}
}

func TestRequestRankerRun_RefusesWithoutWriting(t *testing.T) {
	s := fake.New()
	st := &starts{}
	if err := s.RequestRankerRun(context.Background(), store.Actor{}, runAt, st.start); err == nil {
		t.Error("an actor without a username was accepted")
	}
	s.SetError(errors.New("boom"))
	if err := s.RequestRankerRun(context.Background(), fleetActor, runAt, st.start); err == nil {
		t.Error("SetError did not fail RequestRankerRun")
	}
	s.SetError(nil)
	if err := s.RequestRankerRun(context.Background(), fleetActor, runAt, st.start); err != nil {
		t.Errorf("after the refusals: %v; they must not have written the marker", err)
	}
	if st.count() != 1 {
		t.Errorf("%d starts, want 1", st.count())
	}
}

// Each job has its own marker and its own freshness check: neither run blocks the other.
func TestRequestRankerRun_IndependentOfTheFleetJob(t *testing.T) {
	s := fake.New()
	fleet, ranker := &starts{}, &starts{}
	if err := s.RequestFleetRun(context.Background(), fleetActor, runAt, fleet.start); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestRankerRun(context.Background(), fleetActor, runAt, ranker.start); err != nil {
		t.Errorf("the ranker straight after a fleet run: %v", err)
	}

	s = fake.New()
	fleet, ranker = &starts{}, &starts{}
	if err := s.RequestRankerRun(context.Background(), fleetActor, runAt, ranker.start); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestFleetRun(context.Background(), fleetActor, runAt, fleet.start); err != nil {
		t.Errorf("the fleet job straight after a ranker run: %v", err)
	}

	s = fake.New()
	snapshotGenerated(s, runAt.Add(-time.Minute))
	topListsGenerated(s, runAt.Add(-2*time.Hour))
	if err := s.RequestRankerRun(context.Background(), fleetActor, runAt, (&starts{}).start); err != nil {
		t.Errorf("a fresh fleet snapshot refused the ranker: %v", err)
	}

	s = fake.New()
	topListsGenerated(s, runAt.Add(-time.Minute))
	snapshotGenerated(s, runAt.Add(-2*time.Hour))
	if err := s.RequestFleetRun(context.Background(), fleetActor, runAt, (&starts{}).start); err != nil {
		t.Errorf("fresh top lists refused the fleet job: %v", err)
	}
}
