package fake

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// FleetSnapshot is one run of Rudolph's fleet job: the pointer's counts and
// the lists. Health rows are served in the order given; Readiness[""] is the
// fleet's list and the other keys are groups', each served sorted as
// Rudolph's sort key sorts them (bucket, Pending30, machine ID).
type FleetSnapshot struct {
	Summary   store.FleetSummary
	Health    map[store.FleetCategory][]store.FleetHealthRow
	Readiness map[string][]store.ReadinessRow
}

// fleetSnapshot is a snapshot the store holds. expired stands for its rows
// having been deleted by DynamoDB's TTL.
type fleetSnapshot struct {
	FleetSnapshot
	expired bool
}

// SetFleetSnapshot keeps the snapshot under its Summary.Snapshot and points
// the pointer at it. Earlier snapshots stay readable through their cursors,
// as their rows do in DynamoDB until they expire.
func (s *Store) SetFleetSnapshot(snap FleetSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fleetSnapshots[snap.Summary.Snapshot] = &fleetSnapshot{FleetSnapshot: snap}
	s.fleetPointer = snap.Summary.Snapshot
}

// ExpireFleetSnapshot drops the rows of a snapshot, as DynamoDB's TTL does 3
// days after the run. The pointer is left as it is.
func (s *Store) ExpireFleetSnapshot(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap, ok := s.fleetSnapshots[id]; ok {
		snap.expired = true
	}
}

// SetFleetHealthConfig replaces the stored stale threshold and holiday. A
// zero StaleDays is no threshold stored, read back as Rudolph's default.
func (s *Store) SetFleetHealthConfig(c store.FleetHealthConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fleetConfig = c
}

// SetMachineUnknown sets what MachineUnknown answers for a Mac. The counts
// are not worked out from AddUnknownBinary's rows, since the fake has no
// clock to place them in a window.
func (s *Store) SetMachineUnknown(machineID string, c store.UnknownCounts) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unknownCounts[machineID] = c
}

// AddUnknownBinary adds a row to a Mac's ListMachineUnknown, which serves
// them in SHA-256 order.
func (s *Store) AddUnknownBinary(machineID string, b store.UnknownBinary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unknownBinaries[machineID] = append(s.unknownBinaries[machineID], b)
}

// FleetSummary implements store.FleetReader.
func (s *Store) FleetSummary(context.Context) (store.FleetSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "FleetSummary"}); err != nil {
		return store.FleetSummary{}, err
	}
	snap, ok := s.fleetSnapshots[s.fleetPointer]
	if !ok {
		return store.FleetSummary{}, nil
	}
	return snap.Summary, nil
}

// FleetHealthConfig implements store.FleetReader.
func (s *Store) FleetHealthConfig(context.Context) (store.FleetHealthConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "FleetHealthConfig"}); err != nil {
		return store.FleetHealthConfig{}, err
	}
	return s.fleetHealthConfig(), nil
}

// fleetHealthConfig is the stored setting with Rudolph's default threshold
// when none is stored. The caller holds s.mu.
func (s *Store) fleetHealthConfig() store.FleetHealthConfig {
	c := s.fleetConfig
	if c.StaleDays == 0 {
		c.StaleDays = rudolphrules.DefaultStaleDays
	}
	return c
}

// ListFleetHealth implements store.FleetReader.
func (s *Store) ListFleetHealth(_ context.Context, category store.FleetCategory, p store.PageRequest) (store.FleetList[store.FleetHealthRow], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "ListFleetHealth", Filter: category, Page: p}); err != nil {
		return store.FleetList[store.FleetHealthRow]{}, err
	}
	if !slices.Contains(store.FleetCategories, category) {
		return store.FleetList[store.FleetHealthRow]{}, fmt.Errorf("%w: unknown fleet health category %q", store.ErrInvalidArgument, category)
	}
	return fleetPage(s, fingerprint("ListFleetHealth", "", category), p, func(snap *fleetSnapshot) ([]keyedItem[store.FleetHealthRow], error) {
		rows := snap.Health[category]
		keyed := make([]keyedItem[store.FleetHealthRow], 0, len(rows))
		for i, r := range rows {
			keyed = append(keyed, keyedItem[store.FleetHealthRow]{key: fmt.Sprintf("%08d", i), item: r})
		}
		return keyed, nil
	})
}

