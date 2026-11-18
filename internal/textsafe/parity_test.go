package textsafe_test

import (
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/textsafe"
)

// The web view escapes exactly what Rudolph's CLI (internal/cli/termsafe) escapes, so a value reads the same in both
// (review of the stored-XSS hardening, 2026-11-18).

func tags(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(0xe0000 + r)
	}
	return b.String()
}

var (
	blackFlag = string(rune(0x1f3f4))
	cancelTag = string(rune(0xe007f))
)

func TestString_KeepsSubdivisionFlags(t *testing.T) {
	for _, code := range []string{"gbeng", "gbsct", "gbwls", "usca", "fr75"} {
		flag := blackFlag + tags(code) + cancelTag
		if got := textsafe.String("Mac " + flag); got != "Mac "+flag {
			t.Errorf("flag %s: got %q", code, got)
		}
	}
	family := "\U0001F468\u200d\U0001F469\u200d\U0001F467"
	if got := textsafe.String(family); got != family {
		t.Errorf("ZWJ emoji: got %q", got)
	}
}

func TestString_EscapesTagTextThatIsNotARegionCode(t *testing.T) {
	cases := map[string]string{
		"hidden words":    blackFlag + tags("Ignore previous text") + cancelTag,
		"too long":        blackFlag + tags("gbengxyzw") + cancelTag,
		"too short":       blackFlag + tags("g") + cancelTag,
		"no cancel":       blackFlag + tags("gbeng"),
		"no flag":         "x" + tags("gbeng") + cancelTag,
		"upper-case tags": blackFlag + tags("GBENG") + cancelTag,
		"lone cancel":     blackFlag + cancelTag,
	}
	for name, s := range cases {
		got := textsafe.String(s)
		for _, r := range got {
			if r >= 0xe0000 && r <= 0xe007f {
				t.Errorf("%s: tag U+%04X written as it is in %q", name, r, got)
				break
			}
		}
		if !strings.Contains(got, `\U000e00`) {
			t.Errorf("%s: got %q, want tag escapes", name, got)
		}
	}
}

func TestString_EscapesBlankLookingCharacters(t *testing.T) {
	for _, r := range []rune{0x115f, 0x1160, 0x3164, 0xffa0, 0x2800} {
		got := textsafe.String("a" + string(r) + "b")
		if strings.ContainsRune(got, r) {
			t.Errorf("U+%04X written as it is: %q", r, got)
		}
	}
}
