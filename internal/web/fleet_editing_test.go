package web_test

// Admin actions on the fleet pages (owner decisions, 2026-11-22). Like every editing route they exist only with
// editing on (404 otherwise), take admins only (403 for viewers), are same-origin only, and redirect (303) back.
//
// Holiday mode (admins set and end it here as with `rudolph fleet holiday`; the store writes through Rudolph's
// pkg/model/fleethealth, so the validation and the item are the CLI's, and records fleet.holiday on fleet):
//   - GET /fleet for an admin with editing on shows the stored setting (store.FleetHealthConfig: the normal threshold,
//     and the holiday's days and last day when one is stored, which applies from the job's next run) and a form
//     posting to POST /admin/fleet/holiday with stale_days (a whole number of days) and until (YYYY-MM-DD, a UTC
//     date): store.Writer.SetFleetHoliday(ctx, actor, staleDays, until). With a holiday stored, also a form posting
//     to POST /admin/fleet/holiday/end: store.Writer.EndFleetHoliday(ctx, actor). Both return to /fleet (303).
//   - Refusals are 400 with the store's (Rudolph's) message; stale_days that is not a number, or a missing field, is
//     400 before the store is asked. Ending when no holiday is stored (ErrNothingToChange) writes and records nothing.
//
// The cohort action (design, Fix 3): on a list of Ready Macs (/fleet/readiness/macs?bucket=ready, the fleet's or a
// group's), admins with editing on get a form posting to POST /admin/fleet/cohort with the list's snapshot
// (name="snapshot"), a tick box per Mac (type="checkbox" name="machine" value=<machine ID>) and a select of the target
// group (name="group") offering only registered groups with a LOCKDOWN or STANDALONE client mode, never
// rudolph:ungrouped. Other buckets' lists have no such form. The action is store.Writer.AssignCohort(ctx, actor,
// snapshot, group, machineIDs); on success it returns to the group's members (303 to /groups/<group>/members).
// Refusals: no Macs, more than store.MaxCohortMacs, a group without a lockdown mode, the reserved group, a Mac not
// ready in that snapshot (400, naming it); a group that is not registered (404); a snapshot older than 26 hours
// (store.ErrSnapshotTooOld: 409, asking to reload the list). A *store.PartialCohortError is 500 saying how many of
// how many Macs were assigned ("2 of 3").
//
// The group client mode form (POST /admin/groups/config, unchanged) shows the group's readiness from the latest
// snapshot beside its confirmation: "<Few+Many> of <Macs> Macs still have pending unknown binaries, <Unmeasured> are
// unmeasured", for example "12 of 120 Macs ... 3 are unmeasured".

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func utcDay(offset int) string { return time.Now().UTC().AddDate(0, 0, offset).Format("2006-01-02") }

