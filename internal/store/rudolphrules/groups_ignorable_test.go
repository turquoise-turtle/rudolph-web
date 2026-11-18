package rudolphrules_test

import (
	"errors"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// Owner approved 2026-11-19: a new group name refuses every default-ignorable character, not only format characters.
// A variation selector, combining grapheme joiner, Mongolian free variation selector, Khmer inherent vowel, reserved
// default-ignorable or tag renders as nothing, so "ops" with one after it looks like "ops" while being another group;
// a Hangul filler or the blank braille pattern does the same. Only the zero-width (non-)joiners, which Persian and
// Indic names need, are kept. The emoji presentation selector U+FE0F is refused too: shown text keeps it, but a group
// name gains nothing from it and two names differing only by it would look the same.
var ignorableInGroupNames = []rune{
	0xfe00, 0xfe0e, 0xfe0f, 0xe0100, 0xe01ef, 0x034f, 0x180b, 0x180f, 0x17b4, 0x17b5, 0x2065, 0xfff0, 0xfff8,
	0xe0001, 0xe0041, 0xe0061, 0xe007f, 0xe0fff, 0x115f, 0x3164, 0xffa0, 0x2800,
}

func TestGroupName_RefusesDefaultIgnorableCharacters(t *testing.T) {
	for _, r := range ignorableInGroupNames {
		for _, name := range []string{"ops" + string(r), "o" + string(r) + "ps"} {
			got, err := rudolphrules.GroupName(name)
			if err == nil {
				t.Errorf("GroupName(%+q) with U+%04X = %+q, want an error", name, r, got)
			} else if !errors.Is(err, store.ErrInvalidArgument) {
				t.Errorf("GroupName(%+q) with U+%04X: %v, want a store invalid-argument error", name, r, err)
			}
		}
	}
	for _, r := range []rune{0x200c, 0x200d} {
		if _, err := rudolphrules.GroupName("a" + string(r) + "b"); err != nil {
			t.Errorf("GroupName with U+%04X: %v, want it accepted", r, err)
		}
	}
}

// A group registered before these characters were refused is still named by its stored name when it is changed,
// deleted, assigned or targeted.
func TestRegisteredGroupName_AcceptsDefaultIgnorableCharacters(t *testing.T) {
	for _, r := range []rune{0xfe0e, 0xfe0f, 0xe0100, 0x034f, 0x180b, 0x17b4, 0x2065, 0xfff0, 0x3164, 0x2800} {
		in, want := " Ops"+string(r)+" ", "ops"+string(r)
		for name, f := range map[string]func(string) (string, error){
			"RegisteredGroupName": rudolphrules.RegisteredGroupName,
			"GroupTargetName":     rudolphrules.GroupTargetName,
			"ExistingGroupName":   func(n string) (string, error) { return rudolphrules.ExistingGroupName(n, "changed") },
		} {
			if got, err := f(in); err != nil || got != want {
				t.Errorf("%s(%+q) with U+%04X = %+q, %v; want %+q", name, in, r, got, err, want)
			}
		}
	}
}