// ListReadiness implements store.FleetReader.
func (s *Store) ListReadiness(_ context.Context, f store.ReadinessFilter, p store.PageRequest) (store.FleetList[store.ReadinessRow], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "ListReadiness", Filter: f, Page: p}); err != nil {
		return store.FleetList[store.ReadinessRow]{}, err
	}
	if err := f.Validate(); err != nil {
		return store.FleetList[store.ReadinessRow]{}, err
	}
	return fleetPage(s, fingerprint("ListReadiness", "", f), p, func(snap *fleetSnapshot) ([]keyedItem[store.ReadinessRow], error) {
		rows, listed := snap.Readiness[f.Group]
		if _, counted := snap.Summary.ReadinessGroups[f.Group]; f.Group != "" && !listed && !counted {
			return nil, fmt.Errorf("%w: the snapshot has no readiness list for group %q", store.ErrNotFound, f.Group)
		}
		var keyed []keyedItem[store.ReadinessRow]
		for _, r := range rows {
			if f.Bucket != store.ReadinessAny && r.Bucket != f.Bucket {
				continue
			}
			keyed = append(keyed, keyedItem[store.ReadinessRow]{key: readinessKey(r), item: r})
		}
		return keyed, nil
	})
}

// readinessKey is a readiness row's sort key as Rudolph writes it.
func readinessKey(r store.ReadinessRow) string {
	digit, _ := r.Bucket.SortDigit()
	return fmt.Sprintf("%c#%06d#%s", digit, r.Pending30, r.MachineID)
}

// fleetPage serves one page of a fleet list as the DynamoDB store does: the
// first page reads the snapshot the pointer names, a later one the snapshot
// its cursor pins, whose position is "<snapshot>\x00<key>". No pointer is an
// empty list; a pinned snapshot that has expired is ErrInvalidCursor. The
// caller holds s.mu.
func fleetPage[T any](s *Store, query string, p store.PageRequest, rows func(*fleetSnapshot) ([]keyedItem[T], error)) (store.FleetList[T], error) {
	var none store.FleetList[T]
	if p.Limit < 1 || p.Limit > store.MaxPageSize {
		return none, fmt.Errorf("%w: limit %d outside 1..%d", store.ErrInvalidArgument, p.Limit, store.MaxPageSize)
	}
	id, after := s.fleetPointer, ""
	if p.Cursor != "" {
		position, err := s.cursors.Decode(query, p.Cursor)
		if err != nil {
			return none, err
		}
		var ok bool
		if id, after, ok = strings.Cut(position, "\x00"); !ok {
			return none, store.ErrInvalidCursor
		}
	}
	snap, ok := s.fleetSnapshots[id]
	switch {
	case p.Cursor != "" && (!ok || snap.expired):
		return none, store.ErrInvalidCursor
	case !ok:
		return none, nil
	}
	items, err := rows(snap)
	if err != nil {
		return none, err
	}
	if snap.expired {
		// The pointer outlives its rows when the job stops running.
		items = nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })
	start := 0
	if p.Cursor != "" {
		start = sort.Search(len(items), func(i int) bool { return items[i].key > after })
	}
	end := min(start+p.Limit, len(items))
	list := store.FleetList[T]{Items: make([]T, 0, end-start), Snapshot: id, GeneratedAt: snap.Summary.GeneratedAt}
	for _, it := range items[start:end] {
		list.Items = append(list.Items, it.item)
	}
	if end < len(items) {
		if list.NextCursor, err = s.cursors.Encode(query, id+"\x00"+items[end-1].key); err != nil {
			return none, err
		}
	}
	return list, nil
}

// MachineUnknown implements store.FleetReader.
func (s *Store) MachineUnknown(_ context.Context, machineID string) (store.UnknownCounts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "MachineUnknown", MachineID: machineID}); err != nil {
		return store.UnknownCounts{}, err
	}
	return s.unknownCounts[machineID], nil
}

// ListMachineUnknown implements store.FleetReader.
func (s *Store) ListMachineUnknown(_ context.Context, machineID string, p store.PageRequest) (store.Page[store.UnknownBinary], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "ListMachineUnknown", MachineID: machineID, Page: p}); err != nil {
		return store.Page[store.UnknownBinary]{}, err
	}
	rows := s.unknownBinaries[machineID]
	keyed := make([]keyedItem[store.UnknownBinary], 0, len(rows))
	for _, b := range rows {
		keyed = append(keyed, keyedItem[store.UnknownBinary]{key: b.FileSHA256, item: b})
	}
	return paginate(s.cursors, keyed, fingerprint("ListMachineUnknown", machineID, nil), p)
}

