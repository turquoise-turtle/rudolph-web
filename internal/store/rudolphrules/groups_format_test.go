package rudolphrules_test

import (
	"errors"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// A group name with a bidi override, zero-width or other invisible format character would display as something
// it is not, on the Mac page's "Make this a group" offer and in copy-paste commands (stored-XSS review,
// 2026-11-18). The view refuses them itself, whatever Rudolph version it is built against.
func TestGroupName_RefusesInvisibleFormatCharacters(t *testing.T) {
	for _, name := range []string{"eng\u202e", "en\u200bg", "eng\u2066", "\ufeffeng", "en\u00adg", "eng\u200f", "eng\u061c"} {
		got, err := rudolphrules.GroupName(name)
		if err == nil {
			t.Errorf("GroupName(%q) = %q, want an error", name, got)
		} else if !errors.Is(err, store.ErrInvalidArgument) {
			// Refusals must be the store's invalid-argument error so the view answers 400, as for other bad names.
			t.Errorf("GroupName(%q): %v, want a store invalid-argument error", name, err)
		}
	}
	for _, ok := range []string{"jürgen", "工程", "science dept"} {
		if _, err := rudolphrules.GroupName(ok); err != nil {
			t.Errorf("GroupName(%q): %v, want it accepted", ok, err)
		}
	}
}
