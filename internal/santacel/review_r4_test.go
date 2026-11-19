package santacel_test

// Review of the input handling fixes, round 4: what the round 3 guards still miss. A time zone with a colon is not
// charged, and a long one is charged far less than cel-go's errors about it copy; struct type names and the parts of
// a qualified prefix before a member call are never counted, though the checker suggests a declared name for each;
// and a name written with a leading dot, which the checker resolves from the root, is always counted as undeclared,
// so a valid rule is refused. The pins make sure each pre-check and the deadline stay needed: each case is stopped by
// that one alone, with its own refusal.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/santacel"
)

// The refusals of the pre-checks and the deadline, which the README describes.
const (
	refusedNesting        = "the expression nests lists and maps more than 16 deep, more than the try form checks"
	refusedUndeclaredName = "the expression uses an undeclared name longer than 64 bytes, more than the try form checks"
	refusedUndeclared     = "the expression uses undeclared names more than 32 times, more than the try form checks"
	refusedDeadline       = "the expression took longer than the 1 second the try form allows"
)

// cel-go reads a zone with a colon as an offset and runs strconv.Atoi on each side of it, and each failure's error
// copies the whole string, so the guard's free pass for offsets lets an 8 KiB zone be copied for every character of
// the path. A long zone without a colon is charged a flat 8 KiB as if loading it, but the failed load and its errors
// cost about 110 KiB for an 8 KiB name. Each loop ends in an error, as all goes on after one looking for a false.
func TestEvaluate_ReviewR4_TimeZonesAreChargedForTheirLength(t *testing.T) {
	for _, tc := range []struct {
		name, expr string
		c          santacel.Context
	}{
		{"an 8,000-byte zone with a colon, for each character", "path.split('').all(c, timestamp(0).getHours(path) >= 0)",
			santacel.Context{Path: strings.Repeat("a", 7999) + ":"}},
		{"a zone with a colon, loops nested", "path.split('').all(c, cwd.split('').all(d, timestamp(0).getHours(path) >= 0))",
			santacel.Context{Path: strings.Repeat("a", 7899) + ":", CWD: strings.Repeat("c", 100)}},
		{"an 8 KiB zone without a colon, 300 times", "cwd.split('').all(d, timestamp(0).getHours(path) >= 0)",
			santacel.Context{Path: strings.Repeat("a", 8192), CWD: strings.Repeat("c", 300)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertBounded(t, tc.expr, tc.c)
		})
	}
}

// The checker suggests a declared type for each struct type name it cannot find, as it does for other names, but
// the pre-check never counts struct names: one 16 KiB name, a few hundred long ones, a few thousand short ones.
func TestEvaluate_ReviewR4_CheckingUndeclaredStructNamesIsBounded(t *testing.T) {
	var structs []string
	for i := range 400 {
		structs = append(structs, strings.Repeat("u", 30)+string(rune('a'+i/26%26))+string(rune('a'+i%26))+"xy{}")
	}
	for _, tc := range []struct{ name, expr string }{
		{"one 16,381-byte struct name", strings.Repeat("S", 16381) + "{}"},
		{"400 different 34-character struct names", "[" + strings.Join(structs, ",") + "] == []"},
		{"2,700 structs of one undeclared name", "[" + strings.Repeat("F{},", 2700) + "F{}] == []"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.expr) > 16<<10 {
				t.Fatalf("the expression is %d bytes, longer than Rudolph stores", len(tc.expr))
			}
			assertBounded(t, tc.expr, santacel.Context{})
		})
	}
}

// A member call on a qualified prefix the environment does not declare is looked up whole, as the qualified function
// a.bbb...f, so the checker compares the whole prefix with every declared name, but the pre-check counts only the
// root a and the function f.
func TestEvaluate_ReviewR4_MemberCallOnALongUndeclaredPrefixIsBounded(t *testing.T) {
	expr := "a." + strings.Repeat("b", 16377) + ".f()"
	if len(expr) > 16<<10 {
		t.Fatalf("the expression is %d bytes, longer than Rudolph stores", len(expr))
	}
	assertBounded(t, expr, santacel.Context{})
}

// A leading dot resolves a name from the root, and the checker accepts .ALLOWLIST and
// .santa.cel.v1.ReturnValue.ALLOWLIST, but the pre-check looks the constant up with the dot and counts each as
// undeclared. A rule that passes Check must get its real result from the try form, not the count's refusal. The
// qualified form is there so that a fix covers both spellings.
func TestEvaluate_ReviewR4_LeadingDotNamesAreDeclared(t *testing.T) {
	terms := []string{".santa.cel.v1.ReturnValue.ALLOWLIST == euid"}
	for range 40 {
		terms = append(terms, ".ALLOWLIST == euid")
	}
	expr := strings.Join(terms, " || ")
	if err := santacel.Check(expr); err != nil {
		t.Fatalf("Check refused the expression: %v; the test needs one Santa would compile", err)
	}
	for _, tc := range []struct {
		euid int
		want string
	}{{1, "ALLOWLIST"}, {0, "BLOCKLIST"}} {
		t.Run("euid "+strconv.Itoa(tc.euid), func(t *testing.T) {
			got, err := santacel.Evaluate(expr, santacel.Context{EUID: tc.euid})
			if err != nil {
				t.Fatalf("refused with %q; want %s", err, tc.want)
			}
			if got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// Pins: each pre-check and the deadline is the only thing that stops its case, so removing it fails the test.
// Without the nesting check, 17 deep checks in about 1 MiB and runs; without the name checks, the checker reports
// the names as undeclared. cel-go charges a conversion as one however long its string, and a conversion that
// succeeds builds nothing the guards charge. Parsing an 8 KiB duration (4,096 of 1s) twice an iteration, for a cost
// of about three, takes long enough that without the deadline the cost limit stops the loop only after about 12 s on
// the machine the test was written on, so the deadline stays the only thing that stops it within the bound on
// machines several times faster. A comparison that holds keeps all going, and its iterations build little.
func TestEvaluate_ReviewR4_EachPreCheckAndTheDeadlineAreNeeded(t *testing.T) {
	for _, tc := range []struct {
		name, expr string
		c          santacel.Context
		want       string
	}{
		{"lists nested 17 deep", strings.Repeat("[", 17) + "1" + strings.Repeat("]", 17) + " == []", santacel.Context{}, refusedNesting},
		{"one undeclared name of 65 bytes", strings.Repeat("u", 65) + " == 1", santacel.Context{}, refusedUndeclaredName},
		{"33 undeclared references", "[" + strings.Repeat("u,", 32) + "u] == []", santacel.Context{}, refusedUndeclared},
		{"a long loop of conversions that succeed",
			"[cwd.split('')].all(l, path.split('').all(c, l.all(d, duration(path) == duration(path))))",
			santacel.Context{Path: strings.Repeat("1s", 4096), CWD: strings.Repeat("c", 100)}, refusedDeadline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertRefusedWith(t, tc.expr, tc.c, tc.want)
		})
	}
}
