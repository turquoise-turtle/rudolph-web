package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// maxFilterLen bounds free-text filter values.
const maxFilterLen = 256

// topBlockedWindows are the selectable aggregation windows.
var topBlockedWindows = []string{"1d", "7d", "30d", "90d"}

const defaultTopBlockedWindow = "7d"

// pager holds the navigation links for a list.
type pager struct {
	Next  string // "" on the last page
	First string // "" when already on the first page
}

// pageRequest builds a bounded request; each handler issues exactly one.
func (s *server) pageRequest(q url.Values) store.PageRequest {
	return store.PageRequest{Limit: s.pageSize, Cursor: q.Get("cursor")}
}

// links builds next/first URLs that preserve the filter params.
func links(path string, filters url.Values, cursor, next string) pager {
	var p pager
	if next != "" {
		v := cloneValues(filters)
		v.Set("cursor", next)
		p.Next = path + "?" + v.Encode()
	}
	if cursor != "" {
		p.First = path
		if enc := cloneValues(filters).Encode(); enc != "" {
			p.First += "?" + enc
		}
	}
	return p
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		if len(vs) > 0 && vs[0] != "" {
			out.Set(k, vs[0])
		}
	}
	return out
}

// textFilter returns a trimmed query value, or ok=false if it is too long.
func textFilter(q url.Values, key string) (string, bool) {
	v := strings.TrimSpace(q.Get(key))
	return v, len(v) <= maxFilterLen
}

// ---------------------------------------------------------------------------
// Machines

func (s *server) machines(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	serial, ok1 := textFilter(q, "serial")
	hostname, ok2 := textFilter(q, "hostname")
	if !ok1 || !ok2 {
		s.renderError(w, r, http.StatusBadRequest, "Filter value is too long.")
		return
	}
	if serial != "" && hostname != "" {
		s.renderError(w, r, http.StatusBadRequest, "Filter by serial number or by hostname, not both.")
		return
	}
	filter := store.MachineFilter{SerialPrefix: serial, HostnamePrefix: hostname}
	pr := s.pageRequest(q)
	page, err := s.store.ListMachines(r.Context(), filter, pr)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "machines", "Machines", "machines", struct {
		Filter   store.MachineFilter
		Machines []store.Machine
		Pager    pager
	}{
		Filter:   filter,
		Machines: page.Items,
		Pager:    links("/machines", url.Values{"serial": {serial}, "hostname": {hostname}}, pr.Cursor, page.NextCursor),
	})
}

func (s *server) machineDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.renderError(w, r, http.StatusNotFound, "Machine not found.")
			return
		}
		s.storeError(w, r, err)
		return
	}
	pr := s.pageRequest(r.URL.Query())
	rules, err := s.store.ListMachineRules(r.Context(), id, pr)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	title := machine.Hostname
	if title == "" {
		title = machine.MachineID
	}
	s.render(w, r, http.StatusOK, "machine", "Machine "+title, "machines", struct {
		Machine store.MachineDetail
		Health  syncHealth
		Rules   []store.Rule
		Pager   pager
	}{
		Machine: machine,
		Health:  machineHealth(machine, s.now()),
		Rules:   rules.Items,
		Pager:   links("/machines/"+url.PathEscape(id), url.Values{}, pr.Cursor, rules.NextCursor),
	})
}

// ---------------------------------------------------------------------------
// Rules

func (s *server) rules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.RuleFilter{RuleType: store.RuleType(q.Get("type")), Policy: store.Policy(q.Get("policy"))}
	if filter.RuleType != "" && !slices.Contains(store.RuleTypes, filter.RuleType) {
		s.renderError(w, r, http.StatusBadRequest, "Unknown rule type.")
		return
	}
	if filter.Policy != "" && !slices.Contains(store.Policies, filter.Policy) {
		s.renderError(w, r, http.StatusBadRequest, "Unknown policy.")
		return
	}
	pr := s.pageRequest(q)
	page, err := s.store.ListGlobalRules(r.Context(), filter, pr)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "rules", "Global rules", "rules", struct {
		Filter    store.RuleFilter
		RuleTypes []store.RuleType
		Policies  []store.Policy
		Rules     []store.Rule
		Pager     pager
	}{
		Filter:    filter,
		RuleTypes: store.RuleTypes,
		Policies:  store.Policies,
		Rules:     page.Items,
		Pager: links("/rules", url.Values{"type": {string(filter.RuleType)}, "policy": {string(filter.Policy)}},
			pr.Cursor, page.NextCursor),
	})
}

