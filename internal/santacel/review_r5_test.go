package santacel_test

// Review of the input handling fixes, round 5: a leading dot names the root namespace only, so cel-go never resolves
// .x against a comprehension variable x: the checker reports .x as undeclared and searches the declared names for a
// suggestion, as it does for any undeclared name. The pre-check strips the dot, finds x bound, and counts nothing, so
// the limits on undeclared names (64 bytes each, 32 references) are bypassed by writing each reference with a dot.
// Seventy-seven references to 200-byte names cost about half a second of checker time, and 1,900 short ones about
// 10 MiB.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/santacel"
)

// boundDot wraps terms in a comprehension that binds x, so that a name written .x is the root's x, which the
// comprehension does not bind, and not the iteration variable.
func boundDot(terms []string) string {
	return "[1].all(x, [" + strings.Join(terms, ", ") + "] == [])"
}

func TestEvaluate_ReviewR5_LeadingDotNamesAreNotTheComprehensionVariable(t *testing.T) {
	var fields, members []string
	for i := range 33 {
		fields = append(fields, fmt.Sprintf(".x.a%d", i))
		members = append(members, ".x.a.size()")
	}
	for _, tc := range []struct{ name, expr, want string }{
		{"33 references .x.a0 to .x.a32", boundDot(fields), refusedUndeclared},
		{"one .x name with a 65-byte field", boundDot([]string{".x." + strings.Repeat("a", 65)}), refusedUndeclaredName},
		{"33 member calls .x.a.size()", boundDot(members), refusedUndeclared},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertRefusedWith(t, tc.expr, santacel.Context{}, tc.want)
		})
	}
}

// Positive control: the dotted constants the checker does resolve from the root still give their results, with the
// comprehension variable in scope, so that a fix does not refuse what Santa would accept.
func TestEvaluate_ReviewR5_LeadingDotConstantsStillWork(t *testing.T) {
	expr := "[1].all(x, .ALLOWLIST == euid || .santa.cel.v1.ReturnValue.ALLOWLIST == euid)"
	if err := santacel.Check(expr); err != nil {
		t.Fatalf("Check refused the expression: %v; the test needs one Santa would compile", err)
	}
	for _, tc := range []struct {
		euid int
		want string
	}{{1, "ALLOWLIST"}, {0, "BLOCKLIST"}} {
		got, err := santacel.Evaluate(expr, santacel.Context{EUID: tc.euid})
		if err != nil {
			t.Fatalf("euid %d: refused with %q; want %s", tc.euid, err, tc.want)
		}
		if got != tc.want {
			t.Errorf("euid %d: got %s, want %s", tc.euid, got, tc.want)
		}
	}
}
