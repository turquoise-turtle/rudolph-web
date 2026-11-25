package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// Fleet health and Lockdown readiness (Rudolph's docs/fleet-health.md).
// Rudolph's fleet job writes a snapshot once a day; these pages read its
// pointer and one page of one list, never the fleet.

// DefaultFleetStaleAfter is a day and two hours: the fleet job runs once a
// day, so a snapshot older than that means a run was missed.
const DefaultFleetStaleAfter = 26 * time.Hour

// fleetCategory is how the pages name one of the job's health lists.
type fleetCategory struct {
	Category store.FleetCategory
	// Slug is the category in the list's address, /fleet/health/<slug>.
	Slug  string
	Title string
	About string
}

// fleetCategories are the health lists in Rudolph's order.
var fleetCategories = []fleetCategory{
	{store.FleetCategoryStale, "stale", "Not syncing",
		"No completed sync for longer than the stale threshold, or none on record. These Macs are usually off or offline."},
	{store.FleetCategoryIncomplete, "incomplete", "Syncs not finishing",
		"The current sync started at least 30 minutes before the run and has no postflight. The Mac reaches Rudolph but does not finish, which usually points at the server."},
	{store.FleetCategoryMode, "mode", "Client mode differs",
		"The client mode the Mac last reported is not the one Rudolph intends for it. A mode change takes one sync to arrive, so this also lists a rollout in progress."},
	{store.FleetCategoryRules, "rules", "Rules not applied",
		"The last sync applied fewer rules than it received (NotApplied), or the Mac's rules hash differs from what most Macs with the same rules report (Outlier)."},
	{store.FleetCategorySIP, "sip", "SIP reduced",
		"The Mac reports System Integrity Protection flags other than 0."},
	{store.FleetCategoryVersion, "version", "Santa out of date",
		"The Mac's Santa release is older than the fleet job's minimum."},
}

func fleetHealthURL(c fleetCategory) string { return "/fleet/health/" + c.Slug }

// hoursText writes a duration as hours without a trailing ".0", for "more
// than 26 hours ago".
func hoursText(d time.Duration) string {
	return strconv.FormatFloat(d.Hours(), 'f', -1, 64)
}

// fleetAge is what every fleet page says about the snapshot it shows.
type fleetAge struct {
	Computed    bool
	GeneratedAt time.Time
	// Stale is set when the snapshot is older than the limit, StaleHours.
	Stale      bool
	StaleHours string
}

func (s *server) fleetAge(generatedAt time.Time) fleetAge {
	computed := !generatedAt.IsZero()
	return fleetAge{
		Computed:    computed,
		GeneratedAt: generatedAt,
		Stale:       computed && s.now().Sub(generatedAt) > s.fleetStaleAfter,
		StaleHours:  hoursText(s.fleetStaleAfter),
	}
}

// fleetCategoryRow is one health list on the fleet page.
type fleetCategoryRow struct {
	fleetCategory
	URL   string
	Count int
}

// holidayView is the stored stale threshold as the fleet page shows it.
type holidayView struct {
	// Unread is set when the setting could not be read; the page then shows
	// neither it nor the forms, which are filled from it.
	Unread bool
	store.FleetHealthConfig
	// Stored is set when a holiday is stored, in force or lapsed, and Lapsed
	// when its last day has passed.
	Stored bool
	Lapsed bool
}

// sameVersion reports whether two Santa versions name the same release, so
// 2026.9 and 2026.09 do not warn; versions that cannot be read are compared
// as written.
func sameVersion(a, b string) bool {
	pa, okA := store.ParseVersion(a)
	pb, okB := store.ParseVersion(b)
	if !okA || !okB {
		return a == b
	}
	// A version's trailing zero parts name the same release.
	for len(pa) > 0 && pa[len(pa)-1] == 0 {
		pa = pa[:len(pa)-1]
	}
	for len(pb) > 0 && pb[len(pb)-1] == 0 {
		pb = pb[:len(pb)-1]
	}
	return slices.Equal(pa, pb)
}

