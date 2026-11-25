package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Fleet health and Lockdown readiness (Rudolph's docs/fleet-health.md).
// Rudolph's fleet job runs once a day and writes a snapshot to the events
// table, which the view reads as it reads the ranker's top lists:
//
//	PK=FleetPointer                                   SK=Current   -> FleetSummary
//	PK=FleetSnapshot#<snapshot>#Health#<category>     SK=<order>#<machine id> or <machine id>   -> FleetHealthRow
//	PK=FleetSnapshot#<snapshot>#Ready#All             SK=<bucket>#<pending>#<machine id>        -> ReadinessRow
//	PK=FleetSnapshot#<snapshot>#Ready#Group#<group>   (the same)
//
// A Mac's unknown binaries come from the markers eventupload writes there:
//
//	PK=UnknownSeen#<machine id>                       SK=<file SHA-256>   -> UnknownBinary
//
// and the stale threshold with its holiday from one item of the store table
// (PK=FleetHealth, SK=Config) -> FleetHealthConfig.

// FleetCategory is one of the fleet job's health lists, by Rudolph's own
// name: the last segment of the list's partition key.
type FleetCategory string

const (
	FleetCategoryStale      FleetCategory = "Stale"
	FleetCategoryIncomplete FleetCategory = "Incomplete"
	FleetCategoryMode       FleetCategory = "Mode"
	FleetCategoryRules      FleetCategory = "Rules"
	FleetCategorySIP        FleetCategory = "SIP"
	FleetCategoryVersion    FleetCategory = "Version"
)

// FleetCategories lists every health category, in Rudolph's order.
var FleetCategories = []FleetCategory{
	FleetCategoryStale, FleetCategoryIncomplete, FleetCategoryMode,
	FleetCategoryRules, FleetCategorySIP, FleetCategoryVersion,
}

// ReadinessBucket says how far a Mac is from Lockdown. Each Mac is in one.
type ReadinessBucket string

const (
	// ReadinessAny is no bucket filter.
	ReadinessAny ReadinessBucket = ""
	// ReadinessReady: no unknown binary of the last 30 days is pending.
	ReadinessReady ReadinessBucket = "ready"
	// ReadinessFew: 1 to 9 pending.
	ReadinessFew ReadinessBucket = "few"
	// ReadinessMany: 10 or more pending, or more markers than the job reads.
	ReadinessMany ReadinessBucket = "many"
	// ReadinessUnmeasured: nothing pending would mean nothing (ReadinessRow.Why).
	ReadinessUnmeasured ReadinessBucket = "unmeasured"
	// ReadinessLocked: Rudolph already intends LOCKDOWN or STANDALONE.
	ReadinessLocked ReadinessBucket = "locked"
)

// ReadinessBuckets lists the buckets in sort key order: Rudolph's readiness
// sort key starts with a bucket's position here as one digit.
var ReadinessBuckets = []ReadinessBucket{ReadinessReady, ReadinessFew, ReadinessMany, ReadinessUnmeasured, ReadinessLocked}

// SortDigit is the digit Rudolph's readiness sort key starts with for the
// bucket; ok is false for ReadinessAny and anything that is not a bucket.
func (b ReadinessBucket) SortDigit() (digit byte, ok bool) {
	i := slices.Index(ReadinessBuckets, b)
	if i < 0 {
		return 0, false
	}
	return byte('0' + i), true
}

// ReadinessCounts are the Macs of the fleet or of one group, in all and by
// bucket.
type ReadinessCounts struct {
	Macs       int
	Ready      int
	Few        int
	Many       int
	Unmeasured int
	Locked     int
}

// FleetSummary is the fleet job's pointer: the snapshot the lists are under
// and every count of that run. GeneratedAt is zero, and everything else
// empty, when the job has not run yet.
type FleetSummary struct {
	Snapshot    string
	GeneratedAt time.Time
	// Macs is every Mac the run walked, Attention those in at least one
	// health category.
	Macs      int
	Attention int
	Health    map[FleetCategory]int
	// VersionUnknown counts Macs whose Santa version is empty or cannot be
	// read; they are in no list.
	VersionUnknown int
	// MinSantaVersion is the release below which the run listed a Mac under
	// Version. It is the job's own setting, not the view's.
	MinSantaVersion string
	// StaleDays is the stale threshold the run used; Holiday whether that was
	// a holiday's, which lasts through HolidayUntil (a UTC date).
	StaleDays    int
	Holiday      bool
	HolidayUntil string
	// ReadinessAll counts the fleet; ReadinessGroups every registered group
	// and UngroupedGroup.
	ReadinessAll    ReadinessCounts
	ReadinessGroups map[string]ReadinessCounts
	// Capped counts Macs with more unknown markers than the job reads.
	Capped int
}

// FleetHealthRow is one Mac in a health list. Every row has the Mac and when
// it was last seen; the rest is the detail of the row's category, zero in
// the others.
type FleetHealthRow struct {
	MachineID string
	Hostname  string
	SerialNum string
	LastSeen  time.Time
	// LastCompleted is the Mac's last completed sync; zero when there is none.
	LastCompleted time.Time

	// Stale and Incomplete: no completed sync on record.
	Never bool
	// Incomplete.
	SyncsWithoutPostflight int
	// Mode: the mode the Mac reported, the one Rudolph intends, and what
	// decided that (temporary, own, group:<name>, global or default).
	Reported ClientMode
	Intended ClientMode
	Source   string
	// Rules: NotApplied or Outlier, with the hashes for an outlier.
	Reason         string
	RulesReceived  int
	RulesProcessed int
	Hash           string
	ExpectedHash   string
	// SIP: the flags in hex, such as 0x7f.
	Flags string
	// Version.
	SantaVersion string
}

// ReadinessRow is one Mac in a readiness list. Counted is false on a Locked
// row, whose markers the job does not read, so its four counts mean nothing.
// Pending counts leave out the unknown binaries a global rule covers now.
type ReadinessRow struct {
	MachineID string
	Hostname  string
	SerialNum string
	Bucket    ReadinessBucket
	Counted   bool
	Unknown30 int
	Pending30 int
	Unknown7  int
	Pending7  int
	// Capped: the Mac has more markers than the job reads, so the counts are
	// of the first 1,000 only.
	Capped       bool
	ReportedMode ClientMode
	IntendedMode ClientMode
	ObservedDays int
	// Why an Unmeasured Mac is: stale, incomplete, upload-off, no-rules or
	// observed.
	Why string
}

// FleetList is one page of a fleet job list, in the snapshot's order.
// Snapshot and GeneratedAt are those of the snapshot the page was read from;
// both are zero, and Items empty, when the job has not run yet. Every page
// of one listing comes from the same snapshot.
type FleetList[T any] struct {
	Items       []T
	NextCursor  string
	Snapshot    string
	GeneratedAt time.Time
}

// ReadinessFilter selects a readiness list: the fleet's (Group "") or a
// group's, and all of it (ReadinessAny) or one bucket.
type ReadinessFilter struct {
	Group  string
	Bucket ReadinessBucket
}

// Validate returns ErrInvalidArgument for a bucket that does not exist.
func (f ReadinessFilter) Validate() error {
	if _, ok := f.Bucket.SortDigit(); !ok && f.Bucket != ReadinessAny {
		return fmt.Errorf("%w: unknown readiness bucket %q", ErrInvalidArgument, f.Bucket)
	}
	return nil
}

// MaxUnknownCounted is the most markers MachineUnknown reads of one Mac.
const MaxUnknownCounted = 1000

// UnknownCounts is how many unknown binaries a Mac ran in the last 30 and 7
// days (UTC, today included), by upload date. More is set when the Mac has
// more markers than were read, so the counts are of the first
// MaxUnknownCounted only.
type UnknownCounts struct {
	Last30 int
	Last7  int
	More   bool
}

// UnknownBinary is one unknown binary a Mac ran: a marker of eventupload's.
// LastDay is the UTC date of the Mac's latest upload of an unknown execution
// of it.
type UnknownBinary struct {
	FileSHA256 string
	FileName   string
	SigningID  string
	TeamID     string
	LastDay    time.Time
}

// FleetHealthConfig is the stored stale threshold: the normal one in days,
// and the holiday's days and last day (a UTC date, inclusive), zero when no
// holiday is stored. A stored holiday that has lapsed is still returned. The
// fleet job uses the setting from its next run; FleetSummary says what the
// last run used.
type FleetHealthConfig struct {
	StaleDays        int
	HolidayStaleDays int
	HolidayUntil     string
}

// FleetReader reads the fleet job's snapshot and a Mac's unknown binaries.
type FleetReader interface {
	// FleetSummary returns the fleet job's pointer (one GetItem), or the zero
	// FleetSummary when the job has not run yet.
	FleetSummary(ctx context.Context) (FleetSummary, error)
	// FleetHealthConfig returns the stored stale threshold (one GetItem);
	// StaleDays is Rudolph's default when none is stored.
	FleetHealthConfig(ctx context.Context) (FleetHealthConfig, error)
	// ListFleetHealth returns a page of a health list of the latest
	// snapshot, or of the snapshot a cursor pinned. ErrInvalidArgument for a
	// category that does not exist; ErrInvalidCursor for a cursor whose
	// snapshot has expired (Rudolph deletes its rows 3 days after the run).
	ListFleetHealth(ctx context.Context, category FleetCategory, p PageRequest) (FleetList[FleetHealthRow], error)
	// ListReadiness returns a page of a readiness list the same way, fewest
	// pending first within a bucket. ErrNotFound for a group the snapshot
	// does not name; ErrInvalidArgument for a bucket that does not exist.
	ListReadiness(ctx context.Context, f ReadinessFilter, p PageRequest) (FleetList[ReadinessRow], error)
	// MachineUnknown counts a Mac's unknown binaries of the last 30 and 7
	// days with one bounded read. A Mac without markers has zero counts.
	MachineUnknown(ctx context.Context, machineID string) (UnknownCounts, error)
	// ListMachineUnknown returns a Mac's unknown binaries in SHA-256 order,
	// those outside the 30 days included: markers expire 35 days after the
	// upload, and a filter would leave pages short.
	ListMachineUnknown(ctx context.Context, machineID string, p PageRequest) (Page[UnknownBinary], error)
}

func (Unavailable) FleetSummary(context.Context) (FleetSummary, error) {
	return FleetSummary{}, ErrUnavailable
}

func (Unavailable) FleetHealthConfig(context.Context) (FleetHealthConfig, error) {
	return FleetHealthConfig{}, ErrUnavailable
}

func (Unavailable) ListFleetHealth(context.Context, FleetCategory, PageRequest) (FleetList[FleetHealthRow], error) {
	return FleetList[FleetHealthRow]{}, ErrUnavailable
}

func (Unavailable) ListReadiness(context.Context, ReadinessFilter, PageRequest) (FleetList[ReadinessRow], error) {
	return FleetList[ReadinessRow]{}, ErrUnavailable
}

func (Unavailable) MachineUnknown(context.Context, string) (UnknownCounts, error) {
	return UnknownCounts{}, ErrUnavailable
}

func (Unavailable) ListMachineUnknown(context.Context, string, PageRequest) (Page[UnknownBinary], error) {
	return Page[UnknownBinary]{}, ErrUnavailable
}

// ---------------------------------------------------------------------------
// Writes

// Holiday mode's audit entries, exactly as Rudolph's CLI records them.
const (
	ActionFleetHoliday = "fleet.holiday"
	AuditTargetFleet   = "fleet"
)

// ActionFleetRun is the audit action of a fleet job run started from the web
// view. Rudolph's CLI has no such command, so the entry is the web view's own.
const ActionFleetRun = "fleet.run"

// FleetRunInterval is how long RequestFleetRun refuses another run after the
// last request or the current snapshot. It is the fleet function's timeout:
// a run asked for takes at most that long to show or to fail, so nothing is
// gained by a second one meanwhile, and a button cannot be used to run the
// job, which walks every Mac, over and over.
const FleetRunInterval = 15 * time.Minute

// FleetRunTooSoonError is RequestFleetRun's refusal while the last request
// or the current snapshot is younger than FleetRunInterval; a run can be
// asked for again at RetryAt. It is not invalid input: the request was fine,
// only early.
type FleetRunTooSoonError struct {
	RetryAt time.Time
}

func (e *FleetRunTooSoonError) Error() string {
	return "store: the fleet job ran or was started too recently; it can be run again at " + e.RetryAt.UTC().Format(time.RFC3339)
}

// ErrFleetRunNotStarted is RequestFleetRun's error when the request was
// accepted and its marker written, but starting the job failed. It wraps the
// start's error.
var ErrFleetRunNotStarted = errors.New("store: the fleet job could not be started")

// ActionRankerRun and AuditTargetRanker are the audit entry of a ranker run
// started from the web view. Like fleet.run it is the web view's own:
// Rudolph's CLI starts no ranker.
const (
	ActionRankerRun   = "ranker.run"
	AuditTargetRanker = "ranker"
)

// RankerRunInterval is how long RequestRankerRun refuses another run after
// the last request or the current top lists. A run takes at most the ranker
// function's 5-minute timeout, so one asked for has shown or failed well
// within it; it is as long as FleetRunInterval so that both buttons say the
// same thing, and a button cannot be used to read 90 days of counters over
// and over.
const RankerRunInterval = 15 * time.Minute

// RankerRunTooSoonError is RequestRankerRun's refusal while the last ranker
// request or the current top lists are younger than RankerRunInterval; a run
// can be asked for again at RetryAt. It is a type of its own because the
// ranker's guard and the fleet job's are separate: neither refuses the other.
type RankerRunTooSoonError struct {
	RetryAt time.Time
}

func (e *RankerRunTooSoonError) Error() string {
	return "store: the ranker ran or was started too recently; it can be run again at " + e.RetryAt.UTC().Format(time.RFC3339)
}

// ErrRankerRunNotStarted is RequestRankerRun's error when the request was
// accepted and its marker written, but starting the ranker failed. It wraps
// the start's error.
var ErrRankerRunNotStarted = errors.New("store: the ranker could not be started")

// MaxCohortMacs is the most Macs AssignCohort takes in one request; bulk
// work is the CLI's. Each Mac costs a point read of its readiness row and an
// assignment of about five DynamoDB calls, one after another at roughly
// 10 ms each from the Lambda, so 50 Macs take about 3 seconds of the
// function's 10-second default timeout.
const MaxCohortMacs = 50

// MaxCohortSnapshotAge is how old a snapshot AssignCohort acts on may be,
// by the time in its ID. The fleet job runs once a day, so anything older is
// not the latest run's: the list was left open, and its Macs may have run
// unknown binaries since.
const MaxCohortSnapshotAge = 26 * time.Hour

// ErrSnapshotTooOld is AssignCohort's refusal of a snapshot older than
// MaxCohortSnapshotAge. It is not invalid input: the list was current when
// it was shown, and reloading it gives one that can be used.
var ErrSnapshotTooOld = errors.New("store: the readiness snapshot is too old")

// PartialCohortError is AssignCohort's error when it failed after assigning
// the group to Assigned of Total Macs. A retry is safe: assigning a group a
// Mac already has changes nothing.
type PartialCohortError struct {
	Assigned, Total int
	Err             error
}

func (e *PartialCohortError) Error() string {
	return fmt.Sprintf("the cohort failed part-way, after assigning %d of %d Macs: %v", e.Assigned, e.Total, e.Err)
}

func (e *PartialCohortError) Unwrap() error { return e.Err }

// snapshotTimeLayout is the time a snapshot ID starts with (Rudolph's
// internal/snapshot.ID), followed by a hyphen and eight hexadecimal digits.
const (
	snapshotTimeLayout = "20060102T150405.000000000Z"
	snapshotSuffixLen  = 9
)

// SnapshotTime returns when the snapshot named id was started, or false when
// id is not a snapshot ID of Rudolph's jobs. An ID holds nothing but digits,
// "T", ".", "Z", "-" and lower-case hexadecimal digits, so one that passes
// can be put in a partition key between "#" separators.
func SnapshotTime(id string) (time.Time, bool) {
	if len(id) != len(snapshotTimeLayout)+snapshotSuffixLen || id[len(snapshotTimeLayout)] != '-' {
		return time.Time{}, false
	}
	for _, c := range id[len(snapshotTimeLayout)+1:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return time.Time{}, false
		}
	}
	at, err := time.Parse(snapshotTimeLayout, id[:len(snapshotTimeLayout)])
	// time.Parse reads "1.5" seconds and the like leniently; only the exact
	// form the job writes names a snapshot.
	if err != nil || at.Format(snapshotTimeLayout) != id[:len(snapshotTimeLayout)] {
		return time.Time{}, false
	}
	return at.UTC(), true
}
