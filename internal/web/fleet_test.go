package web_test

// Fleet health and Lockdown readiness pages (design "Fleet health (Fix 2) and Lockdown readiness (Fix 3)", owner
// decisions of 2026-11-22; Rudolph's docs/fleet-health.md). Rudolph's fleet job runs once a day and writes a snapshot;
// the pages read its pointer (store.FleetSummary) and one page of a list, never the fleet. For viewers:
//
//   - GET /fleet: one row per health category, <tr data-category="<Stale|Incomplete|Mode|Rules|SIP|Version>">, holding
//     the count and a link to GET /fleet/health/<category in lower case> (stale, incomplete, mode, rules, sip,
//     version); when the snapshot was generated ("2006-01-02 15:04:05 UTC"); a "Warning" naming the limit ("26 hours")
//     when it is older than Options.FleetStaleAfter (0 means web.DefaultFleetStaleAfter, 26 hours: the job is daily;
//     negative is refused by web.New); the stale threshold the run used ("10 days") and, when the run was in holiday
//     mode, that it was and until when; a "Warning" naming both versions when the snapshot's MinSantaVersion differs
//     from Options.MinSantaVersion; a link to /fleet/readiness. "not been computed yet" without a pointer. Reads
//     FleetSummary and FleetHealthConfig only (no list).
//   - GET /fleet/health/{category}: the list, paged (one ListFleetHealth a page, Limit = page size, rows
//     <tr data-row>, next link <a rel="next">), hostname linking to the Mac's page, the category's detail (never
//     completed, syncs without postflight, reported/intended mode and its source, reason and hashes, SIP flags, Santa
//     version). Unknown category: 404. A cursor whose snapshot expired: 400.
//   - GET /fleet/readiness: one <tr data-row> for the fleet and one per group of the snapshot's Readiness, each with
//     Macs, Ready, Few, Many, Unmeasured and Locked, each bucket count linking to
//     GET /fleet/readiness/macs?bucket=<ready|few|many|unmeasured|locked>[&group=<group>], and the group's name linking
//     to its group page (where the client mode form is). Reads FleetSummary only.
//   - GET /fleet/readiness/macs?group=&bucket=: the Macs, in the snapshot's order (fewest pending first), with both
//     30-day counts (pending and unknown), both modes, why a Mac is unmeasured, "1,000+" for a capped Mac and no counts
//     for a locked one. One ListReadiness a page. Unknown bucket: 400; a group the snapshot does not name: 404.
//   - The machine page: "Unknown binaries: N in the last 30 days, M in the last 7" from MachineUnknown (live; "1,000+"
//     when More), linking to GET /machines/{id}/unknown. If the read fails the page still renders and says the figure
//     "could not be read". /machines/{id}/unknown lists the Mac's markers (ListMachineUnknown, paged) with the existing
//     "Add a rule" link (/admin/rules/new?sha256=&signing_id=&team_id=) for admins with editing on.
//   - A "Fleet" link in the navigation (<a href="/fleet">, aria-current="page" on the fleet pages).
//
// Viewers see all of it without forms; the holiday form and the cohort action are in fleet_editing_test.go.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/fakeidp"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

const (
	fleetMacA = "A0000000-1111-4111-8111-00000000000A"
	fleetMacB = "B0000000-2222-4222-8222-00000000000B"
	fleetMacC = "C0000000-3333-4333-8333-00000000000C"
	fleetMacD = "D0000000-4444-4444-8444-00000000000D"
	fleetMacE = "E0000000-5555-4555-8555-00000000000E"
	fleetMacF = "F0000000-6666-4666-8666-00000000000F"
)

var fleetGenerated = fixedNow.Add(-2 * time.Hour)

// fleetSnapshotID is a snapshot ID as Rudolph's fleet job writes it, generated at (by the real clock, which the
// writers use to judge a snapshot's age).
func fleetSnapshotID(at time.Time) string {
	return at.UTC().Format("20060102T150405.000000000Z") + "-0a1b2c3d"
}

