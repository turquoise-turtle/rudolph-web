// Package fake is an in-memory store.Reader for tests and local development.
//
// It imitates the paging semantics of the future DynamoDB implementation:
// results are ordered by a key, a cursor is the last returned key, cursors are
// bound to the query (method, machine and filter) that produced them, and
// Limit is validated. Every call is recorded so tests can assert how many
// paginated queries a handler performed and with which limit.
package fake

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// Call is one recorded data-layer call.
type Call struct {
	Method    string
	MachineID string
	Filter    any // the filter struct passed, if any
	Page      store.PageRequest
}

// Paginated reports whether the call was a paginated list query (as opposed to
// a bounded point read such as GetMachine or ListGroups, one registry item).
func (c Call) Paginated() bool {
	return c.Method != "GetMachine" && c.Method != "ListGroups" && c.Method != "GlobalClientMode"
}

// Store is an in-memory store.Reader. The zero value is not usable; use New.
type Store struct {
	mu           sync.Mutex
	machines     map[string]store.Machine
	syncStates   map[string]store.SyncState
	configModes  map[string]store.ClientMode
	globalMode   store.ClientMode
	tempModes    map[string]tempMode
	cleanSyncs   map[string]bool
	globalRules  map[string]store.Rule
	machineRules map[string]map[string]store.Rule
	groups       map[string]store.Group
	groupRules   map[string]map[string]store.Rule
	machineGrps  map[string]machineGroups
	execEvents   []store.ExecutionEvent
	faaEvents    []store.FileAccessEvent
	topLists     map[store.TopFilter]topList
	calls        []Call
	err          error
	audit        []store.AuditEntry

	// cursors authenticates page cursors with a per-store key, as the real
	// implementation will with a key derived from the application secret.
	cursors *store.CursorCodec
}

var _ store.Reader = (*Store)(nil)

// New returns an empty store.
func New() *Store {
	codec, err := store.NewCursorCodec(store.RandomCursorKey())
	if err != nil {
		panic(err)
	}
	return &Store{
		cursors:      codec,
		globalMode:   store.ClientModeMonitor,
		machines:     map[string]store.Machine{},
		syncStates:   map[string]store.SyncState{},
		configModes:  map[string]store.ClientMode{},
		tempModes:    map[string]tempMode{},
		cleanSyncs:   map[string]bool{},
		globalRules:  map[string]store.Rule{},
		machineRules: map[string]map[string]store.Rule{},
		groups:       map[string]store.Group{},
		groupRules:   map[string]map[string]store.Rule{},
		machineGrps:  map[string]machineGroups{},
		topLists:     map[store.TopFilter]topList{},
	}
}

// AddMachine inserts or replaces a machine's sensor data.
func (s *Store) AddMachine(m store.Machine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.machines[m.MachineID] = m
}

// SetSyncState inserts or replaces a machine's sync state.
func (s *Store) SetSyncState(machineID string, st store.SyncState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncStates[machineID] = st
}

// SetConfiguredClientMode sets the client mode a machine's config sets ("" to
// follow the global config).
func (s *Store) SetConfiguredClientMode(machineID string, mode store.ClientMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configModes[machineID] = mode
}

// SetGlobalClientMode sets the global config's client mode. A new store has
// MONITOR, which Rudolph sends when there is no global config.
func (s *Store) SetGlobalClientMode(mode store.ClientMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.globalMode = mode
}

// GlobalClientMode implements store.MachineReader.
func (s *Store) GlobalClientMode(context.Context) (store.ClientMode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "GlobalClientMode"}); err != nil {
		return "", err
	}
	return s.globalMode, nil
}

type topList struct {
	generatedAt time.Time
	items       []store.TopBinary
}

type tempMode struct {
	mode  store.ClientMode
	until time.Time
}

// SetTemporaryClientMode sets a machine config's temporary client mode and
// when it ends ("" or a zero until removes it, as Rudolph ignores an expiry
// of 0). until may be past, like an ended mode Rudolph leaves on the row.
func (s *Store) SetTemporaryClientMode(machineID string, mode store.ClientMode, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mode == "" || until.IsZero() {
		delete(s.tempModes, machineID)
		return
	}
	s.tempModes[machineID] = tempMode{mode: mode, until: until}
}

// SetCleanSyncRequested sets a machine config's CleanSync flag.
func (s *Store) SetCleanSyncRequested(machineID string, requested bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanSyncs[machineID] = requested
}