// fleet shows the counts of the latest run and the stored stale threshold.
func (s *server) fleet(w http.ResponseWriter, r *http.Request) {
	sum, err := s.store.FleetSummary(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	// The stored setting is what the next run will use, which the snapshot
	// cannot say; the page is worth showing without it.
	holiday := holidayView{}
	cfg, err := s.store.FleetHealthConfig(r.Context())
	if err != nil {
		s.log.Warn("reading the fleet health setting failed", "err", err)
		holiday.Unread = true
	} else {
		holiday.FleetHealthConfig = cfg
		holiday.Stored = cfg.HolidayStaleDays != 0 || cfg.HolidayUntil != ""
		holiday.Lapsed = holiday.Stored && cfg.HolidayUntil < s.now().UTC().Format(time.DateOnly)
	}
	rows := make([]fleetCategoryRow, 0, len(fleetCategories))
	for _, c := range fleetCategories {
		rows = append(rows, fleetCategoryRow{fleetCategory: c, URL: fleetHealthURL(c), Count: sum.Health[c.Category]})
	}
	canRun := s.fleetRunner != nil && s.canEdit(r)
	s.render(w, r, http.StatusOK, "fleet", "Fleet health", "fleet", struct {
		Age     fleetAge
		Summary store.FleetSummary
		Rows    []fleetCategoryRow
		// VersionDiffers is set when the job lists Macs below another Santa
		// version than this view marks; ViewMinVersion is the view's, "" when
		// its marker is off.
		VersionDiffers bool
		ViewMinVersion string
		Holiday        holidayView
		Edit           editForms
		// CanRun is set for admins who can start the fleet job, and RunStarted
		// when they have just done so.
		CanRun     bool
		RunStarted bool
	}{
		Age:            s.fleetAge(sum.GeneratedAt),
		Summary:        sum,
		Rows:           rows,
		VersionDiffers: sum.MinSantaVersion != "" && !sameVersion(sum.MinSantaVersion, s.minSantaVersion),
		ViewMinVersion: s.minSantaVersion,
		Holiday:        holiday,
		Edit:           s.editForms(r),
		CanRun:         canRun,
		RunStarted:     canRun && r.URL.Query().Get("run") == "started",
	})
}

// fleetHealthList shows one page of a health list. The cursor pins the
// snapshot of the first page, so later pages stay on it when the job runs
// meanwhile.
func (s *server) fleetHealthList(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("category")
	i := slices.IndexFunc(fleetCategories, func(c fleetCategory) bool { return c.Slug == slug })
	if i < 0 {
		s.renderError(w, r, http.StatusNotFound, "There is no fleet health list at this address.")
		return
	}
	c := fleetCategories[i]
	pr, nav, ok := s.pageRequest(w, r, r.URL.Query())
	if !ok {
		return
	}
	list, err := s.store.ListFleetHealth(r.Context(), c.Category, pr)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "fleethealth", "Fleet health: "+c.Title, "fleet", struct {
		Age      fleetAge
		Category fleetCategory
		Rows     []store.FleetHealthRow
		Pager    pager
	}{
		Age:      s.fleetAge(list.GeneratedAt),
		Category: c,
		Rows:     list.Items,
		Pager:    links(fleetHealthURL(c), url.Values{}, nav, list.NextCursor),
	})
}

// readinessBucket is how the pages name a readiness bucket; its Value is the
// bucket in ?bucket=.
type readinessBucket struct {
	Value store.ReadinessBucket
	Title string
	About string
}

// readinessBuckets are the buckets in the order the lists sort them.
var readinessBuckets = []readinessBucket{
	{store.ReadinessReady, "Ready", "no unknown binary of the last 30 days is still pending"},
	{store.ReadinessFew, "Few", "1 to 9 pending"},
	{store.ReadinessMany, "Many", "10 or more pending, or more than 1,000 unknown binaries"},
	{store.ReadinessUnmeasured, "Unmeasured", "nothing pending would mean nothing: the Mac is not syncing, uploads no unknown executions, reports no rules or has been seen for fewer than 30 days"},
	{store.ReadinessLocked, "Locked", "Rudolph already intends LOCKDOWN or STANDALONE"},
}

