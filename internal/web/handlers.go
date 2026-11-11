package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// maxFilterLen bounds free-text filter values.
const maxFilterLen = 256

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
	pr, nav, ok := s.pageRequest(w, r, q)
	if !ok {
		return
	}
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
		Pager:    links("/machines", url.Values{"serial": {serial}, "hostname": {hostname}}, nav, page.NextCursor),
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
	pr, nav, ok := s.pageRequest(w, r, r.URL.Query())
	if !ok {
		return
	}
	rules, err := s.store.ListMachineRules(r.Context(), id, pr)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	// The global config only fills in what the Mac's own config leaves out,
	// so the page is shown without it rather than failing.
	global, err := s.store.GlobalConfig(r.Context())
	globalUnread := err != nil
	if globalUnread {
		s.log.Warn("reading the global config failed", "err", err)
		global = store.Config{}
	}
	globalMode := global.ClientMode
	title := machine.Hostname
	if title == "" {
		title = machine.MachineID
	}
	edit := s.editForms(r)
	var groupForms machineGroupForms
	if edit.On {
		groupForms = s.machineGroupForms(r, machine)
	}
	s.render(w, r, http.StatusOK, "machine", "Machine "+title, "machines", struct {
		Machine      store.MachineDetail
		GlobalMode   store.ClientMode
		Config       []configRow
		GlobalUnread bool
		Health       syncHealth
		Rules        []store.Rule
		Pager        pager
		Edit         editForms
		Groups       machineGroupForms
	}{
		Machine:      machine,
		GlobalMode:   globalMode,
		Config:       machineConfigRows(machine.Config, global),
		GlobalUnread: globalUnread,
		Health:       machineHealth(machine, s.now(), globalMode),
		Rules:        rules.Items,
		Pager:        links("/machines/"+url.PathEscape(id), url.Values{}, nav, rules.NextCursor),
		Edit:         edit,
		Groups:       groupForms,
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
	pr, nav, ok := s.pageRequest(w, r, q)
	if !ok {
		return
	}
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
		Edit      editForms
	}{
		Filter:    filter,
		RuleTypes: store.RuleTypes,
		Policies:  store.Policies,
		Rules:     page.Items,
		Edit:      s.editForms(r),
		Pager: links("/rules", url.Values{"type": {string(filter.RuleType)}, "policy": {string(filter.Policy)}},
			nav, page.NextCursor),
	})
}

// ---------------------------------------------------------------------------
// Groups

// groupRow is one group in the groups list. Reported is "yes" or "no", or ""
// for the reserved ungrouped group, which has no members of its own.
type groupRow struct {
	store.Group
	Reported string
}

func (s *server) groups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.ListGroups(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	rows := make([]groupRow, 0, len(groups)+1)
	for _, g := range groups {
		if g.Name == store.UngroupedGroup {
			continue
		}
		reported := "no"
		if g.AcceptReported {
			reported = "yes"
		}
		rows = append(rows, groupRow{Group: g, Reported: reported})
	}
	// Rudolph never registers the ungrouped group, but it can have rules, so
	// it is always listed, last.
	rows = append(rows, groupRow{Group: store.Group{Name: store.UngroupedGroup}})
	s.render(w, r, http.StatusOK, "groups", "Groups", "groups", struct {
		Groups    []groupRow
		Edit      editForms
		MaxGroups int
	}{rows, s.editForms(r), rudolphrules.MaxGroups})
}

func (s *server) groupDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	group := store.Group{Name: name}
	if name != store.UngroupedGroup {
		groups, err := s.store.ListGroups(r.Context())
		if err != nil {
			s.storeError(w, r, err)
			return
		}
		i := slices.IndexFunc(groups, func(g store.Group) bool { return g.Name == name })
		if i < 0 {
			s.renderError(w, r, http.StatusNotFound, "Group not found.")
			return
		}
		group = groups[i]
	}
	pr, nav, ok := s.pageRequest(w, r, r.URL.Query())
	if !ok {
		return
	}
	page, err := s.store.ListGroupRules(r.Context(), name, pr)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "group", "Group "+groupLabel(name), "groups", struct {
		Group store.Group
		Rules []store.Rule
		Pager pager
		Edit  editForms
	}{
		Group: group,
		Rules: page.Items,
		Pager: links(groupURL(name), url.Values{}, nav, page.NextCursor),
		Edit:  s.editForms(r),
	})
}

// groupLabel is how a group is named on the page.
func groupLabel(name string) string {
	if name == store.UngroupedGroup {
		return "Macs in no group"
	}
	return name
}

func groupURL(name string) string { return "/groups/" + url.PathEscape(name) }

// ---------------------------------------------------------------------------
// Events