// ---------------------------------------------------------------------------
// Writes. Unlike the fake's other writes these check against the real clock,
// as the DynamoDB writer does: a holiday's last day and a snapshot's age mean
// nothing without one.

// SetFleetHoliday implements store.Writer.
func (s *Store) SetFleetHoliday(_ context.Context, actor store.Actor, staleDays int, until string) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if err := rudolphrules.CheckHoliday(s.fleetConfig.StaleDays, staleDays, until, time.Now()); err != nil {
		return err
	}
	s.fleetConfig.HolidayStaleDays, s.fleetConfig.HolidayUntil = staleDays, until
	s.recordAudit(who, store.ActionFleetHoliday, store.AuditTargetFleet, rudolphrules.SetHolidaySummary(staleDays, until, s.fleetHealthConfig().StaleDays))
	return nil
}

// EndFleetHoliday implements store.Writer.
func (s *Store) EndFleetHoliday(_ context.Context, actor store.Actor) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	ended := s.fleetConfig
	if ended.HolidayStaleDays == 0 && ended.HolidayUntil == "" {
		return store.ErrNothingToChange
	}
	s.fleetConfig.HolidayStaleDays, s.fleetConfig.HolidayUntil = 0, ""
	s.recordAudit(who, store.ActionFleetHoliday, store.AuditTargetFleet,
		rudolphrules.EndHolidaySummary(ended.HolidayStaleDays, ended.HolidayUntil, s.fleetHealthConfig().StaleDays))
	return nil
}

// AssignCohort implements store.Writer. A snapshot the fake does not hold, or
// one that has expired, has no Mac ready in it.
func (s *Store) AssignCohort(_ context.Context, actor store.Actor, snapshot, group string, machineIDs []string) (int, error) {
	cohort, err := rudolphrules.CheckCohort(actor, snapshot, group, machineIDs, time.Now())
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	g, ok := s.groups[cohort.Group]
	if !ok {
		return 0, store.ErrNotFound
	}
	if !rudolphrules.IsLockdownMode(g.ClientMode) {
		return 0, rudolphrules.NotALockdownGroup(cohort.Group)
	}
	var ready []store.ReadinessRow
	if snap, ok := s.fleetSnapshots[snapshot]; ok && !snap.expired {
		ready = snap.Readiness[""]
	}
	for _, id := range cohort.MachineIDs {
		if !slices.ContainsFunc(ready, func(r store.ReadinessRow) bool {
			return r.MachineID == id && r.Bucket == store.ReadinessReady
		}) {
			return 0, rudolphrules.NotReady(id)
		}
	}
	for _, id := range cohort.MachineIDs {
		mg := s.machineGrps[id]
		if !slices.Contains(mg.assigned, cohort.Group) {
			mg.assigned = slices.Sorted(slices.Values(append(slices.Clone(mg.assigned), cohort.Group)))
		}
		s.machineGrps[id] = mg
		s.recordAudit(cohort.Actor, store.ActionGroupAdd, store.MachineAuditTarget(id), rudolphrules.AssignGroupSummary(cohort.Group))
	}
	return len(cohort.MachineIDs), nil
}

// RequestFleetRun implements store.Writer. Unlike the writes above it takes
// its clock from the caller, as the DynamoDB store does.
func (s *Store) RequestFleetRun(ctx context.Context, actor store.Actor, now time.Time, start func(context.Context) error) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	if err := s.markFleetRun(now); err != nil {
		return err
	}
	// The lock is not held here: a start that takes long must not hold up
	// the requests it makes the store refuse.
	if err := start(ctx); err != nil {
		return fmt.Errorf("%w: %w", store.ErrFleetRunNotStarted, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordAudit(who, store.ActionFleetRun, store.AuditTargetFleet, rudolphrules.FleetRunSummary)
	return nil
}

// markFleetRun decides a request under the lock, as the DynamoDB store's
// conditional write does: it is refused, or the marker moves to now.
func (s *Store) markFleetRun(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	var generatedAt time.Time
	if snap, ok := s.fleetSnapshots[s.fleetPointer]; ok {
		generatedAt = snap.Summary.GeneratedAt
	}
	if retryAt, soon := store.FleetRunRetryAt(now, s.fleetRunRequested, generatedAt); soon {
		return &store.FleetRunTooSoonError{RetryAt: retryAt}
	}
	s.fleetRunRequested = now
	return nil
}