// readinessMacsURL links to a readiness list: the fleet's (group "") or a
// group's, one bucket or all of it (ReadinessAny).
func readinessMacsURL(group string, bucket store.ReadinessBucket) string {
	v := url.Values{}
	if group != "" {
		v.Set("group", group)
	}
	if bucket != store.ReadinessAny {
		v.Set("bucket", string(bucket))
	}
	if enc := v.Encode(); enc != "" {
		return "/fleet/readiness/macs?" + enc
	}
	return "/fleet/readiness/macs"
}

// readinessCell is one bucket's count in a row of the readiness page.
type readinessCell struct {
	Count int
	URL   string
}

// readinessScope is one row of the readiness page: the fleet (Group "") or
// a group.
type readinessScope struct {
	Group string
	Macs  int
	Cells []readinessCell
}

func bucketCount(c store.ReadinessCounts, b store.ReadinessBucket) int {
	switch b {
	case store.ReadinessReady:
		return c.Ready
	case store.ReadinessFew:
		return c.Few
	case store.ReadinessMany:
		return c.Many
	case store.ReadinessUnmeasured:
		return c.Unmeasured
	case store.ReadinessLocked:
		return c.Locked
	}
	return 0
}

func readinessScopeOf(group string, c store.ReadinessCounts) readinessScope {
	row := readinessScope{Group: group, Macs: c.Macs}
	for _, b := range readinessBuckets {
		row.Cells = append(row.Cells, readinessCell{Count: bucketCount(c, b.Value), URL: readinessMacsURL(group, b.Value)})
	}
	return row
}

// readiness shows the buckets' counts for the fleet and each group, from the
// pointer alone.
func (s *server) readiness(w http.ResponseWriter, r *http.Request) {
	sum, err := s.store.FleetSummary(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	names := make([]string, 0, len(sum.ReadinessGroups))
	for name := range sum.ReadinessGroups {
		names = append(names, name)
	}
	// By name, with the Macs in no group last, as on the groups page.
	slices.SortFunc(names, func(a, b string) int {
		if ua, ub := a == store.UngroupedGroup, b == store.UngroupedGroup; ua != ub {
			if ua {
				return 1
			}
			return -1
		}
		return strings.Compare(a, b)
	})
	scopes := []readinessScope{readinessScopeOf("", sum.ReadinessAll)}
	for _, name := range names {
		scopes = append(scopes, readinessScopeOf(name, sum.ReadinessGroups[name]))
	}
	s.render(w, r, http.StatusOK, "readiness", "Lockdown readiness", "fleet", struct {
		Age     fleetAge
		Buckets []readinessBucket
		Scopes  []readinessScope
		Capped  int
	}{
		Age:     s.fleetAge(sum.GeneratedAt),
		Buckets: readinessBuckets,
		Scopes:  scopes,
		Capped:  sum.Capped,
	})
}

// readinessMacRow is a readiness row with its bucket named for the page.
type readinessMacRow struct {
	store.ReadinessRow
	BucketTitle string
}

// cohortForm is the "add these Macs to a Lockdown group" form of a list of
// Ready Macs, for admins with editing on.
type cohortForm struct {
	On bool
	// Groups are the registered groups a cohort can be added to: those with
	// a LOCKDOWN or STANDALONE client mode. GroupsUnread is set when they
	// could not be read.
	Groups       []store.Group
	GroupsUnread bool
	Max          int
}

// readinessMacs shows one page of a readiness list, fewest pending first.
func (s *server) readinessMacs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	group := q.Get("group")
	// A registered name is at most 128 bytes; the bound keeps a made-up one
	// out of the cursor fingerprint and the page links.
	if len(group) > maxFilterLen || !utf8.ValidString(group) {
		s.renderError(w, r, http.StatusBadRequest, "Unknown group.")
		return
	}
	filter := store.ReadinessFilter{Group: group, Bucket: store.ReadinessBucket(q.Get("bucket"))}
	if filter.Validate() != nil {
		s.renderError(w, r, http.StatusBadRequest, "Unknown bucket; choose ready, few, many, unmeasured or locked.")
		return
	}
	pr, nav, ok := s.pageRequest(w, r, q)
	if !ok {
		return
	}
	list, err := s.store.ListReadiness(r.Context(), filter, pr)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.renderError(w, r, http.StatusNotFound, "The latest fleet snapshot has no readiness list for this group.")
			return
		}
		s.storeError(w, r, err)
		return
	}
	titles := map[store.ReadinessBucket]string{}
	for _, b := range readinessBuckets {
		titles[b.Value] = b.Title
	}
	rows := make([]readinessMacRow, 0, len(list.Items))
	for _, row := range list.Items {
		rows = append(rows, readinessMacRow{ReadinessRow: row, BucketTitle: titles[row.Bucket]})
	}
	// Only Ready Macs can be added to a Lockdown group, so only a list of
	// them has the form, and only then are the groups read.
	var cohort cohortForm
	if s.canEdit(r) && filter.Bucket == store.ReadinessReady && len(rows) > 0 {
		cohort = cohortForm{On: true, Max: store.MaxCohortMacs}
		registered, err := s.store.ListGroups(r.Context())
		if err != nil {
			s.log.Warn("reading the groups failed", "err", err)
			cohort.GroupsUnread = true
		}
		for _, g := range registered {
			if g.Name != store.UngroupedGroup && rudolphrules.IsLockdownMode(g.ClientMode) {
				cohort.Groups = append(cohort.Groups, g)
			}
		}
	}
	var choices []topLink
	choices = append(choices, topLink{Title: "All", URL: readinessMacsURL(group, store.ReadinessAny)})
	for _, b := range readinessBuckets {
		choices = append(choices, topLink{Title: b.Title, URL: readinessMacsURL(group, b.Value)})
	}
	title := "Lockdown readiness: all Macs"
	if group != "" {
		title = "Lockdown readiness: " + groupLabel(group)
	}
	s.render(w, r, http.StatusOK, "readinessmacs", title, "fleet", struct {
		Age      fleetAge
		Filter   store.ReadinessFilter
		Choices  []topLink
		Snapshot string
		Rows     []readinessMacRow
		Cohort   cohortForm
		Pager    pager
	}{
		Age:      s.fleetAge(list.GeneratedAt),
		Filter:   filter,
		Choices:  choices,
		Snapshot: list.Snapshot,
		Rows:     rows,
		Cohort:   cohort,
		Pager:    links("/fleet/readiness/macs", url.Values{"group": {group}, "bucket": {string(filter.Bucket)}}, nav, list.NextCursor),
	})
}

