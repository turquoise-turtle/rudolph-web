package web_test

// "Run the ranker now", and both run buttons on the admin home (owner request, 2026-11-25). An admin with editing on
// can start Rudolph's ranker (the Lambda ${prefix}_rudolph_ranker, which otherwise rebuilds the top lists on its
// schedule, hourly by default) as the fleet job is started (fleet_run_test.go):
//
//   - web.RankerRunner{ Start(ctx) error } starts one asynchronous run (production: internal/awslambda's runner, an
//     Invoke with InvocationType Event; the ranker takes no input). It comes in through web.Options.RankerRunner; nil
//     (no RUDOLPH_WEB_RANKER_FUNCTION), or no Writer, means no button, no link and no route.
//   - POST /admin/ranker/run exists only with editing on and a ranker runner: 404 otherwise (a fleet runner alone does
//     not make it). Viewers 403, cross-site 403, GET 405. It takes no fields.
//   - The guard is the store's: store.Writer.RequestRankerRun(ctx, actor, now, start) refuses with a
//     *store.RankerRunTooSoonError{RetryAt} within store.RankerRunInterval (15 minutes) of the last ranker request or
//     of the current top lists' GeneratedAt (the blocked list over 1 day). The handler passes Options.Now and the
//     runner's Start. The fleet job's guard and the ranker's are separate: neither run blocks the other.
//   - Started: one audit entry ranker.run on target ranker by the web actor, and 303 to /admin?started=ranker, which
//     shows "Started: the top lists update in a few minutes."
//   - Refused: 409 saying "Not started" and the wait in whole minutes rounded up ("in 10 minutes", "in 1 minute").
//   - Start failed: 500 saying the ranker "could not be started", without the AWS error; the marker kept, so a retry at
//     once is 409 without asking Lambda again; nothing audited.
//
// Both buttons live on /admin, the admin home: "Run the fleet job now" (a form posting to /admin/fleet/run) and "Run
// the ranker now" (a form posting to /admin/ranker/run), each only with editing on and its runner. A start redirects
// back to /admin with its notice (/admin?started=fleet or /admin?started=ranker); any other value shows no notice.
// /fleet no longer has the fleet button: admins with the fleet runner get a paragraph with a link to the admin page,
// "Run it now on the admin page" (href /admin, optionally with a fragment). The top-list pages (/events/top-blocked,
// /events/top-allowed, /events/top-unknown) give admins with the ranker runner the same link. Viewers see neither
// buttons nor links, and the link costs a viewer's page no extra store reads.

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

var _ web.RankerRunner = (*fleetRunner)(nil)

// newRunsApp is the fleet app with editing on (unless w is nil), the fleet and ranker runners (none when nil) and a
// settable clock starting at fixedNow.
func newRunsApp(t *testing.T, s *fake.Store, w store.Writer, fleet, ranker *fleetRunner) (*app, *clock) {
	t.Helper()
	c := &clock{now: fixedNow}
	a := newFleetApp(t, s, w, func(o *web.Options) {
		o.Now = c.Now
		if fleet != nil {
			o.FleetRunner = fleet
		}
		if ranker != nil {
			o.RankerRunner = ranker
		}
	})
	return a, c
}

// rankerStore is the fleet store with top lists the ranker computed an hour before fixedNow.
func rankerStore() *fake.Store {
	s := fleetStore()
	setTopLists(s, fixedNow.Add(-time.Hour))
	return s
}

func setTopLists(s *fake.Store, generatedAt time.Time) {
	for _, k := range []store.TopKind{store.TopKindBlocked, store.TopKindAllowed, store.TopKindUnknown} {
		for _, days := range []int{1, 7} {
			s.SetTopList(k, days, generatedAt, []store.TopBinary{{FileSHA256: strings.Repeat("b", 64), FileName: "listed-binary", Count: 3}})
		}
	}
}

var (
	rankerRunForm  = regexp.MustCompile(`(?s)<form[^>]*action="/admin/ranker/run"[^>]*>(.*?)</form>`)
	paragraphRe    = regexp.MustCompile(`(?s)<p[ >].*?</p>`)
	tagRe          = regexp.MustCompile(`<[^>]*>`)
	adminHrefRe    = regexp.MustCompile(`<a [^>]*href="/admin(#[^"]*)?"`)
	runItNowOnPage = "Run it now on the admin page"
)