// ---------------------------------------------------------------------------
// Events

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	machine, ok1 := textFilter(q, "machine")
	teamID, ok2 := textFilter(q, "team_id")
	if !ok1 || !ok2 {
		s.renderError(w, r, http.StatusBadRequest, "Filter value is too long.")
		return
	}
	filter := store.EventFilter{MachineID: machine, TeamID: teamID, Decision: store.Decision(q.Get("decision"))}
	// The events table has one key per filter, so filters cannot be combined.
	set := 0
	for _, v := range []string{machine, teamID, string(filter.Decision)} {
		if v != "" {
			set++
		}
	}
	if set > 1 {
		s.renderError(w, r, http.StatusBadRequest, "Filter by one field at a time: machine, decision or team ID.")
		return
	}
	if filter.Decision != "" && !slices.Contains(store.Decisions, filter.Decision) {
		s.renderError(w, r, http.StatusBadRequest, "Unknown decision.")
		return
	}
	pr := s.pageRequest(q)
	page, err := s.store.ListExecutionEvents(r.Context(), filter, pr)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "events", "Events", "events", struct {
		Filter    store.EventFilter
		Decisions []store.Decision
		Events    []store.ExecutionEvent
		Pager     pager
	}{
		Filter:    filter,
		Decisions: store.Decisions,
		Events:    page.Items,
		Pager: links("/events", url.Values{"machine": {machine}, "decision": {string(filter.Decision)}, "team_id": {teamID}},
			pr.Cursor, page.NextCursor),
	})
}

// topBlocked ranks blocked binaries in a window. The window start ("since")
// is carried in next links so later pages query exactly the same window as
// the first, even as the clock moves; cursors are bound to their query.
func (s *server) topBlocked(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	window := q.Get("window")
	if window == "" {
		window = defaultTopBlockedWindow
	}
	if !slices.Contains(topBlockedWindows, window) {
		s.renderError(w, r, http.StatusBadRequest, "Unknown time window.")
		return
	}
	days, _ := strconv.Atoi(strings.TrimSuffix(window, "d"))
	now := s.now().UTC().Truncate(time.Minute)
	since := now.AddDate(0, 0, -days)
	if raw := q.Get("since"); raw != "" && q.Get("cursor") != "" {
		unix, err := strconv.ParseInt(raw, 10, 64)
		t := time.Unix(unix, 0).UTC()
		if err != nil || t.After(now) || t.Before(now.AddDate(0, 0, -days-1)) {
			s.renderError(w, r, http.StatusBadRequest, "This page link is invalid or has expired. Start again from the first page.")
			return
		}
		since = t
	}
	pr := s.pageRequest(q)
	page, err := s.store.TopBlockedBinaries(r.Context(), store.TopBlockedFilter{Since: since}, pr)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	p := links("/events/top-blocked", url.Values{"window": {window}, "since": {strconv.FormatInt(since.Unix(), 10)}},
		pr.Cursor, page.NextCursor)
	if p.First != "" {
		p.First = "/events/top-blocked?" + url.Values{"window": {window}}.Encode()
	}
	s.render(w, r, http.StatusOK, "topblocked", "Top blocked binaries", "top-blocked", struct {
		Window   string
		Windows  []string
		Since    time.Time
		Binaries []store.BlockedBinary
		Pager    pager
	}{
		Window:   window,
		Windows:  topBlockedWindows,
		Since:    since,
		Binaries: page.Items,
		Pager:    p,
	})
}

// ---------------------------------------------------------------------------
// Admin

// admin is a placeholder for future write actions (Rudolph.Admin only).
func (s *server) admin(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "admin", "Admin", "admin", nil)
}