// storedMachineID returns the spelling a Mac is stored under, trying id as typed, then upper case, then lower case,
// each an exact point read. It returns id unchanged when no Mac matches, so events of a Mac with no machine row can
// still be found.
func (s *server) storedMachineID(ctx context.Context, id string) (string, error) {
	tried := map[string]bool{}
	for _, candidate := range []string{id, strings.ToUpper(id), strings.ToLower(id)} {
		if tried[candidate] {
			continue
		}
		tried[candidate] = true
		_, err := s.store.GetMachine(ctx, candidate)
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}
	return id, nil
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	machine, ok1 := textFilter(q, "machine")
	teamID, ok2 := textFilter(q, "team_id")
	if !ok1 || !ok2 {
		s.renderError(w, r, http.StatusBadRequest, "Filter value is too long.")
		return
	}
	// The events table's keys are exact, so a value typed in the wrong case would find nothing. Team IDs are always
	// upper case. Machine IDs are stored as Santa sends them, by default the upper-case hardware UUID but any case when
	// a profile sets one, so the machine filter takes the spelling the Mac is stored under rather than upper-casing.
	teamID = strings.ToUpper(teamID)
	// Later pages need no lookup: their links carry the spelling page 1 found, and the cursor is bound to it.
	if machine != "" && q.Get("cursor") == "" {
		stored, err := s.storedMachineID(r.Context(), machine)
		if err != nil {
			s.storeError(w, r, err)
			return
		}
		machine = stored
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
	pr, nav, ok := s.pageRequest(w, r, q)
	if !ok {
		return
	}
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
			nav, page.NextCursor),
	})
}

// topListPage is one of the ranker's two lists.
type topListPage struct {
	Kind  store.TopKind
	Path  string
	Title string
	Nav   string
	Noun  string // what Count counts, for the column heading and the empty list
}

var (
	topBlockedPage = topListPage{Kind: store.TopKindBlocked, Path: "/events/top-blocked", Title: "Top blocked binaries", Nav: "top-blocked", Noun: "blocked executions"}
	topAllowedPage = topListPage{Kind: store.TopKindAllowed, Path: "/events/top-allowed", Title: "Top allowed binaries", Nav: "top-allowed", Noun: "allowed executions"}
)

const defaultTopWindowDays = 7

// DefaultTopListStaleAfter is three missed runs of a ranker that runs every
// hour around the clock.
const DefaultTopListStaleAfter = 3 * time.Hour

// topWindowLink is one window in the window navigation.
type topWindowLink struct {
	Days    int
	URL     string
	Current bool
}

func (s *server) topBlocked(w http.ResponseWriter, r *http.Request) {
	s.topList(w, r, topBlockedPage)
}

func (s *server) topAllowed(w http.ResponseWriter, r *http.Request) {
	s.topList(w, r, topAllowedPage)
}

// topWindowDays reads `days`, which must be written exactly as one of the
// ranker's windows.
func topWindowDays(q url.Values) (int, bool) {
	raw := q.Get("days")
	if raw == "" {
		return defaultTopWindowDays, true
	}
	for _, d := range store.TopWindowDays {
		if raw == strconv.Itoa(d) {
			return d, true
		}
	}
	return 0, false
}

func topListURL(path string, days int) string {
	return path + "?" + url.Values{"days": {strconv.Itoa(days)}}.Encode()
}

// topList shows one page of a ranker list. The cursor pins the snapshot of
// the first page, so later pages stay consistent when the ranker runs
// meanwhile.
func (s *server) topList(w http.ResponseWriter, r *http.Request, page topListPage) {
	q := r.URL.Query()
	days, ok := topWindowDays(q)
	if !ok {
		s.renderError(w, r, http.StatusBadRequest, "Unknown time window.")
		return
	}
	pr, nav, ok := s.pageRequest(w, r, q)
	if !ok {
		return
	}
	list, err := s.store.TopBinaries(r.Context(), store.TopFilter{Kind: page.Kind, WindowDays: days}, pr)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	windows := make([]topWindowLink, 0, len(store.TopWindowDays))
	for _, d := range store.TopWindowDays {
		windows = append(windows, topWindowLink{Days: d, URL: topListURL(page.Path, d), Current: d == days})
	}
	other := topAllowedPage
	if page.Kind == store.TopKindAllowed {
		other = topBlockedPage
	}
	computed := !list.GeneratedAt.IsZero()
	s.render(w, r, http.StatusOK, "toplist", page.Title, page.Nav, struct {
		List        topListPage
		Days        int
		Windows     []topWindowLink
		OtherTitle  string
		OtherURL    string
		Computed    bool
		GeneratedAt time.Time
		Stale       bool
		StaleHours  string
		Binaries    []store.TopBinary
		Pager       pager
	}{
		List:        page,
		Days:        days,
		Windows:     windows,
		OtherTitle:  other.Title,
		OtherURL:    topListURL(other.Path, days),
		Computed:    computed,
		GeneratedAt: list.GeneratedAt,
		Stale:       computed && s.now().Sub(list.GeneratedAt) > s.topListStaleAfter,
		StaleHours:  strconv.FormatFloat(s.topListStaleAfter.Hours(), 'f', -1, 64),
		Binaries:    list.Items,
		Pager:       links(page.Path, url.Values{"days": {strconv.Itoa(days)}}, nav, list.NextCursor),
	})
}

// ---------------------------------------------------------------------------
// Admin

// admin says where the admin actions are (Rudolph.Admin only).
func (s *server) admin(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "admin", "Admin", "admin", struct{ Editing bool }{s.writer != nil})
}
