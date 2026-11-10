package web_test

// Groups in the web view (owner request; editing on, admins only, as every other write):
//   - /groups has a form to create a group (name, description, "Macs that report it are members", ticked by default),
//     posting to /admin/groups; the new group's page follows (303).
//   - A group's page has forms to change its description and reported membership (/admin/groups/update) and to
//     delete it (/admin/groups/delete, with "also delete its rules" for force). Ungrouped has neither.
//   - A Mac's page lists, for each name it reports that is not a registered group, a "Make this a group" button (a
//     create form with the name as reported); assigns a registered group (/admin/machines/{id}/groups); and removes
//     an assigned one (/admin/machines/{id}/groups/remove). Both return to the Mac's page.
// Every write records the CLI's audit entry (group.create, group.update, group.delete, group.add, group.remove) with
// the web actor. Viewers, and admins with editing off, see no forms; their POSTs get 403 and 404 as elsewhere.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func groupsEditStore() *fake.Store {
	s := fake.New()
	s.AddGroup(store.Group{Name: "eng", Description: "Engineering", AcceptReported: true})
	s.AddMachine(store.Machine{MachineID: editMachine, SerialNum: "SER1", Hostname: "lab-mac-01", ClientMode: store.ClientModeMonitor, LastSeen: fixedNow})
	s.SetMachineGroups(editMachine, []string{"eng"}, []string{"eng"}, []string{"Eng", "Science Dept", "rudolph:sneaky"})
	return s
}

func groupByName(t *testing.T, s *fake.Store, name string) *store.Group {
	t.Helper()
	gs, err := s.ListGroups(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := range gs {
		if gs[i].Name == name {
			return &gs[i]
		}
	}
	return nil
}

func see303(t *testing.T, rec interface {
	Result() *http.Response
}, want string) {
	t.Helper()
	res := rec.Result()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != want {
		t.Fatalf("status %d, Location %q; want 303 to %s", res.StatusCode, res.Header.Get("Location"), want)
	}
}

func TestGroupsEditing_AdminCreatesAGroup(t *testing.T) {
	s := groupsEditStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)

	_, body := func() (int, string) { r := a.get(t, "/groups", admin); return r.Code, r.Body.String() }()
	mustContain(t, body, `action="/admin/groups"`, `name="name"`, `name="description"`, `name="accept_reported"`)
	if !strings.Contains(body, `name="accept_reported" value="on" checked`) {
		t.Errorf("the create form should count reported membership by default (accept_reported ticked)")
	}

	rec := a.post(t, "/admin/groups", url.Values{"name": {" Lab Macs "}, "description": {"Teaching labs"}, "accept_reported": {"on"}}, admin, true)
	see303(t, rec, "/groups/lab%20macs")
	if g := groupByName(t, s, "lab macs"); g == nil || g.Description != "Teaching labs" || !g.AcceptReported {
		t.Fatalf("created group %+v", g)
	}

	rec = a.post(t, "/admin/groups", url.Values{"name": {"admins"}, "description": {"Privileged"}}, admin, true)
	see303(t, rec, "/groups/admins")
	if g := groupByName(t, s, "admins"); g == nil || g.AcceptReported {
		t.Errorf("unticked accept_reported should create a group that ignores reported membership: %+v", g)
	}

	entries := auditOf(t, s)
	if len(entries) != 2 {
		t.Fatalf("audit %+v", entries)
	}
	for _, e := range entries {
		if e.Action != "group.create" || !strings.HasPrefix(e.Target, "group:") || !strings.Contains(e.Actor, "vera@example.com") {
			t.Errorf("audit entry %+v; want group.create by the web actor", e)
		}
	}
}

