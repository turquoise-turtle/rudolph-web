package rudolphrules

import (
	"fmt"
	"time"

	"github.com/airbnb/rudolph/pkg/model/fleethealth"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// Holiday mode, the minimum Santa version and the cohort action, checked alike by the DynamoDB writer
// and the fake. Holiday mode's rules are Rudolph's own (pkg/model/fleethealth),
// which its CLI uses too.

// DefaultStaleDays is the fleet job's stale threshold when none is stored.
const DefaultStaleDays = fleethealth.DefaultStaleDays

// MinSantaVersionOff is the minimum Santa version that turns the check off.
const MinSantaVersionOff = fleethealth.MinSantaVersionOff

// CheckHoliday refuses a holiday as rudolph fleet holiday does, against the
// normal threshold stored (0 for none) at now.
func CheckHoliday(normalStaleDays, staleDays int, until string, now time.Time) error {
	if err := (fleethealth.Config{StaleDays: normalStaleDays}).ValidateHoliday(staleDays, until, now); err != nil {
		return store.Invalid("%v", err)
	}
	return nil
}

// SetHolidaySummary is the CLI's audit summary of a holiday being set.
func SetHolidaySummary(staleDays int, until string, normalStaleDays int) string {
	return fmt.Sprintf("set a holiday: stale after %d days until %s (normally %d days)", staleDays, until, normalStaleDays)
}

// EndHolidaySummary is the CLI's audit summary of a holiday being ended:
// staleDays and until are the holiday's as it was stored, 0 and "" when what
// was stored could not be read as one.
func EndHolidaySummary(staleDays int, until string, normalStaleDays int) string {
	if staleDays == 0 {
		return fmt.Sprintf("ended the holiday; stale threshold back to %d days", normalStaleDays)
	}
	return fmt.Sprintf("ended the holiday of %d stale days until %s; stale threshold back to %d days", staleDays, until, normalStaleDays)
}

// CheckMinSantaVersion refuses a minimum Santa version as rudolph fleet
// min-santa-version does, and returns it as Rudolph stores it: trimmed, off in
// any case as off.
func CheckMinSantaVersion(version string) (string, error) {
	checked, err := fleethealth.ValidateMinSantaVersion(version)
	if err != nil {
		return "", store.Invalid("%v", err)
	}
	return checked, nil
}

// MinSantaVersionAsRead is a raw MinSantaVersion attribute as Rudolph's
// fleethealth.FromItem reads it: "" when Rudolph cannot use it.
func MinSantaVersionAsRead(raw string) string {
	usable, _ := fleethealth.ValidateMinSantaVersion(raw)
	return usable
}

// SetMinSantaVersionSummary is the CLI's audit summary of the minimum Santa
// version being set to version (as stored), or turned off. before is the
// stored value as it was read before the change, "" for none usable.
func SetMinSantaVersionSummary(version, before string) string {
	was, _, stored := fleethealth.Config{MinSantaVersion: before}.MinSantaVersionInForce()
	if !stored {
		was = "the default " + was
	}
	if version == fleethealth.MinSantaVersionOff {
		return fmt.Sprintf("turned the minimum Santa version check off (was %s)", was)
	}
	return fmt.Sprintf("set the minimum Santa version to %s (was %s)", version, was)
}

// ClearMinSantaVersionSummary is the CLI's audit summary of the stored
// minimum Santa version being removed. before is the removed value as it was
// read, "" when what was stored could not be used.
func ClearMinSantaVersionSummary(before string) string {
	cleared := "an unusable value"
	if before != "" {
		cleared = before
	}
	return fmt.Sprintf("cleared the stored minimum Santa version (was %s); the default %s applies", cleared, fleethealth.DefaultMinSantaVersion)
}

// Cohort is a checked cohort assignment: the audit name of the actor, the
// group's normalised name and the Macs, each once, in the order given.
type Cohort struct {
	Actor      string
	Group      string
	MachineIDs []string
}

// CheckCohort checks everything about a cohort assignment that needs no
// read: the actor, the number of Macs and that each is a machine ID, that
// the snapshot is one of Rudolph's and not older than
// store.MaxCohortSnapshotAge at now, and the group's name, refusing the group
// of Macs in no group, which nothing can be assigned.
func CheckCohort(actor store.Actor, snapshot, group string, machineIDs []string, now time.Time) (Cohort, error) {
	who, err := actor.AuditName()
	if err != nil {
		return Cohort{}, err
	}
	// A form can repeat a field thousands of times, so the IDs are made
	// unique in one pass before they are counted.
	seen := make(map[string]bool, len(machineIDs))
	var ids []string
	for _, id := range machineIDs {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	switch {
	case len(ids) == 0:
		return Cohort{}, store.Invalid("no Macs were chosen; tick the Macs to add to the group")
	case len(ids) > store.MaxCohortMacs:
		return Cohort{}, store.Invalid("%d Macs were chosen; at most %d can be added at a time", len(ids), store.MaxCohortMacs)
	}
	for _, id := range ids {
		if err := MachineID(id); err != nil {
			return Cohort{}, err
		}
	}
	at, ok := store.SnapshotTime(snapshot)
	if !ok {
		return Cohort{}, store.Invalid("the readiness snapshot is not one of Rudolph's; reload the list of ready Macs and choose again")
	}
	if now.Sub(at) > store.MaxCohortSnapshotAge {
		return Cohort{}, fmt.Errorf("%w: it was generated %s, more than %d hours ago", store.ErrSnapshotTooOld,
			at.Format("2006-01-02 15:04 UTC"), int(store.MaxCohortSnapshotAge/time.Hour))
	}
	name, err := RegisteredGroupName(group)
	if err != nil {
		return Cohort{}, err
	}
	return Cohort{Actor: who, Group: name, MachineIDs: ids}, nil
}

// NotALockdownGroup is the refusal of a cohort group whose client mode is
// not LOCKDOWN or STANDALONE: adding Macs to it would not lock them, and
// setting a group's mode is where locking is confirmed.
func NotALockdownGroup(name string) error {
	return store.Invalid("group %q has no LOCKDOWN or STANDALONE client mode; set one on the group's page first", name)
}

// IsLockdownMode reports whether a group with this client mode is one a
// cohort can be added to.
func IsLockdownMode(mode store.ClientMode) bool {
	return mode == store.ClientModeLockdown || mode == store.ClientModeStandalone
}

// NotReady is the refusal of a cohort holding a Mac that is not Ready in the
// snapshot's list of the fleet.
func NotReady(machineID string) error {
	return store.Invalid("Mac %s is not ready in this snapshot; reload the list of ready Macs and choose again", machineID)
}
