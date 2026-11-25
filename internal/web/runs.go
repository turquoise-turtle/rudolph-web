package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// "Run the fleet job now" and "Run the ranker now": an admin starts one of
// Rudolph's scheduled jobs instead of waiting for its next run. Both buttons
// are on the admin page, and both work the same way, so what differs between
// the jobs is data here.

// jobRun is one job an admin can start.
type jobRun struct {
	// Slug names the job in its route (/admin/<slug>/run), in the notice's
	// address (/admin?started=<slug>) and in the button's anchor.
	Slug string
	// Name is the job in a sentence ("the fleet job"), Button the button's
	// text, Updates what a run rebuilds, for the notice, and About the hint
	// under the button.
	Name, Button, Updates, About string

	interval time.Duration
	start    func(context.Context) error
	// request is the store's guard for this job: each has its own, so
	// neither run refuses the other.
	request func(ctx context.Context, actor store.Actor, now time.Time, start func(context.Context) error) error
	// retryAt reads the time a refusal names; ok is false for any other
	// error.
	retryAt    func(err error) (at time.Time, ok bool)
	notStarted error
}

// adminRun is a job's button on the admin page; Started is set when the
// admin has just started it.
type adminRun struct {
	jobRun
	Started bool
}

// jobRuns are the jobs this deployment lets admins start: none with editing
// off, and each only with its runner.
func (s *server) jobRuns() []jobRun {
	var runs []jobRun
	if s.fleetRunner != nil {
		runs = append(runs, jobRun{
			Slug:    "fleet",
			Name:    "the fleet job",
			Button:  "Run the fleet job now",
			Updates: "the fleet lists",
			About: "Rebuilds the fleet health lists and the Lockdown readiness figures now instead of at the job's next daily run, for instance after changing the stale threshold, a group or a client mode. " +
				"A run takes from seconds to a few minutes.",
			interval: store.FleetRunInterval,
			start:    s.fleetRunner.Start,
			request:  s.writer.RequestFleetRun,
			retryAt: func(err error) (time.Time, bool) {
				var soon *store.FleetRunTooSoonError
				if !errors.As(err, &soon) {
					return time.Time{}, false
				}
				return soon.RetryAt, true
			},
			notStarted: store.ErrFleetRunNotStarted,
		})
	}
	if s.rankerRunner != nil {
		runs = append(runs, jobRun{
			Slug:    "ranker",
			Name:    "the ranker",
			Button:  "Run the ranker now",
			Updates: "the top lists",
			About: "Rebuilds the top blocked, top allowed and top unknown lists now instead of at the ranker's next scheduled run, for instance after adding a global rule for an unknown binary. " +
				"A run takes from seconds to a couple of minutes.",
			interval: store.RankerRunInterval,
			start:    s.rankerRunner.Start,
			request:  s.writer.RequestRankerRun,
			retryAt: func(err error) (time.Time, bool) {
				var soon *store.RankerRunTooSoonError
				if !errors.As(err, &soon) {
					return time.Time{}, false
				}
				return soon.RetryAt, true
			},
			notStarted: store.ErrRankerRunNotStarted,
		})
	}
	return runs
}

// IntervalText is the job's rerun interval for the hint under its button.
func (j jobRun) IntervalText() string { return minutesText(j.interval) }

// minutesText writes a wait as whole minutes, rounded up so that the time
// named has passed when it is over: "15 minutes", "1 minute".
func minutesText(d time.Duration) string {
	n := max(int((d+time.Minute-1)/time.Minute), 1)
	if n == 1 {
		return "1 minute"
	}
	return strconv.Itoa(n) + " minutes"
}

// runFleetJob starts Rudolph's fleet job now instead of at its next daily
// run, and runRanker the ranker instead of at its next scheduled one. The
// routes exist only with the job's runner, so the job is always found.
func (s *server) runFleetJob(w http.ResponseWriter, r *http.Request) { s.runJob(w, r, "fleet") }

func (s *server) runRanker(w http.ResponseWriter, r *http.Request) { s.runJob(w, r, "ranker") }

// runJob starts one job. How often is the store's to say: its guard is one
// conditional write, which holds across Lambda instances where a lock here
// would not.
func (s *server) runJob(w http.ResponseWriter, r *http.Request, slug string) {
	var job jobRun
	for _, j := range s.jobRuns() {
		if j.Slug == slug {
			job = j
		}
	}
	if job.Slug == "" {
		s.renderError(w, r, http.StatusNotFound, "Page not found.")
		return
	}
	// The form has no fields; parsing only bounds the body.
	if !s.parseForm(w, r) {
		return
	}
	now := s.now()
	err := job.request(r.Context(), actorOf(r), now, job.start)
	retryAt, soon := job.retryAt(err)
	switch {
	case soon:
		s.renderError(w, r, http.StatusConflict, "Not started: "+job.Name+" ran, or was started, less than "+
			minutesText(job.interval)+" ago. It can be run again in "+minutesText(retryAt.Sub(now))+".")
		return
	case errors.Is(err, job.notStarted):
		// The AWS error says which function and why; that is for the log.
		s.log.Error("starting "+job.Name+" failed", "path", r.URL.Path, "err", err)
		s.renderError(w, r, http.StatusInternalServerError, upperFirst(job.Name)+" could not be started. It can be tried again in "+
			minutesText(job.interval)+"; if it fails again, tell whoever looks after Rudolph.")
		return
	case errors.Is(err, store.ErrNotAudited):
		s.log.Error(job.Name+" started but not audited", "path", r.URL.Path, "err", err)
		s.renderError(w, r, http.StatusInternalServerError, upperFirst(job.Name)+" was started, but that could not be recorded in the audit trail. Tell whoever looks after Rudolph.")
		return
	case err != nil:
		s.writeError(w, r, err, "")
		return
	}
	http.Redirect(w, r, "/admin?started="+job.Slug, http.StatusSeeOther)
}

// upperFirst starts a sentence with a job's name, which is ASCII.
func upperFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
