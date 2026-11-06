package web_test

// Device groups in the web view: a Groups page lists the registered groups (name, description, whether reported
// membership counts); a page per group lists its rules; the machine page shows the Mac's groups (assigned, reported
// and counted, as Santa sent them) or says it is ungrouped. The reserved group "rudolph:ungrouped" is shown as
// "Macs in no group".

import (
	"net/http"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func groupsStore() *fake.Store {
	s := fake.New()
	s.AddGroup(store.Group{Name: "eng", Description: "Engineering", AcceptReported: true})
	s.AddGroup(store.Group{Name: "admins", Description: "Admin Macs", AcceptReported: false})
	s.AddGroup(store.Group{Name: "rudolph:ungrouped"})
	s.AddGroupRule("eng", store.Rule{RuleType: store.RuleTypeSigningID, Policy: store.PolicyBlocklist, Identifier: "EQHXZ8M8AV:com.google.GeminiMacOS", CustomMessage: "Not for Eng"})
	s.AddGroupRule("rudolph:ungrouped", store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyBlocklist, Identifier: "sha-ungrouped"})
	s.AddMachine(store.Machine{MachineID: "m-eng", Hostname: "eng-mac", LastSeen: fixedNow})
	s.AddMachine(store.Machine{MachineID: "m-none", Hostname: "lonely-mac", LastSeen: fixedNow})
	s.SetMachineGroups("m-eng", []string{"admins"}, []string{"eng"}, []string{"Eng", "staff"})
	return s
}

func TestGroups_ListPage(t *testing.T) {
	a := newApp(t, groupsStore(), 10)
	code, body := a.viewerGet(t, "/groups")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "eng", "Engineering", "admins", "Admin Macs", "Macs in no group", `href="/groups/eng"`)
}

func TestGroups_GroupPageListsItsRules(t *testing.T) {
	a := newApp(t, groupsStore(), 10)
	code, body := a.viewerGet(t, "/groups/eng")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "Engineering", "EQHXZ8M8AV:com.google.GeminiMacOS", "BLOCKLIST", "Not for Eng")
	mustNotContain(t, body, "sha-ungrouped")

	code, body = a.viewerGet(t, "/groups/rudolph:ungrouped")
	if code != http.StatusOK {
		t.Fatalf("ungrouped: status %d", code)
	}
	mustContain(t, body, "Macs in no group", "sha-ungrouped")

	if code, _ := a.viewerGet(t, "/groups/nonexistent"); code != http.StatusNotFound {
		t.Errorf("unknown group: status %d, want 404", code)
	}
}

func TestGroups_MachinePageShowsGroups(t *testing.T) {
	a := newApp(t, groupsStore(), 10)
	_, body := a.viewerGet(t, "/machines/m-eng")
	mustContain(t, body, "admins", "eng", "Eng", "staff", `href="/groups/eng"`, `href="/groups/admins"`)
	mustNotContain(t, body, "Ungrouped")
	_, body = a.viewerGet(t, "/machines/m-none")
	mustContain(t, body, "Ungrouped", `href="/groups/rudolph:ungrouped"`)
}

func TestGroups_NamesAreEscaped(t *testing.T) {
	s := fake.New()
	s.AddGroup(store.Group{Name: `"><script>alert(1)</script>`, Description: `<img src=x onerror=alert(2)>`})
	a := newApp(t, s, 10)
	_, body := a.viewerGet(t, "/groups")
	mustNotContain(t, body, "<script>alert(1)", "<img src=x")
}
