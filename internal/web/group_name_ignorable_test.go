package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

// Owner approved 2026-11-19: new group names refuse every default-ignorable character (and blank-looking ones), so a
// Mac's page never offers a reported name carrying one as a new group, and creating one is refused; a group registered
// with one before is still managed by its stored name.
func TestGroupNameWithDefaultIgnorableCharacter(t *testing.T) {
	vs := string(rune(0xfe0e))
	reported := "ops" + vs
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: editMachine, Hostname: "lab-mac", LastSeen: fixedNow})
	s.SetMachineGroups(editMachine, nil, nil, []string{reported})
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleViewer, auth.RoleAdmin)

	rec := a.get(t, "/machines/"+editMachine, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("machine page: status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Make this a group") {
		t.Errorf("%+q (a variation selector after ops) is offered as a new group", reported)
	}

	for _, r := range []rune{0xfe0e, 0xfe0f, 0xe0100, 0x034f, 0x180b, 0x17b4, 0x2065, 0xfff0, 0xe0041, 0x3164, 0x2800} {
		name := "ops" + string(r)
		if rec := a.post(t, "/admin/groups", url.Values{"name": {name}, "accept_reported": {"on"}}, admin, true); rec.Code != http.StatusBadRequest {
			t.Errorf("creating group with U+%04X: status %d, want 400", r, rec.Code)
		}
	}
	if gs, err := s.ListGroups(t.Context()); err == nil && len(gs) > 0 {
		for _, g := range gs {
			t.Errorf("group %+q created", g.Name)
		}
	}

	legacy := "lab" + vs
	s.AddGroup(store.Group{Name: legacy, Description: "old"})
	see303(t, a.post(t, "/admin/machines/"+editMachine+"/groups", url.Values{"group": {legacy}}, admin, true), "/machines/"+editMachine)
	see303(t, a.post(t, "/admin/machines/"+editMachine+"/groups/remove", url.Values{"group": {legacy}}, admin, true), "/machines/"+editMachine)
	see303(t, a.post(t, "/admin/groups/update", url.Values{"name": {legacy}, "description": {"new"}}, admin, true), "/groups/"+url.PathEscape(legacy))
	see303(t, a.post(t, "/admin/groups/delete", url.Values{"name": {legacy}}, admin, true), "/groups")
}