func fleetAuditOf(t *testing.T, s *fake.Store, action string) []store.AuditEntry {
	t.Helper()
	all, err := s.ListAudit(context.Background(), time.Now().Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEntry
	for _, e := range all {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

var (
	holidayForm    = regexp.MustCompile(`(?s)<form[^>]*action="/admin/fleet/holiday"[^>]*>(.*?)</form>`)
	holidayEndForm = regexp.MustCompile(`(?s)<form[^>]*action="/admin/fleet/holiday/end"[^>]*>(.*?)</form>`)
	cohortForm     = regexp.MustCompile(`(?s)<form[^>]*action="/admin/fleet/cohort"[^>]*>(.*?)</form>`)
)

func TestHoliday_FormForAdminsWithEditing(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, s, nil)
	_, body := adminGet(t, a, "/fleet")
	m := holidayForm.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no holiday form")
	}
	mustContain(t, m[1], `name="stale_days"`, `name="until"`)
	if holidayEndForm.MatchString(body) {
		t.Error("an end form with no holiday stored")
	}

	until := utcDay(20)
	s.SetFleetHealthConfig(store.FleetHealthConfig{StaleDays: 10, HolidayStaleDays: 60, HolidayUntil: until})
	_, body = adminGet(t, a, "/fleet")
	if !holidayEndForm.MatchString(body) {
		t.Error("no form to end the stored holiday")
	}
	mustContain(t, body, until, "60 days")
}

func TestHoliday_SetAndEnd(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, s, nil)
	admin := a.cookie(t, auth.RoleAdmin)
	until := utcDay(30)

	rec := a.post(t, "/admin/fleet/holiday", url.Values{"stale_days": {"60"}, "until": {until}}, admin, true)
	see303(t, rec, "/fleet")
	if cfg, _ := s.FleetHealthConfig(context.Background()); cfg != (store.FleetHealthConfig{StaleDays: 10, HolidayStaleDays: 60, HolidayUntil: until}) {
		t.Errorf("stored %+v", cfg)
	}
	entries := fleetAuditOf(t, s, "fleet.holiday")
	if len(entries) != 1 || entries[0].Target != "fleet" || !strings.HasPrefix(entries[0].Actor, "web:") ||
		!strings.Contains(entries[0].Actor, "vera@example.com") || !strings.Contains(entries[0].Summary, until) {
		t.Errorf("audit %+v; want fleet.holiday on fleet by the web actor", entries)
	}

	rec = a.post(t, "/admin/fleet/holiday/end", url.Values{}, admin, true)
	see303(t, rec, "/fleet")
	if cfg, _ := s.FleetHealthConfig(context.Background()); cfg.HolidayStaleDays != 0 || cfg.HolidayUntil != "" {
		t.Errorf("after the end %+v", cfg)
	}
	if entries := fleetAuditOf(t, s, "fleet.holiday"); len(entries) != 2 || !strings.Contains(entries[0].Summary, "ended") {
		t.Errorf("audit after the end %+v", entries)
	}

	rec = a.post(t, "/admin/fleet/holiday/end", url.Values{}, admin, true)
	if rec.Code >= 500 {
		t.Errorf("ending when no holiday is stored: status %d", rec.Code)
	}
	if n := len(fleetAuditOf(t, s, "fleet.holiday")); n != 2 {
		t.Errorf("ending no holiday was audited (%d entries)", n)
	}
}

func TestHoliday_ValidationErrorsShown(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, s, nil)
	admin := a.cookie(t, auth.RoleAdmin)
	for name, c := range map[string]struct {
		form    url.Values
		message string
	}{
		"0 days":       {url.Values{"stale_days": {"0"}, "until": {utcDay(5)}}, "between 1 and 365"},
		"366 days":     {url.Values{"stale_days": {"366"}, "until": {utcDay(5)}}, "between 1 and 365"},
		"below normal": {url.Values{"stale_days": {"5"}, "until": {utcDay(5)}}, "below the normal threshold"},
		"yesterday":    {url.Values{"stale_days": {"30"}, "until": {utcDay(-1)}}, "in the past"},
		"too far":      {url.Values{"stale_days": {"30"}, "until": {utcDay(367)}}, "366 days ahead"},
		"not a date":   {url.Values{"stale_days": {"30"}, "until": {"next week"}}, "YYYY-MM-DD"},
		"not a number": {url.Values{"stale_days": {"sixty"}, "until": {utcDay(5)}}, ""},
		"no until":     {url.Values{"stale_days": {"30"}}, ""},
		"no days":      {url.Values{"until": {utcDay(5)}}, ""},
	} {
		rec := a.post(t, "/admin/fleet/holiday", c.form, admin, true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
			continue
		}
		if c.message != "" && !strings.Contains(rec.Body.String(), c.message) {
			t.Errorf("%s: the page does not show %q", name, c.message)
		}
	}
	if cfg, _ := s.FleetHealthConfig(context.Background()); cfg.HolidayStaleDays != 0 {
		t.Errorf("a refused holiday was stored: %+v", cfg)
	}
	if n := len(fleetAuditOf(t, s, "fleet.holiday")); n != 0 {
		t.Errorf("refused holidays were audited (%d)", n)
	}
}

func TestHoliday_AdminOnlySameOriginEditingOn(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, s, nil)
	form := url.Values{"stale_days": {"60"}, "until": {utcDay(30)}}
	if rec := a.post(t, "/admin/fleet/holiday", form, a.cookie(t, auth.RoleViewer), true); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status %d, want 403", rec.Code)
	}
	if rec := a.post(t, "/admin/fleet/holiday", form, a.cookie(t, auth.RoleAdmin), false); rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin: status %d, want 403", rec.Code)
	}
	off := newFleetApp(t, s, nil, nil)
	for _, p := range []string{"/admin/fleet/holiday", "/admin/fleet/holiday/end"} {
		if rec := off.post(t, p, form, off.cookie(t, auth.RoleAdmin), true); rec.Code != http.StatusNotFound {
			t.Errorf("editing off %s: status %d, want 404", p, rec.Code)
		}
	}
	if cfg, _ := s.FleetHealthConfig(context.Background()); cfg.HolidayStaleDays != 0 {
		t.Errorf("a refused request stored %+v", cfg)
	}
}

