package web_test

// Review of the input handling fixes, round 1: the events page's team ID filter, and what one try on /rules/cel may
// cost within the bounds the fixes set.

import (
	"context"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

// filterSpy is the in-memory store, noting the filter the events page asks it for.
type filterSpy struct {
	*fake.Store
	asked  bool
	teamID string
}

func (s *filterSpy) ListExecutionEvents(ctx context.Context, f store.EventFilter, p store.PageRequest) (store.Page[store.ExecutionEvent], error) {
	s.asked, s.teamID = true, f.TeamID
	return s.Store.ListExecutionEvents(ctx, f, p)
}

// The team ID filter is checked against its 256 bytes before it is upper-cased, and strings.ToUpper writes each byte
// of invalid UTF-8 as U+FFFD, three bytes: 256 bytes of 0xFF reach the store as 768, which then travel in the
// cursor and every page link. No team ID is invalid UTF-8, so such a filter is best refused.
func TestEvents_ReviewR1_TeamIDFilterStaysWithinItsLengthAfterUpperCasing(t *testing.T) {
	spy := &filterSpy{Store: fake.New()}
	a := newReaderApp(t, spy, 10)
	code, _ := a.viewerGet(t, "/events?"+url.Values{"team_id": {strings.Repeat("\xff", 256)}}.Encode())
	switch {
	case code == http.StatusBadRequest:
	case code != http.StatusOK:
		t.Fatalf("a 256-byte team ID of invalid UTF-8: status %d, want 400 (or 200 with the filter kept to 256 bytes)", code)
	case spy.asked && len(spy.teamID) > 256:
		t.Errorf("a 256-byte team ID filter of invalid UTF-8 reached the store as %d bytes; filters are at most 256", len(spy.teamID))
	}
	// An ordinary team ID in lower case is still upper-cased and listed.
	spy.asked = false
	if code, _ := a.viewerGet(t, "/events?team_id=eqhxz8m8av"); code != http.StatusOK || !spy.asked || spy.teamID != "EQHXZ8M8AV" {
		t.Errorf("an ordinary team ID: status %d, store asked for %q; want 200 and EQHXZ8M8AV", code, spy.teamID)
	}
}

// The cost limit does not stop a replace before it runs, so replacing each of 8192 characters with an 8192-byte
// string builds 64 MiB within the bounds the fixes set (an 8 KiB sample, a 16 KiB expression). The try form is open
// to every viewer and a Lambda has a few hundred MiB, so a handful of such requests at once would end it. The bound
// is a quarter of those 64 MiB: an ordinary try allocates under 1 MiB, so it leaves room for the page and for the
// process allocating elsewhere meanwhile, and a fix that stops the string being built passes it however it is made.
const maxTryAllocBytes = 16 << 20

func TestCELTry_ReviewR1_AnExpressionCannotBuildAHugeString(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	long := strings.Repeat("a", 8000)
	for name, q := range map[string]url.Values{
		"the path replaced into itself": {"expr": {"path.replace('a', path) == ''"}, "path": {strings.Repeat("a", 8192)}},
		"two literals":                  {"expr": {"'" + long + "'.replace('a', '" + long + "') == ''"}},
	} {
		t.Run(name, func(t *testing.T) {
			target := "/rules/cel?" + q.Encode()
			// The least of a few tries, so that an allocation elsewhere in the process during one of them, on a
			// loaded machine, does not count against the request.
			least := uint64(1 << 63)
			for range 3 {
				var before, after runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&before)
				code, _ := a.viewerGet(t, target)
				runtime.ReadMemStats(&after)
				if code != http.StatusOK && code != http.StatusBadRequest {
					t.Fatalf("status %d, want an answer (200 with the error shown, or 400)", code)
				}
				least = min(least, after.TotalAlloc-before.TotalAlloc)
			}
			if least > maxTryAllocBytes {
				t.Errorf("one try allocated %d MiB; want at most %d MiB", least>>20, maxTryAllocBytes>>20)
			}
			t.Logf("one try allocated %.1f MiB", float64(least)/(1<<20))
		})
	}
}
