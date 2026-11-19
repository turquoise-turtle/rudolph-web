package santacel_test

// Review of the input handling fixes, round 3: what one try on /rules/cel may still cost. Round 2's bounds hold for
// what the guarded functions build, but parsing, checking, formatting nested values, failed conversions and named
// time zones cost far more than the 16 KiB expression and 8 KiB sample suggest, and several of the guards could be
// removed with every test still passing. The owner's decision stands: a try is refused only for what its evaluation
// actually builds or costs, and a per-try backstop (a deadline of about a second, a lower cost limit) is acceptable.

import (
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/santacel"
)

// maxTryDuration bounds how long one try may take. A backstop of about a second is the owner's choice; twice that
// leaves room for a loaded machine.
const maxTryDuration = 2 * time.Second

// r3Try is one try's answer and the least it allocated and took over up to three runs, so that the process doing
// something else during one run does not count against it. A run far past either bound (four times the allocation,
// three times the time) is not repeated, to spare the shared machine.
type r3Try struct {
	result  string
	err     error
	alloc   uint64
	elapsed time.Duration
}

func r3Evaluate(t *testing.T, expr string, c santacel.Context) r3Try {
	t.Helper()
	got := r3Try{alloc: 1 << 63, elapsed: 1 << 62}
	for range 3 {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		got.result, got.err = santacel.Evaluate(expr, c)
		elapsed := time.Since(start)
		runtime.ReadMemStats(&after)
		n := after.TotalAlloc - before.TotalAlloc
		got.alloc, got.elapsed = min(got.alloc, n), min(got.elapsed, elapsed)
		t.Logf("one try allocated %.1f MiB in %v", float64(n)/(1<<20), elapsed.Round(time.Millisecond))
		within := got.alloc <= maxTryAllocBytes && got.elapsed <= maxTryDuration
		farPast := got.alloc > 4*maxTryAllocBytes || got.elapsed > 3*maxTryDuration
		if within || farPast {
			break
		}
	}
	return got
}

// assertBounded checks the property every attack must keep: within the allocation and time bounds, and refused or
// stopped with an error.
func assertBounded(t *testing.T, expr string, c santacel.Context) r3Try {
	t.Helper()
	got := r3Evaluate(t, expr, c)
	if got.err == nil {
		t.Errorf("%.80q... gave %s; want it refused, or stopped with an error", expr, got.result)
	}
	if got.alloc > maxTryAllocBytes {
		t.Errorf("one try allocated %d MiB; want at most %d MiB", got.alloc>>20, maxTryAllocBytes>>20)
	}
	if got.elapsed > maxTryDuration {
		t.Errorf("one try took %v; want at most %v", got.elapsed.Round(time.Millisecond), maxTryDuration)
	}
	return got
}

// assertRefusedWith is assertBounded for a pin on one guard: the refusal must be that guard's own, so removing the
// guard fails the test even where something else (cel-go's cost limit, another guard) would still stop the try.
func assertRefusedWith(t *testing.T, expr string, c santacel.Context, want string) {
	t.Helper()
	got := assertBounded(t, expr, c)
	if got.err != nil && !strings.Contains(got.err.Error(), want) {
		t.Errorf("refused with %q; want %q", got.err, want)
	}
}

// The fix counts the minus signs written on a number before parsing, since cel-go's parser looks far ahead for each
// one, but its scanner is not cel-go's lexer: a comment between the sign and the digit, "in" before the sign (a
// keyword it takes for an operand), and a quote left unclosed (which it reads to the end as a string, where cel-go's
// lexer ends the string at the newline) each hide thousands of them. Each parse allocates far more than the bound.
func TestEvaluate_ReviewR3_ParseCostOfNegativeNumbersIsBounded(t *testing.T) {
	for _, tc := range []struct{ name, expr string }{
		{"a comment between each sign and its digit", "[" + strings.Repeat("-//\n1,", 2000) + "1] == []"},
		{"in before each negative number", "[" + strings.Repeat("1 in -1,", 1500) + "1] == []"},
		{"an unclosed quote before them", "true || '\n[" + strings.Repeat("-1,", 3000) + "1] == []"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.expr) > 16<<10 {
				t.Fatalf("the expression is %d bytes, longer than Rudolph stores", len(tc.expr))
			}
			assertBounded(t, tc.expr, santacel.Context{})
		})
	}
}

