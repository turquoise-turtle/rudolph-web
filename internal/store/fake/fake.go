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
	"sort"
	"strings"
	"sync"

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
// a bounded point read such as GetMachine).
func (c Call) Paginated() bool {
	return c.Method != "GetMachine"
}

// Store is an in-memory store.Reader. The zero value is not usable; use New.
type Store struct {
	mu           sync.Mutex
	machines     map[string]store.Machine
	syncStates   map[string]store.SyncState
	configModes  map[string]store.ClientMode
	globalRules  map[string]store.Rule
	machineRules map[string]map[string]store.Rule
	execEvents   []store.ExecutionEvent
	faaEvents    []store.FileAccessEvent
	calls        []Call
	err          error

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
		machines:     map[string]store.Machine{},
		syncStates:   map[string]store.SyncState{},
		configModes:  map[string]store.ClientMode{},
		globalRules:  map[string]store.Rule{},
		machineRules: map[string]map[string]store.Rule{},
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
	// Matches the DynamoDB reader: serials compare upper case, hostnames
	// without case, and a prefix that is only spaces is no filter.
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
		case f.SerialPrefix != "":
			if m.SerialNum == "" || !strings.HasPrefix(m.SerialNum, f.SerialPrefix) {
				continue
			}
			k = m.SerialNum + "\x00" + m.MachineID
		case f.HostnamePrefix != "":
			hostnameKey := strings.ToLower(strings.TrimSpace(m.Hostname))
			if hostnameKey == "" || !strings.HasPrefix(hostnameKey, f.HostnamePrefix) {
				continue
			}
			k = hostnameKey + "\x00" + m.MachineID
		default:
			// Ordered by serial like the DynamoDB reader. Unlike its serial
			// index, the fake keeps Macs without a serial, first.
			k = m.SerialNum + "\x00" + m.MachineID
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
	d := store.MachineDetail{Machine: m, ConfiguredClientMode: s.configModes[machineID]}
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

// ListExecutionEvents implements store.EventReader.
func (s *Store) ListExecutionEvents(_ context.Context, f store.EventFilter, p store.PageRequest) (store.Page[store.ExecutionEvent], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "ListExecutionEvents", Filter: f, Page: p}); err != nil {
		return store.Page[store.ExecutionEvent]{}, err
	}
	var keyed []keyedItem[store.ExecutionEvent]
	for i, e := range s.execEvents {
		if (f.MachineID != "" && e.MachineID != f.MachineID) ||
			(f.Decision != "" && e.Decision != f.Decision) ||
			(f.TeamID != "" && e.TeamID != f.TeamID) {
			continue
		}
		k := fmt.Sprintf("%020d|%08d", uint64(math.MaxInt64-e.ExecutionTime.UnixNano()), i)
		keyed = append(keyed, keyedItem[store.ExecutionEvent]{key: k, item: e})
	}
	return paginate(s.cursors, keyed, fingerprint("ListExecutionEvents", "", f), p)
}

// TopBlockedBinaries implements store.EventReader.
func (s *Store) TopBlockedBinaries(_ context.Context, f store.TopBlockedFilter, p store.PageRequest) (store.Page[store.BlockedBinary], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "TopBlockedBinaries", Filter: f, Page: p}); err != nil {
		return store.Page[store.BlockedBinary]{}, err
	}
	agg := map[string]*store.BlockedBinary{}
	machines := map[string]map[string]bool{}
	for _, e := range s.execEvents {
		if !e.Decision.IsBlock() || e.ExecutionTime.Before(f.Since) {
			continue
		}
		b := agg[e.FileSHA256]
		if b == nil {
			b = &store.BlockedBinary{FileSHA256: e.FileSHA256}
			agg[e.FileSHA256] = b
			machines[e.FileSHA256] = map[string]bool{}
		}
		b.BlockCount++
		machines[e.FileSHA256][e.MachineID] = true
		b.MachineCount = len(machines[e.FileSHA256])
		if e.ExecutionTime.After(b.LastBlocked) {
			b.LastBlocked = e.ExecutionTime
			b.FileName, b.TeamID, b.SigningID = e.FileName, e.TeamID, e.SigningID
		}
	}
	var keyed []keyedItem[store.BlockedBinary]
	for sha, b := range agg {
		k := fmt.Sprintf("%010d|%s", math.MaxInt32-b.BlockCount, sha)
		keyed = append(keyed, keyedItem[store.BlockedBinary]{key: k, item: *b})
	}
	return paginate(s.cursors, keyed, fingerprint("TopBlockedBinaries", "", f.Since.UTC().Format("2006-01-02T15:04:05.999999999Z")), p)
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
		if (f.MachineID != "" && e.MachineID != f.MachineID) || (f.Decision != "" && e.Decision != f.Decision) {
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