func TestGroupsEditing_CreateRefusalsAre400(t *testing.T) {
	s := groupsEditStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	for name, form := range map[string]url.Values{
		"empty":    {"name": {"  "}},
		"reserved": {"name": {"rudolph:mine"}},
		"too long": {"name": {strings.Repeat("x", 129)}},
		"existing": {"name": {"ENG"}},
	} {
		rec := a.post(t, "/admin/groups", form, admin, true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
	if len(auditOf(t, s)) != 0 {
		t.Error("refused creations were audited")
	}
}

func TestGroupsEditing_UpdateAndDelete(t *testing.T) {
	s := groupsEditStore()
	s.AddGroupRule("eng", store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyBlocklist, Identifier: editSHA})
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)

	body := a.get(t, "/groups/eng", admin).Body.String()
	mustContain(t, body, `action="/admin/groups/update"`, `action="/admin/groups/delete"`, `name="force"`, `value="Engineering"`)

	see303(t, a.post(t, "/admin/groups/update", url.Values{"name": {"eng"}, "description": {"Engineering team"}}, admin, true), "/groups/eng")
	if g := groupByName(t, s, "eng"); g == nil || g.Description != "Engineering team" || g.AcceptReported {
		t.Errorf("after update %+v; want the new description and accept_reported off (unticked)", g)
	}
	entries := auditOf(t, s)
	if len(entries) != 1 || entries[0].Action != "group.update" || entries[0].Target != "group:eng" {
		t.Fatalf("audit %+v", entries)
	}

	// Posting the form unchanged writes and records nothing.
	see303(t, a.post(t, "/admin/groups/update", url.Values{"name": {"eng"}, "description": {"Engineering team"}}, admin, true), "/groups/eng")
	if n := len(auditOf(t, s)); n != 1 {
		t.Errorf("an unchanged update was audited (%d entries)", n)
	}

	if rec := a.post(t, "/admin/groups/delete", url.Values{"name": {"eng"}}, admin, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "rules") {
		t.Errorf("deleting a group with rules without force: status %d; want 400 explaining its rules", rec.Code)
	}
	see303(t, a.post(t, "/admin/groups/delete", url.Values{"name": {"eng"}, "force": {"on"}}, admin, true), "/groups")
	if groupByName(t, s, "eng") != nil {
		t.Error("eng still registered after a forced delete")
	}
	entries = auditOf(t, s)
	if entries[0].Action != "group.delete" || entries[0].Summary != "deleted group eng and its rules" {
		t.Errorf("latest audit entry %+v", entries[0])
	}

	if rec := a.post(t, "/admin/groups/delete", url.Values{"name": {"no-such-group"}}, admin, true); rec.Code != http.StatusNotFound {
		t.Errorf("deleting a missing group: status %d, want 404", rec.Code)
	}
}

func TestGroupsEditing_UngroupedHasNoEditForms(t *testing.T) {
	s := groupsEditStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	body := a.get(t, "/groups/"+url.PathEscape(store.UngroupedGroup), admin).Body.String()
	mustNotContain(t, body, `action="/admin/groups/update"`, `action="/admin/groups/delete"`)
	if rec := a.post(t, "/admin/groups/delete", url.Values{"name": {store.UngroupedGroup}, "force": {"on"}}, admin, true); rec.Code != http.StatusBadRequest {
		t.Errorf("deleting Ungrouped: status %d, want 400", rec.Code)
	}
}