// unknownFigure is a Mac's unknown binaries on its page. Unread is set when
// the markers could not be read; the page then says so. More is set when the
// Mac has more markers than were read, and the page writes a plus after each
// count ("1,000+"): html/template would write a plus in a value as a
// character reference.
type unknownFigure struct {
	Unread bool
	Last30 string
	Last7  string
	More   bool
	URL    string
}

// thousands writes a count of markers with a comma, as in "1,000".
func thousands(n int) string {
	text := strconv.Itoa(n)
	if n >= 1000 {
		text = strconv.Itoa(n/1000) + "," + text[len(text)-3:]
	}
	return text
}

// machineUnknownFigure reads the Mac's unknown counts for its page. Like the
// global config and the groups, a failed read leaves the figure out rather
// than failing the page.
func (s *server) machineUnknownFigure(r *http.Request, machineID string) unknownFigure {
	fig := unknownFigure{URL: machineUnknownURL(machineID)}
	c, err := s.store.MachineUnknown(r.Context(), machineID)
	if err != nil {
		// A deployment without the events table has no markers to read; that is not worth a log line on every view.
		if !errors.Is(err, store.ErrUnavailable) {
			s.log.Warn("reading the unknown markers failed", "err", err)
		}
		fig.Unread = true
		return fig
	}
	fig.Last30, fig.Last7, fig.More = thousands(c.Last30), thousands(c.Last7), c.More
	return fig
}

func machineUnknownURL(id string) string { return "/machines/" + url.PathEscape(id) + "/unknown" }

// unknownRow is one unknown binary of a Mac with what the page adds to it.
type unknownRow struct {
	store.UnknownBinary
	// Old is set when the Mac last uploaded an execution of the binary more
	// than 30 days ago, so it no longer counts; the marker is still there
	// because markers expire after 35 days.
	Old bool
	// AddRuleURL is the pre-filled add-rule page, for admins with editing on.
	AddRuleURL string
}