// newFleetApp is the app over any reader and writer (nil keeps editing off), with opts adjusted by configure.
func newFleetApp(t *testing.T, r store.Reader, w store.Writer, configure func(*web.Options)) *app {
	t.Helper()
	idp := fakeidp.New(t, tenantID, clientID, "secret")
	a, err := auth.New(auth.Config{
		Issuer: idp.Issuer, TenantID: tenantID, ClientID: clientID, ClientSecret: "secret",
		RedirectURL: origin + "/auth/callback", CookieKey: []byte("0123456789abcdef0123456789abcdef"),
		SessionTTL: time.Hour, Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := web.Options{Auth: a, Store: r, PageSize: 10, Now: func() time.Time { return fixedNow }, MinSantaVersion: "2026.9"}
	if w != nil {
		opts.Writer = w
	}
	if configure != nil {
		configure(&opts)
	}
	h, err := web.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	app := &app{h: h, auth: a, idp: idp}
	if s, ok := r.(*fake.Store); ok {
		app.store = s
	}
	return app
}

func fleetSummary() store.FleetSummary {
	return store.FleetSummary{
		Snapshot:    fleetSnapshotID(time.Now().Add(-2 * time.Hour)),
		GeneratedAt: fleetGenerated,
		Macs:        240,
		Attention:   41,
		Health: map[store.FleetCategory]int{
			store.FleetCategoryStale: 30, store.FleetCategoryIncomplete: 2, store.FleetCategoryMode: 5,
			store.FleetCategoryRules: 1, store.FleetCategorySIP: 3, store.FleetCategoryVersion: 7,
		},
		VersionUnknown:  1,
		MinSantaVersion: "2026.9",
		StaleDays:       10,
		ReadinessAll:    store.ReadinessCounts{Macs: 240, Ready: 200, Few: 8, Many: 4, Unmeasured: 3, Locked: 25},
		ReadinessGroups: map[string]store.ReadinessCounts{
			"pilot":              {Macs: 120, Ready: 100, Few: 8, Many: 4, Unmeasured: 3, Locked: 5},
			"staff":              {Macs: 100, Ready: 80, Unmeasured: 0, Locked: 20},
			store.UngroupedGroup: {Macs: 20, Ready: 20},
		},
	}
}

func fleetReadyRows() []store.ReadinessRow {
	return []store.ReadinessRow{
		{MachineID: fleetMacA, Hostname: "ready-a", SerialNum: "C02A", Bucket: store.ReadinessReady, Counted: true, Unknown30: 4,
			ReportedMode: store.ClientModeMonitor, IntendedMode: store.ClientModeMonitor, ObservedDays: 45},
		{MachineID: fleetMacB, Hostname: "ready-b", SerialNum: "C02B", Bucket: store.ReadinessReady, Counted: true,
			ReportedMode: store.ClientModeMonitor, IntendedMode: store.ClientModeMonitor, ObservedDays: 45},
		{MachineID: fleetMacC, Hostname: "few-c", SerialNum: "C02C", Bucket: store.ReadinessFew, Counted: true, Unknown30: 9, Pending30: 7,
			Unknown7: 2, Pending7: 1, ReportedMode: store.ClientModeMonitor, IntendedMode: store.ClientModeMonitor, ObservedDays: 45},
		{MachineID: fleetMacE, Hostname: "capped-e", SerialNum: "C02E", Bucket: store.ReadinessMany, Counted: true, Capped: true,
			Unknown30: 1001, Pending30: 1001, ReportedMode: store.ClientModeMonitor, IntendedMode: store.ClientModeMonitor, ObservedDays: 45},
		{MachineID: fleetMacF, Hostname: "unmeasured-f", SerialNum: "C02F", Bucket: store.ReadinessUnmeasured, Counted: true, Why: "upload-off",
			ReportedMode: store.ClientModeMonitor, IntendedMode: store.ClientModeMonitor, ObservedDays: 45},
		{MachineID: fleetMacD, Hostname: "locked-d", SerialNum: "C02D", Bucket: store.ReadinessLocked,
			ReportedMode: store.ClientModeLockdown, IntendedMode: store.ClientModeLockdown, ObservedDays: 45},
	}
}

// fleetStore holds a snapshot generated two hours before the pages are shown, groups pilot (LOCKDOWN), kiosk
// (STANDALONE) and staff (no mode), and the six Macs.
func fleetStore() *fake.Store {
	s := fake.New()
	for _, m := range []struct{ id, host string }{
		{fleetMacA, "ready-a"}, {fleetMacB, "ready-b"}, {fleetMacC, "few-c"}, {fleetMacD, "locked-d"}, {fleetMacE, "capped-e"}, {fleetMacF, "unmeasured-f"},
	} {
		s.AddMachine(store.Machine{MachineID: m.id, Hostname: m.host, SerialNum: "C02" + m.id[:1], ClientMode: store.ClientModeMonitor, LastSeen: fixedNow})
	}
	s.AddGroup(store.Group{Name: "pilot", Description: "Lockdown pilot", ClientMode: store.ClientModeLockdown})
	s.AddGroup(store.Group{Name: "kiosk", Description: "Kiosks", ClientMode: store.ClientModeStandalone})
	s.AddGroup(store.Group{Name: "staff", Description: "Staff", AcceptReported: true})
	rows := fleetReadyRows()
	s.SetFleetSnapshot(fake.FleetSnapshot{
		Summary: fleetSummary(),
		Health: map[store.FleetCategory][]store.FleetHealthRow{
			store.FleetCategoryStale: {
				{MachineID: fleetMacA, Hostname: "never-synced", SerialNum: "C02A", LastSeen: fixedNow.Add(-20 * 24 * time.Hour), Never: true},
				{MachineID: fleetMacB, Hostname: "gone-quiet", SerialNum: "C02B", LastSeen: fixedNow.Add(-12 * 24 * time.Hour),
					LastCompleted: fixedNow.Add(-12 * 24 * time.Hour)},
			},
			store.FleetCategoryIncomplete: {{MachineID: fleetMacC, Hostname: "half-done", LastSeen: fixedNow.Add(-time.Hour), SyncsWithoutPostflight: 4}},
			store.FleetCategoryMode: {{MachineID: fleetMacC, Hostname: "wrong-mode", LastSeen: fixedNow, Reported: store.ClientModeMonitor,
				Intended: store.ClientModeLockdown, Source: "group:pilot"}},
			store.FleetCategoryRules: {{MachineID: fleetMacD, Hostname: "odd-hash", LastSeen: fixedNow, Reason: "Outlier", RulesReceived: 12,
				RulesProcessed: 12, Hash: "hash-mine", ExpectedHash: "hash-majority"}},
			store.FleetCategorySIP:     {{MachineID: fleetMacE, Hostname: "sip-off", LastSeen: fixedNow, Flags: "0x7f"}},
			store.FleetCategoryVersion: {{MachineID: fleetMacF, Hostname: "old-santa", LastSeen: fixedNow, SantaVersion: "2026.8.1"}},
		},
		Readiness: map[string][]store.ReadinessRow{
			"":      rows,
			"pilot": {rows[0], rows[2]},
			"staff": {rows[1]},
		},
	})
	return s
}

// categoryRow returns the /fleet row of one health category.
func categoryRow(t *testing.T, body, category string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<tr data-category="`+regexp.QuoteMeta(category)+`">(.*?)</tr>`).FindAllStringSubmatch(body, -1)
	if len(m) != 1 {
		t.Fatalf("found %d rows for category %s, want 1", len(m), category)
	}
	return m[0][1]
}

var hrefRe = regexp.MustCompile(`href="([^"]*)"`)

// hasLink reports whether the body links to path with exactly these query values, in any order.
func hasLink(body, path string, query url.Values) bool {
	for _, m := range hrefRe.FindAllStringSubmatch(body, -1) {
		u, err := url.Parse(html.UnescapeString(m[1]))
		if err != nil || u.Path != path {
			continue
		}
		if q := u.Query(); len(q) == len(query) {
			same := true
			for k := range query {
				if q.Get(k) != query.Get(k) {
					same = false
				}
			}
			if same {
				return true
			}
		}
	}
	return false
}

func containsFold(body, sub string) bool {
	return strings.Contains(strings.ToLower(body), strings.ToLower(sub))
}

func TestFleetPage_CountsLinksAndGeneratedAt(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, nil, nil)
	s.ResetCalls()
	code, body := a.viewerGet(t, "/fleet")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	for category, want := range map[string]struct {
		path  string
		count int
	}{
		"Stale": {"/fleet/health/stale", 30}, "Incomplete": {"/fleet/health/incomplete", 2}, "Mode": {"/fleet/health/mode", 5},
		"Rules": {"/fleet/health/rules", 1}, "SIP": {"/fleet/health/sip", 3}, "Version": {"/fleet/health/version", 7},
	} {
		row := categoryRow(t, body, category)
		mustContain(t, row, `href="`+want.path+`"`, fmt.Sprintf(">%d<", want.count))
	}
	mustContain(t, body, fleetGenerated.Format("2006-01-02 15:04:05 UTC"), "240", "41", "10 days", `href="/fleet/readiness"`)
	mustNotContain(t, body, "Warning", "not been computed yet")
	for _, c := range s.Calls() {
		if c.Paginated() || (c.Method != "FleetSummary" && c.Method != "FleetHealthConfig") {
			t.Errorf("call %+v; the fleet page reads the summary and the holiday setting only", c)
		}
	}
	if n := len(s.Calls()); n == 0 || n > 2 {
		t.Errorf("%d calls, want 1 or 2: %+v", n, s.Calls())
	}
}