// AddGlobalRule inserts or replaces a global rule (keyed by type+identifier).
func (s *Store) AddGlobalRule(r store.Rule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.globalRules[ruleKey(r)] = r
}

// AddMachineRule inserts or replaces a machine-specific rule.
func (s *Store) AddMachineRule(machineID string, r store.Rule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.machineRules[machineID] == nil {
		s.machineRules[machineID] = map[string]store.Rule{}
	}
	s.machineRules[machineID][ruleKey(r)] = r
}

type machineGroups struct {
	assigned, reported, reportedAsSent []string
}

// AddGroup inserts or replaces a registered group.
func (s *Store) AddGroup(g store.Group) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups[g.Name] = g
}

// AddGroupRule inserts or replaces a rule of a group.
func (s *Store) AddGroupRule(group string, r store.Rule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.groupRules[group] == nil {
		s.groupRules[group] = map[string]store.Rule{}
	}
	s.groupRules[group][ruleKey(r)] = r
}

// SetMachineGroups sets a Mac's assigned groups, its reported groups that
// count, and the groups Santa reported as sent.
func (s *Store) SetMachineGroups(machineID string, assigned, reported, reportedAsSent []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.machineGrps[machineID] = machineGroups{
		assigned:       slices.Sorted(slices.Values(assigned)),
		reported:       slices.Sorted(slices.Values(reported)),
		reportedAsSent: slices.Clone(reportedAsSent),
	}
}

// AddExecutionEvent appends an execution event.
func (s *Store) AddExecutionEvent(e store.ExecutionEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execEvents = append(s.execEvents, e)
}

// AddFileAccessEvent appends a file access event.
func (s *Store) AddFileAccessEvent(e store.FileAccessEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faaEvents = append(s.faaEvents, e)
}

// SetError makes every subsequent read fail with err (nil clears it).
func (s *Store) SetError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// Calls returns a copy of the recorded calls.
func (s *Store) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// ResetCalls forgets recorded calls.
func (s *Store) ResetCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

func (s *Store) record(c Call) error {
	s.calls = append(s.calls, c)
	return s.err
}

// ListMachines implements store.MachineReader.
func (s *Store) ListMachines(_ context.Context, f store.MachineFilter, p store.PageRequest) (store.Page[store.Machine], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "ListMachines", Filter: f, Page: p}); err != nil {
		return store.Page[store.Machine]{}, err
	}
	// Matches the DynamoDB reader: serials and hostnames compare without case,
	// a prefix that is only spaces is no filter, and a Mac is listed by serial
	// only if it has one and by hostname only if it has one.
	f = store.MachineFilter{
		SerialPrefix:   strings.ToUpper(strings.TrimSpace(f.SerialPrefix)),
		HostnamePrefix: strings.ToLower(strings.TrimSpace(f.HostnamePrefix)),
	}
	if f.SerialPrefix != "" && f.HostnamePrefix != "" {
		return store.Page[store.Machine]{}, fmt.Errorf("%w: filter by serial or hostname, not both", store.ErrInvalidArgument)
	}
	var keyed []keyedItem[store.Machine]
	for _, m := range s.machines {
		var k string
		switch {
		case f.HostnamePrefix == "":
			serialKey := strings.ToUpper(strings.TrimSpace(m.SerialNum))
			if serialKey == "" || !strings.HasPrefix(serialKey, f.SerialPrefix) {
				continue
			}
			k = serialKey + "\x00" + m.MachineID
		default:
			hostnameKey := strings.ToLower(strings.TrimSpace(m.Hostname))
			if hostnameKey == "" || !strings.HasPrefix(hostnameKey, f.HostnamePrefix) {
				continue
			}
			k = hostnameKey + "\x00" + m.MachineID
		}
		keyed = append(keyed, keyedItem[store.Machine]{key: k, item: m})
	}
	return paginate(s.cursors, keyed, fingerprint("ListMachines", "", f), p)
}

