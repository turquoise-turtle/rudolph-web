package web_test

// The events page's filters forgive case (review): a hand-typed lower-case value used to be queried as typed and found
// nothing.
//   - Team IDs are always upper case (Apple's format), so the team ID filter is upper-cased.
//   - Machine IDs are stored exactly as Santa sends them: the hardware UUID (upper case) by default, but a profile can
//     set any ID, in any case. So the machine filter is not upper-cased blindly: it uses the spelling the Mac is stored
//     under, trying the value as typed, then upper case, then lower case, and queries as typed when no Mac matches.

import (
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func eventFilters(t *testing.T, s *fake.Store) []store.EventFilter {
	t.Helper()
	var filters []store.EventFilter
	for _, c := range s.Calls() {
		if f, ok := c.Filter.(store.EventFilter); ok && c.Method == "ListExecutionEvents" {
			filters = append(filters, f)
		}
	}
	return filters
}

func TestEvents_FiltersForgiveCase(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA", Hostname: "uuid-mac"})
	s.AddMachine(store.Machine{MachineID: "lab-mac-07", Hostname: "custom-id-mac"})
	a := newApp(t, s, 10)

	for _, c := range []struct {
		query     string
		machineID string
		teamID    string
	}{
		{"machine=aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa", "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA", ""},
		{"machine=AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA", "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA", ""},
		{"machine=lab-mac-07", "lab-mac-07", ""},
		{"machine=LAB-MAC-07", "lab-mac-07", ""},
		{"machine=no-such-mac", "no-such-mac", ""},
		{"team_id=eqhxz8m8av", "", "EQHXZ8M8AV"},
	} {
		s.ResetCalls()
		if code, _ := a.viewerGet(t, "/events?"+c.query); code != 200 {
			t.Fatalf("%s: status %d", c.query, code)
		}
		filters := eventFilters(t, s)
		if len(filters) != 1 {
			t.Fatalf("%s: %d execution event queries, want 1: %+v", c.query, len(filters), s.Calls())
		}
		if filters[0].MachineID != c.machineID || filters[0].TeamID != c.teamID {
			t.Errorf("%s: queried machine %q team %q, want machine %q team %q", c.query, filters[0].MachineID, filters[0].TeamID, c.machineID, c.teamID)
		}
	}
}
