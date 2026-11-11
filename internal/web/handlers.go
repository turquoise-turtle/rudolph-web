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
	// One read of the groups serves both the client mode the Mac's groups
	// give it and the admin's group forms. Without it the page still shows
	// the Mac's own and the global settings.
	registered, err := s.store.ListGroups(r.Context())
	groupsUnread := err != nil
	if groupsUnread {
		s.log.Warn("reading the groups failed", "err", err)
	}
	var fromGroup groupMode
	if !groupsUnread {
		fromGroup.Mode, fromGroup.Group = rudolphrules.MachineGroupClientMode(registered, machine.AssignedGroups, machine.ReportedGroups)
	}
	inherited := groupMode{Mode: global.ClientMode}
	if fromGroup.Mode != "" {
		inherited = fromGroup
	}
	title := machine.Hostname
	if title == "" {
		title = machine.MachineID
	}
	edit := s.editForms(r)
	var groupForms machineGroupForms
	if edit.On && !groupsUnread {
		groupForms = machineGroupFormsOf(registered, machine)
	}
	s.render(w, r, http.StatusOK, "machine", "Machine "+title, "machines", struct {
		Machine      store.MachineDetail
		Inherited    groupMode
		Config       []configRow
		ConfigForm   configForm
		GlobalUnread bool
		GroupsUnread bool
		Health       syncHealth
		Rules        []store.Rule
		Pager        pager
		Edit         editForms
		Groups       machineGroupForms
	}{
		Machine:      machine,
		Inherited:    inherited,
		Config:       machineConfigRows(machine.Config, global, !globalUnread, fromGroup),
		ConfigForm:   machineConfigForm(edit, machine, global, !globalUnread, fromGroup),
		GlobalUnread: globalUnread,
		GroupsUnread: groupsUnread,
		Health:       machineHealth(machine, s.now(), inherited),
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
	edit := s.editForms(r)
	// Only admins can turn collection on, so only they need to know whether
	// Macs upload allowed executions; viewers' pages cost no extra read.
	var allEventUpload, globalUnread bool
	if edit.On {
		global, err := s.store.GlobalConfig(r.Context())
		if err != nil {
			s.log.Warn("reading the global config failed", "err", err)
			globalUnread = true
		}
		allEventUpload = err == nil && global.EnableAllEventUpload != nil && *global.EnableAllEventUpload
	}
	s.render(w, r, http.StatusOK, "rules", "Global rules", "rules", struct {
		Filter         store.RuleFilter
		RuleTypes      []store.RuleType
		Policies       []store.Policy
		Rules          []store.Rule
		Pager          pager
		Edit           editForms
		AllEventUpload bool
		GlobalUnread   bool
	}{
		Filter:         filter,
		RuleTypes:      store.RuleTypes,
		Policies:       store.Policies,
		Rules:          page.Items,
		Edit:           edit,
		AllEventUpload: allEventUpload,
		GlobalUnread:   globalUnread,
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
	// Rudolph registers the ungrouped group only once it has a rule or a
	// client mode, but it can have rules before then, so it is always listed,
	// last.
	rows = append(rows, groupRow{Group: ungroupedOf(groups)})
	s.render(w, r, http.StatusOK, "groups", "Groups", "groups", struct {
		Groups    []groupRow
		Edit      editForms
		MaxGroups int
	}{rows, s.editForms(r), rudolphrules.MaxGroups})
}

// ungroupedOf is the ungrouped group as registered, or with no settings when
// it is not.
func ungroupedOf(groups []store.Group) store.Group {
	if i := slices.IndexFunc(groups, func(g store.Group) bool { return g.Name == store.UngroupedGroup }); i >= 0 {
		return groups[i]
	}
	return store.Group{Name: store.UngroupedGroup}
}

// findGroup returns the named group: a registered one, or the ungrouped
// group, which exists whether or not it is registered.
func findGroup(groups []store.Group, name string) (store.Group, bool) {
	if name == store.UngroupedGroup {
		return ungroupedOf(groups), true
	}
	i := slices.IndexFunc(groups, func(g store.Group) bool { return g.Name == name })
	if i < 0 {
		return store.Group{}, false
	}
	return groups[i], true
}

func (s *server) groupDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	groups, err := s.store.ListGroups(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	group, ok := findGroup(groups, name)
	if !ok {
		s.renderError(w, r, http.StatusNotFound, "Group not found.")
		return
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
	edit := s.editForms(r)
	var modeOptions []configOption
	if edit.On {
		modeOptions = groupModeOptions(group.ClientMode)
	}
	s.render(w, r, http.StatusOK, "group", "Group "+groupLabel(name), "groups", struct {
		Group       store.Group
		ModeOptions []configOption
		Rules       []store.Rule
		Pager       pager
		Edit        editForms
	}{
		Group:       group,
		ModeOptions: modeOptions,
		Rules:       page.Items,
		Pager:       links(groupURL(name), url.Values{}, nav, page.NextCursor),
		Edit:        edit,
	})
}

// memberRow is one Mac on a group's members page. How is "assigned",
// "reported" or "assigned, reported".
type memberRow struct {
	store.Machine
	How string
}

// groupMembers lists a registered group's Macs from Rudolph's member rows.
// The ungrouped group has none: Rudolph does not index the Macs in no group,
// and listing them would need a Scan.
func (s *server) groupMembers(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	groups, err := s.store.ListGroups(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	group, ok := findGroup(groups, name)
	if !ok || name == store.UngroupedGroup {
		s.renderError(w, r, http.StatusNotFound, "Group not found.")
		return
	}
	pr, nav, ok := s.pageRequest(w, r, r.URL.Query())
	if !ok {
		return
	}
	// A Mac that only reports a group not accepting reported membership has a
	// member row (Rudolph mirrors what the Mac sent) but is not a member.
	filter := store.GroupMemberFilter{AssignedOnly: !group.AcceptReported}
	page, err := s.store.ListGroupMembers(r.Context(), name, filter, pr)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	rows := make([]memberRow, 0, len(page.Items))
	for _, m := range page.Items {
		var how []string
		if m.Assigned {
			how = append(how, "assigned")
		}
		if m.Reported {
			how = append(how, "reported")
		}
		rows = append(rows, memberRow{Machine: m.Machine, How: strings.Join(how, ", ")})
	}
	s.render(w, r, http.StatusOK, "members", "Macs in group "+name, "groups", struct {
		Group        store.Group
		AssignedOnly bool
		Members      []memberRow
		Pager        pager
	}{
		Group:        group,
		AssignedOnly: filter.AssignedOnly,
		Members:      rows,
		Pager:        links(groupMembersURL(name), url.Values{}, nav, page.NextCursor),
	})
}

func groupMembersURL(name string) string { return groupURL(name) + "/members" }

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
	m, err := s.storedMachine(ctx, id)
	return m.MachineID, err
}

// storedMachine is storedMachineID with the Mac's sensor data, for pages that also show its hostname; without a
// machine row only MachineID is set.
func (s *server) storedMachine(ctx context.Context, id string) (store.Machine, error) {
	tried := map[string]bool{}
	for _, candidate := range []string{id, strings.ToUpper(id), strings.ToLower(id)} {
		if tried[candidate] {
			continue
		}
		tried[candidate] = true
		m, err := s.store.GetMachine(ctx, candidate)
		if err == nil {
			m.MachineID = candidate
			return m.Machine, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return store.Machine{}, err
		}
	}
	return store.Machine{MachineID: id}, nil
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

// fileAccessEvents lists Santa's file access authorisation events, as the events page lists executions.
func (s *server) fileAccessEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	machine, ok := textFilter(q, "machine")
	if !ok {
		s.renderError(w, r, http.StatusBadRequest, "Filter value is too long.")
		return
	}
	decision := store.FileAccessDecision(q.Get("decision"))
	// Checked before the machine lookup, so a request the store would refuse reads nothing.
	if machine != "" && decision != "" {
		s.renderError(w, r, http.StatusBadRequest, "Filter by one field at a time: machine or decision.")
		return
	}
	if decision != "" && !slices.Contains(store.FileAccessDecisions, decision) {
		s.renderError(w, r, http.StatusBadRequest, "Unknown decision.")
		return
	}
	pr, nav, ok := s.pageRequest(w, r, q)
	if !ok {
		return
	}
	// The Mac's hostname comes with the machine read that finds its stored spelling, so a machine's list names it at
	// no extra cost; the fleet list would need a read per Mac, so it shows machine IDs.
	hostnames := map[string]string{}
	if machine != "" {
		// As for executions, later pages carry the spelling page 1 found; they read the Mac once more for its
		// hostname.
		var m store.Machine
		var err error
		if pr.Cursor == "" {
			m, err = s.storedMachine(r.Context(), machine)
		} else {
			var d store.MachineDetail
			if d, err = s.store.GetMachine(r.Context(), machine); errors.Is(err, store.ErrNotFound) {
				err = nil
			}
			m = d.Machine
		}
		if err != nil {
			s.storeError(w, r, err)
			return
		}
		if m.MachineID != "" {
			machine = m.MachineID
		}
		if m.Hostname != "" {
			hostnames[machine] = m.Hostname
		}
	}
	filter := store.FileAccessEventFilter{MachineID: machine, Decision: decision}
	page, err := s.store.ListFileAccessEvents(r.Context(), filter, pr)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "fileaccess", "File access events", "events", struct {
		Filter    store.FileAccessEventFilter
		Decisions []store.FileAccessDecision
		Events    []store.FileAccessEvent
		Hostnames map[string]string
		Pager     pager
	}{
		Filter:    filter,
		Decisions: store.FileAccessDecisions,
		Events:    page.Items,
		Hostnames: hostnames,
		Pager:     links("/events/file-access", url.Values{"machine": {machine}, "decision": {string(decision)}}, nav, page.NextCursor),
	})
}

// topListPage is one of the ranker's list pages.
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
	topUnknownPage = topListPage{Kind: store.TopKindUnknown, Path: "/events/top-unknown", Title: "Top unknown binaries", Nav: "top-unknown", Noun: "unknown executions"}
	// topPages is the order the pages link to each other in.
	topPages = []topListPage{topBlockedPage, topAllowedPage, topUnknownPage}
)

// topUnknownDecision is one choice of the top unknown page's decision filter.
type topUnknownDecision struct {
	Value string
	Label string
	Kind  store.TopKind
	Noun  string
}

// topUnknownDecisions are the filter's choices, the default first.
var topUnknownDecisions = []topUnknownDecision{
	{"all", "Blocked and allowed", store.TopKindUnknown, "unknown executions"},
	{"blocked", "Blocked (lockdown)", store.TopKindUnknownBlocked, "unknown blocked executions"},
	{"allowed", "Allowed (monitor)", store.TopKindUnknownAllowed, "unknown allowed executions"},
}

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

// topLink links to another top list page.
type topLink struct {
	Title string
	URL   string
}

// topRow is a top list row with what the page adds to it.
type topRow struct {
	store.TopBinary
	// RuleType and RuleIdentifier describe GlobalRule; RuleType is empty
	// when the row has none (or one this version cannot read).
	RuleType       string
	RuleIdentifier string
	// AddRuleURL is the pre-filled add-rule page, for admins with editing
	// on, on unknown rows that no global rule covers yet and that have an
	// identifier a rule can take.
	AddRuleURL string
}

func (s *server) topBlocked(w http.ResponseWriter, r *http.Request) {
	s.topList(w, r, topBlockedPage, nil)
}

func (s *server) topAllowed(w http.ResponseWriter, r *http.Request) {
	s.topList(w, r, topAllowedPage, nil)
}

// topUnknown serves the three unknown lists on one page, chosen with the
// decision filter, which every link on the page keeps.
func (s *server) topUnknown(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("decision")
	if raw == "" {
		raw = topUnknownDecisions[0].Value
	}
	i := slices.IndexFunc(topUnknownDecisions, func(d topUnknownDecision) bool { return d.Value == raw })
	if i < 0 {
		s.renderError(w, r, http.StatusBadRequest, "Unknown decision; choose all, blocked or allowed.")
		return
	}
	d := topUnknownDecisions[i]
	page := topUnknownPage
	page.Kind, page.Noun = d.Kind, d.Noun
	s.topList(w, r, page, &d)
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

// topListURL links to a window of a list page; decision is the unknown page's
// filter, or "" for the default.
func topListURL(path string, days int, decision string) string {
	return path + "?" + topListQuery(days, decision).Encode()
}

func topListQuery(days int, decision string) url.Values {
	v := url.Values{"days": {strconv.Itoa(days)}}
	if decision != "" && decision != topUnknownDecisions[0].Value {
		v.Set("decision", decision)
	}
	return v
}

// globalRuleLabels name the rule types the ranker's GlobalRule can hold.
var globalRuleLabels = map[store.RuleType]string{
	store.RuleTypeBinary:    "Binary",
	store.RuleTypeSigningID: "Signing ID",
	store.RuleTypeTeamID:    "Team ID",
}

// newRuleURL links to the pre-filled add-rule page for a row, or returns ""
// when no identifier of the row would make a valid rule, so the page never
// offers a link that ends in an error.
func newRuleURL(b store.TopBinary) string {
	v := url.Values{}
	for _, c := range prefillChoices(b.SigningID, b.TeamID, b.FileSHA256) {
		if rudolphrules.ValidIdentifier(c.Type, c.Identifier) {
			v.Set(c.Param, c.Identifier)
		}
	}
	if len(v) == 0 {
		return ""
	}
	return "/admin/rules/new?" + v.Encode()
}

// topList shows one page of a ranker list. The cursor pins the snapshot of
// the first page, so later pages stay consistent when the ranker runs
// meanwhile. decision is the unknown page's filter, nil on the others.
func (s *server) topList(w http.ResponseWriter, r *http.Request, page topListPage, decision *topUnknownDecision) {
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
	decisionValue := ""
	if decision != nil {
		decisionValue = decision.Value
	}
	windows := make([]topWindowLink, 0, len(store.TopWindowDays))
	for _, d := range store.TopWindowDays {
		windows = append(windows, topWindowLink{Days: d, URL: topListURL(page.Path, d, decisionValue), Current: d == days})
	}
	var others []topLink
	for _, p := range topPages {
		if p.Path != page.Path {
			others = append(others, topLink{Title: p.Title, URL: topListURL(p.Path, days, "")})
		}
	}
	canEdit := s.canEdit(r)
	rows := make([]topRow, 0, len(list.Items))
	for _, b := range list.Items {
		row := topRow{TopBinary: b}
		if t, id, ok := rudolphrules.RuleFromSortKey(b.GlobalRule); ok && globalRuleLabels[t] != "" {
			row.RuleType, row.RuleIdentifier = globalRuleLabels[t], id
		}
		if canEdit && page.Kind.IsUnknown() && b.GlobalRule == "" {
			row.AddRuleURL = newRuleURL(b)
		}
		rows = append(rows, row)
	}
	computed := !list.GeneratedAt.IsZero()
	s.render(w, r, http.StatusOK, "toplist", page.Title, page.Nav, struct {
		List        topListPage
		Unknown     bool
		Decision    string
		Decisions   []topUnknownDecision
		Days        int
		Windows     []topWindowLink
		Others      []topLink
		Computed    bool
		GeneratedAt time.Time
		Stale       bool
		StaleHours  string
		Binaries    []topRow
		Pager       pager
	}{
		List:        page,
		Unknown:     decision != nil,
		Decision:    decisionValue,
		Decisions:   topUnknownDecisions,
		Days:        days,
		Windows:     windows,
		Others:      others,
		Computed:    computed,
		GeneratedAt: list.GeneratedAt,
		Stale:       computed && s.now().Sub(list.GeneratedAt) > s.topListStaleAfter,
		StaleHours:  strconv.FormatFloat(s.topListStaleAfter.Hours(), 'f', -1, 64),
		Binaries:    rows,
		Pager:       links(page.Path, topListQuery(days, decisionValue), nav, list.NextCursor),
	})
}

// ---------------------------------------------------------------------------
// Admin

// admin says where the admin actions are (Rudolph.Admin only).
func (s *server) admin(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "admin", "Admin", "admin", struct{ Editing bool }{s.writer != nil})
}
