package textsafe_test

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/turquoise-turtle/rudolph-web/internal/textsafe"
)

// Every default-ignorable code point renders as nothing, so one can hide in a value or carry text no one sees
// (variation selectors after a letter smuggle bytes, for instance). The owner approved escaping all of them on
// 2026-11-19, as Rudolph's CLI termsafe does from the same change, except those emoji and joined scripts need: the
// zero-width (non-)joiner, the emoji presentation selector U+FE0F, and the tags of a valid subdivision flag.

// defaultIgnorable is Unicode's derived Default_Ignorable_Code_Point property (DerivedCoreProperties.txt), which Go's
// unicode package does not provide as one table.
func defaultIgnorable(r rune) bool {
	switch {
	case unicode.Is(unicode.White_Space, r), r >= 0xfff9 && r <= 0xfffb, r >= 0x13430 && r <= 0x1343f,
		unicode.Is(unicode.Prepended_Concatenation_Mark, r):
		return false
	}
	return unicode.In(r, unicode.Other_Default_Ignorable_Code_Point, unicode.Cf, unicode.Variation_Selector)
}

// escaped is the escape String writes for r: lower-case hex, \u up to U+FFFF and \U above.
func escaped(r rune) string {
	if r > 0xffff {
		return fmt.Sprintf(`\U%08x`, r)
	}
	return fmt.Sprintf(`\u%04x`, r)
}

func TestString_EscapesDefaultIgnorables(t *testing.T) {
	for _, r := range []rune{0xfe00, 0xfe0e, 0xe0100, 0xe01ef, 0x034f, 0x180b, 0x180f, 0x17b4, 0x17b5, 0xfff0, 0x2065, 0x1d173} {
		in := "a" + string(r) + "b"
		if got, want := textsafe.String(in), "a"+escaped(r)+"b"; got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestString_EscapesSmuggledVariationSelectors(t *testing.T) {
	var in, want strings.Builder
	in.WriteString("x")
	want.WriteString("x")
	for r := rune(0xe0100); r <= 0xe01ef; r++ {
		in.WriteRune(r)
		want.WriteString(escaped(r))
	}
	if got := textsafe.String(in.String()); got != want.String() {
		t.Errorf("String(smuggled payload) = %+q..., want %q...", got[:min(len(got), 40)], want.String()[:40])
	}
}

// The whole property, so a code point added to it by a later Unicode version, or missed from the list above, is
// caught.
func TestString_EscapesEveryDefaultIgnorableExceptThoseKept(t *testing.T) {
	missed := 0
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !defaultIgnorable(r) || r == 0x200c || r == 0x200d || r == 0xfe0f {
			continue
		}
		if got := textsafe.String("a" + string(r) + "b"); strings.ContainsRune(got, r) {
			if missed++; missed <= 20 {
				t.Errorf("U+%04X written as it is: %q", r, got)
			}
		}
	}
	if missed > 20 {
		t.Errorf("and %d more", missed-20)
	}
}

func TestString_KeepsEmojiAndJoinedScripts(t *testing.T) {
	vs16, zwj, zwnj := string(rune(0xfe0f)), string(rune(0x200d)), string(rune(0x200c))
	for name, s := range map[string]string{
		"red heart":     string(rune(0x2764)) + vs16,
		"keycap 1":      "1" + vs16 + string(rune(0x20e3)),
		"family":        string(rune(0x1f468)) + zwj + string(rune(0x1f469)) + zwj + string(rune(0x1f467)),
		"Persian word":  string([]rune{0x645, 0x6cc}) + zwnj + string([]rune{0x631, 0x648, 0x62f}),
		"flag of Wales": blackFlag + tags("gbwls") + cancelTag,
	} {
		if got := textsafe.String("Mac " + s); got != "Mac "+s {
			t.Errorf("%s: got %q", name, got)
		}
	}
}