func TestFleetPage_NotComputedYet(t *testing.T) {
	a := newFleetApp(t, fake.New(), nil, nil)
	for _, p := range []string{"/fleet", "/fleet/health/stale", "/fleet/readiness", "/fleet/readiness/macs?bucket=ready"} {
		code, body := a.viewerGet(t, p)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", p, code)
		}
		mustContain(t, body, "not been computed yet")
	}
}

func TestFleetPage_WarnsWhenOld(t *testing.T) {
	s := fake.New()
	sum := fleetSummary()
	sum.GeneratedAt = fixedNow.Add(-27 * time.Hour)
	s.SetFleetSnapshot(fake.FleetSnapshot{Summary: sum})

	_, body := newFleetApp(t, s, nil, nil).viewerGet(t, "/fleet")
	mustContain(t, body, "Warning", "26 hours")

	_, body = newFleetApp(t, s, nil, func(o *web.Options) { o.FleetStaleAfter = 48 * time.Hour }).viewerGet(t, "/fleet")
	mustNotContain(t, body, "Warning")

	if web.DefaultFleetStaleAfter != 26*time.Hour {
		t.Errorf("DefaultFleetStaleAfter = %v, want 26h", web.DefaultFleetStaleAfter)
	}
}

func TestNew_RejectsANegativeFleetStaleAfter(t *testing.T) {
	idp := fakeidp.New(t, tenantID, clientID, "secret")
	a, err := auth.New(auth.Config{Issuer: idp.Issuer, TenantID: tenantID, ClientID: clientID, ClientSecret: "secret",
		RedirectURL: origin + "/auth/callback", CookieKey: []byte("0123456789abcdef0123456789abcdef"), SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := web.New(web.Options{Auth: a, Store: fake.New(), FleetStaleAfter: -time.Hour}); err == nil {
		t.Error("New accepted a negative FleetStaleAfter")
	}
}

func TestFleetPage_WarnsWhenMinVersionDiffers(t *testing.T) {
	s := fake.New()
	sum := fleetSummary()
	sum.MinSantaVersion = "2026.8"
	s.SetFleetSnapshot(fake.FleetSnapshot{Summary: sum})
	_, body := newFleetApp(t, s, nil, nil).viewerGet(t, "/fleet")
	mustContain(t, body, "Warning", "2026.8", "2026.9")

	_, body = newFleetApp(t, s, nil, func(o *web.Options) { o.MinSantaVersion = "2026.8" }).viewerGet(t, "/fleet")
	mustNotContain(t, body, "Warning")
}

func TestFleetPage_ThresholdAndHolidayOfTheRun(t *testing.T) {
	s := fake.New()
	sum := fleetSummary()
	sum.StaleDays, sum.Holiday, sum.HolidayUntil = 60, true, "2027-02-01"
	s.SetFleetSnapshot(fake.FleetSnapshot{Summary: sum})
	_, body := newFleetApp(t, s, nil, nil).viewerGet(t, "/fleet")
	mustContain(t, body, "60 days", "2027-02-01")
	if !containsFold(body, "holiday") {
		t.Error("the page does not say the run was in holiday mode")
	}
}

func TestFleetHealthList_RowsAndDetail(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, nil, nil)

	s.ResetCalls()
	code, body := a.viewerGet(t, "/fleet/health/stale")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if rows(body) != 2 {
		t.Errorf("%d rows, want 2", rows(body))
	}
	mustContain(t, body, "never-synced", "gone-quiet", `href="/machines/`+fleetMacA+`"`, fleetGenerated.Format("2006-01-02 15:04:05 UTC"))
	if i, j := strings.Index(body, "never-synced"), strings.Index(body, "gone-quiet"); i > j {
		t.Error("the snapshot's order was not kept")
	}
	if !containsFold(ruleRow(t, body, "never-synced"), "never") {
		t.Error("the row of a Mac that never completed a sync does not say so")
	}
	calls := s.Calls()
	if len(calls) != 1 || calls[0].Method != "ListFleetHealth" || calls[0].Filter != store.FleetCategoryStale || calls[0].Page.Limit != 10 {
		t.Errorf("calls %+v; want one ListFleetHealth of Stale with the page size", calls)
	}

	for path, want := range map[string][]string{
		"/fleet/health/incomplete": {"half-done", ">4<"},
		"/fleet/health/mode":       {"wrong-mode", "MONITOR", "LOCKDOWN", "group:pilot"},
		"/fleet/health/rules":      {"odd-hash", "Outlier", "hash-mine", "hash-majority"},
		"/fleet/health/sip":        {"sip-off", "0x7f"},
		"/fleet/health/version":    {"old-santa", "2026.8.1"},
	} {
		code, body := a.viewerGet(t, path)
		if code != http.StatusOK {
			t.Errorf("%s: status %d", path, code)
			continue
		}
		if rows(body) != 1 {
			t.Errorf("%s: %d rows, want 1", path, rows(body))
		}
		mustContain(t, body, want...)
	}

	for _, bad := range []string{"/fleet/health/bogus", "/fleet/health/attention", "/fleet/health/"} {
		if code, _ := a.viewerGet(t, bad); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", bad, code)
		}
	}
}

