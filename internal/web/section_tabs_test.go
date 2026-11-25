package web_test

// Sub-tabs for the Rules and Events sections (owner request, 2026-11-25), as the Fleet section has them ("fleetnav":
// <nav class="choices" aria-label="Fleet pages"> with aria-current="page" on the page shown):
//
//   - Rules: <nav class="choices" aria-label="Rules pages"> with "All rules" (/rules) and "CEL help" (/rules/cel).
//   - Events: <nav class="choices" aria-label="Events pages"> with "Executions" (/events) and "File access"
//     (/events/file-access).
//
// Each page of a section shows its tabs with exactly its own tab marked aria-current="page", also when its list is
// filtered; the navbar keeps its Rules or Events entry marked on both pages; the navbar's entries do not change. The
// small links between the pages (the CEL link in the add-rule form, "File access events" and "Execution events" in
// the hints) may stay, so they are not checked.

import (
	"html"
	"net/http"
	"regexp"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

type sectionTab struct {
	title, href string
}

var tabLink = regexp.MustCompile(`<a href="([^"]*)"( aria-current="page")?>([^<]*)</a>`)

// sectionTabs returns the links of the <nav class="choices" aria-label="<label>"> on the page and which one is
// marked current ("" when none is).
func sectionTabs(t *testing.T, body, label string) (tabs []sectionTab, current string) {
	t.Helper()
	m := regexp.MustCompile(`(?s)<nav class="choices" aria-label="`+regexp.QuoteMeta(label)+`">(.*?)</nav>`).FindAllStringSubmatch(body, -1)
	if len(m) != 1 {
		t.Fatalf("found %d <nav class=\"choices\" aria-label=%q>, want 1", len(m), label)
	}
	for _, l := range tabLink.FindAllStringSubmatch(m[0][1], -1) {
		tab := sectionTab{title: html.UnescapeString(l[3]), href: html.UnescapeString(l[1])}
		tabs = append(tabs, tab)
		if l[2] != "" {
			if current != "" {
				t.Errorf("%s: more than one tab is marked current", label)
			}
			current = tab.title
		}
	}
	return tabs, current
}

// navCurrent returns the navbar entries (the header's <nav>) and the one marked current.
func navCurrent(t *testing.T, body string) (entries []string, current string) {
	t.Helper()
	m := regexp.MustCompile(`(?s)<header class="top">.*?<nav>(.*?)</nav>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no navbar")
	}
	for _, l := range tabLink.FindAllStringSubmatch(m[1], -1) {
		entries = append(entries, l[1])
		if l[2] != "" {
			current = l[1]
		}
	}
	return entries, current
}

func sectionTabsStore() *fake.Store {
	s := fake.New()
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeBinary, Identifier: "1111111111111111111111111111111111111111111111111111111111111111",
		Policy: store.PolicyAllowlist, State: store.RuleStateInForce})
	return s
}

func TestSectionTabs_Rules(t *testing.T) {
	a := newApp(t, sectionTabsStore(), 10)
	want := []sectionTab{{"All rules", "/rules"}, {"CEL help", "/rules/cel"}}
	for path, cur := range map[string]string{
		"/rules":               "All rules",
		"/rules?type=BINARY":   "All rules",
		"/rules/cel":           "CEL help",
		"/rules/cel?expr=true": "CEL help",
	} {
		code, body := a.viewerGet(t, path)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", path, code)
		}
		tabs, current := sectionTabs(t, body, "Rules pages")
		if len(tabs) != len(want) || tabs[0] != want[0] || tabs[1] != want[1] {
			t.Errorf("%s: tabs %v, want %v", path, tabs, want)
		}
		if current != cur {
			t.Errorf("%s: current tab %q, want %q", path, current, cur)
		}
		if _, nav := navCurrent(t, body); nav != "/rules" {
			t.Errorf("%s: navbar marks %q, want /rules", path, nav)
		}
	}
}

func TestSectionTabs_Events(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	want := []sectionTab{{"Executions", "/events"}, {"File access", "/events/file-access"}}
	for path, cur := range map[string]string{
		"/events":                             "Executions",
		"/events?decision=BLOCK_BINARY":       "Executions",
		"/events/file-access":                 "File access",
		"/events/file-access?decision=DENIED": "File access",
	} {
		code, body := a.viewerGet(t, path)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", path, code)
		}
		tabs, current := sectionTabs(t, body, "Events pages")
		if len(tabs) != len(want) || tabs[0] != want[0] || tabs[1] != want[1] {
			t.Errorf("%s: tabs %v, want %v", path, tabs, want)
		}
		if current != cur {
			t.Errorf("%s: current tab %q, want %q", path, current, cur)
		}
		if _, nav := navCurrent(t, body); nav != "/events" {
			t.Errorf("%s: navbar marks %q, want /events", path, nav)
		}
	}
}

// The tabs belong to their section only: the top lists keep their own navbar entries and get neither set of tabs,
// and the Fleet tabs are unchanged.
func TestSectionTabs_OnlyInTheirSection(t *testing.T) {
	a := newApp(t, sectionTabsStore(), 10)
	for _, path := range []string{"/machines", "/groups", "/events/top-blocked", "/fleet"} {
		_, body := a.viewerGet(t, path)
		mustNotContain(t, body, `aria-label="Rules pages"`, `aria-label="Events pages"`)
	}
	_, body := a.viewerGet(t, "/fleet")
	tabs, current := sectionTabs(t, body, "Fleet pages")
	if len(tabs) != 2 || tabs[0] != (sectionTab{"Health", "/fleet"}) || tabs[1] != (sectionTab{"Lockdown readiness", "/fleet/readiness"}) || current != "Health" {
		t.Errorf("fleet tabs %v (current %q) changed", tabs, current)
	}
}

func TestSectionTabs_NavbarEntriesUnchanged(t *testing.T) {
	viewerWant := []string{"/machines", "/rules", "/groups", "/events", "/events/top-blocked", "/events/top-allowed", "/events/top-unknown", "/fleet"}
	adminWant := append(append([]string(nil), viewerWant...), "/admin", "/admin/config", "/admin/audit")
	a := newApp(t, sectionTabsStore(), 10)
	for _, path := range []string{"/rules", "/rules/cel", "/events", "/events/file-access"} {
		_, body := a.viewerGet(t, path)
		if got, _ := navCurrent(t, body); !equalStrings(got, viewerWant) {
			t.Errorf("%s: viewer navbar %v, want %v", path, got, viewerWant)
		}
		rec := a.get(t, path, a.cookie(t, auth.RoleAdmin))
		if got, _ := navCurrent(t, rec.Body.String()); !equalStrings(got, adminWant) {
			t.Errorf("%s: admin navbar %v, want %v", path, got, adminWant)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
