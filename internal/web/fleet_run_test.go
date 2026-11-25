package web_test

// "Run the fleet job now" (owner request, 2026-11-25): an admin with editing on can start Rudolph's fleet job
// (the Lambda ${prefix}_rudolph_fleet, which otherwise runs once a day) from /fleet instead of waiting for the next
// scheduled run.
//
//   - web.FleetRunner{ Start(ctx) error } starts one asynchronous run (production: a Lambda Invoke with
//     InvocationType Event, internal/awslambda). It comes in through web.Options.FleetRunner; nil (no
//     RUDOLPH_WEB_FLEET_FUNCTION) means no button and no route.
//   - POST /admin/fleet/run exists only with editing on and a runner: 404 otherwise. Viewers 403, cross-site 403, GET
//     405, as the other admin forms. It takes no fields.
//   - The rerun guard is the store's: store.Writer.RequestFleetRun(ctx, actor, now, start) refuses with a
//     *store.FleetRunTooSoonError{RetryAt} while the last request (the marker FleetHealth / RunRequested) or the
//     current snapshot (FleetSummary.GeneratedAt) is younger than store.FleetRunInterval (15 minutes). Otherwise it
//     writes the marker with a conditional write, which decides between concurrent requests, and only the winner
//     calls start. The handler passes its own clock (Options.Now) as now and the runner's Start as start.
//   - Started: one audit entry fleet.run on target fleet by the web actor, with a short summary naming the fleet job,
//     and 303 to /fleet?run=started, which shows "Started: the fleet lists update in a few minutes."
//   - Refused: 409 saying "Not started" and how long until the next allowed run, in whole minutes rounded up
//     ("in 15 minutes", "in 1 minute"); nothing started, nothing audited, and a refusal because of a fresh snapshot
//     writes no marker.
//   - Start failed: the marker is kept, so the button cannot be hammered against a failing Lambda; 500 saying the fleet
//     job "could not be started", without the AWS error; nothing audited (nothing ran). store.ErrFleetRunNotStarted
//     wraps the runner's error.
//   - The button (a form posting to /admin/fleet/run, "Run the fleet job now") shows only to admins with editing on
//     and a runner. A viewer's /fleet makes no extra reads.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

// fleetRunner counts the runs it was asked to start. With hold set, Start signals entered and waits for hold to be
// closed, so a test can send other requests while one is starting the job.
type fleetRunner struct {
	mu      sync.Mutex
	starts  int
	err     error
	hold    chan struct{}
	entered chan struct{}
}

