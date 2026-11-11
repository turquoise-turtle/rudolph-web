package web_test

// Rule forms folded (owner request, 2026-11-11: "The rule changes should also be hidden under a fold, as they won't
// often be changed, but they will at some points."). On /rules, a Mac's page and a group's page, with editing on, an
// admin sees the rules table first:
//   - Each row's change buttons (remove, collect or stop collecting) sit in its Change cell inside a closed
//     <details class="edit rule-edit"><summary>Change</summary>, so a row shows no button until it is opened.
//   - The add-rule form comes after the rules table, in its closed <details class="edit"><summary>Add a …</summary>,
//     as does the Mac page's "Move this Mac's rules to another Mac" form.
// /admin/rules/new (reached from an event or top-list link to add that rule) still shows its forms open.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
)

var ruleEditFold = regexp.MustCompile(`(?s)<details class="edit rule-edit"><summary>Change</summary>.*?</details>`)

func TestRuleFormsFold_ButtonsFoldedInEachRow(t *testing.T) {
	a := newEditApp(t, collectStore(), true)
	admin := a.cookie(t, auth.RoleAdmin)
	for _, path := range []string{"/rules", "/machines/" + editMachine, "/groups/eng"} {
		body := a.get(t, path, admin).Body.String()
		row := ruleRow(t, body, collectAllow)
		folds := ruleEditFold.FindAllString(row, -1)
		if len(folds) != 1 {
			t.Errorf("%s: %d closed Change folds in the rule's row, want 1:\n%s", path, len(folds), row)
			continue
		}
		mustContain(t, folds[0], `action="/admin/rules/remove"`)
		if outside := ruleEditFold.ReplaceAllString(row, ""); strings.Contains(outside, `action="/admin/rules/`) {
			t.Errorf("%s: a rule form outside the fold:\n%s", path, row)
		}
	}
}

func TestRuleFormsFold_AddFormAfterTheTableAndClosed(t *testing.T) {
	a := newEditApp(t, collectStore(), true)
	admin := a.cookie(t, auth.RoleAdmin)
	for path, summary := range map[string]string{
		"/rules":                   "Add a global rule",
		"/machines/" + editMachine: "Add a rule for this Mac",
		"/groups/eng":              "Add a rule for this group",
	} {
		body := a.get(t, path, admin).Body.String()
		closed := `<details class="edit"><summary>` + summary + `</summary>`
		at := strings.Index(body, closed)
		if at < 0 {
			t.Errorf("%s: no closed fold %q", path, closed)
			continue
		}
		if row := strings.Index(body, "<tr data-row"); row < 0 || row > at {
			t.Errorf("%s: the add-rule form comes before the rules table", path)
		}
	}

	body := a.get(t, "/machines/"+editMachine, admin).Body.String()
	move := strings.Index(body, `<details class="edit"><summary>Move this Mac's rules to another Mac</summary>`)
	if move < 0 || move < strings.Index(body, "<tr data-row") {
		t.Error("the move form is missing, open, or comes before the Mac's rules table")
	}
}

func TestRuleFormsFold_NewRulePageStaysOpen(t *testing.T) {
	a := newEditApp(t, collectStore(), true)
	body := a.get(t, "/admin/rules/new?sha256="+collectAllow, a.cookie(t, auth.RoleAdmin)).Body.String()
	if !strings.Contains(body, `<details class="edit" open>`) {
		t.Errorf("/admin/rules/new does not show its add-rule form open:\n%s", body)
	}
}

func TestRuleFormsFold_ViewersSeeNoFold(t *testing.T) {
	a := newEditApp(t, collectStore(), true)
	for _, path := range []string{"/rules", "/machines/" + editMachine, "/groups/eng"} {
		_, body := a.viewerGet(t, path)
		mustNotContain(t, body, "rule-edit", `action="/admin/rules`)
	}
}
