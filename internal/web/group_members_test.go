package web_test

// Group members (owner approved, 2026-11-11). Rudolph keeps an index of each group's Macs, `GroupMembers#<group>` /
// `<machine id>` (Assigned and Reported flags), in step with each Mac's groups item.
//   - store.GroupReader gains ListGroupMembers(ctx, group, store.GroupMemberFilter{AssignedOnly}, PageRequest), one
//     paginated query a page, by machine ID, returning store.GroupMember{Machine (MachineID always; the rest from the
//     Mac's sensor data when it has any), Assigned, Reported}.
//   - GET /groups/{name}/members (viewers): a table of the group's Macs, one <tr data-row> each: the hostname (or the
//     machine ID when there is none) linking to the Mac's page, serial, model, how it is a member ("assigned",
//     "reported" or "assigned, reported") and when it was last seen. A group that counts only assigned Macs lists
//     only those (AssignedOnly), and says so. The page reads the groups with one ListGroups call. Unknown groups and
//     rudolph:ungrouped (every Mac in no group, which is not listed): 404, without listing members.
//   - A registered group's page links to its members page; the ungrouped page does not.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

const (
	memberAssigned = "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA"
	memberReported = "BBBBBBBB-2222-4222-8222-BBBBBBBBBBBB"
	memberBoth     = "CCCCCCCC-3333-4333-8333-CCCCCCCCCCCC"
	memberNoSensor = "EEEEEEEE-5555-4555-8555-EEEEEEEEEEEE"
)

func groupMembersStore() *fake.Store {
	s := fake.New()
	s.AddGroup(store.Group{Name: "eng", Description: "Engineering", AcceptReported: true})
	s.AddGroup(store.Group{Name: "admins", Description: "Admins"})
	s.AddGroup(store.Group{Name: store.UngroupedGroup})
	s.AddMachine(store.Machine{MachineID: memberAssigned, SerialNum: "C02ASG", Hostname: "eng-assigned", ModelIdentifier: "Mac16,12", LastSeen: fixedNow})
	s.AddMachine(store.Machine{MachineID: memberReported, SerialNum: "C02REP", Hostname: "eng-reported", LastSeen: fixedNow})
	s.AddMachine(store.Machine{MachineID: memberBoth, SerialNum: "C02BTH", Hostname: "eng-both", LastSeen: fixedNow})
	s.SetMachineGroups(memberAssigned, []string{"eng", "admins"}, nil, nil)
	s.SetMachineGroups(memberReported, nil, []string{"eng", "admins"}, []string{"Eng", "Admins"})
	s.SetMachineGroups(memberBoth, []string{"eng"}, []string{"eng"}, []string{"Eng"})
	s.SetMachineGroups(memberNoSensor, []string{"eng"}, nil, nil) // assigned before it ever synced
	return s
}

// rowFor returns the members table row of the machine, found by its link to the Mac's page.
func rowFor(t *testing.T, body, machineID string) string {
	t.Helper()
	for _, row := range strings.Split(body, "<tr data-row") {
		if strings.Contains(row, `href="/machines/`+machineID+`"`) {
			return row[:strings.Index(row+"</tr>", "</tr>")]
		}
	}
	return ""
}

func TestGroupMembers_ListsTheGroupsMacs(t *testing.T) {
	s := groupMembersStore()
	a := newApp(t, s, 10)
	s.ResetCalls()
	code, body := a.viewerGet(t, "/groups/eng/members")
	if code != http.StatusOK {
		t.Fatalf("status %d\n%s", code, body)
	}
	if n := rows(body); n != 4 {
		t.Errorf("%d member rows, want 4", n)
	}
	assigned := rowFor(t, body, memberAssigned)
	mustContain(t, assigned, "eng-assigned", "C02ASG", "Mac16,12", "assigned")
	mustNotContain(t, assigned, "reported")
	reported := rowFor(t, body, memberReported)
	mustContain(t, reported, "eng-reported", "reported")
	mustNotContain(t, reported, "assigned")
	mustContain(t, rowFor(t, body, memberBoth), "eng-both", "assigned, reported")
	mustContain(t, rowFor(t, body, memberNoSensor), memberNoSensor, "assigned")

	var listed bool
	for _, c := range s.Calls() {
		switch c.Method {
		case "ListGroupMembers":
			listed = true
			if c.Filter != (store.GroupMemberFilter{}) {
				t.Errorf("eng counts reported Macs, but the filter was %+v", c.Filter)
			}
		case "ListGroups":
		default:
			t.Errorf("unexpected call %s", c.Method)
		}
	}
	if !listed {
		t.Error("no ListGroupMembers call")
	}
}

func TestGroupMembers_AssignedOnlyGroup(t *testing.T) {
	s := groupMembersStore()
	a := newApp(t, s, 10)
	s.ResetCalls()
	code, body := a.viewerGet(t, "/groups/admins/members")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if rowFor(t, body, memberAssigned) == "" {
		t.Error("the assigned Mac is not listed")
	}
	if rowFor(t, body, memberReported) != "" {
		t.Error("a Mac that only reports a group counting only assigned Macs is listed")
	}
	mustContain(t, body, "only assigned")
	for _, c := range s.Calls() {
		if c.Method == "ListGroupMembers" && c.Filter != (store.GroupMemberFilter{AssignedOnly: true}) {
			t.Errorf("filter %+v, want AssignedOnly", c.Filter)
		}
	}
}

func TestGroupMembers_UnknownAndUngrouped(t *testing.T) {
	s := groupMembersStore()
	a := newApp(t, s, 10)
	for _, target := range []string{"/groups/nope/members", "/groups/" + store.UngroupedGroup + "/members"} {
		s.ResetCalls()
		code, _ := a.viewerGet(t, target)
		if code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", target, code)
		}
		for _, c := range s.Calls() {
			if c.Method == "ListGroupMembers" {
				t.Errorf("%s listed members", target)
			}
		}
	}
}

func TestGroupMembers_GroupPageLinksToThem(t *testing.T) {
	a := newApp(t, groupMembersStore(), 10)
	_, eng := a.viewerGet(t, "/groups/eng")
	mustContain(t, eng, `href="/groups/eng/members"`)
	_, ungrouped := a.viewerGet(t, "/groups/"+store.UngroupedGroup)
	mustNotContain(t, ungrouped, "/members")
}

func TestGroupMembers_NeedsAViewer(t *testing.T) {
	s := groupMembersStore()
	a := newApp(t, s, 10)
	s.ResetCalls()
	if rec := a.get(t, "/groups/eng/members", nil); rec.Code != http.StatusFound {
		t.Errorf("unauthenticated: status %d, want 302", rec.Code)
	}
	if rec := a.get(t, "/groups/eng/members", a.cookie(t)); rec.Code != http.StatusForbidden {
		t.Errorf("no role: status %d, want 403", rec.Code)
	}
	if n := len(s.Calls()); n != 0 {
		t.Errorf("%d data-layer calls without a viewer, want 0", n)
	}
}
