package web_test

// Editing the minimum Santa version from the web view (owner decision 4, after Rudolph 05f9516). The setting is
// Rudolph's (FleetHealth / Config, MinSantaVersion); the CLI changes it with
// `rudolph fleet min-santa-version (<version> | off | --default)`.
//
// Where: GET /admin/config (admins only, editing on or off, as today) has a section
//
//	<section id="min-santa-version"> ... </section>
//
// holding everything below. It shows the value in force and its source in <p data-min-santa-version>: the stored
// value with "set here" (a release, or off), or, with nothing stored, the default with "the default 2026.9"
// (Rudolph's fleethealth.DefaultMinSantaVersion). It shows the equivalent CLI commands, built by
// clicmd.Builder.FleetMinSantaVersion: `rudolph fleet min-santa-version off --yes` and
// `rudolph fleet min-santa-version --default --yes`, with the deployment's ENV= prefix when one is set.
//
// Admins with editing off see the section read-only: the value, its source and the commands, no form and no preview.
// Viewers cannot open /admin/config; they see the value in force and its source on /fleet (the same
// <p data-min-santa-version>, "the default" when nothing is stored), where admins with editing on also get a link to
// the section (href="/admin/config#min-santa-version").
//
// Admins with editing on also get, in the section:
//   - A preview form, <form method="get" action="/admin/config"> with a text input name="min_santa_version". It is a
//     plain GET, no script: the count runs once per Preview click, never per keystroke.
//   - A form turning the check off: POST /admin/config/min-santa-version with min_santa_version=off (a hidden input
//     name="min_santa_version" value="off"), shown unless off is in force.
//   - With a value stored, a form returning to the default: POST /admin/config/min-santa-version/default.
//   - A hint that the "update needed" marker follows at once, while the fleet job's Version list follows at the job's
//     next run; with a fleet function configured (Options.FleetRunner), the existing link to run it now,
//     href="/admin#run-fleet", as /fleet links it. No link without one.
//
// The preview (decision: "Preview" then "Save"): GET /admin/config?min_santa_version=<candidate>, admins with editing
// on only. The candidate is checked with Rudolph's fleethealth.ValidateMinSantaVersion first: a refused value is a 400
// with Rudolph's message ("must be a release such as 2026.10"), reading no Macs. Otherwise the page walks the machine
// list, the sensor data the machine list already reads (store.MachineReader.ListMachines, no filter, Limit
// store.MaxPageSize, following the cursor), over at most 5,000 Macs (50 pages), and counts the Macs the marker would
// call "update needed" at the candidate and at the value in force, each as "<n> of <m> Macs", where m is the Macs
// walked; Macs whose version cannot be read count in m but never as below. The two counts are in
// <p data-preview="candidate"> (naming the candidate) and <p data-preview="in-force"> (naming the value in force).
// When the walk stops at 5,000 Macs the section says the counts are of the first 5,000. The preview then offers a
// Save form, POST /admin/config/min-santa-version with a hidden min_santa_version holding the candidate as Rudolph
// stores it (trimmed, OFF as off), shows the command for it (`rudolph fleet min-santa-version <candidate> --yes`), and
// keeps the candidate in the text input. A preview writes nothing. GET /admin/config without the parameter, or for an
// admin with editing off, reads no Macs.
//
// The writes, routes that exist only with editing on (404 otherwise), admins only (403 for viewers), same-origin only
// (403), like every other admin form:
//   - POST /admin/config/min-santa-version (min_santa_version=<release or off>): store.Writer.SetMinSantaVersion. A
//     value Rudolph refuses, or none, is 400 with Rudolph's message; nothing is written or audited.
//   - POST /admin/config/min-santa-version/default: store.Writer.ClearMinSantaVersion. With nothing stored
//     (store.ErrNothingToChange) it is not an error: the admin lands on a page saying nothing changed, and nothing is
//     audited.
//   - Success redirects (303) to /admin/config (a query string may say what happened).
//   - Each change is audited by the store exactly as Rudolph's CLI does: action fleet.min-santa-version, target fleet,
//     and the CLI's summary (internal/store/fake/min_santa_version_test.go).
//   - After a write from this handler the "update needed" marker follows at once, without waiting out the 60-second
//     cache (min_santa_version_test.go).