func TestFleetHealthList_PagerAndCursor(t *testing.T) {
	s := fake.New()
	var stale []store.FleetHealthRow
	for i := 1; i <= 5; i++ {
		stale = append(stale, store.FleetHealthRow{MachineID: fmt.Sprintf("%08d-0000-4000-8000-000000000000", i), Hostname: fmt.Sprintf("old-%d", i)})
	}
	sum := fleetSummary()
	s.SetFleetSnapshot(fake.FleetSnapshot{Summary: sum, Health: map[store.FleetCategory][]store.FleetHealthRow{store.FleetCategoryStale: stale}})
	a := newFleetApp(t, s, nil, func(o *web.Options) { o.PageSize = 2 })

	_, body := a.viewerGet(t, "/fleet/health/stale")
	next := nextLink(t, body)
	if rows(body) != 2 || next == "" {
		t.Fatalf("first page: %d rows, next %q", rows(body), next)
	}
	// The next day's run between the two pages: the link keeps to the snapshot it started on.
	newer := fleetSummary()
	newer.Snapshot = fleetSnapshotID(time.Now())
	s.SetFleetSnapshot(fake.FleetSnapshot{Summary: newer, Health: map[store.FleetCategory][]store.FleetHealthRow{
		store.FleetCategoryStale: {{MachineID: fleetMacA, Hostname: "new-1"}}}})
	code, body := a.viewerGet(t, next)
	if code != http.StatusOK || rows(body) != 2 {
		t.Fatalf("second page: status %d, %d rows", code, rows(body))
	}
	mustContain(t, body, "old-3", "old-4")
	mustNotContain(t, body, "new-1")

	s.ExpireFleetSnapshot(sum.Snapshot)
	if code, _ := a.viewerGet(t, next); code != http.StatusBadRequest {
		t.Errorf("a link to an expired snapshot: status %d, want 400", code)
	}
	if code, _ := a.viewerGet(t, "/fleet/health/stale?cursor=bm90LWEtY3Vyc29y"); code != http.StatusBadRequest {
		t.Errorf("a forged cursor: status %d, want 400", code)
	}
}

