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

// Second review of the stored-XSS fixes (2026-11-19): Rudolph trims a reported group name, so a Mac reporting "ops"
// with a trailing line break is in ops. Its page must offer to make ops a group, and the button must create it.
func TestGroupNameReportedWithTrailingLineBreak(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: editMachine, Hostname: "lab-mac", LastSeen: fixedNow})
	s.SetMachineGroups(editMachine, nil, nil, []string{"ops\n"})
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleViewer, auth.RoleAdmin)

	rec := a.get(t, "/machines/"+editMachine, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("machine page: status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Make this a group") {
		t.Errorf("%+q, which Rudolph reads as ops, is not offered as a new group", "ops\n")
	}
	see303(t, a.post(t, "/admin/groups", url.Values{"name": {"ops\n"}, "accept_reported": {"on"}}, admin, true), "/groups/ops")
	if g := groupByName(t, s, "ops"); g == nil || !g.AcceptReported {
		t.Errorf("Make this a group created %+v; want ops counting reported membership", g)
	}
}