// machineUnknown lists a Mac's unknown binaries from its markers.
func (s *server) machineUnknown(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pr, nav, ok := s.pageRequest(w, r, r.URL.Query())
	if !ok {
		return
	}
	// As for the events of a Mac: markers are keyed by the machine ID exactly
	// as stored, so the first page finds that spelling and later pages carry
	// it in their links.
	candidates := []string{id}
	if pr.Cursor == "" {
		candidates = rudolphrules.Spellings(id)
	}
	var machine store.Machine
	found := false
	for _, candidate := range candidates {
		d, err := s.store.GetMachine(r.Context(), candidate)
		if err == nil {
			machine, found = d.Machine, true
			machine.MachineID = candidate
			break
		}
		if !errors.Is(err, store.ErrNotFound) {
			s.storeError(w, r, err)
			return
		}
	}
	if !found {
		s.renderError(w, r, http.StatusNotFound, "Machine not found.")
		return
	}
	page, err := s.store.ListMachineUnknown(r.Context(), machine.MachineID, pr)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	canEdit := s.canEdit(r)
	oldest := s.now().UTC().AddDate(0, 0, -29).Format(time.DateOnly)
	rows := make([]unknownRow, 0, len(page.Items))
	for _, b := range page.Items {
		row := unknownRow{UnknownBinary: b, Old: b.LastDay.UTC().Format(time.DateOnly) < oldest}
		if canEdit {
			row.AddRuleURL = newRuleURL(store.TopBinary{FileSHA256: b.FileSHA256, SigningID: b.SigningID, TeamID: b.TeamID})
		}
		rows = append(rows, row)
	}
	title := machine.Hostname
	if title == "" {
		title = machine.MachineID
	}
	s.render(w, r, http.StatusOK, "unknown", "Unknown binaries on "+title, "machines", struct {
		Machine store.Machine
		Rows    []unknownRow
		CanEdit bool
		Pager   pager
	}{
		Machine: machine,
		Rows:    rows,
		CanEdit: canEdit,
		Pager:   links(machineUnknownURL(machine.MachineID), url.Values{}, nav, page.NextCursor),
	})
}

// groupReadiness is a group's readiness from the latest snapshot, shown
// beside the confirmation of its client mode form.
type groupReadiness struct {
	// Known is false when there is no snapshot, the snapshot does not name
	// the group or it could not be read; the form then shows no figures.
	Known       bool
	GeneratedAt time.Time
	Counts      store.ReadinessCounts
	// Pending is the Macs with unknown binaries still pending: Few and Many.
	Pending int
	URL     string
}

// groupReadinessOf reads the pointer for a group's figures. The form works
// without them, so a failed read only leaves them out.
func (s *server) groupReadinessOf(r *http.Request, group string) groupReadiness {
	sum, err := s.store.FleetSummary(r.Context())
	if err != nil {
		if !errors.Is(err, store.ErrUnavailable) {
			s.log.Warn("reading the fleet pointer failed", "err", err)
		}
		return groupReadiness{}
	}
	c, ok := sum.ReadinessGroups[group]
	if !ok || sum.GeneratedAt.IsZero() {
		return groupReadiness{}
	}
	return groupReadiness{Known: true, GeneratedAt: sum.GeneratedAt, Counts: c, Pending: c.Few + c.Many, URL: readinessMacsURL(group, store.ReadinessAny)}
}

// ---------------------------------------------------------------------------
// Admin actions

// setFleetHoliday raises the stale threshold until a date, as rudolph fleet
// holiday does. Whether the days and the date make a holiday is Rudolph's to
// say; the handler only reads the form.
func (s *server) setFleetHoliday(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	daysText, until := strings.TrimSpace(r.PostForm.Get("stale_days")), strings.TrimSpace(r.PostForm.Get("until"))
	if daysText == "" || until == "" {
		s.renderError(w, r, http.StatusBadRequest, "Not saved: a holiday needs both the stale days and its last day.")
		return
	}
	days, err := strconv.Atoi(daysText)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Not saved: the stale days must be a whole number of days.")
		return
	}
	if err := s.writer.SetFleetHoliday(r.Context(), actorOf(r), days, until); err != nil {
		s.writeError(w, r, err, "")
		return
	}
	http.Redirect(w, r, "/fleet", http.StatusSeeOther)
}