// GetMachine implements store.MachineReader.
func (s *Store) GetMachine(_ context.Context, machineID string) (store.MachineDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "GetMachine", MachineID: machineID}); err != nil {
		return store.MachineDetail{}, err
	}
	m, ok := s.machines[machineID]
	if !ok {
		return store.MachineDetail{}, store.ErrNotFound
	}
	tm := s.tempModes[machineID]
	d := store.MachineDetail{
		Machine:                  m,
		ConfiguredClientMode:     s.configModes[machineID],
		TemporaryClientMode:      tm.mode,
		TemporaryClientModeUntil: tm.until,
		CleanSyncRequested:       s.cleanSyncs[machineID],
	}
	if g, ok := s.machineGrps[machineID]; ok {
		d.AssignedGroups = slices.Clone(g.assigned)
		d.ReportedGroups = slices.Clone(g.reported)
		d.ReportedAsSent = slices.Clone(g.reportedAsSent)
	}
	if st, ok := s.syncStates[machineID]; ok {
		d.Sync = &st
	}
	return d, nil
}

// ListMachineRules implements store.MachineReader.
func (s *Store) ListMachineRules(_ context.Context, machineID string, p store.PageRequest) (store.Page[store.Rule], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "ListMachineRules", MachineID: machineID, Page: p}); err != nil {
		return store.Page[store.Rule]{}, err
	}
	var keyed []keyedItem[store.Rule]
	for k, r := range s.machineRules[machineID] {
		keyed = append(keyed, keyedItem[store.Rule]{key: k, item: r})
	}
	return paginate(s.cursors, keyed, fingerprint("ListMachineRules", machineID, nil), p)
}

// ListGlobalRules implements store.RuleReader.
func (s *Store) ListGlobalRules(_ context.Context, f store.RuleFilter, p store.PageRequest) (store.Page[store.Rule], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "ListGlobalRules", Filter: f, Page: p}); err != nil {
		return store.Page[store.Rule]{}, err
	}
	var keyed []keyedItem[store.Rule]
	for k, r := range s.globalRules {
		if (f.RuleType != "" && r.RuleType != f.RuleType) || (f.Policy != "" && r.Policy != f.Policy) {
			continue
		}
		keyed = append(keyed, keyedItem[store.Rule]{key: k, item: r})
	}
	return paginate(s.cursors, keyed, fingerprint("ListGlobalRules", "", f), p)
}

// ListGroups implements store.GroupReader.
func (s *Store) ListGroups(context.Context) ([]store.Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "ListGroups"}); err != nil {
		return nil, err
	}
	groups := make([]store.Group, 0, len(s.groups))
	for _, g := range s.groups {
		groups = append(groups, g)
	}
	slices.SortFunc(groups, func(a, b store.Group) int { return strings.Compare(a.Name, b.Name) })
	return groups, nil
}

// ListGroupRules implements store.GroupReader.
func (s *Store) ListGroupRules(_ context.Context, group string, p store.PageRequest) (store.Page[store.Rule], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "ListGroupRules", Filter: group, Page: p}); err != nil {
		return store.Page[store.Rule]{}, err
	}
	var keyed []keyedItem[store.Rule]
	for k, r := range s.groupRules[group] {
		keyed = append(keyed, keyedItem[store.Rule]{key: k, item: r})
	}
	return paginate(s.cursors, keyed, fingerprint("ListGroupRules", "", group), p)
}

// ListExecutionEvents implements store.EventReader. Like the DynamoDB reader,
// which has one key per filter, it refuses more than one filter at once.
func (s *Store) ListExecutionEvents(_ context.Context, f store.EventFilter, p store.PageRequest) (store.Page[store.ExecutionEvent], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "ListExecutionEvents", Filter: f, Page: p}); err != nil {
		return store.Page[store.ExecutionEvent]{}, err
	}
	set := 0
	for _, v := range []string{f.MachineID, string(f.Decision), f.TeamID} {
		if v != "" {
			set++
		}
	}
	if set > 1 {
		return store.Page[store.ExecutionEvent]{}, fmt.Errorf("%w: filter events by machine, decision or team ID, one at a time", store.ErrInvalidArgument)
	}
	var keyed []keyedItem[store.ExecutionEvent]
	for i, e := range s.execEvents {
		// Machine and team IDs match whatever their case: the web view upper-cases these filters, as Santa reports
		// them, while test fixtures may hold short lower-case IDs.
		if (f.MachineID != "" && !strings.EqualFold(e.MachineID, f.MachineID)) ||
			(f.Decision != "" && e.Decision != f.Decision) ||
			(f.TeamID != "" && !strings.EqualFold(e.TeamID, f.TeamID)) {
			continue
		}
		k := fmt.Sprintf("%020d|%08d", uint64(math.MaxInt64-e.ExecutionTime.UnixNano()), i)
		keyed = append(keyed, keyedItem[store.ExecutionEvent]{key: k, item: e})
	}
	return paginate(s.cursors, keyed, fingerprint("ListExecutionEvents", "", f), p)
}

