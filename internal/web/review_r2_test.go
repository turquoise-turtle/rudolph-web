package web_test

// Review of the input handling fixes, round 2: text filters that grow when case-mapped, and a harmless try on
// /rules/cel with the longest sample the form takes.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

// Filters are upper- or lower-cased before they reach the store, and some valid letters grow when they are: U+0250
// is two bytes and upper-cases to U+2C6F, three; U+023A lower-cases to U+2C65, three. So 128 of either is a 256-byte
// filter that reaches the store as 384 bytes in one of its casings, and is refused as too long. Invalid UTF-8 is
// refused with its own message, since no serial, hostname or team ID is invalid text. Pinned separately because
// either check could be dropped while the other still turns the filter away.
func TestFilters_ReviewR2_GrowingAndInvalidValuesAreRefusedWithTheirReason(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	growsUpper := strings.Repeat("\u0250", 128)
	growsLower := strings.Repeat("\u023a", 128)
	if len(growsUpper) != 256 || len(strings.ToUpper(growsUpper)) != 384 || len(growsLower) != 256 || len(strings.ToLower(growsLower)) != 384 {
		t.Fatal("the test's filters do not grow from 256 to 384 bytes when case-mapped")
	}
	for _, f := range []struct{ path, key string }{
		{"/events", "team_id"},
		{"/machines", "serial"},
		{"/machines", "hostname"},
	} {
		for _, tc := range []struct{ name, value, want string }{
			{"grows when upper-cased", growsUpper, "Filter value is too long."},
			{"grows when lower-cased", growsLower, "Filter value is too long."},
			{"invalid UTF-8", strings.Repeat("\xff", 10), "Filter value is not valid text."},
		} {
			t.Run(f.key+"/"+tc.name, func(t *testing.T) {
				code, body := a.viewerGet(t, f.path+"?"+url.Values{f.key: {tc.value}}.Encode())
				if code != http.StatusBadRequest {
					t.Fatalf("%s?%s %s: status %d, want 400", f.path, f.key, tc.name, code)
				}
				if !strings.Contains(body, tc.want) {
					t.Errorf("%s?%s %s: the page does not say %q", f.path, f.key, tc.name, tc.want)
				}
			})
		}
	}
	// A 256-byte filter that keeps its length in both casings is still listed.
	if code, _ := a.viewerGet(t, "/machines?"+url.Values{"hostname": {strings.Repeat("\u00e9", 128)}}.Encode()); code != http.StatusOK {
		t.Errorf("a 256-byte hostname filter of U+00E9: status %d, want 200", code)
	}
}

// The try form takes a sample of 8 KiB, and a try on it that builds nothing large runs and shows its real result
// rather than a refusal from an estimate of what it might build (the owner's decision; santacel's review_r2_test.go
// has the cases).
func TestCELTry_ReviewR2_AHarmlessTryOnTheLongestSampleRuns(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	q := url.Values{"expr": {"path.replace('a', path) == path"}, "path": {"/" + strings.Repeat("b", 8191)}}
	code, body := a.viewerGet(t, "/rules/cel?"+q.Encode())
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}
	if !strings.Contains(body, `<output class="cel-result">ALLOWLIST</output>`) {
		i := strings.Index(body, `class="cel-answer"`)
		t.Errorf("an 8 KiB path with no 'a' replaced into itself was not answered ALLOWLIST; the page says %.300q", body[max(i, 0):])
	}
}