// ---------------------------------------------------------------------------
// The cohort action

func TestCohort_FormOnReadyListsOnly(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, s, nil)
	sum, _ := s.FleetSummary(context.Background())

	for _, p := range []string{"/fleet/readiness/macs?bucket=ready", "/fleet/readiness/macs?group=pilot&bucket=ready"} {
		_, body := adminGet(t, a, p)
		m := cohortForm.FindStringSubmatch(body)
		if m == nil {
			t.Errorf("%s: no cohort form", p)
			continue
		}
		mustContain(t, m[1], `name="snapshot" value="`+sum.Snapshot+`"`, `type="checkbox"`, `name="machine"`, `value="`+fleetMacA+`"`,
			`name="group"`, `<option value="pilot"`, `<option value="kiosk"`)
		mustNotContain(t, m[1], `<option value="staff"`, `<option value="`+store.UngroupedGroup+`"`)
	}
	for _, p := range []string{"/fleet/readiness/macs?bucket=few", "/fleet/readiness/macs", "/fleet/readiness/macs?bucket=locked"} {
		_, body := adminGet(t, a, p)
		if cohortForm.MatchString(body) {
			t.Errorf("%s: a cohort form on a list that is not of Ready Macs", p)
		}
	}
}

func cohortPost(snapshot, group string, macs ...string) url.Values {
	return url.Values{"snapshot": {snapshot}, "group": {group}, "machine": macs}
}

func TestCohort_AssignsAndAuditsEachMac(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, s, nil)
	sum, _ := s.FleetSummary(context.Background())
	rec := a.post(t, "/admin/fleet/cohort", cohortPost(sum.Snapshot, "pilot", fleetMacA, fleetMacB), a.cookie(t, auth.RoleAdmin), true)
	see303(t, rec, "/groups/pilot/members")
	for _, id := range []string{fleetMacA, fleetMacB} {
		d, err := s.GetMachine(context.Background(), id)
		if err != nil || len(d.AssignedGroups) != 1 || d.AssignedGroups[0] != "pilot" {
			t.Errorf("%s assigned %v, %v; want pilot", id, d.AssignedGroups, err)
		}
	}
	adds := fleetAuditOf(t, s, "group.add")
	targets := map[string]bool{}
	for _, e := range adds {
		targets[e.Target] = strings.HasPrefix(e.Actor, "web:")
	}
	if len(adds) != 2 || !targets["machine:"+fleetMacA] || !targets["machine:"+fleetMacB] {
		t.Errorf("audit %+v; want group.add on each Mac by the web actor", adds)
	}
}