func (s *server) endFleetHoliday(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	// A second click, or the CLI ending it meanwhile, leaves it as asked.
	if err := s.writer.EndFleetHoliday(r.Context(), actorOf(r)); err != nil && !errors.Is(err, store.ErrNothingToChange) {
		s.writeError(w, r, err, "")
		return
	}
	http.Redirect(w, r, "/fleet", http.StatusSeeOther)
}

// assignCohort adds the ticked Ready Macs to a Lockdown group. The store
// checks the snapshot, the group and each Mac before it writes anything.
func (s *server) assignCohort(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	group := r.PostForm.Get("group")
	n, err := s.writer.AssignCohort(r.Context(), actorOf(r), r.PostForm.Get("snapshot"), group, r.PostForm["machine"])
	var partial *store.PartialCohortError
	switch {
	case errors.As(err, &partial):
		s.log.Error("cohort failed part-way", "path", r.URL.Path, "assigned", n, "err", err)
		msg := strconv.Itoa(partial.Assigned) + " of " + strconv.Itoa(partial.Total) + " Macs were added to the group before something went wrong; the others were not. Reload the list and add the rest again: adding a Mac twice changes nothing."
		if errors.Is(err, store.ErrNotAudited) {
			msg += " The last of them could not be recorded in the audit trail. Tell whoever looks after Rudolph."
		}
		s.renderError(w, r, http.StatusInternalServerError, msg)
		return
	case errors.Is(err, store.ErrSnapshotTooOld):
		s.renderError(w, r, http.StatusConflict, "Not saved: this list of ready Macs is from a fleet job run more than "+
			hoursText(store.MaxCohortSnapshotAge)+" hours ago. Reload the list and choose the Macs again.")
		return
	case err != nil:
		s.writeError(w, r, err, "Group not found: it may have been deleted.")
		return
	}
	// The store accepted the name, so it normalises; the members page is
	// where the Macs just added show.
	name, err := rudolphrules.RegisteredGroupName(group)
	if err != nil {
		name = group
	}
	http.Redirect(w, r, groupMembersURL(name), http.StatusSeeOther)
}

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
// run. How often is the store's to say: its guard is one conditional write,
// which holds across Lambda instances where a lock here would not.
func (s *server) runFleetJob(w http.ResponseWriter, r *http.Request) {
	// The form has no fields; parsing only bounds the body.
	if !s.parseForm(w, r) {
		return
	}
	now := s.now()
	err := s.writer.RequestFleetRun(r.Context(), actorOf(r), now, s.fleetRunner.Start)
	var soon *store.FleetRunTooSoonError
	switch {
	case errors.As(err, &soon):
		s.renderError(w, r, http.StatusConflict, "Not started: the fleet job ran, or was started, less than "+
			minutesText(store.FleetRunInterval)+" ago. It can be run again in "+minutesText(soon.RetryAt.Sub(now))+".")
		return
	case errors.Is(err, store.ErrFleetRunNotStarted):
		// The AWS error says which function and why; that is for the log.
		s.log.Error("starting the fleet job failed", "path", r.URL.Path, "err", err)
		s.renderError(w, r, http.StatusInternalServerError, "The fleet job could not be started. It can be tried again in "+
			minutesText(store.FleetRunInterval)+"; if it fails again, tell whoever looks after Rudolph.")
		return
	case errors.Is(err, store.ErrNotAudited):
		s.log.Error("fleet job started but not audited", "path", r.URL.Path, "err", err)
		s.renderError(w, r, http.StatusInternalServerError, "The fleet job was started, but that could not be recorded in the audit trail. Tell whoever looks after Rudolph.")
		return
	case err != nil:
		s.writeError(w, r, err, "")
		return
	}
	http.Redirect(w, r, "/fleet?run=started", http.StatusSeeOther)
}