// hasAdminRunLink reports whether a paragraph of the page reads "Run it now on the admin page" and links to /admin.
func hasAdminRunLink(body string) bool {
	for _, p := range paragraphRe.FindAllString(body, -1) {
		if strings.Contains(tagRe.ReplaceAllString(p, ""), runItNowOnPage) && adminHrefRe.MatchString(p) {
			return true
		}
	}
	return false
}

func TestRankerRun_StartsAuditsAndSaysSo(t *testing.T) {
	s := rankerStore()
	r := &fleetRunner{}
	a, _ := newRunsApp(t, s, s, nil, r)
	admin := a.cookie(t, auth.RoleAdmin)

	see303(t, a.post(t, "/admin/ranker/run", url.Values{}, admin, true), "/admin?started=ranker")
	if n := r.count(); n != 1 {
		t.Errorf("the ranker was started %d times, want once", n)
	}
	entries := fleetAuditOf(t, s, store.ActionRankerRun)
	if len(entries) != 1 {
		t.Fatalf("audit %+v; want one ranker.run entry", entries)
	}
	e := entries[0]
	if e.Target != "ranker" || !strings.HasPrefix(e.Actor, "web:") || !strings.Contains(e.Actor, "vera@example.com") {
		t.Errorf("audit %+v; want ranker.run on ranker by the web actor", e)
	}
	if !containsFold(e.Summary, "ranker") || len(e.Summary) > 200 {
		t.Errorf("audit summary %q; want a short one naming the ranker", e.Summary)
	}
	if n := len(fleetAuditOf(t, s, store.ActionFleetRun)); n != 0 {
		t.Errorf("starting the ranker recorded %d fleet.run entries", n)
	}

	_, body := adminGet(t, a, "/admin?started=ranker")
	mustContain(t, body, "Started: the top lists update in a few minutes.")
	mustNotContain(t, body, "Started: the fleet lists update")
	_, body = adminGet(t, a, "/admin")
	mustNotContain(t, body, "Started: the top lists update")
}

func TestRankerRun_RefusedWithinFifteenMinutesOfTheLastRequest(t *testing.T) {
	s := rankerStore()
	r := &fleetRunner{}
	a, c := newRunsApp(t, s, s, nil, r)
	admin := a.cookie(t, auth.RoleAdmin)

	see303(t, a.post(t, "/admin/ranker/run", url.Values{}, admin, true), "/admin?started=ranker")
	rec := a.post(t, "/admin/ranker/run", url.Values{}, admin, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a second request at once: status %d, want 409", rec.Code)
	}
	mustContain(t, rec.Body.String(), "Not started", "ranker", "in 15 minutes")

	c.advance(14 * time.Minute)
	rec = a.post(t, "/admin/ranker/run", url.Values{}, admin, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("14 minutes later: status %d, want 409", rec.Code)
	}
	mustContain(t, rec.Body.String(), "in 1 minute")
	mustNotContain(t, rec.Body.String(), "1 minutes")

	c.advance(time.Minute)
	see303(t, a.post(t, "/admin/ranker/run", url.Values{}, admin, true), "/admin?started=ranker")
	if n := r.count(); n != 2 {
		t.Errorf("%d starts, want 2", n)
	}
	if n := len(fleetAuditOf(t, s, store.ActionRankerRun)); n != 2 {
		t.Errorf("%d ranker.run entries, want 2: refusals are not audited", n)
	}
}

func TestRankerRun_RefusedWhileTheTopListsAreFresh(t *testing.T) {
	s := fleetStore()
	setTopLists(s, fixedNow.Add(-5*time.Minute))
	r := &fleetRunner{}
	a, c := newRunsApp(t, s, s, nil, r)
	admin := a.cookie(t, auth.RoleAdmin)

	rec := a.post(t, "/admin/ranker/run", url.Values{}, admin, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("top lists 5 minutes old: status %d, want 409", rec.Code)
	}
	mustContain(t, rec.Body.String(), "Not started", "in 10 minutes")
	if n := r.count(); n != 0 {
		t.Errorf("%d starts, want none", n)
	}

	c.advance(10 * time.Minute)
	see303(t, a.post(t, "/admin/ranker/run", url.Values{}, admin, true), "/admin?started=ranker")
}

