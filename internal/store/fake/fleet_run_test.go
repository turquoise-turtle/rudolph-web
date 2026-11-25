package fake_test

// The fake's rerun guard for "Run the fleet job now", which the web tests rely on and which the DynamoDB store
// implements alike (fleet_run_integration_test.go there):
//
//	(*Store).RequestFleetRun(ctx, actor store.Actor, now time.Time, start func(context.Context) error) error
//
//   - Refused with a *store.FleetRunTooSoonError{RetryAt} while the last request (the marker, FleetHealth /
//     RunRequested in DynamoDB) is younger than store.FleetRunInterval (15 minutes), RetryAt being the request's time
//     plus 15 minutes; or while the pointer's snapshot (FleetSummary.GeneratedAt) is, RetryAt being GeneratedAt plus
//     15 minutes. When both apply, RetryAt is the later. No snapshot yet is no refusal. A refusal calls no start,
//     records no audit entry, and a refusal for the snapshot writes no marker.
//   - Otherwise the marker is set to now, decided under the store's lock, which is released before start is called:
//     a start that takes long never holds up other requests, which are refused meanwhile.
//   - start's error is returned wrapped with store.ErrFleetRunNotStarted (errors.Is finds both); the marker is kept
//     and nothing is audited.
//   - A start that succeeds records fleet.run (store.ActionFleetRun) on target fleet (store.AuditTargetFleet) by the
//     actor, with a short summary naming the fleet job.
//   - An actor that cannot be audited is refused before anything is written; SetError fails it too.
//   - store.FleetRunTooSoonError is not invalid input (not ErrInvalidArgument): the request was fine, only early.

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

var runAt = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

type starts struct {
	mu  sync.Mutex
	n   int
	err error
}

func (s *starts) start(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return s.err
}