func TestReadinessPage_ByGroup(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, nil, nil)
	s.ResetCalls()
	code, body := a.viewerGet(t, "/fleet/readiness")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if got := rows(body); got != 4 {
		t.Errorf("%d rows, want 4: the fleet and three groups", got)
	}
	for _, b := range []string{"ready", "few", "many", "unmeasured", "locked"} {
		if !hasLink(body, "/fleet/readiness/macs", url.Values{"bucket": {b}}) {
			t.Errorf("no link to the fleet's %s Macs", b)
		}
		for _, g := range []string{"pilot", "staff", store.UngroupedGroup} {
			if !hasLink(body, "/fleet/readiness/macs", url.Values{"group": {g}, "bucket": {b}}) {
				t.Errorf("no link to %s's %s Macs", g, b)
			}
		}
	}
	pilot := ruleRow(t, body, "pilot")
	mustContain(t, pilot, ">120<", ">100<", ">8<", ">4<", ">3<", ">5<", `href="/groups/pilot"`)
	mustContain(t, body, `href="/groups/`+url.PathEscape(store.UngroupedGroup)+`"`)
	calls := s.Calls()
	if len(calls) != 1 || calls[0].Method != "FleetSummary" {
		t.Errorf("calls %+v; want one FleetSummary", calls)
	}
}