func TestRankerRun_NoListsYetCanStart(t *testing.T) {
	s := fake.New()
	r := &fleetRunner{}
	a, _ := newRunsApp(t, s, s, nil, r)
	see303(t, a.post(t, "/admin/ranker/run", url.Values{}, a.cookie(t, auth.RoleAdmin), true), "/admin?started=ranker")
	if n := r.count(); n != 1 {
		t.Errorf("before the ranker's first run: %d starts, want 1", n)
	}
}

func TestRankerRun_StartFailureKeepsTheMarker(t *testing.T) {
	s := rankerStore()
	r := &fleetRunner{err: errorString("operation error Lambda: Invoke, https response error StatusCode: 403, AccessDeniedException: not authorized")}
	a, _ := newRunsApp(t, s, s, nil, r)
	admin := a.cookie(t, auth.RoleAdmin)

	rec := a.post(t, "/admin/ranker/run", url.Values{}, admin, true)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("a failed start: status %d, want 500", rec.Code)
	}
	mustContain(t, rec.Body.String(), "could not be started")
	mustNotContain(t, rec.Body.String(), "AccessDeniedException", "StatusCode")
	if n := len(fleetAuditOf(t, s, store.ActionRankerRun)); n != 0 {
		t.Errorf("a run that never started was audited (%d entries)", n)
	}
	if rec := a.post(t, "/admin/ranker/run", url.Values{}, admin, true); rec.Code != http.StatusConflict {
		t.Errorf("a retry straight after a failed start: status %d, want 409", rec.Code)
	}
	if n := r.count(); n != 1 {
		t.Errorf("Lambda was asked %d times, want once", n)
	}
}

func TestRankerRun_AdminOnlySameOriginPostEditingAndFunction(t *testing.T) {
	s := rankerStore()
	r := &fleetRunner{}
	a, _ := newRunsApp(t, s, s, nil, r)

	if rec := a.post(t, "/admin/ranker/run", url.Values{}, a.cookie(t, auth.RoleViewer), true); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status %d, want 403", rec.Code)
	}
	if rec := a.post(t, "/admin/ranker/run", url.Values{}, a.cookie(t, auth.RoleAdmin), false); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: status %d, want 403", rec.Code)
	}
	if rec := a.get(t, "/admin/ranker/run", a.cookie(t, auth.RoleAdmin)); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d, want 405", rec.Code)
	}

	off, _ := newRunsApp(t, s, nil, nil, r)
	if rec := off.post(t, "/admin/ranker/run", url.Values{}, off.cookie(t, auth.RoleAdmin), true); rec.Code != http.StatusNotFound {
		t.Errorf("editing off: status %d, want 404", rec.Code)
	}
	fleetOnly, _ := newRunsApp(t, s, s, &fleetRunner{}, nil)
	if rec := fleetOnly.post(t, "/admin/ranker/run", url.Values{}, fleetOnly.cookie(t, auth.RoleAdmin), true); rec.Code != http.StatusNotFound {
		t.Errorf("no ranker function (a fleet one only): status %d, want 404", rec.Code)
	}
	rankerOnly, _ := newRunsApp(t, s, s, nil, &fleetRunner{})
	if rec := rankerOnly.post(t, "/admin/fleet/run", url.Values{}, rankerOnly.cookie(t, auth.RoleAdmin), true); rec.Code != http.StatusNotFound {
		t.Errorf("POST /admin/fleet/run with a ranker function only: status %d, want 404", rec.Code)
	}

	if n := r.count(); n != 0 {
		t.Errorf("refused requests started the ranker %d times", n)
	}
	if n := len(fleetAuditOf(t, s, store.ActionRankerRun)); n != 0 {
		t.Errorf("refused requests were audited (%d entries)", n)
	}
}