func (s *starts) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func runAudit(t *testing.T, s *fake.Store) []store.AuditEntry {
	t.Helper()
	all, err := s.ListAudit(context.Background(), time.Now().Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEntry
	for _, e := range all {
		if e.Action == store.ActionFleetRun {
			out = append(out, e)
		}
	}
	return out
}

func tooSoon(t *testing.T, err error, retryAt time.Time) {
	t.Helper()
	var soon *store.FleetRunTooSoonError
	if !errors.As(err, &soon) {
		t.Fatalf("error %v, want a *store.FleetRunTooSoonError", err)
	}
	if !soon.RetryAt.Equal(retryAt) {
		t.Errorf("RetryAt %v, want %v", soon.RetryAt, retryAt)
	}
	if errors.Is(err, store.ErrInvalidArgument) {
		t.Error("a request made too soon is ErrInvalidArgument; it is not bad input")
	}
}

func snapshotGenerated(s *fake.Store, at time.Time) {
	s.SetFleetSnapshot(fake.FleetSnapshot{Summary: store.FleetSummary{Snapshot: snapshotAt(at), GeneratedAt: at, Macs: 1}})
}

func TestRequestFleetRun_StartsAndAudits(t *testing.T) {
	s := fake.New()
	snapshotGenerated(s, runAt.Add(-3*time.Hour))
	st := &starts{}
	if err := s.RequestFleetRun(context.Background(), fleetActor, runAt, st.start); err != nil {
		t.Fatal(err)
	}
	if st.count() != 1 {
		t.Errorf("%d starts, want 1", st.count())
	}
	entries := runAudit(t, s)
	if len(entries) != 1 || entries[0].Target != store.AuditTargetFleet || !strings.HasPrefix(entries[0].Actor, "web:") ||
		!strings.Contains(entries[0].Actor, "admin@example.com") || !strings.Contains(strings.ToLower(entries[0].Summary), "fleet job") {
		t.Errorf("audit %+v; want one fleet.run on fleet by the web actor naming the fleet job", entries)
	}
}

func TestRequestFleetRun_WithinFifteenMinutesOfTheLastRequest(t *testing.T) {
	s := fake.New()
	st := &starts{}
	if err := s.RequestFleetRun(context.Background(), fleetActor, runAt, st.start); err != nil {
		t.Fatal(err)
	}
	for _, after := range []time.Duration{0, time.Second, 14*time.Minute + 59*time.Second} {
		err := s.RequestFleetRun(context.Background(), fleetActor, runAt.Add(after), st.start)
		tooSoon(t, err, runAt.Add(store.FleetRunInterval))
	}
	if st.count() != 1 || len(runAudit(t, s)) != 1 {
		t.Errorf("refusals started (%d) or audited (%d)", st.count(), len(runAudit(t, s)))
	}
	if err := s.RequestFleetRun(context.Background(), fleetActor, runAt.Add(15*time.Minute), st.start); err != nil {
		t.Errorf("15 minutes later: %v", err)
	}
	// The marker moved to the new request.
	tooSoon(t, s.RequestFleetRun(context.Background(), fleetActor, runAt.Add(20*time.Minute), st.start), runAt.Add(30*time.Minute))
	if st.count() != 2 {
		t.Errorf("%d starts, want 2", st.count())
	}
}

func TestRequestFleetRun_WhileTheSnapshotIsFresh(t *testing.T) {
	s := fake.New()
	generated := runAt.Add(-5 * time.Minute)
	snapshotGenerated(s, generated)
	st := &starts{}
	tooSoon(t, s.RequestFleetRun(context.Background(), fleetActor, runAt, st.start), generated.Add(15*time.Minute))
	if st.count() != 0 || len(runAudit(t, s)) != 0 {
		t.Errorf("a refusal started (%d) or audited (%d)", st.count(), len(runAudit(t, s)))
	}
	// No marker was written: once the snapshot is 15 minutes old the run starts.
	if err := s.RequestFleetRun(context.Background(), fleetActor, generated.Add(15*time.Minute), st.start); err != nil {
		t.Errorf("once the snapshot is 15 minutes old: %v", err)
	}
}

func TestRequestFleetRun_RetryAtIsTheLaterOfBoth(t *testing.T) {
	s := fake.New()
	st := &starts{}
	if err := s.RequestFleetRun(context.Background(), fleetActor, runAt, st.start); err != nil {
		t.Fatal(err)
	}
	// The run finished quickly and wrote a snapshot 10 minutes after the request.
	snapshotGenerated(s, runAt.Add(10*time.Minute))
	tooSoon(t, s.RequestFleetRun(context.Background(), fleetActor, runAt.Add(16*time.Minute), st.start), runAt.Add(25*time.Minute))
}

func TestRequestFleetRun_StartFailureKeepsTheMarker(t *testing.T) {
	s := fake.New()
	invokeErr := errors.New("operation error Lambda: Invoke, ResourceNotFoundException")
	st := &starts{err: invokeErr}
	err := s.RequestFleetRun(context.Background(), fleetActor, runAt, st.start)
	if !errors.Is(err, store.ErrFleetRunNotStarted) || !errors.Is(err, invokeErr) {
		t.Errorf("error %v, want one wrapping store.ErrFleetRunNotStarted and the start's error", err)
	}
	if n := len(runAudit(t, s)); n != 0 {
		t.Errorf("a run that never started was audited (%d)", n)
	}
	st.err = nil
	tooSoon(t, s.RequestFleetRun(context.Background(), fleetActor, runAt.Add(time.Minute), st.start), runAt.Add(15*time.Minute))
	if st.count() != 1 {
		t.Errorf("%d starts, want 1", st.count())
	}
}

func TestRequestFleetRun_ConcurrentRequestsStartOnce(t *testing.T) {
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
	go func() { first <- s.RequestFleetRun(context.Background(), fleetActor, runAt, slow) }()
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
			others <- s.RequestFleetRun(context.Background(), fleetActor, runAt, slow)
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
		var soon *store.FleetRunTooSoonError
		if !errors.As(err, &soon) {
			t.Errorf("a request during the start: %v, want FleetRunTooSoonError", err)
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

func TestRequestFleetRun_RefusesWithoutWriting(t *testing.T) {
	s := fake.New()
	st := &starts{}
	if err := s.RequestFleetRun(context.Background(), store.Actor{}, runAt, st.start); err == nil {
		t.Error("an actor without a username was accepted")
	}
	s.SetError(errors.New("boom"))
	if err := s.RequestFleetRun(context.Background(), fleetActor, runAt, st.start); err == nil {
		t.Error("SetError did not fail RequestFleetRun")
	}
	s.SetError(nil)
	if err := s.RequestFleetRun(context.Background(), fleetActor, runAt, st.start); err != nil {
		t.Errorf("after the refusals: %v; they must not have written the marker", err)
	}
	if st.count() != 1 {
		t.Errorf("%d starts, want 1", st.count())
	}
	if store.FleetRunInterval != 15*time.Minute || store.ActionFleetRun != "fleet.run" {
		t.Errorf("FleetRunInterval %v, ActionFleetRun %q", store.FleetRunInterval, store.ActionFleetRun)
	}
}
