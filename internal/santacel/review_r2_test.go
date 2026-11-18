package santacel_test

// Review of the input handling fixes, round 2: what one try on /rules/cel may cost. The try form is open to every
// viewer and runs in a Lambda of a few hundred MiB, so no try may build or compile anything near that; but a try
// whose sample is long and whose evaluation stays small must still run and give its real result. The owner's
// decision is that a try is refused only for what its evaluation actually builds (a string, a regex program) past a
// bound, not for what an estimate from the sample's lengths says it might, which refuses harmless tries.

import (
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/santacel"
)

// maxTryAllocBytes bounds what one try may allocate. An ordinary try allocates under 1 MiB; this leaves room for
// the page and for the process allocating elsewhere meanwhile, and is a small part of a Lambda's memory.
const maxTryAllocBytes = 16 << 20

// measuredEvaluate evaluates expr against c and returns its answer and the least it allocated over up to three
// tries, so that an allocation elsewhere in the process during one of them does not count against it. It stops
// early once a try is within the bound, or so far past it (four times) that no noise explains it, to spare the
// shared machine repeating a try that builds hundreds of MiB.
func measuredEvaluate(t *testing.T, expr string, c santacel.Context) (string, uint64, error) {
	t.Helper()
	var (
		result string
		err    error
		least  = uint64(1 << 63)
	)
	for range 3 {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		result, err = santacel.Evaluate(expr, c)
		elapsed := time.Since(start)
		runtime.ReadMemStats(&after)
		n := after.TotalAlloc - before.TotalAlloc
		least = min(least, n)
		t.Logf("one try allocated %.1f MiB in %v", float64(n)/(1<<20), elapsed.Round(time.Millisecond))
		if least <= maxTryAllocBytes || least > 4*maxTryAllocBytes {
			break
		}
	}
	return result, least, err
}

// Each of these evaluates to something small, whatever the sample's lengths: a replace that finds nothing (or one
// character) to replace, a replace on a one-character string, a list mapped onto itself. The fix branch refuses
// them because the estimate assumes every character of the path is replaced by the whole path, an int's string is
// unbounded, and so on.
func TestEvaluate_ReviewR2_HarmlessTriesWithLongSamplesRun(t *testing.T) {
	noA := strings.Repeat("b", 8192)
	oneA := strings.Repeat("b", 4096) + "a" + strings.Repeat("b", 4095)
	var args []string
	for range 256 {
		args = append(args, strings.Repeat("c", 31))
	}
	for _, tc := range []struct {
		name, expr string
		c          santacel.Context
		want       string
	}{
		{"replace finding nothing in an 8 KiB path", "path.replace('a', path) == ''", santacel.Context{Path: noA}, "BLOCKLIST"},
		{"replace finding nothing, compared with the path", "path.replace('a', path) == path", santacel.Context{Path: noA}, "ALLOWLIST"},
		{"replace finding one character in an 8 KiB path", "path.replace('a', path).size() == 16383", santacel.Context{Path: oneA}, "ALLOWLIST"},
		{"replace on the euid's string", "string(euid).replace('0', 'x') == 'x'", santacel.Context{EUID: 0, Path: noA}, "ALLOWLIST"},
		{"256 arguments mapped twice and joined", "args.map(a, a).map(a, a).join('') != ''", santacel.Context{Args: args, Path: strings.Repeat("p", 64)}, "ALLOWLIST"},
		{"a modest repeat matched against a long path", "path.matches('b{100}')", santacel.Context{Path: noA}, "ALLOWLIST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, alloc, err := measuredEvaluate(t, tc.expr, tc.c)
			if err != nil {
				t.Fatalf("%s: refused with %q; want it run, giving %s", tc.expr, err, tc.want)
			}
			if got != tc.want {
				t.Errorf("%s = %s, want %s", tc.expr, got, tc.want)
			}
			if alloc > maxTryAllocBytes {
				t.Errorf("%s allocated %d MiB; want at most %d MiB", tc.expr, alloc>>20, maxTryAllocBytes>>20)
			}
		})
	}
}

// attackCase is a try that builds or compiles far more than the bound, within the bounds the try form sets (a
// 16 KiB expression, an 8 KiB sample, though the sample is given here directly).
type attackCase struct {
	name, expr string
	c          santacel.Context
}

