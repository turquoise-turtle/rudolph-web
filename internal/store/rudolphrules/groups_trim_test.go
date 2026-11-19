package rudolphrules_test

import (
	"errors"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// Second review of the stored-XSS fixes (2026-11-19). Rudolph's groups.Normalise trims a name before refusing what is
// left, so a Mac reporting "ops" with a trailing line break is in the group ops. GroupName refused hidden characters
// before trimming, so the line feed, tab, next-line (U+0085) or line separator (U+2028) that Normalise trims made the
// name look like a hidden character's, and the console refused a group Rudolph accepts.
func TestGroupName_TrimsBeforeRefusingHiddenCharacters(t *testing.T) {
	for _, name := range []string{
		"ops\n", "ops\t", "ops\r\n", "\tops", "ops" + string(rune(0x85)), "ops" + string(rune(0x2028)),
		string(rune(0x2029)) + "ops", " ops ", " OPS\n",
	} {
		if got, err := rudolphrules.GroupName(name); err != nil || got != "ops" {
			t.Errorf("GroupName(%+q) = %+q, %v; want \"ops\"", name, got, err)
		}
	}
	// Trimming takes only the white space round the name: a hidden character inside it, or next to the white space
	// that is trimmed, is still refused.
	for _, name := range []string{
		"o" + string(rune(0x202e)) + "ps\n", "ops" + string(rune(0x200b)) + "\n", "\n" + string(rune(0xfe0f)) + "ops",
		" ops" + string(rune(0x3164)) + " ", "o\nps", "o\tps",
	} {
		got, err := rudolphrules.GroupName(name)
		if err == nil {
			t.Errorf("GroupName(%+q) = %+q, want an error", name, got)
		} else if !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("GroupName(%+q): %v, want a store invalid-argument error", name, err)
		}
	}
}