func TestCohort_Refusals(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, s, nil)
	admin := a.cookie(t, auth.RoleAdmin)
	sum, _ := s.FleetSummary(context.Background())
	old := fleetSummary()
	old.Snapshot = fleetSnapshotID(time.Now().Add(-27 * time.Hour))
	s.SetFleetSnapshot(fake.FleetSnapshot{Summary: old, Readiness: map[string][]store.ReadinessRow{"": fleetReadyRows()}})
	s.SetFleetSnapshot(fake.FleetSnapshot{Summary: sum, Readiness: map[string][]store.ReadinessRow{"": fleetReadyRows()}})
	s.AddGroup(store.Group{Name: store.UngroupedGroup, ClientMode: store.ClientModeLockdown})

	tooMany := make([]string, store.MaxCohortMacs+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("%08X-0000-4000-8000-000000000000", i)
	}
	for name, c := range map[string]struct {
		form url.Values
		code int
		says string
	}{
		"no Macs":           {cohortPost(sum.Snapshot, "pilot"), http.StatusBadRequest, ""},
		"51 Macs":           {cohortPost(sum.Snapshot, "pilot", tooMany...), http.StatusBadRequest, "50"},
		"no lockdown mode":  {cohortPost(sum.Snapshot, "staff", fleetMacA), http.StatusBadRequest, ""},
		"unknown group":     {cohortPost(sum.Snapshot, "nope", fleetMacA), http.StatusNotFound, ""},
		"the ungrouped":     {cohortPost(sum.Snapshot, store.UngroupedGroup, fleetMacA), http.StatusBadRequest, ""},
		"a Mac not ready":   {cohortPost(sum.Snapshot, "pilot", fleetMacA, fleetMacC), http.StatusBadRequest, fleetMacC},
		"an old snapshot":   {cohortPost(old.Snapshot, "pilot", fleetMacA), http.StatusConflict, ""},
		"no snapshot":       {url.Values{"group": {"pilot"}, "machine": {fleetMacA}}, http.StatusBadRequest, ""},
		"not a snapshot ID": {cohortPost("latest", "pilot", fleetMacA), http.StatusBadRequest, ""},
	} {
		rec := a.post(t, "/admin/fleet/cohort", c.form, admin, true)
		if rec.Code != c.code {
			t.Errorf("%s: status %d, want %d", name, rec.Code, c.code)
		}
		if c.says != "" && !strings.Contains(rec.Body.String(), c.says) {
			t.Errorf("%s: the page does not say %q", name, c.says)
		}
	}
	if rec := a.post(t, "/admin/fleet/cohort", cohortPost(sum.Snapshot, "pilot", fleetMacA), a.cookie(t, auth.RoleViewer), true); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status %d, want 403", rec.Code)
	}
	if rec := a.post(t, "/admin/fleet/cohort", cohortPost(sum.Snapshot, "pilot", fleetMacA), admin, false); rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin: status %d, want 403", rec.Code)
	}
	off := newFleetApp(t, s, nil, nil)
	if rec := off.post(t, "/admin/fleet/cohort", cohortPost(sum.Snapshot, "pilot", fleetMacA), off.cookie(t, auth.RoleAdmin), true); rec.Code != http.StatusNotFound {
		t.Errorf("editing off: status %d, want 404", rec.Code)
	}
	for _, id := range []string{fleetMacA, fleetMacB, fleetMacC} {
		if d, _ := s.GetMachine(context.Background(), id); len(d.AssignedGroups) != 0 {
			t.Errorf("%s assigned %v by a refused request", id, d.AssignedGroups)
		}
	}
	if adds := fleetAuditOf(t, s, "group.add"); len(adds) != 0 {
		t.Errorf("refusals were audited: %+v", adds)
	}
}

// partialWriter is the in-memory store whose cohort assignment stops after two of three Macs.
type partialWriter struct{ *fake.Store }

func (partialWriter) AssignCohort(context.Context, store.Actor, string, string, []string) (int, error) {
	return 2, &store.PartialCohortError{Assigned: 2, Total: 3, Err: errors.New("operation error DynamoDB: TransactWriteItems, ThrottlingException")}
}

func TestCohort_PartialFailureSaysHowMany(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, partialWriter{s}, nil)
	sum, _ := s.FleetSummary(context.Background())
	rec := a.post(t, "/admin/fleet/cohort", cohortPost(sum.Snapshot, "pilot", fleetMacA, fleetMacB, fleetMacC), a.cookie(t, auth.RoleAdmin), true)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", rec.Code)
	}
	mustContain(t, rec.Body.String(), "2 of 3")
	mustNotContain(t, rec.Body.String(), "ThrottlingException", "TransactWriteItems")
}

func TestGroupModeForm_ShowsReadinessBesideTheConfirmation(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, s, nil)
	_, body := adminGet(t, a, "/groups/pilot")
	m := configEditDetails.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no group settings form")
	}
	mustContain(t, m[1], "12 of 120 Macs", "3 are unmeasured")

	// No snapshot yet: the form is still there, without figures.
	s2 := fake.New()
	s2.AddGroup(store.Group{Name: "pilot", ClientMode: store.ClientModeLockdown})
	a2 := newFleetApp(t, s2, s2, nil)
	_, body = adminGet(t, a2, "/groups/pilot")
	m = configEditDetails.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no group settings form without a snapshot")
	}
	mustNotContain(t, m[1], "of 0 Macs")
}