func TestReadinessMacs_BucketFilterAndOrder(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, nil, nil)

	s.ResetCalls()
	code, body := a.viewerGet(t, "/fleet/readiness/macs?bucket=ready")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if rows(body) != 2 {
		t.Errorf("%d rows, want the two ready Macs", rows(body))
	}
	mustContain(t, body, "ready-a", "ready-b", `href="/machines/`+fleetMacA+`"`)
	mustNotContain(t, body, "few-c", "locked-d")
	calls := s.Calls()
	if len(calls) != 1 || calls[0].Method != "ListReadiness" || calls[0].Filter != (store.ReadinessFilter{Bucket: store.ReadinessReady}) {
		t.Errorf("calls %+v; want one ListReadiness of the fleet's ready Macs", calls)
	}

	s.ResetCalls()
	_, body = a.viewerGet(t, "/fleet/readiness/macs?group=pilot&bucket=few")
	mustContain(t, body, "few-c")
	if c := s.Calls(); len(c) != 1 || c[0].Filter != (store.ReadinessFilter{Group: "pilot", Bucket: store.ReadinessFew}) {
		t.Errorf("calls %+v; want pilot's few Macs", c)
	}

	_, body = a.viewerGet(t, "/fleet/readiness/macs")
	if rows(body) != 6 {
		t.Errorf("no bucket: %d rows, want every Mac", rows(body))
	}
	order := []string{"ready-a", "ready-b", "few-c", "capped-e", "unmeasured-f", "locked-d"}
	for i := 1; i < len(order); i++ {
		if strings.Index(body, order[i-1]) > strings.Index(body, order[i]) {
			t.Errorf("%s shown after %s; the snapshot's order is kept", order[i-1], order[i])
		}
	}
	few := ruleRow(t, body, "few-c")
	mustContain(t, few, ">7<", ">9<", "MONITOR")
	mustContain(t, ruleRow(t, body, "capped-e"), "1,000+")
	mustContain(t, ruleRow(t, body, "unmeasured-f"), "upload-off")
	locked := ruleRow(t, body, "locked-d")
	mustContain(t, locked, "LOCKDOWN")
	mustNotContain(t, locked, ">0<")

	if code, _ := a.viewerGet(t, "/fleet/readiness/macs?bucket=soon"); code != http.StatusBadRequest {
		t.Errorf("an unknown bucket: status %d, want 400", code)
	}
	if code, _ := a.viewerGet(t, "/fleet/readiness/macs?group=nope&bucket=ready"); code != http.StatusNotFound {
		t.Errorf("a group the snapshot does not name: status %d, want 404", code)
	}
}

// unknownFailing is the in-memory store whose unknown marker read fails.
type unknownFailing struct{ *fake.Store }

func (unknownFailing) MachineUnknown(context.Context, string) (store.UnknownCounts, error) {
	return store.UnknownCounts{}, errors.New("operation error DynamoDB: Query, AccessDeniedException")
}

func TestMachinePage_UnknownFigure(t *testing.T) {
	s := fleetStore()
	s.SetMachineUnknown(fleetMacA, store.UnknownCounts{Last30: 12, Last7: 3})
	a := newFleetApp(t, s, nil, nil)
	s.ResetCalls()
	code, body := a.viewerGet(t, "/machines/"+fleetMacA)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "Unknown binaries", "12 in the last 30 days", "3 in the last 7", `href="/machines/`+fleetMacA+`/unknown"`)
	n := 0
	for _, c := range s.Calls() {
		if c.Method == "MachineUnknown" {
			n++
			if c.MachineID != fleetMacA {
				t.Errorf("MachineUnknown for %q, want %s", c.MachineID, fleetMacA)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d MachineUnknown calls, want 1", n)
	}
}

func TestMachinePage_UnknownFigureCutAtAThousand(t *testing.T) {
	s := fleetStore()
	s.SetMachineUnknown(fleetMacA, store.UnknownCounts{Last30: 1000, Last7: 640, More: true})
	_, body := newFleetApp(t, s, nil, nil).viewerGet(t, "/machines/"+fleetMacA)
	mustContain(t, body, "1,000+")
	mustNotContain(t, body, "1000 in the last 30 days")
}

func TestMachinePage_UnknownFigureReadFails(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, unknownFailing{s}, nil, nil)
	code, body := a.viewerGet(t, "/machines/"+fleetMacA)
	if code != http.StatusOK {
		t.Fatalf("status %d; the machine page renders without the figure", code)
	}
	mustContain(t, body, "ready-a", "Unknown binaries", "could not be read")
	mustNotContain(t, body, "AccessDenied", "in the last 30 days")
}

