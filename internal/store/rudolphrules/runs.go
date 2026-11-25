package rudolphrules

import (
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// The audit summaries of runs started from the web view. Rudolph's CLI
// records nothing like them, so the words are the web view's.
const (
	FleetRunSummary  = "started the fleet job from the web view"
	RankerRunSummary = "started the ranker from the web view"
)

// JobRun is what differs between the scheduled jobs of Rudolph an admin can
// start from the web view. The stores guard and record every job the same
// way, each with its own marker and its own results, so that the DynamoDB
// store and the fake cannot drift apart per job.
type JobRun struct {
	// Interval is how long another run is refused after the last request or
	// the job's current results.
	Interval time.Duration
	// TooSoon is the refusal, which names when a run can be asked for again.
	TooSoon func(retryAt time.Time) error
	// NotStarted wraps the error of a start that failed.
	NotStarted error
	// Action, Target and Summary are the audit entry of a run that started.
	Action, Target, Summary string
}

// FleetRun is "Run the fleet job now" and RankerRun "Run the ranker now".
var (
	FleetRun = JobRun{
		Interval:   store.FleetRunInterval,
		TooSoon:    func(retryAt time.Time) error { return &store.FleetRunTooSoonError{RetryAt: retryAt} },
		NotStarted: store.ErrFleetRunNotStarted,
		Action:     store.ActionFleetRun,
		Target:     store.AuditTargetFleet,
		Summary:    FleetRunSummary,
	}
	RankerRun = JobRun{
		Interval:   store.RankerRunInterval,
		TooSoon:    func(retryAt time.Time) error { return &store.RankerRunTooSoonError{RetryAt: retryAt} },
		NotStarted: store.ErrRankerRunNotStarted,
		Action:     store.ActionRankerRun,
		Target:     store.AuditTargetRanker,
		Summary:    RankerRunSummary,
	}
)

// RetryAt returns when the job may be run again and whether that is still
// ahead at now, from the last request and the time of the job's current
// results, either zero when there is none. Both stores decide a refusal with
// it.
func (j JobRun) RetryAt(now, lastRequest, resultsAt time.Time) (time.Time, bool) {
	var retryAt time.Time
	for _, t := range []time.Time{lastRequest, resultsAt} {
		if t.IsZero() {
			continue
		}
		if next := t.Add(j.Interval); next.After(now) && next.After(retryAt) {
			retryAt = next
		}
	}
	return retryAt, !retryAt.IsZero()
}