// cel-go's checker suggests a declared name for each one it cannot find, comparing it with every declared name, so an
// expression of undeclared names costs far more to check than to parse: one 16 KiB name, a few hundred long ones, a
// few thousand calls of an undeclared function.
func TestEvaluate_ReviewR3_CheckingUndeclaredNamesIsBounded(t *testing.T) {
	var ids []string
	for i := range 450 {
		ids = append(ids, strings.Repeat("u", 30)+string(rune('a'+i/26%26))+string(rune('a'+i%26))+"xy")
	}
	for _, tc := range []struct{ name, expr string }{
		{"one 16 KiB undeclared call", strings.Repeat("a", 16<<10-2) + "()"},
		{"450 undeclared 34-character identifiers", "[" + strings.Join(ids, ",") + "] == []"},
		{"2,700 calls of an undeclared function", "[" + strings.Repeat("f(x),", 2700) + "1] == []"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.expr) > 16<<10 {
				t.Fatalf("the expression is %d bytes, longer than Rudolph stores", len(tc.expr))
			}
			assertBounded(t, tc.expr, santacel.Context{})
		})
	}
}

// format's %s writes a nested value by writing each level to a string of its own and copying it into the level
// above, so a list of 128 copies of a 7,500-byte path (under 1 MiB, which the fix measures once) nested in 70 or 100
// one-entry maps is copied once per level: hundreds of MiB for a result the fix accepts as under 1 MiB.
func TestEvaluate_ReviewR3_FormatOfNestedMapsIsBounded(t *testing.T) {
	c := santacel.Context{Path: strings.Repeat("p", 7500), CWD: strings.Repeat("c", 128)}
	for _, depth := range []int{70, 100} {
		t.Run(strconv.Itoa(depth)+" maps deep", func(t *testing.T) {
			expr := "'%s'.format([" + strings.Repeat("{1:", depth) + "cwd.split('').map(c, path)" + strings.Repeat("}", depth) + "]) == ''"
			assertBounded(t, expr, c)
		})
	}
}

// A failed conversion's error quotes the string it could not convert, so converting an 8 KiB path in a loop over
// the path's characters writes 8 KiB per iteration, and two loops nested write it hundreds of thousands of times
// before the cost limit, which charges a conversion as one, stops them. (timestamp of the path in two nested loops
// takes minutes, so is left out.)
func TestEvaluate_ReviewR3_FailedConversionsInLoopsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name, expr string
		c          santacel.Context
	}{
		{"int of the path for each character", "path.split('').all(c, int(path) > 0)", santacel.Context{Path: strings.Repeat("a", 8000)}},
		{"int of the path, loops nested", "path.split('').all(c, cwd.split('').all(d, int(path) > 0))",
			santacel.Context{Path: strings.Repeat("a", 7900), CWD: strings.Repeat("c", 100)}},
		{"timestamp of the path for each character", "path.split('').all(c, timestamp(path) > timestamp(0))", santacel.Context{Path: strings.Repeat("a", 8000)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertBounded(t, tc.expr, tc.c)
		})
	}
}

// getHours with a named zone loads the zone from the system's zone files (or Go's embedded copy) on every call, and
// cel-go charges the call as one, so 8,000 of them read and parse the zone 8,000 times.
func TestEvaluate_ReviewR3_NamedTimeZonesInLoopsAreBounded(t *testing.T) {
	if _, err := time.LoadLocation("Australia/Sydney"); err != nil {
		t.Skipf("this machine cannot load Australia/Sydney: %v", err)
	}
	assertBounded(t, "path.split('').all(c, timestamp(0).getHours('Australia/Sydney') >= 0)", santacel.Context{Path: strings.Repeat("a", 8000)})
}

