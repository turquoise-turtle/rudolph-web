package web

// Review of the input handling fixes, round 1: the pager's link budget, tested on the links builder itself so the
// longest cursors it accepts can be given without building rows that produce them.

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

// reviewLinkBudget is the longest path and query a page link may be. CloudFront allows a URL 8192 bytes, but that
// counts the scheme and host as well, so a link needs room left for them.
const reviewLinkBudget = 7680

// Four earlier cursors and the current one, each as long as the pager accepts, beside a filter as long as a filter
// may be, add up to well over a URL: the Next link must give up the oldest earlier pages, never its own cursor, and
// keep as many of the newest as fit.
func TestLinks_ReviewR1_NextLinkFitsTheBudgetWithTheLongestCursors(t *testing.T) {
	cursor := func(c byte) string { return strings.Repeat(string(c), 2048) }
	nav := pageNav{Cursor: cursor('e'), Number: 6, Back: []string{cursor('a'), cursor('b'), cursor('c'), cursor('d')}}
	next := cursor('n')
	filters := url.Values{"team_id": {strings.Repeat("T", 256)}}
	p := links("/events", filters, nav, next)

	if p.Next == "" {
		t.Fatal("no Next link")
	}
	if len(p.Next) > reviewLinkBudget {
		t.Errorf("the Next link is %d bytes long; a link may be at most %d", len(p.Next), reviewLinkBudget)
	}
	u, err := url.Parse(p.Next)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("cursor") != next {
		t.Fatalf("the Next link carries a cursor of %d bytes, want its own of %d", len(q.Get("cursor")), len(next))
	}
	if q.Get("team_id") != filters.Get("team_id") || q.Get("page") != "7" {
		t.Errorf("the Next link lost its filter or number: team_id of %d bytes, page %q", len(q.Get("team_id")), q.Get("page"))
	}
	var back []string
	if b := q.Get("back"); b != "" {
		back = strings.Split(b, backSep)
	}
	trail := append(slices.Clone(nav.Back), nav.Cursor)
	if len(back) == 0 {
		t.Fatal("the Next link keeps no earlier page; the newest ones fit")
	}
	// The newest earlier pages are the end of the trail, page 5 (the current one) last.
	if !slices.Equal(back, trail[len(trail)-len(back):]) {
		t.Errorf("the Next link keeps %d earlier cursors that are not the newest of the trail", len(back))
	}
	// One more earlier page would not have fitted, so none was given up needlessly.
	if len(back) < len(trail) {
		v := cloneValues(q)
		v.Set("back", strings.Join(trail[len(trail)-len(back)-1:], backSep))
		if longer := "/events?" + v.Encode(); len(longer) <= reviewLinkBudget {
			t.Errorf("the Next link keeps %d earlier pages, but %d would fit in %d bytes", len(back), len(back)+1, len(longer))
		}
	}
}
