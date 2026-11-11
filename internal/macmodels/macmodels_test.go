package macmodels_test

// Apple's marketing names for Mac model identifiers (owner request, 2026-11-11). The table is models.tsv in this
// package (identifier, tab, name; one line per pair, from Apple's "Identify your … model" support pages), embedded in
// the binary: no network lookup at run time. Name returns the name for an identifier, several names joined with " or "
// (in the table's order) when Apple uses one identifier for more than one model, and "" for an unknown or empty one.
// Identifiers match exactly, after trimming spaces.

import (
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/macmodels"
)

func TestName(t *testing.T) {
	for id, want := range map[string]string{
		"Mac16,12":       "MacBook Air (13-inch, M4, 2025)",
		"MacBookAir10,1": "MacBook Air (M1, 2020)",
		"Mac14,2":        "MacBook Air (M2, 2022)",
		"iMac21,1":       "iMac (24-inch, M1, 2021)",
		" Mac16,12 ":     "MacBook Air (13-inch, M4, 2025)",
		"MacBookPro15,1": "MacBook Pro (15-inch, 2018) or MacBook Pro (15-inch, 2019)",
		"Mac16,9":        "Mac Studio (2025)",
		"Mac99,1":        "",
		"mac16,12":       "",
		"":               "",
	} {
		if got := macmodels.Name(id); got != want {
			t.Errorf("Name(%q) = %q, want %q", id, got, want)
		}
	}
}