func (r *fleetRunner) Start(ctx context.Context) error {
	r.mu.Lock()
	r.starts++
	hold, entered, err := r.hold, r.entered, r.err
	r.mu.Unlock()
	if hold != nil {
		entered <- struct{}{}
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (r *fleetRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

var _ web.FleetRunner = (*fleetRunner)(nil)

// clock is a settable Options.Now.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newFleetRunApp is the fleet app with editing on (unless w is nil), the runner (none when nil) and a settable clock
// starting at fixedNow.
func newFleetRunApp(t *testing.T, s *fake.Store, w store.Writer, r web.FleetRunner) (*app, *clock) {
	t.Helper()
	c := &clock{now: fixedNow}
	a := newFleetApp(t, s, w, func(o *web.Options) {
		o.Now = c.Now
		if r != nil {
			o.FleetRunner = r
		}
	})
	return a, c
}

var fleetRunForm = regexp.MustCompile(`(?s)<form[^>]*action="/admin/fleet/run"[^>]*>(.*?)</form>`)

func TestFleetRun_ButtonOnlyForAdminsWithEditingAndAFunction(t *testing.T) {
	s := fleetStore()
	a, _ := newFleetRunApp(t, s, s, &fleetRunner{})
	_, body := adminGet(t, a, "/fleet")
	m := fleetRunForm.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("an admin with editing on and a fleet function has no form posting to /admin/fleet/run")
	}
	if !regexp.MustCompile(`<form[^>]*method="post"[^>]*action="/admin/fleet/run"`).MatchString(body) {
		t.Error("the run form does not post")
	}
	mustContain(t, m[1], `type="submit"`, "Run the fleet job now")

	_, body = a.viewerGet(t, "/fleet")
	mustNotContain(t, body, "/admin/fleet/run", "Run the fleet job now")

	noFunction, _ := newFleetRunApp(t, s, s, nil)
	_, body = adminGet(t, noFunction, "/fleet")
	mustNotContain(t, body, "/admin/fleet/run", "Run the fleet job now")

	editingOff, _ := newFleetRunApp(t, s, nil, &fleetRunner{})
	_, body = adminGet(t, editingOff, "/fleet")
	mustNotContain(t, body, "/admin/fleet/run", "Run the fleet job now")
}

func TestFleetRun_ViewerPageMakesNoExtraReads(t *testing.T) {
	s := fleetStore()
	a, _ := newFleetRunApp(t, s, s, &fleetRunner{})
	s.ResetCalls()
	if code, _ := a.viewerGet(t, "/fleet"); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	for _, c := range s.Calls() {
		if c.Method != "FleetSummary" && c.Method != "FleetHealthConfig" {
			t.Errorf("call %+v; a viewer's fleet page reads the summary and the holiday setting only", c)
		}
	}
	if n := len(s.Calls()); n == 0 || n > 2 {
		t.Errorf("%d calls, want 1 or 2: %+v", n, s.Calls())
	}
}

func TestFleetRun_StartsAuditsAndSaysSo(t *testing.T) {
	s := fleetStore()
	r := &fleetRunner{}
	a, _ := newFleetRunApp(t, s, s, r)
	admin := a.cookie(t, auth.RoleAdmin)

	rec := a.post(t, "/admin/fleet/run", url.Values{}, admin, true)
	see303(t, rec, "/fleet?run=started")
	if n := r.count(); n != 1 {
		t.Errorf("the fleet job was started %d times, want once", n)
	}
	entries := fleetAuditOf(t, s, store.ActionFleetRun)
	if len(entries) != 1 {
		t.Fatalf("audit %+v; want one fleet.run entry", entries)
	}
	e := entries[0]
	if e.Target != "fleet" || !strings.HasPrefix(e.Actor, "web:") || !strings.Contains(e.Actor, "vera@example.com") {
		t.Errorf("audit %+v; want fleet.run on fleet by the web actor", e)
	}
	if !containsFold(e.Summary, "fleet job") || len(e.Summary) > 200 {
		t.Errorf("audit summary %q; want a short one naming the fleet job", e.Summary)
	}
	if store.ActionFleetRun != "fleet.run" {
		t.Errorf("ActionFleetRun = %q, want fleet.run", store.ActionFleetRun)
	}

	_, body := adminGet(t, a, "/fleet?run=started")
	mustContain(t, body, "Started: the fleet lists update in a few minutes.")
	_, body = adminGet(t, a, "/fleet")
	mustNotContain(t, body, "Started: the fleet lists update")
}

func TestFleetRun_RefusedWithinFifteenMinutesOfTheLastRequest(t *testing.T) {
	s := fleetStore()
	r := &fleetRunner{}
	a, c := newFleetRunApp(t, s, s, r)
	admin := a.cookie(t, auth.RoleAdmin)

	see303(t, a.post(t, "/admin/fleet/run", url.Values{}, admin, true), "/fleet?run=started")

	rec := a.post(t, "/admin/fleet/run", url.Values{}, admin, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a second request at once: status %d, want 409", rec.Code)
	}
	mustContain(t, rec.Body.String(), "Not started", "in 15 minutes")

	c.advance(14 * time.Minute)
	rec = a.post(t, "/admin/fleet/run", url.Values{}, admin, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("14 minutes later: status %d, want 409", rec.Code)
	}
	mustContain(t, rec.Body.String(), "in 1 minute")
	mustNotContain(t, rec.Body.String(), "1 minutes")

	if n := r.count(); n != 1 {
		t.Errorf("refused requests started the job: %d starts, want 1", n)
	}
	if n := len(fleetAuditOf(t, s, store.ActionFleetRun)); n != 1 {
		t.Errorf("refusals were audited: %d fleet.run entries, want 1", n)
	}

	c.advance(time.Minute)
	see303(t, a.post(t, "/admin/fleet/run", url.Values{}, admin, true), "/fleet?run=started")
	if n := r.count(); n != 2 {
		t.Errorf("15 minutes after the first request: %d starts, want 2", n)
	}
	if store.FleetRunInterval != 15*time.Minute {
		t.Errorf("FleetRunInterval = %v, want 15m", store.FleetRunInterval)
	}
}

func TestFleetRun_RefusedWhileTheSnapshotIsFresh(t *testing.T) {
	s := fleetStore()
	sum := fleetSummary()
	sum.GeneratedAt = fixedNow.Add(-5 * time.Minute)
	s.SetFleetSnapshot(fake.FleetSnapshot{Summary: sum})
	r := &fleetRunner{}
	a, c := newFleetRunApp(t, s, s, r)
	admin := a.cookie(t, auth.RoleAdmin)

	rec := a.post(t, "/admin/fleet/run", url.Values{}, admin, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a snapshot 5 minutes old: status %d, want 409", rec.Code)
	}
	mustContain(t, rec.Body.String(), "Not started", "in 10 minutes")
	if n := r.count(); n != 0 {
		t.Errorf("%d starts, want none", n)
	}
	if n := len(fleetAuditOf(t, s, store.ActionFleetRun)); n != 0 {
		t.Errorf("the refusal was audited (%d entries)", n)
	}

	// Had the refusal written the marker, the next run would wait until 15 minutes after it.
	c.advance(10 * time.Minute)
	see303(t, a.post(t, "/admin/fleet/run", url.Values{}, admin, true), "/fleet?run=started")
	if n := r.count(); n != 1 {
		t.Errorf("once the snapshot is 15 minutes old: %d starts, want 1", n)
	}
}

func TestFleetRun_NoSnapshotYetCanStart(t *testing.T) {
	s := fake.New()
	r := &fleetRunner{}
	a, _ := newFleetRunApp(t, s, s, r)
	see303(t, a.post(t, "/admin/fleet/run", url.Values{}, a.cookie(t, auth.RoleAdmin), true), "/fleet?run=started")
	if n := r.count(); n != 1 {
		t.Errorf("before the job's first run: %d starts, want 1", n)
	}
}

// One request is starting the job, its marker written; every request meanwhile is refused without starting
// another run. The guard is decided before the invoke, and the store does not hold a lock across it.
func TestFleetRun_OnlyOneOfConcurrentRequestsStarts(t *testing.T) {
	s := fleetStore()
	r := &fleetRunner{hold: make(chan struct{}), entered: make(chan struct{}, 1)}
	a, _ := newFleetRunApp(t, s, s, r)
	admin := a.cookie(t, auth.RoleAdmin)

	first := make(chan int, 1)
	go func() { first <- a.post(t, "/admin/fleet/run", url.Values{}, admin, true).Code }()
	select {
	case <-r.entered:
	case <-time.After(5 * time.Second):
		close(r.hold)
		t.Fatal("the first request never started the job")
	}

	var wg sync.WaitGroup
	codes := make(chan int, 5)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- a.post(t, "/admin/fleet/run", url.Values{}, admin, true).Code
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(r.hold)
		t.Fatal("requests made while the job was starting waited for it instead of being refused")
	}
	close(codes)
	for code := range codes {
		if code != http.StatusConflict {
			t.Errorf("a request while the job was starting: status %d, want 409", code)
		}
	}
	close(r.hold)
	if code := <-first; code != http.StatusSeeOther {
		t.Errorf("the first request: status %d, want 303", code)
	}
	if n := r.count(); n != 1 {
		t.Errorf("%d starts, want exactly 1", n)
	}
	if n := len(fleetAuditOf(t, s, store.ActionFleetRun)); n != 1 {
		t.Errorf("%d fleet.run entries, want 1", n)
	}
}

func TestFleetRun_StartFailureKeepsTheMarker(t *testing.T) {
	s := fleetStore()
	r := &fleetRunner{err: errors.New("operation error Lambda: Invoke, https response error StatusCode: 403, AccessDeniedException: not authorized")}
	a, _ := newFleetRunApp(t, s, s, r)
	admin := a.cookie(t, auth.RoleAdmin)

	rec := a.post(t, "/admin/fleet/run", url.Values{}, admin, true)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("a failed start: status %d, want 500", rec.Code)
	}
	mustContain(t, rec.Body.String(), "could not be started")
	mustNotContain(t, rec.Body.String(), "AccessDeniedException", "StatusCode")
	if n := len(fleetAuditOf(t, s, store.ActionFleetRun)); n != 0 {
		t.Errorf("a run that never started was audited (%d entries)", n)
	}

	// The marker stays: a retry at once is refused without asking Lambda again.
	rec = a.post(t, "/admin/fleet/run", url.Values{}, admin, true)
	if rec.Code != http.StatusConflict {
		t.Errorf("a retry straight after a failed start: status %d, want 409", rec.Code)
	}
	if n := r.count(); n != 1 {
		t.Errorf("Lambda was asked %d times, want once", n)
	}
}