func TestMachineUnknownList_RowsAndAddRuleLinks(t *testing.T) {
	s := fleetStore()
	sha := strings.Repeat("d4", 32)
	s.AddUnknownBinary(fleetMacA, store.UnknownBinary{FileSHA256: sha, FileName: "zoom-helper", SigningID: "BJ4HAAB9B3:us.zoom.helper",
		TeamID: "BJ4HAAB9B3", LastDay: time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)})
	s.AddUnknownBinary(fleetMacA, store.UnknownBinary{FileSHA256: strings.Repeat("e5", 32), FileName: "old-tool",
		LastDay: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)})
	s.AddUnknownBinary(fleetMacB, store.UnknownBinary{FileSHA256: strings.Repeat("f6", 32), FileName: "other-mac"})

	ro := newFleetApp(t, s, nil, nil)
	s.ResetCalls()
	code, body := ro.viewerGet(t, "/machines/"+fleetMacA+"/unknown")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if rows(body) != 2 {
		t.Errorf("%d rows, want 2", rows(body))
	}
	mustContain(t, body, "zoom-helper", "BJ4HAAB9B3:us.zoom.helper", "2026-09-16", "old-tool", "2026-08-01")
	mustNotContain(t, body, "other-mac", "/admin/rules/new")
	listed := false
	for _, c := range s.Calls() {
		if c.Method == "ListMachineUnknown" {
			listed = c.MachineID == fleetMacA && c.Page.Limit == 10
		}
	}
	if !listed {
		t.Errorf("calls %+v; want ListMachineUnknown of the Mac with the page size", s.Calls())
	}

	ed := newFleetApp(t, s, s, nil)
	body = ed.get(t, "/machines/"+fleetMacA+"/unknown", ed.cookie(t, auth.RoleAdmin)).Body.String()
	row := ruleRow(t, body, sha)
	mustContain(t, row, "Add a rule", "/admin/rules/new?")
	m := regexp.MustCompile(`href="(/admin/rules/new\?[^"]*)"`).FindStringSubmatch(row)
	if m == nil {
		t.Fatal("no add-rule link")
	}
	u, _ := url.Parse(html.UnescapeString(m[1]))
	if q := u.Query(); q.Get("sha256") != sha || q.Get("signing_id") != "BJ4HAAB9B3:us.zoom.helper" || q.Get("team_id") != "BJ4HAAB9B3" {
		t.Errorf("add-rule link %s", u)
	}
	_, viewer := ed.viewerGet(t, "/machines/"+fleetMacA+"/unknown")
	mustNotContain(t, viewer, "/admin/rules/new")
}

var navFleet = regexp.MustCompile(`<nav>(?s:.*?)<a href="/fleet"[^>]*>Fleet</a>(?s:.*?)</nav>`)

func TestFleetNav(t *testing.T) {
	a := newFleetApp(t, fleetStore(), nil, nil)
	_, body := a.viewerGet(t, "/machines")
	if !navFleet.MatchString(body) {
		t.Error("no Fleet link in the navigation")
	}
	for _, p := range []string{"/fleet", "/fleet/health/stale", "/fleet/readiness", "/fleet/readiness/macs?bucket=ready"} {
		_, body := a.viewerGet(t, p)
		mustContain(t, body, `<a href="/fleet" aria-current="page">Fleet</a>`)
	}
}