func TestGroupsEditing_MachinePage(t *testing.T) {
	s := groupsEditStore()
	s.AddGroup(store.Group{Name: "lab", Description: "Labs", AcceptReported: true})
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	machinePath := "/machines/" + editMachine

	body := a.get(t, machinePath, admin).Body.String()
	// "Science Dept" is reported but not registered: offer to make it a group, with the name as reported.
	mustContain(t, body, "Make this a group", `value="Science Dept"`)
	// "Eng" is registered (as eng), and rudolph:sneaky can never be a group: no button for either.
	if strings.Count(body, "Make this a group") != 1 {
		t.Errorf("want exactly one Make this a group button (Science Dept), got %d", strings.Count(body, "Make this a group"))
	}
	// Assign a registered group not already assigned; remove an assigned one.
	mustContain(t, body, `action="/admin`+machinePath+`/groups"`, `<option value="lab">`, `action="/admin`+machinePath+`/groups/remove"`)
	if strings.Contains(body, `<option value="eng">`) {
		t.Error("the assign list offers eng, which is already assigned")
	}

	see303(t, a.post(t, "/admin/groups", url.Values{"name": {"Science Dept"}, "accept_reported": {"on"}}, admin, true), "/groups/science%20dept")
	if g := groupByName(t, s, "science dept"); g == nil || !g.AcceptReported {
		t.Errorf("Make this a group created %+v; want science dept counting reported membership", g)
	}

	see303(t, a.post(t, "/admin"+machinePath+"/groups", url.Values{"group": {"lab"}}, admin, true), machinePath)
	m, err := s.GetMachine(t.Context(), editMachine)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.AssignedGroups, ",") != "eng,lab" {
		t.Errorf("assigned %v, want eng,lab", m.AssignedGroups)
	}
	see303(t, a.post(t, "/admin"+machinePath+"/groups/remove", url.Values{"group": {"eng"}}, admin, true), machinePath)
	m, _ = s.GetMachine(t.Context(), editMachine)
	if strings.Join(m.AssignedGroups, ",") != "lab" {
		t.Errorf("after removing eng, assigned %v, want lab", m.AssignedGroups)
	}
	if rec := a.post(t, "/admin"+machinePath+"/groups", url.Values{"group": {"no-such-group"}}, admin, true); rec.Code != http.StatusBadRequest {
		t.Errorf("assigning an unregistered group: status %d, want 400", rec.Code)
	}

	entries := auditOf(t, s)
	if len(entries) != 3 || entries[0].Action != "group.remove" || entries[1].Action != "group.add" || entries[1].Target != "machine:"+editMachine || entries[1].Summary != "assigned group lab" {
		t.Errorf("audit %+v; want group.create, group.add (machine:%s, assigned group lab), group.remove", entries, editMachine)
	}
}

func TestGroupsEditing_OnlyAdminsWithEditing(t *testing.T) {
	posts := map[string]url.Values{
		"/admin/groups":                                     {"name": {"x"}},
		"/admin/groups/update":                              {"name": {"eng"}, "description": {"x"}},
		"/admin/groups/delete":                              {"name": {"eng"}},
		"/admin/machines/" + editMachine + "/groups":        {"group": {"eng"}},
		"/admin/machines/" + editMachine + "/groups/remove": {"group": {"eng"}},
	}

	s := groupsEditStore()
	a := newEditApp(t, s, true)
	viewer := a.cookie(t, auth.RoleViewer)
	for _, page := range []string{"/groups", "/groups/eng", "/machines/" + editMachine} {
		body := a.get(t, page, viewer).Body.String()
		mustNotContain(t, body, `action="/admin/groups`, "Make this a group", `/groups/remove"`)
	}
	for target, form := range posts {
		if rec := a.post(t, target, form, viewer, true); rec.Code != http.StatusForbidden {
			t.Errorf("viewer POST %s: status %d, want 403", target, rec.Code)
		}
		if rec := a.post(t, target, form, a.cookie(t, auth.RoleAdmin), false); rec.Code != http.StatusForbidden {
			t.Errorf("cross-site POST %s: status %d, want 403", target, rec.Code)
		}
	}

	ro := newEditApp(t, groupsEditStore(), false)
	admin := ro.cookie(t, auth.RoleAdmin)
	for _, page := range []string{"/groups", "/groups/eng", "/machines/" + editMachine} {
		body := ro.get(t, page, admin).Body.String()
		mustNotContain(t, body, `action="/admin/groups`, "Make this a group", `/groups/remove"`)
	}
	for target, form := range posts {
		if rec := ro.post(t, target, form, admin, true); rec.Code != http.StatusNotFound {
			t.Errorf("editing off, POST %s: status %d, want 404", target, rec.Code)
		}
	}
	if len(auditOf(t, s)) != 0 {
		t.Error("refused POSTs were audited")
	}
}

// Writes live under /admin only, like every other edit: the Mac's own path takes no POSTs.
func TestGroupsEditing_NoWritesOutsideAdmin(t *testing.T) {
	a := newEditApp(t, groupsEditStore(), true)
	admin := a.cookie(t, auth.RoleAdmin)
	for _, target := range []string{"/machines/" + editMachine + "/groups", "/machines/" + editMachine + "/groups/remove"} {
		if rec := a.post(t, target, url.Values{"group": {"eng"}}, admin, true); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: status %d, want 404 or 405", target, rec.Code)
		}
	}
}