// A backstop (a deadline, a lower cost limit) must still let ordinary tries on the longest sample run with their
// real answer: a loop over an 8 KiB path, a named zone, a conversion that succeeds, a long literal list.
func TestEvaluate_ReviewR3_HarmlessTriesStillRun(t *testing.T) {
	path := "/" + strings.Repeat("b", 8191)
	for _, tc := range []struct {
		name, expr string
		c          santacel.Context
		want       string
	}{
		{"a loop over every character of an 8 KiB path", "path.split('').all(c, c == '/' || c == 'b')", santacel.Context{Path: path}, "ALLOWLIST"},
		{"a named zone", "timestamp(0).getHours('Australia/Sydney') == 10", santacel.Context{}, "ALLOWLIST"},
		{"a conversion that succeeds", "int('501') == 501 && euid == 501", santacel.Context{EUID: 501}, "ALLOWLIST"},
		{"a list of 2,000 ints", "[" + strings.Repeat("1,", 2000) + "1].size() == 2001", santacel.Context{}, "ALLOWLIST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "a named zone" {
				if _, err := time.LoadLocation("Australia/Sydney"); err != nil {
					t.Skipf("this machine cannot load Australia/Sydney: %v", err)
				}
			}
			got := r3Evaluate(t, tc.expr, tc.c)
			if got.err != nil {
				t.Fatalf("%.80q: refused with %q; want %s", tc.expr, got.err, tc.want)
			}
			if got.result != tc.want {
				t.Errorf("%.80q = %s, want %s", tc.expr, got.result, tc.want)
			}
		})
	}
}

// The refusals try.go gives, which the README describes.
const (
	refusedString  = "the expression builds a string longer than the 1 MiB the try form allows"
	refusedBuilt   = "the expression builds more than the 4 MiB of strings the try form allows in all"
	refusedClauses = "a format string has more than 10,000 clauses, more than the try form formats"
	refusedPattern = "a regular expression is longer than the 16 KiB the try form compiles"
	refusedSteps   = "matching the expression's regular expressions would take longer than the try form allows"
)

// doubled nests one-element lists mapped onto themselves, CEL's only way to name a value, so that each level doubles
// the string the level above built: s is path+path at the first level, and the innermost is given the last name.
func doubled(levels int, inner func(name string) string) string {
	var b strings.Builder
	prev := "path"
	for i := range levels {
		name := "d" + strconv.Itoa(i)
		b.WriteString("[" + prev + " + " + prev + "].all(" + name + ", ")
		prev = name
	}
	b.WriteString(inner(prev))
	b.WriteString(strings.Repeat(")", levels))
	return b.String()
}

