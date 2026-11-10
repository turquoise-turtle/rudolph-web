package web_test

// The events page's machine and team ID filters are upper-cased, as the machines page normalises its filters (review):
// Santa reports machine IDs as upper-case UUIDs and team IDs are upper case, so a hand-typed lower-case value queried
// as typed found nothing.

import (
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func TestEvents_FiltersAreUpperCased(t *testing.T) {
	s := fake.New()
	a := newApp(t, s, 10)
	s.ResetCalls()
	if code, _ := a.viewerGet(t, "/events?machine=aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"); code != 200 {
		t.Fatalf("status %d", code)
	}
	if code, _ := a.viewerGet(t, "/events?team_id=eqhxz8m8av"); code != 200 {
		t.Fatalf("status %d", code)
	}
	var filters []store.EventFilter
	for _, c := range s.Calls() {
		if f, ok := c.Filter.(store.EventFilter); ok && c.Method == "ListExecutionEvents" {
			filters = append(filters, f)
		}
	}
	if len(filters) != 2 {
		t.Fatalf("recorded %d execution event queries, want 2: %+v", len(filters), s.Calls())
	}
	if filters[0].MachineID != "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA" {
		t.Errorf("machine filter queried as %q, want upper case", filters[0].MachineID)
	}
	if filters[1].TeamID != "EQHXZ8M8AV" {
		t.Errorf("team ID filter queried as %q, want upper case", filters[1].TeamID)
	}
}