// SetTopList stores the list Rudolph's ranker would have computed for a kind
// and window, in rank order, replacing any earlier one.
func (s *Store) SetTopList(kind store.TopKind, windowDays int, generatedAt time.Time, items []store.TopBinary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.topLists[store.TopFilter{Kind: kind, WindowDays: windowDays}] = topList{
		generatedAt: generatedAt,
		items:       append([]store.TopBinary(nil), items...),
	}
}

// TopBinaries implements store.EventReader, serving the list set with
// SetTopList in its stored order. A list never set has not been computed:
// empty, with a zero GeneratedAt.
func (s *Store) TopBinaries(_ context.Context, f store.TopFilter, p store.PageRequest) (store.TopList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "TopBinaries", Filter: f, Page: p}); err != nil {
		return store.TopList{}, err
	}
	if err := f.Validate(); err != nil {
		return store.TopList{}, err
	}
	l := s.topLists[f]
	keyed := make([]keyedItem[store.TopBinary], 0, len(l.items))
	for i, b := range l.items {
		keyed = append(keyed, keyedItem[store.TopBinary]{key: fmt.Sprintf("%06d", i+1), item: b})
	}
	page, err := paginate(s.cursors, keyed, fingerprint("TopBinaries", "", f), p)
	if err != nil {
		return store.TopList{}, err
	}
	// paginate never hands out a cursor to an empty page, so an empty page here
	// means the list was replaced by a shorter one: the fake's stand-in for the
	// cursor's snapshot having expired.
	if p.Cursor != "" && len(page.Items) == 0 {
		return store.TopList{}, store.ErrInvalidCursor
	}
	return store.TopList{Items: page.Items, NextCursor: page.NextCursor, GeneratedAt: l.generatedAt}, nil
}

// ListFileAccessEvents implements store.EventReader.
func (s *Store) ListFileAccessEvents(_ context.Context, f store.FileAccessEventFilter, p store.PageRequest) (store.Page[store.FileAccessEvent], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "ListFileAccessEvents", Filter: f, Page: p}); err != nil {
		return store.Page[store.FileAccessEvent]{}, err
	}
	var keyed []keyedItem[store.FileAccessEvent]
	for i, e := range s.faaEvents {
		if (f.MachineID != "" && !strings.EqualFold(e.MachineID, f.MachineID)) || (f.Decision != "" && e.Decision != f.Decision) {
			continue
		}
		k := fmt.Sprintf("%020d|%08d", uint64(math.MaxInt64-e.AccessTime.UnixNano()), i)
		keyed = append(keyed, keyedItem[store.FileAccessEvent]{key: k, item: e})
	}
	return paginate(s.cursors, keyed, fingerprint("ListFileAccessEvents", "", f), p)
}

func ruleKey(r store.Rule) string {
	return string(r.RuleType) + "#" + r.Identifier
}

type keyedItem[T any] struct {
	key  string
	item T
}

func fingerprint(method, machineID string, filter any) string {
	return fmt.Sprintf("%s|%s|%#v", method, machineID, filter)
}

func paginate[T any](codec *store.CursorCodec, items []keyedItem[T], query string, p store.PageRequest) (store.Page[T], error) {
	if p.Limit < 1 || p.Limit > store.MaxPageSize {
		return store.Page[T]{}, fmt.Errorf("%w: limit %d outside 1..%d", store.ErrInvalidArgument, p.Limit, store.MaxPageSize)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })

	start := 0
	if p.Cursor != "" {
		after, err := codec.Decode(query, p.Cursor)
		if err != nil {
			return store.Page[T]{}, err
		}
		start = sort.Search(len(items), func(i int) bool { return items[i].key > after })
	}

	end := min(start+p.Limit, len(items))
	page := store.Page[T]{Items: make([]T, 0, end-start)}
	for _, it := range items[start:end] {
		page.Items = append(page.Items, it.item)
	}
	if end < len(items) {
		cursor, err := codec.Encode(query, items[end-1].key)
		if err != nil {
			return store.Page[T]{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}
