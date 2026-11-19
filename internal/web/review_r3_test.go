package web_test

// Review of the input handling fixes, round 3: what a try on /rules/cel costs beyond the evaluation itself.

import (
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/santacel"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

// leastAlloc is the least f allocated over three runs, so that the process doing something else during one run
// does not count against it.
func leastAlloc(f func()) uint64 {
	least := uint64(1 << 63)
	for range 3 {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		f()
		runtime.ReadMemStats(&after)
		least = min(least, after.TotalAlloc-before.TotalAlloc)
	}
	return least
}

// Evaluate parses and checks the expression, then the handler asks santacel.Cacheable, which parses and checks it
// again. For an expression of 10 KiB, parsing and checking are most of what a try costs, so the second time nearly
// doubles it. The page's own work (decoding the query, rendering the page with the expression in it twice or so)
// is a small part of one parse and check of 5,000 ints, so the whole request should stay within one and a half times
// what Evaluate alone allocates: it does if the handler compiles once, and not if it compiles twice.
func TestCELTry_ReviewR3_TheExpressionIsCompiledOnce(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	expr := "[" + strings.Repeat("1,", 5000) + "1].size() == 5001"
	if len(expr) < 10_000 || len(expr) > 10<<10 {
		t.Fatalf("the expression is %d bytes; want about 10 KiB", len(expr))
	}
	target := "/rules/cel?" + url.Values{"expr": {expr}}.Encode()
	cookie := a.cookie(t, auth.RoleViewer)
	var code int
	var body string
	request := leastAlloc(func() {
		rec := a.get(t, target, cookie)
		code, body = rec.Code, rec.Body.String()
	})
	if code != http.StatusOK || !strings.Contains(body, `<output class="cel-result">ALLOWLIST</output>`) {
		t.Fatalf("status %d, and the page does not answer ALLOWLIST", code)
	}
	var err error
	evaluate := leastAlloc(func() { _, err = santacel.Evaluate(expr, santacel.Context{}) })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Evaluate allocates %.1f MiB, the request %.1f MiB", float64(evaluate)/(1<<20), float64(request)/(1<<20))
	if request*2 > evaluate*3 {
		t.Errorf("a try of a 10 KiB list allocates %.1f MiB through /rules/cel, %.2f times the %.1f MiB Evaluate alone does; want at most 1.5 times",
			float64(request)/(1<<20), float64(request)/float64(evaluate), float64(evaluate)/(1<<20))
	}
}