// Each guard in try.go could be removed today with every earlier test still passing, as another guard or cel-go's
// cost limit stops the same tries. Each case here is stopped by one guard alone, and asserts that guard's refusal.
//
// A loop goes on after a call is refused (all looks for a false to outweigh the error), and cel-go charges the
// refused calls too, so a loop that runs into the cost limit ends with the cost limit's error whatever the guards
// did. Each loop is therefore sized to pass the guard's bound well within the cost limit: concatenating the 8 KiB
// path to itself costs about 1,650 and is charged 16 KiB, bytes() about 820 for 8 KiB, an 8,192-part split about
// 9,000 for 128 KiB. The strings extension's rune copies cost about 9,000 for the 40 KiB charged, so their own
// charges alone cannot reach 4 MiB before the cost limit does: those cases first build 3.75 MiB by concatenation,
// and the rune copies pass the bound only if they are charged.
func TestEvaluate_ReviewR3_EachGuardIsNeeded(t *testing.T) {
	path8k := func(cwd int) santacel.Context {
		return santacel.Context{Path: strings.Repeat("a", 8192), CWD: strings.Repeat("c", cwd)}
	}
	// 240 concatenations are 3.75 MiB, then up to 20 calls of the rune-copying function.
	runeCopies := func(call string) string {
		return "cwd.split('').all(c, (path + path).size() > 0) && [" + strings.Repeat("0,", 19) + "0].all(i, " + call + ".size() > 0)"
	}
	// Invalid bytes become a 3-byte U+FFFD in the copy, more than the byte each is in the path.
	invalid := santacel.Context{Path: strings.Repeat("\xff", 8192), CWD: strings.Repeat("c", 240)}
	for _, tc := range []struct {
		name, expr string
		c          santacel.Context
		want       string
	}{
		// 300 iterations of 16 KiB each, no string near 1 MiB.
		{"concatenation in a loop", "cwd.split('').all(c, (path + path).size() > 0)", path8k(300), refusedBuilt},
		// 8,192 references to the path joined: one 64 MiB string.
		{"join", "path.split('').map(c, path).join() == ''", path8k(0), refusedString},
		// Each inner split is a list of 8,192 strings, 128 KiB of headers sharing the path's bytes; 50 of them.
		{"split in a loop", "cwd.split('').all(c, path.split('').size() > 0)", path8k(50), refusedBuilt},
		{"lowerAscii after 3.75 MiB", runeCopies("path.lowerAscii()"), invalid, refusedBuilt},
		{"upperAscii after 3.75 MiB", runeCopies("path.upperAscii()"), invalid, refusedBuilt},
		{"reverse after 3.75 MiB", runeCopies("path.reverse()"), invalid, refusedBuilt},
		{"substring after 3.75 MiB", runeCopies("path.substring(1)"), invalid, refusedBuilt},
		// 600 copies of the path's 8 KiB.
		{"bytes in a loop", "cwd.split('').all(c, bytes(path).size() > 0)", path8k(600), refusedBuilt},
		// Seven doublings of the 8 KiB path reach exactly 1 MiB and one more byte passes it, about 3 MiB in all:
		// within the total, so only the per-string bound refuses it.
		{"one string just over 1 MiB", doubled(7, func(s string) string { return "(" + s + " + 'x').size() > 0" }), path8k(0), refusedString},
		// Four copies of 2,600 %s is 10,400 clauses, from a 5,200-byte path.
		{"more than 10,000 format clauses", "(path + path + path + path).format((path + path + path + path).split('')) == ''",
			santacel.Context{Path: strings.Repeat("%s", 2600)}, refusedClauses},
		// A character class of 24 KiB of one letter compiles to one instruction: only the pattern's length refuses it.
		{"a 24 KiB pattern from the sample", "'a'.matches('[' + path + path + path + ']')", path8k(0), refusedPattern},
		// Counted as about 1,000 instructions, against 2,000 characters that is 2 million steps a match, and eight
		// matches take 16 million, which cel-go charges only after each has run. The match fails, so all goes on.
		{"a regex matched against a long input many times", "cwd.split('').all(c, !path.matches('" + strings.Repeat("a?", 250) + "b'))",
			santacel.Context{Path: strings.Repeat("a", 2000), CWD: strings.Repeat("c", 8)}, refusedSteps},
		// dyn hides the receiver's type from the checker. replace has one overload of each arity, so the checker still
		// resolves it; _+_ has many, so with both operands dyn the call is dispatched by the function's name at run
		// time, which only the guard's binding by name covers.
		{"replace of a dyn receiver", "dyn(path).replace('a', path) == ''", path8k(0), refusedString},
		{"concatenation dispatched at run time", "cwd.split('').all(c, (dyn(path) + dyn(path)).size() > 0)", path8k(300), refusedBuilt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.expr) > 16<<10 {
				t.Fatalf("the expression is %d bytes, longer than Rudolph stores", len(tc.expr))
			}
			assertRefusedWith(t, tc.expr, tc.c, tc.want)
		})
	}
}
