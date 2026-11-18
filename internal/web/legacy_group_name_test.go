package web_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

// New group names cannot carry invisible format characters, but a group registered before they were refused must
// still be shown, assigned, changed and deleted, by the name it is stored under; zero-width (non-)joiners, which
// Persian and Indic names need, are not refused at all.
func TestGroupRegisteredWithAFormatCharacterCanStillBeManaged(t *testing.T) {
	const legacy = "ops\u202e"
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: editMachine, Hostname: "lab-mac", LastSeen: fixedNow})
	s.AddGroup(store.Group{Name: legacy, Description: "old"})
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleViewer, auth.RoleAdmin)

	rec := a.get(t, "/groups/"+url.PathEscape(legacy), admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("group page: status %d", rec.Code)
	}
	mustContain(t, rec.Body.String(), `ops\u202e`)

	see303(t, a.post(t, "/admin/machines/"+editMachine+"/groups", url.Values{"group": {legacy}}, admin, true), "/machines/"+editMachine)
	see303(t, a.post(t, "/admin/machines/"+editMachine+"/groups/remove", url.Values{"group": {legacy}}, admin, true), "/machines/"+editMachine)
	see303(t, a.post(t, "/admin/groups/update", url.Values{"name": {legacy}, "description": {"new"}}, admin, true), "/groups/"+url.PathEscape(legacy))
	see303(t, a.post(t, "/admin/groups/delete", url.Values{"name": {legacy}}, admin, true), "/groups")

	for _, name := range []string{"می\u200cرود", "a\u200db"} {
		if rec := a.post(t, "/admin/groups", url.Values{"name": {name}}, admin, true); rec.Code != http.StatusSeeOther {
			t.Errorf("creating group %q: status %d, want 303", name, rec.Code)
		}
	}
}