func TestFleetRun_AdminOnlySameOriginPostEditingAndFunction(t *testing.T) {
	s := fleetStore()
	r := &fleetRunner{}
	a, _ := newFleetRunApp(t, s, s, r)

	if rec := a.post(t, "/admin/fleet/run", url.Values{}, a.cookie(t, auth.RoleViewer), true); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status %d, want 403", rec.Code)
	}
	if rec := a.post(t, "/admin/fleet/run", url.Values{}, a.cookie(t, auth.RoleAdmin), false); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: status %d, want 403", rec.Code)
	}
	if rec := a.get(t, "/admin/fleet/run", a.cookie(t, auth.RoleAdmin)); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d, want 405", rec.Code)
	}

	off, _ := newFleetRunApp(t, s, nil, r)
	if rec := off.post(t, "/admin/fleet/run", url.Values{}, off.cookie(t, auth.RoleAdmin), true); rec.Code != http.StatusNotFound {
		t.Errorf("editing off: status %d, want 404", rec.Code)
	}
	noFunction, _ := newFleetRunApp(t, s, s, nil)
	if rec := noFunction.post(t, "/admin/fleet/run", url.Values{}, noFunction.cookie(t, auth.RoleAdmin), true); rec.Code != http.StatusNotFound {
		t.Errorf("no fleet function: status %d, want 404", rec.Code)
	}

	if n := r.count(); n != 0 {
		t.Errorf("refused requests started the job %d times", n)
	}
	if n := len(fleetAuditOf(t, s, store.ActionFleetRun)); n != 0 {
		t.Errorf("refused requests were audited (%d entries)", n)
	}
}