// Each runner is started through its own guard: a run of one never refuses the other, and each Start goes to its
// own function.
func TestRuns_FleetAndRankerDoNotBlockEachOther(t *testing.T) {
	for _, first := range []string{"fleet", "ranker"} {
		s := rankerStore()
		fleet, ranker := &fleetRunner{}, &fleetRunner{}
		a, _ := newRunsApp(t, s, s, fleet, ranker)
		admin := a.cookie(t, auth.RoleAdmin)
		order := []string{"fleet", "ranker"}
		if first == "ranker" {
			order = []string{"ranker", "fleet"}
		}
		for _, job := range order {
			see303(t, a.post(t, "/admin/"+job+"/run", url.Values{}, admin, true), "/admin?started="+job)
		}
		if fleet.count() != 1 || ranker.count() != 1 {
			t.Errorf("%s first: fleet started %d times, ranker %d; want once each", first, fleet.count(), ranker.count())
		}
	}

	// Fresh results of one job do not refuse the other.
	s := fleetStore()
	sum := fleetSummary()
	sum.GeneratedAt = fixedNow.Add(-time.Minute)
	s.SetFleetSnapshot(fake.FleetSnapshot{Summary: sum})
	setTopLists(s, fixedNow.Add(-2*time.Hour))
	a, _ := newRunsApp(t, s, s, &fleetRunner{}, &fleetRunner{})
	see303(t, a.post(t, "/admin/ranker/run", url.Values{}, a.cookie(t, auth.RoleAdmin), true), "/admin?started=ranker")

	s = fleetStore()
	setTopLists(s, fixedNow.Add(-time.Minute))
	a, _ = newRunsApp(t, s, s, &fleetRunner{}, &fleetRunner{})
	see303(t, a.post(t, "/admin/fleet/run", url.Values{}, a.cookie(t, auth.RoleAdmin), true), "/admin?started=fleet")
}

func TestAdminPage_RunButtonsEachWithItsRunner(t *testing.T) {
	s := rankerStore()
	postsTo := func(body, action string) bool {
		return regexp.MustCompile(`<form[^>]*method="post"[^>]*action="` + regexp.QuoteMeta(action) + `"`).MatchString(body)
	}

	both, _ := newRunsApp(t, s, s, &fleetRunner{}, &fleetRunner{})
	_, body := adminGet(t, both, "/admin")
	m := rankerRunForm.FindStringSubmatch(body)
	if m == nil || !postsTo(body, "/admin/ranker/run") {
		t.Fatal("an admin with editing on and a ranker function has no form posting to /admin/ranker/run on /admin")
	}
	mustContain(t, m[1], `type="submit"`, "Run the ranker now")
	f := fleetRunForm.FindStringSubmatch(body)
	if f == nil || !postsTo(body, "/admin/fleet/run") {
		t.Fatal("an admin with editing on and a fleet function has no form posting to /admin/fleet/run on /admin")
	}
	mustContain(t, f[1], `type="submit"`, "Run the fleet job now")

	fleetOnly, _ := newRunsApp(t, s, s, &fleetRunner{}, nil)
	_, body = adminGet(t, fleetOnly, "/admin")
	mustContain(t, body, "/admin/fleet/run", "Run the fleet job now")
	mustNotContain(t, body, "/admin/ranker/run", "Run the ranker now")

	rankerOnly, _ := newRunsApp(t, s, s, nil, &fleetRunner{})
	_, body = adminGet(t, rankerOnly, "/admin")
	mustContain(t, body, "/admin/ranker/run", "Run the ranker now")
	mustNotContain(t, body, "/admin/fleet/run", "Run the fleet job now")

	editingOff, _ := newRunsApp(t, s, nil, &fleetRunner{}, &fleetRunner{})
	_, body = adminGet(t, editingOff, "/admin")
	mustNotContain(t, body, "/admin/ranker/run", "Run the ranker now", "/admin/fleet/run", "Run the fleet job now")
}

func TestAdminPage_StartedNotices(t *testing.T) {
	s := rankerStore()
	a, _ := newRunsApp(t, s, s, &fleetRunner{}, &fleetRunner{})
	for query, want := range map[string]string{
		"?started=fleet":  "Started: the fleet lists update in a few minutes.",
		"?started=ranker": "Started: the top lists update in a few minutes.",
	} {
		code, body := adminGet(t, a, "/admin"+query)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", query, code)
		}
		mustContain(t, body, want, `role="status"`)
	}
	for _, query := range []string{"", "?started=", "?started=other", "?started=%3Cb%3E", "?run=started"} {
		_, body := adminGet(t, a, "/admin"+query)
		mustNotContain(t, body, "Started: the fleet lists", "Started: the top lists")
	}
}