func TestFleet_ViewersSeeEverythingButNoForms(t *testing.T) {
	s := fleetStore()
	s.SetFleetHealthConfig(store.FleetHealthConfig{StaleDays: 10, HolidayStaleDays: 60, HolidayUntil: "2027-02-01"})
	pages := []string{"/fleet", "/fleet/health/stale", "/fleet/readiness", "/fleet/readiness/macs?bucket=ready",
		"/fleet/readiness/macs?group=pilot&bucket=ready", "/machines/" + fleetMacA + "/unknown"}
	for name, c := range map[string]struct {
		writable bool
		role     string
	}{
		"viewer, editing on":  {true, auth.RoleViewer},
		"viewer, editing off": {false, auth.RoleViewer},
		"admin, editing off":  {false, auth.RoleAdmin},
	} {
		var w store.Writer
		if c.writable {
			w = s
		}
		a := newFleetApp(t, s, w, nil)
		for _, p := range pages {
			rec := a.get(t, p, a.cookie(t, c.role))
			if rec.Code != http.StatusOK {
				t.Errorf("%s %s: status %d", name, p, rec.Code)
				continue
			}
			mustNotContain(t, rec.Body.String(), `action="/admin/fleet/holiday"`, `action="/admin/fleet/holiday/end"`, `action="/admin/fleet/cohort"`, `type="checkbox"`)
		}
	}
}

func TestFleet_SignedOutAndNoRole(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, nil, nil)
	for _, p := range []string{"/fleet", "/fleet/health/stale", "/fleet/readiness", "/fleet/readiness/macs?bucket=ready", "/machines/" + fleetMacA + "/unknown"} {
		s.ResetCalls()
		rec := a.get(t, p, nil)
		if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/login") {
			t.Errorf("%s signed out: status %d, Location %q", p, rec.Code, rec.Header().Get("Location"))
		}
		if rec := a.get(t, p, a.cookie(t)); rec.Code != http.StatusForbidden {
			t.Errorf("%s without a role: status %d, want 403", p, rec.Code)
		}
		if n := len(s.Calls()); n != 0 {
			t.Errorf("%s: %d data-layer calls before the role check", p, n)
		}
	}
}

func TestFleet_HostileDataIsEscaped(t *testing.T) {
	s := fake.New()
	evil := `<script>alert("fleet")</script>`
	sum := fleetSummary()
	sum.MinSantaVersion = `<b>2026.8</b>`
	sum.Holiday, sum.HolidayUntil = true, `"><img src=x onerror=alert(1)>`
	sum.ReadinessGroups = map[string]store.ReadinessCounts{`<iframe src="javascript:alert(2)">`: {Macs: 1, Ready: 1}}
	s.SetFleetSnapshot(fake.FleetSnapshot{
		Summary: sum,
		Health: map[store.FleetCategory][]store.FleetHealthRow{
			store.FleetCategoryStale: {{MachineID: `evil"onmouseover="alert(3)`, Hostname: evil, SerialNum: `<b>SERIAL</b>`}},
			store.FleetCategoryMode:  {{MachineID: fleetMacA, Hostname: "m", Source: `<svg onload=alert(4)>`, Reported: store.ClientMode(evil)}},
			store.FleetCategoryRules: {{MachineID: fleetMacA, Hostname: "r", Reason: evil, Hash: `</td></tr></table><script>alert(5)</script>`}},
		},
		Readiness: map[string][]store.ReadinessRow{"": {{MachineID: fleetMacA, Hostname: evil, Bucket: store.ReadinessUnmeasured, Counted: true, Why: `<svg onload=alert(6)>`}}},
	})
	s.AddMachine(store.Machine{MachineID: fleetMacA, Hostname: "host"})
	s.AddUnknownBinary(fleetMacA, store.UnknownBinary{FileSHA256: strings.Repeat("a", 64), FileName: evil, SigningID: `<img src=x onerror=alert(7)>`})
	a := newFleetApp(t, s, nil, nil)
	for _, p := range []string{"/fleet", "/fleet/health/stale", "/fleet/health/mode", "/fleet/health/rules", "/fleet/readiness",
		"/fleet/readiness/macs", "/machines/" + fleetMacA + "/unknown"} {
		t.Run(p, func(t *testing.T) {
			code, body := a.viewerGet(t, p)
			if code != http.StatusOK {
				t.Fatalf("status %d", code)
			}
			if n := strings.Count(body, "<script"); n != 1 {
				t.Errorf("want only the page's own script, got %d <script elements", n)
			}
			mustNotContain(t, body, "<script>alert", "<img src=x", "<svg", "<iframe", "<b>SERIAL", "<b>2026.8", `"onmouseover="`, "</table><script")
		})
	}
}