// assertRefusedWithinBound checks each case stays within the bound and is refused or stopped with an error, but for
// those named in mayAnswer: a try whose result is small and whose cost is all in building it may give its answer,
// so long as building it stays within the bound.
func assertRefusedWithinBound(t *testing.T, cases []attackCase, mayAnswer ...string) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, alloc, err := measuredEvaluate(t, tc.expr, tc.c)
			if err == nil && !slices.Contains(mayAnswer, tc.name) {
				t.Errorf("%.80s... gave %s; want it refused, or stopped with an error", tc.expr, got)
			}
			if alloc > maxTryAllocBytes {
				t.Errorf("one try allocated %d MiB; want at most %d MiB", alloc>>20, maxTryAllocBytes>>20)
			}
		})
	}
}

// list is n copies of v as a CEL list literal.
func list(v string, n int) string {
	return "[" + strings.TrimSuffix(strings.Repeat(v+",", n), ",") + "]"
}

// format writes each 1e308 with %f as 309 digits and six decimals, and %.100f with a hundred; a few thousand
// clauses build half a MiB, and replacing the empty string with the path then puts the path between every two characters of it, tens of
// MiB. cel-go charges neither format nor replace until it has run. %s of a list of 8,000 doubles and %b of 3,270
// negative ints allocate tens and over a hundred MiB on the way to a short string; the %b try may give its answer,
// so long as building it stays within the bound.
func TestEvaluate_ReviewR2_AttackTriesStayBounded(t *testing.T) {
	path50 := "/" + strings.Repeat("p", 49)
	emptyArgs := make([]string, 1300)
	assertRefusedWithinBound(t, []attackCase{
		{"the path replaced into itself", "path.replace('a', path) == ''", santacel.Context{Path: strings.Repeat("a", 8192)}},
		{"two literals", "'" + strings.Repeat("a", 8000) + "'.replace('a', '" + strings.Repeat("a", 8000) + "') == ''", santacel.Context{}},
		{"1,900 %f of 1e308, the path between each character",
			"'" + strings.Repeat("%f", 1900) + "'.format(" + list("1e308", 1900) + ").replace('', path) == ''",
			santacel.Context{Path: path50}},
		{"1,200 %.100f of 1e308, the path between each character",
			"'" + strings.Repeat("%.100f", 1200) + "'.format(" + list("1e308", 1200) + ").replace('', path) == ''",
			santacel.Context{Path: path50}},
		{"the working directory as the format string, the arguments as its values",
			"cwd.format(args.map(x, 1e308)).replace('', path) == ''",
			santacel.Context{CWD: strings.Repeat("%f", 1300), Args: emptyArgs, Path: path50}},
		{"%s of a list of 8,000 doubles",
			"'%s'.format([cwd.split('').map(c, 1e308)]) == ''",
			santacel.Context{CWD: strings.Repeat("a", 8000)}},
		{"3,270 %b of -1",
			"'" + strings.Repeat("%b", 3270) + "'.format(" + list("-1", 3270) + ") == ''",
			santacel.Context{}},
	}, "3,270 %b of -1")
}

// Go's regexp compiles a{1000} to a thousand instructions, and a pattern of a few thousand such repeats (well
// under the 16 KiB expression limit) to millions, hundreds of MiB, before matching a one-character string. The
// pattern may also come from the sample, where an estimate made before the try cannot see it, but the try can: the
// pattern is known when matches is called. And a program of 40,000 instructions matched against 2,000 characters
// takes seconds, which the cost limit charges only after the match has run. Each must be refused (or stopped with
// an error) without compiling or running that much.
func TestEvaluate_ReviewR2_RegexProgramsStayBounded(t *testing.T) {
	assertRefusedWithinBound(t, []attackCase{
		{"2,300 repeats of a{1000} in a literal", "'a'.matches('" + strings.Repeat("a{1000}", 2300) + "')", santacel.Context{}},
		{"300 repeats of a{1000} in a literal", "'a'.matches('" + strings.Repeat("a{1000}", 300) + "')", santacel.Context{}},
		{"1,170 repeats of a{1000} in the path", "'a'.matches(path)", santacel.Context{Path: strings.Repeat("a{1000}", 1170)}},
		{"40 repeats of (a?){1000} matched against 2,000 characters",
			"path.matches('" + strings.Repeat("(a?){1000}", 40) + "b')",
			santacel.Context{Path: strings.Repeat("a", 2000)}},
	})
}