func TestFleetPage_LinksAdminsToTheAdminPageInsteadOfTheButton(t *testing.T) {
	s := rankerStore()
	a, _ := newRunsApp(t, s, s, &fleetRunner{}, nil)
	_, body := adminGet(t, a, "/fleet")
	if !hasAdminRunLink(body) {
		t.Errorf("an admin with a fleet function: /fleet has no paragraph %q linking to /admin", runItNowOnPage)
	}
	if fleetRunForm.MatchString(body) {
		t.Error("/fleet still has the form posting to /admin/fleet/run; the button is on /admin now")
	}
	mustNotContain(t, body, "Run the fleet job now", "/admin/ranker/run")

	_, body = a.viewerGet(t, "/fleet")
	mustNotContain(t, body, runItNowOnPage, "/admin/fleet/run")

	for name, other := range map[string]*app{
		"no fleet function": first(newRunsApp(t, s, s, nil, nil)),
		"a ranker only":     first(newRunsApp(t, s, s, nil, &fleetRunner{})),
		"editing off":       first(newRunsApp(t, s, nil, &fleetRunner{}, &fleetRunner{})),
	} {
		if _, body := adminGet(t, other, "/fleet"); strings.Contains(body, runItNowOnPage) {
			t.Errorf("%s: an admin's /fleet offers to run the fleet job from the admin page", name)
		}
	}
}

func TestTopListPages_LinkAdminsToTheAdminPage(t *testing.T) {
	s := rankerStore()
	a, _ := newRunsApp(t, s, s, nil, &fleetRunner{})
	empty := fake.New()
	notComputed, _ := newRunsApp(t, empty, empty, nil, &fleetRunner{})
	for _, path := range []string{"/events/top-blocked", "/events/top-allowed", "/events/top-unknown", "/events/top-blocked?days=1"} {
		code, body := adminGet(t, a, path)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", path, code)
		}
		if !hasAdminRunLink(body) {
			t.Errorf("%s: an admin with a ranker function has no paragraph %q linking to /admin", path, runItNowOnPage)
		}
		mustNotContain(t, body, "/admin/ranker/run", "Run the ranker now")

		_, body = adminGet(t, notComputed, path)
		if !hasAdminRunLink(body) {
			t.Errorf("%s, lists not computed yet: no link to run the ranker from the admin page", path)
		}

		_, body = a.viewerGet(t, path)
		mustNotContain(t, body, runItNowOnPage, "/admin/ranker/run")

		for _, other := range []*app{
			first(newRunsApp(t, s, s, nil, nil)),
			first(newRunsApp(t, s, s, &fleetRunner{}, nil)),
			first(newRunsApp(t, s, nil, &fleetRunner{}, &fleetRunner{})),
		} {
			_, body := adminGet(t, other, path)
			mustNotContain(t, body, runItNowOnPage)
		}
	}
}

// The link is for admins only and reads nothing: a viewer's page makes the same store calls with a ranker function as
// without.
func TestTopListPages_ViewerMakesNoExtraReads(t *testing.T) {
	callsOf := func(r *fleetRunner) []string {
		s := rankerStore()
		var a *app
		if r != nil {
			a, _ = newRunsApp(t, s, s, nil, r)
		} else {
			a, _ = newRunsApp(t, s, s, nil, nil)
		}
		s.ResetCalls()
		if code, _ := a.viewerGet(t, "/events/top-blocked"); code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		var out []string
		for _, c := range s.Calls() {
			out = append(out, c.Method)
		}
		return out
	}
	with, without := strings.Join(callsOf(&fleetRunner{}), ","), strings.Join(callsOf(nil), ",")
	if with != without {
		t.Errorf("a viewer's top list makes calls %s with a ranker function, %s without", with, without)
	}
}

func first(a *app, _ *clock) *app { return a }

type errorString string

func (e errorString) Error() string { return string(e) }
