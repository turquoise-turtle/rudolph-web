package web_test

// Top unknowns (owner, 2026-11-11): the binaries no rule decided, for deciding what to allow or block next.
//   - GET /events/top-unknown?days=<1|7|30|90>&decision=<all|blocked|allowed>, for viewers: days defaults to 7 and
//     decision to all (lists Unknown, UnknownBlocked, UnknownAllowed). One store call, TopBinaries. Other values: 400.
//   - At the top, a GET form with a dropdown (select name="decision": all, blocked, allowed) and the window links,
//     which keep the decision. The top-blocked and top-allowed pages link to it, and it to them.
//   - Rows keep the ranker's order (no global rule first, then by Macs). A row with GlobalRule says "now has a global
//     rule" with the rule's type and identifier (e.g. Signing ID TEAM:covered) and offers no "Add a rule" link.
//   - Admins with editing on get "Add a rule" on each other row: a link to GET /admin/rules/new with the row's sha256,
//     signing_id and team_id. That page (admins, editing on; 404 otherwise, like other editing routes) shows one
//     add-rule form per identifier that is valid for its type (signing ID first, then team ID, then binary), each
//     posting to the existing /admin/rules with target global and that type and identifier filled in. Invalid
//     identifiers are left out; none valid is 400.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

var (
	unkSHA     = strings.Repeat("d4", 32)
	coveredSHA = strings.Repeat("e5", 32)
)

func unknownStore() *fake.Store {
	s := fake.New()
	gen := fixedNow.Add(-30 * time.Minute)
	s.SetTopList(store.TopKindUnknown, 7, gen, []store.TopBinary{
		{FileSHA256: unkSHA, FileName: "zoom-helper", TeamID: "BJ4HAAB9B3", SigningID: "BJ4HAAB9B3:us.zoom.helper", Count: 40, Machines: 12, MachinesKnown: true},
		{FileSHA256: coveredSHA, FileName: "covered-tool", SigningID: "TEAMCOVER1:covered", Count: 90, Machines: 1, MachinesKnown: true, GlobalRule: "SigningID#TEAMCOVER1:covered"},
	})
	s.SetTopList(store.TopKindUnknownBlocked, 7, gen, []store.TopBinary{{FileSHA256: strings.Repeat("f6", 32), FileName: "blocked-only", Count: 3}})
	s.SetTopList(store.TopKindUnknownAllowed, 30, gen, []store.TopBinary{{FileSHA256: strings.Repeat("a7", 32), FileName: "allowed-30", Count: 3}})
	return s
}

func TestTopUnknown_PageAndFilter(t *testing.T) {
	s := unknownStore()
	a := newApp(t, s, 10)

	s.ResetCalls()
	code, body := a.viewerGet(t, "/events/top-unknown")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "zoom-helper", "covered-tool", `<select name="decision"`, `value="blocked"`, `value="allowed"`)
	if i, j := strings.Index(body, "zoom-helper"), strings.Index(body, "covered-tool"); i > j {
		t.Error("the ranker's order was not kept")
	}
	calls := s.Calls()
	if len(calls) != 1 || calls[0].Method != "TopBinaries" || calls[0].Filter != (store.TopFilter{Kind: store.TopKindUnknown, WindowDays: 7}) {
		t.Fatalf("calls %+v; want one TopBinaries for Unknown, 7 days", calls)
	}
	covered := ruleRow(t, body, coveredSHA)
	mustContain(t, covered, "now has a global rule", "TEAMCOVER1:covered")

	_, body = a.viewerGet(t, "/events/top-unknown?decision=blocked")
	mustContain(t, body, "blocked-only")
	mustNotContain(t, body, "zoom-helper")
	_, body = a.viewerGet(t, "/events/top-unknown?decision=allowed&days=30")
	mustContain(t, body, "allowed-30")
	// Window links keep the decision.
	mustContain(t, body, "decision=allowed")

	for _, bad := range []string{"/events/top-unknown?decision=maybe", "/events/top-unknown?days=5"} {
		if code, _ := a.viewerGet(t, bad); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", bad, code)
		}
	}
	_, body = a.viewerGet(t, "/events/top-blocked")
	mustContain(t, body, `href="/events/top-unknown`)
}

func TestTopUnknown_AddRuleLinksForAdmins(t *testing.T) {
	s := unknownStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)

	body := a.get(t, "/events/top-unknown", admin).Body.String()
	row := ruleRow(t, body, unkSHA)
	mustContain(t, row, "Add a rule", "/admin/rules/new?")
	mustNotContain(t, ruleRow(t, body, coveredSHA), "/admin/rules/new")

	_, viewerBody := a.viewerGet(t, "/events/top-unknown")
	mustNotContain(t, viewerBody, "/admin/rules/new")
	ro := newEditApp(t, s, false)
	mustNotContain(t, ro.get(t, "/events/top-unknown", ro.cookie(t, auth.RoleAdmin)).Body.String(), "/admin/rules/new")
}

func TestTopUnknown_PrefilledAddRulePage(t *testing.T) {
	s := unknownStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	q := url.Values{"sha256": {unkSHA}, "signing_id": {"BJ4HAAB9B3:us.zoom.helper"}, "team_id": {"BJ4HAAB9B3"}}

	rec := a.get(t, "/admin/rules/new?"+q.Encode(), admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if n := strings.Count(body, `action="/admin/rules"`); n != 3 {
		t.Errorf("%d add-rule forms, want 3 (signing ID, team ID, binary)", n)
	}
	mustContain(t, body, `value="BJ4HAAB9B3:us.zoom.helper"`, `value="BJ4HAAB9B3"`, `value="`+unkSHA+`"`, `name="target" value="global"`)
	if i, j, k := strings.Index(body, "us.zoom.helper"), strings.Index(body, `value="BJ4HAAB9B3"`), strings.Index(body, unkSHA); !(i < j && j < k) {
		t.Error("forms not in signing ID, team ID, binary order")
	}

	// An invalid team ID is left out; only invalid identifiers is 400.
	q.Set("team_id", "not a team")
	body = a.get(t, "/admin/rules/new?"+q.Encode(), admin).Body.String()
	if n := strings.Count(body, `action="/admin/rules"`); n != 2 {
		t.Errorf("%d forms with an invalid team ID, want 2", n)
	}
	bad := url.Values{"sha256": {"xyz"}, "team_id": {"lower"}}
	if rec := a.get(t, "/admin/rules/new?"+bad.Encode(), admin); rec.Code != http.StatusBadRequest {
		t.Errorf("no valid identifier: status %d, want 400", rec.Code)
	}

	if rec := a.get(t, "/admin/rules/new?"+q.Encode(), a.cookie(t, auth.RoleViewer)); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status %d, want 403", rec.Code)
	}
	ro := newEditApp(t, s, false)
	if rec := ro.get(t, "/admin/rules/new?"+q.Encode(), ro.cookie(t, auth.RoleAdmin)); rec.Code != http.StatusNotFound {
		t.Errorf("editing off: status %d, want 404", rec.Code)
	}
}

// The fake starts with no unknown lists computed, like a deployment before the ranker's first run after the upgrade.
func TestTopUnknown_NotComputedYet(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	code, body := a.viewerGet(t, "/events/top-unknown")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "not been computed yet")
}
