package textsafe_test

import (
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/textsafe"
)

func TestString(t *testing.T) {
	for in, want := range map[string]string{
		"plain": "plain",
		"Jürgen’s Mac 👨\u200d👩\u200d👧 工程": "Jürgen’s Mac 👨\u200d👩\u200d👧 工程",
		"lab\u202emac\nFAKE":              `lab\u202emac\nFAKE`,
		"C02\x1b[2J\x07\r\t\x7f":          `C02\x1b[2J\x07\r\t\x7f`,
		"2026.9\u0085":                    `2026.9\u0085`,
		"a\u200bb\u2060c\ufeffd\u00ade\u061cf\u200eg\u200fh\u2066i\u2069": `a\u200bb\u2060c\ufeffd\u00ade\u061cf\u200eg\u200fh\u2066i\u2069`,
		"sep\u2028\u2029": `sep\u2028\u2029`,
		"tag\U000E0041":   `tag\U000e0041`,
		"bad\xffutf8":     `bad\xffutf8`,
		`back\slash`:      `back\slash`,
	} {
		if got := textsafe.String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinesKeepsLineFeeds(t *testing.T) {
	if got, want := textsafe.Lines("ERROR: x\r\n | a\u202eb\n"), "ERROR: x\\r\n | a\\u202eb\n"; got != want {
		t.Errorf("Lines = %q, want %q", got, want)
	}
}

func TestHasFormat(t *testing.T) {
	for _, s := range []string{"eng\u202e", "en\u200bg", "en\u00adg"} {
		if !textsafe.HasFormat(s) {
			t.Errorf("HasFormat(%q) = false", s)
		}
	}
	for _, s := range []string{"jürgen", "工程", "science dept", "a\u200cb", "a\u200db"} {
		if textsafe.HasFormat(s) {
			t.Errorf("HasFormat(%q) = true", s)
		}
	}
}