import (
	"context"
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
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

var (
	minVersionSectionRe = regexp.MustCompile(`(?s)<section[^>]*\bid="min-santa-version"[^>]*>(.*?)</section>`)
	minVersionShownRe   = regexp.MustCompile(`(?s)<p[^>]*\bdata-min-santa-version\b[^>]*>(.*?)</p>`)
	formRe              = regexp.MustCompile(`(?s)<form([^>]*)>(.*?)</form>`)
	formMethodRe        = regexp.MustCompile(`\bmethod="([^"]*)"`)
	formActionRe        = regexp.MustCompile(`\baction="([^"]*)"`)
)

func previewRe(which string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)<p[^>]*\bdata-preview="` + which + `"[^>]*>(.*?)</p>`)
}

// minVersionSection returns the section of /admin/config about the minimum Santa version.
func minVersionSection(t *testing.T, body string) string {
	t.Helper()
	m := minVersionSectionRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf(`no <section id="min-santa-version"> on the page; it reads %.300q`, visibleText(body))
	}
	return m[1]
}

// minVersionShown is the text of the <p data-min-santa-version> in body, or "" when there is none.
func minVersionShown(body string) string {
	m := minVersionShownRe.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return visibleText(m[1])
}

type pageForm struct{ method, action, body string }

func formsIn(html string) []pageForm {
	var out []pageForm
	for _, m := range formRe.FindAllStringSubmatch(html, -1) {
		f := pageForm{body: m[2]}
		if mm := formMethodRe.FindStringSubmatch(m[1]); mm != nil {
			f.method = strings.ToLower(mm[1])
		}
		if am := formActionRe.FindStringSubmatch(m[1]); am != nil {
			f.action = am[1]
		}
		out = append(out, f)
	}
	return out
}

func formTo(forms []pageForm, method, action string) (pageForm, bool) {
	for _, f := range forms {
		if f.method == method && f.action == action {
			return f, true
		}
	}
	return pageForm{}, false
}

func listMachinesCalls(s *fake.Store) []fake.Call {
	var out []fake.Call
	for _, c := range s.Calls() {
		if c.Method == "ListMachines" {
			out = append(out, c)
		}
	}
	return out
}

func minVersionEntriesOf(t *testing.T, s *fake.Store) []store.AuditEntry {
	t.Helper()
	return fleetAuditOf(t, s, "fleet.min-santa-version")
}

func storedMinVersionOf(t *testing.T, s *fake.Store) string {
	t.Helper()
	cfg, err := s.FleetHealthConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cfg.MinSantaVersion
}

// postFollowing posts as an admin and, on a 303, checks it leads to /admin/config and loads it.
func postFollowing(t *testing.T, a *app, path string, form url.Values) (int, string) {
	t.Helper()
	admin := a.cookie(t, auth.RoleAdmin)
	rec := a.post(t, path, form, admin, true)
	if rec.Code != http.StatusSeeOther {
		return rec.Code, rec.Body.String()
	}
	loc := rec.Header().Get("Location")
	if u, err := url.Parse(loc); err != nil || u.Path != "/admin/config" {
		t.Errorf("POST %s redirects to %q, want /admin/config", path, loc)
	}
	next := a.get(t, loc, admin)
	return next.Code, next.Body.String()
}

func TestMinSantaVersionSection_ShowsTheValueInForceAndItsSource(t *testing.T) {
	for _, c := range []struct {
		stored string
		want   []string
		not    []string
	}{
		{"", []string{"2026.9", "the default 2026.9"}, []string{"set here"}},
		{"2026.10", []string{"2026.10", "set here"}, []string{"the default"}},
		{"off", []string{"off", "set here"}, []string{"the default"}},
	} {
		for _, writable := range []bool{true, false} {
			s := versionStore()
			s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: c.stored})
			a := newEditApp(t, s, writable)
			code, body := adminGet(t, a, "/admin/config")
			if code != http.StatusOK {
				t.Fatalf("stored %q, editing %v: status %d", c.stored, writable, code)
			}
			section := minVersionSection(t, body)
			shown := minVersionShown(section)
			if shown == "" {
				t.Fatalf("stored %q, editing %v: no <p data-min-santa-version> in the section", c.stored, writable)
			}
			for _, w := range c.want {
				if !strings.Contains(shown, w) {
					t.Errorf("stored %q, editing %v: %q does not say %q", c.stored, writable, shown, w)
				}
			}
			for _, n := range c.not {
				if strings.Contains(shown, n) {
					t.Errorf("stored %q, editing %v: %q says %q", c.stored, writable, shown, n)
				}
			}
			mustContain(t, section, "rudolph fleet min-santa-version off --yes", "rudolph fleet min-santa-version --default --yes")
		}
	}
}

func TestMinSantaVersionSection_FormsForAdminsWithEditing(t *testing.T) {
	s := versionStore()
	a := newEditApp(t, s, true)
	s.ResetCalls()
	_, body := adminGet(t, a, "/admin/config")
	section := minVersionSection(t, body)
	forms := formsIn(section)
	preview, ok := formTo(forms, "get", "/admin/config")
	if !ok {
		t.Fatalf("no preview form (method get, action /admin/config) in the section; forms %+v", forms)
	}
	mustContain(t, preview.body, `name="min_santa_version"`)
	off, ok := formTo(forms, "post", "/admin/config/min-santa-version")
	if !ok {
		t.Fatal("no form turning the check off (POST /admin/config/min-santa-version)")
	}
	mustContain(t, off.body, `name="min_santa_version"`, `value="off"`)
	if _, ok := formTo(forms, "post", "/admin/config/min-santa-version/default"); ok {
		t.Error("a form returning to the default while nothing is stored")
	}
	if previewRe("candidate").MatchString(section) || previewRe("in-force").MatchString(section) {
		t.Error("preview counts without a value to preview")
	}
	if n := len(listMachinesCalls(s)); n != 0 {
		t.Errorf("the page without a preview read the machine list %d times", n)
	}
	text := visibleText(section)
	if !strings.Contains(text, "next") || !strings.Contains(strings.ToLower(text), "fleet") {
		t.Errorf("the section does not say the fleet's Version list follows at the fleet job's next run: %q", text)
	}
	mustNotContain(t, section, `href="/admin#run-fleet"`)

	s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: "2026.10"})
	_, body = adminGet(t, a, "/admin/config")
	forms = formsIn(minVersionSection(t, body))
	if _, ok := formTo(forms, "post", "/admin/config/min-santa-version/default"); !ok {
		t.Error("no form returning to the default while 2026.10 is stored")
	}

	s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: "off"})
	_, body = adminGet(t, a, "/admin/config")
	forms = formsIn(minVersionSection(t, body))
	if f, ok := formTo(forms, "post", "/admin/config/min-santa-version"); ok && strings.Contains(f.body, `value="off"`) {
		t.Error("a form turning the check off while it is off")
	}
	if _, ok := formTo(forms, "get", "/admin/config"); !ok {
		t.Error("no preview form while the check is off; a release can still be set")
	}
}

func TestMinSantaVersionSection_LinksTheFleetRunWhenItCanRun(t *testing.T) {
	s := versionStore()
	a := newFleetApp(t, s, s, func(o *web.Options) { o.FleetRunner = &fleetRunner{} })
	_, body := adminGet(t, a, "/admin/config")
	mustContain(t, minVersionSection(t, body), `href="/admin#run-fleet"`)
}

func TestMinSantaVersionSection_ReadOnlyWithEditingOff(t *testing.T) {
	s := versionStore()
	s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: "2026.10"})
	a := newEditApp(t, s, false)
	s.ResetCalls()
	_, body := adminGet(t, a, "/admin/config?min_santa_version=2027.9")
	section := minVersionSection(t, body)
	if forms := formsIn(section); len(forms) != 0 {
		t.Errorf("an admin with editing off gets %d forms in the section", len(forms))
	}
	if previewRe("candidate").MatchString(section) {
		t.Error("an admin with editing off gets a preview")
	}
	if n := len(listMachinesCalls(s)); n != 0 {
		t.Errorf("a preview request with editing off read the machine list %d times", n)
	}
	if shown := minVersionShown(section); !strings.Contains(shown, "2026.10") {
		t.Errorf("the section shows %q, want the stored 2026.10", shown)
	}
}

// Viewers cannot open /admin/config, so /fleet shows them the value in force and its source.
func TestFleetPage_ShowsTheMinSantaVersionInForce(t *testing.T) {
	s := fleetStore()
	a := newFleetApp(t, s, s, nil)
	_, body := a.viewerGet(t, "/fleet")
	if shown := minVersionShown(body); !strings.Contains(shown, "2026.9") || !strings.Contains(shown, "the default") {
		t.Errorf("/fleet shows %q, want the default 2026.9 said to be the default", shown)
	}
	mustNotContain(t, body, `href="/admin/config#min-santa-version"`)

	s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: "2026.10"})
	_, body = a.viewerGet(t, "/fleet")
	if shown := minVersionShown(body); !strings.Contains(shown, "2026.10") || strings.Contains(shown, "the default") {
		t.Errorf("/fleet shows %q, want the stored 2026.10, not the default", shown)
	}
	s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: "off"})
	_, body = a.viewerGet(t, "/fleet")
	if shown := minVersionShown(body); !strings.Contains(shown, "off") {
		t.Errorf("/fleet shows %q, want off", shown)
	}
	_, body = adminGet(t, a, "/fleet")
	mustContain(t, body, `href="/admin/config#min-santa-version"`)
}

func TestMinSantaVersionPreview_CountsBothValues(t *testing.T) {
	for _, c := range []struct {
		stored, candidate, normalised string
		candidateCount, inForceCount  string
		inForce                       string
	}{
		{"", "2027.9", "2027.9", "4 of 6 Macs", "2 of 6 Macs", "2026.9"},
		{"", " 2026.10 ", "2026.10", "3 of 6 Macs", "2 of 6 Macs", "2026.9"},
		{"2026.10", "OFF", "off", "0 of 6 Macs", "3 of 6 Macs", "2026.10"},
		{"off", "2026.9", "2026.9", "2 of 6 Macs", "0 of 6 Macs", "off"},
	} {
		s := versionStore()
		s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: c.stored})
		a := newEditApp(t, s, true)
		s.ResetCalls()
		code, body := adminGet(t, a, "/admin/config?"+url.Values{"min_santa_version": {c.candidate}}.Encode())
		if code != http.StatusOK {
			t.Fatalf("preview %q: status %d", c.candidate, code)
		}
		section := minVersionSection(t, body)
		cand := previewRe("candidate").FindStringSubmatch(section)
		now := previewRe("in-force").FindStringSubmatch(section)
		if cand == nil || now == nil {
			t.Fatalf("preview %q: no <p data-preview=\"candidate\"> and <p data-preview=\"in-force\"> in %q", c.candidate, visibleText(section))
		}
		if text := visibleText(cand[1]); !strings.Contains(text, c.candidateCount) || !strings.Contains(text, c.normalised) {
			t.Errorf("preview %q: candidate reads %q, want %q naming %s", c.candidate, text, c.candidateCount, c.normalised)
		}
		if text := visibleText(now[1]); !strings.Contains(text, c.inForceCount) || !strings.Contains(text, c.inForce) {
			t.Errorf("preview %q: in force reads %q, want %q naming %s", c.candidate, text, c.inForceCount, c.inForce)
		}
		save, ok := formTo(formsIn(section), "post", "/admin/config/min-santa-version")
		if !ok || !strings.Contains(save.body, `name="min_santa_version"`) || !strings.Contains(save.body, `value="`+c.normalised+`"`) {
			t.Errorf("preview %q: no Save form posting min_santa_version=%s; forms %+v", c.candidate, c.normalised, formsIn(section))
		}
		mustContain(t, section, "rudolph fleet min-santa-version "+c.normalised+" --yes")
		for _, call := range listMachinesCalls(s) {
			if call.Page.Limit != store.MaxPageSize || call.Filter != (store.MachineFilter{}) {
				t.Errorf("preview %q: ListMachines %+v, want no filter and Limit %d", c.candidate, call, store.MaxPageSize)
			}
		}
		if n := len(listMachinesCalls(s)); n != 1 {
			t.Errorf("preview %q: %d ListMachines calls for 6 Macs, want 1", c.candidate, n)
		}
		if got := storedMinVersionOf(t, s); got != c.stored {
			t.Errorf("preview %q changed the stored value to %q", c.candidate, got)
		}
		if n := len(minVersionEntriesOf(t, s)); n != 0 {
			t.Errorf("preview %q was audited", c.candidate)
		}
	}
}

// The preview pages through the machine list, and stops at 5,000 Macs, saying so.
func TestMinSantaVersionPreview_PagesAndIsBounded(t *testing.T) {
	s := fake.New()
	for i := range 250 {
		s.AddMachine(store.Machine{MachineID: fmt.Sprintf("m-%05d", i), SerialNum: fmt.Sprintf("C%05d", i), Hostname: fmt.Sprintf("h-%05d", i),
			SantaVersion: map[bool]string{true: "2026.8", false: "2026.10"}[i%5 == 0], LastSeen: fixedNow})
	}
	a := newEditApp(t, s, true)
	s.ResetCalls()
	_, body := adminGet(t, a, "/admin/config?min_santa_version=2026.9")
	section := minVersionSection(t, body)
	if cand := previewRe("candidate").FindStringSubmatch(section); cand == nil || !strings.Contains(visibleText(cand[1]), "50 of 250 Macs") {
		t.Errorf("250 Macs, 50 below 2026.9: the candidate reads %q", visibleText(section))
	}
	calls := listMachinesCalls(s)
	if len(calls) != 3 || calls[0].Page.Cursor != "" || calls[1].Page.Cursor == "" || calls[2].Page.Cursor == "" {
		t.Errorf("250 Macs: ListMachines calls %+v, want 3 pages of %d following the cursor", calls, store.MaxPageSize)
	}
	if strings.Contains(visibleText(section), "5,000") {
		t.Error("250 Macs: the section says the counts stopped at 5,000")
	}

	big := fake.New()
	for i := range 5001 {
		big.AddMachine(store.Machine{MachineID: fmt.Sprintf("b-%05d", i), SerialNum: fmt.Sprintf("B%05d", i), Hostname: fmt.Sprintf("b-%05d", i),
			SantaVersion: "2026.8", LastSeen: fixedNow})
	}
	a = newEditApp(t, big, true)
	big.ResetCalls()
	_, body = adminGet(t, a, "/admin/config?min_santa_version=2026.9")
	section = minVersionSection(t, body)
	if n := len(listMachinesCalls(big)); n > 50 {
		t.Errorf("5,001 Macs: %d ListMachines calls, want at most 50", n)
	}
	if cand := previewRe("candidate").FindStringSubmatch(section); cand == nil || !strings.Contains(visibleText(cand[1]), "5,000 of 5,000 Macs") {
		t.Errorf("5,001 Macs: the candidate does not count the first 5,000; the section reads %q", visibleText(section))
	}
	if !strings.Contains(visibleText(section), "first 5,000") {
		t.Errorf("5,001 Macs: the section does not say the counts are of the first 5,000 Macs: %q", visibleText(section))
	}
}

func TestMinSantaVersionPreview_RefusesWhatRudolphRefuses(t *testing.T) {
	s := versionStore()
	a := newEditApp(t, s, true)
	for _, bad := range []string{"latest", "2026", "2026.x", "2026.10\u202e", "<script>alert(1)</script>"} {
		s.ResetCalls()
		code, body := adminGet(t, a, "/admin/config?"+url.Values{"min_santa_version": {bad}}.Encode())
		if code != http.StatusBadRequest {
			t.Errorf("preview %q: status %d, want 400", bad, code)
		}
		if !strings.Contains(body, "must be a release such as 2026.10") {
			t.Errorf("preview %q: the page does not give Rudolph's message", bad)
		}
		mustNotContain(t, body, "<script>alert(1)</script>", "\u202e")
		if n := len(listMachinesCalls(s)); n != 0 {
			t.Errorf("preview %q: read the machine list %d times", bad, n)
		}
	}
}

func TestMinSantaVersionEdit_SetOffAndDefault(t *testing.T) {
	s := versionStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)

	rec := a.post(t, "/admin/config/min-santa-version", url.Values{"min_santa_version": {"2026.10"}}, admin, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("set: status %d, want 303", rec.Code)
	}
	if u, _ := url.Parse(rec.Header().Get("Location")); u == nil || u.Path != "/admin/config" {
		t.Errorf("set: Location %q, want /admin/config", rec.Header().Get("Location"))
	}
	if got := storedMinVersionOf(t, s); got != "2026.10" {
		t.Errorf("set: stored %q, want 2026.10", got)
	}
	entries := minVersionEntriesOf(t, s)
	if len(entries) != 1 || entries[0].Target != "fleet" || entries[0].Summary != "set the minimum Santa version to 2026.10 (was the default 2026.9)" ||
		!strings.HasPrefix(entries[0].Actor, "web:") || !strings.Contains(entries[0].Actor, "vera@example.com") {
		t.Errorf("set: audit %+v; want the CLI's entry on fleet by the web actor", entries)
	}

	if code, _ := postFollowing(t, a, "/admin/config/min-santa-version", url.Values{"min_santa_version": {"off"}}); code != http.StatusOK {
		t.Errorf("off: final status %d", code)
	}
	if got := storedMinVersionOf(t, s); got != "off" {
		t.Errorf("off: stored %q", got)
	}
	if entries := minVersionEntriesOf(t, s); len(entries) != 2 || entries[0].Summary != "turned the minimum Santa version check off (was 2026.10)" {
		t.Errorf("off: audit %+v", entries)
	}

	if code, _ := postFollowing(t, a, "/admin/config/min-santa-version/default", url.Values{}); code != http.StatusOK {
		t.Errorf("default: final status %d", code)
	}
	if got := storedMinVersionOf(t, s); got != "" {
		t.Errorf("default: stored %q, want nothing", got)
	}
	if entries := minVersionEntriesOf(t, s); len(entries) != 3 ||
		entries[0].Summary != "cleared the stored minimum Santa version (was off); the default 2026.9 applies" {
		t.Errorf("default: audit %+v", entries)
	}

	_, before := adminGet(t, a, "/admin/config")
	if containsFold(visibleText(before), "nothing changed") {
		t.Fatal("/admin/config says nothing changed before anything was asked")
	}
	code, body := postFollowing(t, a, "/admin/config/min-santa-version/default", url.Values{})
	if code != http.StatusOK || !containsFold(visibleText(body), "nothing changed") {
		t.Errorf("default with nothing stored: status %d, page %.300q; want 200 saying nothing changed", code, visibleText(body))
	}
	if n := len(minVersionEntriesOf(t, s)); n != 3 {
		t.Errorf("default with nothing stored was audited (%d entries)", n)
	}
}

func TestMinSantaVersionEdit_Refusals(t *testing.T) {
	s := versionStore()
	s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: "2026.10"})
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	for name, form := range map[string]url.Values{
		"latest":      {"min_santa_version": {"latest"}},
		"no release":  {"min_santa_version": {"2026"}},
		"bidi":        {"min_santa_version": {"2026.10\u202e"}},
		"empty":       {"min_santa_version": {""}},
		"no field":    {},
		"script":      {"min_santa_version": {"<script>alert(1)</script>"}},
		"over 32":     {"min_santa_version": {"2026." + strings.Repeat("1", 30)}},
		"just spaces": {"min_santa_version": {"   "}},
	} {
		rec := a.post(t, "/admin/config/min-santa-version", form, admin, true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "must be a release such as 2026.10") {
			t.Errorf("%s: the page does not give Rudolph's message", name)
		}
		mustNotContain(t, rec.Body.String(), "<script>alert(1)</script>", "\u202e")
	}
	if got := storedMinVersionOf(t, s); got != "2026.10" {
		t.Errorf("refusals changed the stored value to %q", got)
	}
	if n := len(minVersionEntriesOf(t, s)); n != 0 {
		t.Errorf("refusals were audited (%d entries)", n)
	}
}

func TestMinSantaVersionEdit_AdminOnlySameOriginEditingOn(t *testing.T) {
	s := versionStore()
	a := newEditApp(t, s, true)
	form := url.Values{"min_santa_version": {"2026.10"}}
	for _, p := range []string{"/admin/config/min-santa-version", "/admin/config/min-santa-version/default"} {
		if rec := a.post(t, p, form, a.cookie(t, auth.RoleViewer), true); rec.Code != http.StatusForbidden {
			t.Errorf("viewer %s: status %d, want 403", p, rec.Code)
		}
		if rec := a.post(t, p, form, a.cookie(t, auth.RoleAdmin), false); rec.Code != http.StatusForbidden {
			t.Errorf("cross-origin %s: status %d, want 403", p, rec.Code)
		}
	}
	off := newEditApp(t, s, false)
	for _, p := range []string{"/admin/config/min-santa-version", "/admin/config/min-santa-version/default"} {
		if rec := off.post(t, p, form, off.cookie(t, auth.RoleAdmin), true); rec.Code != http.StatusNotFound {
			t.Errorf("editing off %s: status %d, want 404", p, rec.Code)
		}
	}
	if got := storedMinVersionOf(t, s); got != "" {
		t.Errorf("a refused request stored %q", got)
	}
	if n := len(minVersionEntriesOf(t, s)); n != 0 {
		t.Errorf("refused requests were audited (%d entries)", n)
	}
}

// A change made here shows on the machine pages at once, though the cached minimum is younger than 60 seconds.
func TestMinSantaVersionEdit_TheMarkerFollowsAtOnce(t *testing.T) {
	s := versionStore()
	c := &clock{now: fixedNow}
	a := newFleetApp(t, s, s, func(o *web.Options) { o.Now = c.Now })
	note := func(id string) string {
		t.Helper()
		_, body := a.viewerGet(t, "/machines/"+id)
		return santaLine(visibleText(body))
	}
	if line := note("m-min"); strings.Contains(line, "update needed") {
		t.Fatalf("m-min (2026.9) is marked below the default: %q", line)
	}
	steps := []struct {
		path     string
		form     url.Values
		id, want string
		wantMark bool
	}{
		{"/admin/config/min-santa-version", url.Values{"min_santa_version": {"2026.10"}}, "m-min", "update needed (below 2026.10)", true},
		{"/admin/config/min-santa-version", url.Values{"min_santa_version": {"off"}}, "m-legacy", "", false},
		{"/admin/config/min-santa-version/default", url.Values{}, "m-old", "update needed (below 2026.9)", true},
	}
	for _, step := range steps {
		c.advance(time.Second)
		if rec := a.post(t, step.path, step.form, a.cookie(t, auth.RoleAdmin), true); rec.Code != http.StatusSeeOther {
			t.Fatalf("POST %s %v: status %d", step.path, step.form, rec.Code)
		}
		line := note(step.id)
		if step.wantMark && !strings.Contains(line, step.want) {
			t.Errorf("after POST %s %v, %s reads %q; want %q at once", step.path, step.form, step.id, line, step.want)
		}
		if !step.wantMark && strings.Contains(line, "update needed") {
			t.Errorf("after POST %s %v, %s reads %q; want no marker at once", step.path, step.form, step.id, line)
		}
	}
}
