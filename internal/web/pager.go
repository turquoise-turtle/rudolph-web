package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// Lists page with signed store cursors, so there is no total count: the pager numbers the pages reached so far and
// the next one ("Prev 1 … 9 10 11 [12] 13 Next"). A page link carries its own cursor (`cursor`), its number (`page`)
// and the cursors of the earlier pages it can link back to (`back`, joined with "."; base64url without padding never
// contains a dot). Page 1 needs no cursor, so `back` starts at page 2.
const (
	// maxBackPages bounds how many earlier pages keep a link, so URLs stay short. Older pages collapse to an
	// ellipsis after page 1.
	maxBackPages = 4
	// maxCursorLen bounds each cursor in `back`; store cursors are far shorter. A longer one is dropped from the
	// trail rather than producing a link that would be refused.
	maxCursorLen = 1024
	backSep      = "."
	// maxPageNumber keeps an edited `page` from overflowing when the next page is numbered.
	maxPageNumber = 1 << 20
)

// pageNav is where a list request sits in the walk through its pages.
type pageNav struct {
	Cursor string   // "" on page 1
	Number int      // 1-based
	Back   []string // cursors of pages Number-len(Back) … Number-1, all ≥ 2
}

// pager is the rendered navigation for a list.
type pager struct {
	Prev  string // "" on page 1
	Next  string // "" on the last page
	Items []pagerItem
}

type pagerItem struct {
	Number  int
	URL     string // "" for the current page
	Current bool
	Gap     bool // an ellipsis for pages that no longer have a link
}

// pageRequest builds a bounded store request from the query; each handler issues exactly one. A malformed page
// number or trail of earlier cursors is answered with 400 here and ok is false. Cursors in the trail are not checked
// until their link is followed, when the store verifies them as `cursor`.
func (s *server) pageRequest(w http.ResponseWriter, r *http.Request, q url.Values) (store.PageRequest, pageNav, bool) {
	nav, ok := parsePageNav(q)
	if !ok {
		s.renderError(w, r, http.StatusBadRequest, "This page link is invalid. Start again from the first page.")
		return store.PageRequest{}, pageNav{}, false
	}
	return store.PageRequest{Limit: s.pageSize, Cursor: nav.Cursor}, nav, true
}

func parsePageNav(q url.Values) (pageNav, bool) {
	nav := pageNav{Cursor: q.Get("cursor")}
	if b := q.Get("back"); b != "" {
		if len(b) > maxBackPages*(maxCursorLen+len(backSep)) {
			return nav, false
		}
		nav.Back = strings.Split(b, backSep)
		if len(nav.Back) > maxBackPages {
			return nav, false
		}
		for _, c := range nav.Back {
			if c == "" || len(c) > maxCursorLen {
				return nav, false
			}
		}
		if nav.Cursor == "" {
			return nav, false
		}
	}
	least := 1
	if nav.Cursor != "" {
		least = len(nav.Back) + 2
	}
	nav.Number = least
	if p := q.Get("page"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < least || n > maxPageNumber || (nav.Cursor == "" && n != 1) {
			return nav, false
		}
		nav.Number = n
	}
	return nav, true
}

// links builds the numbered pager for the page nav describes, keeping the filter params on every link. next is the
// store's cursor for the following page, "" on the last page.
func links(path string, filters url.Values, nav pageNav, next string) pager {
	base := cloneValues(filters)
	pageURL := func(number int, cursor string, back []string) string {
		v := cloneValues(base)
		if cursor != "" {
			v.Set("cursor", cursor)
			v.Set("page", strconv.Itoa(number))
			if len(back) > 0 {
				v.Set("back", strings.Join(back, backSep))
			}
		}
		if enc := v.Encode(); enc != "" {
			return path + "?" + enc
		}
		return path
	}

	var p pager
	if nav.Cursor == "" && next == "" {
		return p // a single page needs no pager
	}
	first := nav.Number - len(nav.Back) // first page with a cursor in the trail
	if nav.Number > 1 {
		p.Items = append(p.Items, pagerItem{Number: 1, URL: pageURL(1, "", nil)})
		if first > 2 {
			p.Items = append(p.Items, pagerItem{Gap: true})
		}
		for i, c := range nav.Back {
			p.Items = append(p.Items, pagerItem{Number: first + i, URL: pageURL(first+i, c, nav.Back[:i])})
		}
		if last := p.Items[len(p.Items)-1]; last.Number == nav.Number-1 {
			p.Prev = last.URL
		}
	}
	p.Items = append(p.Items, pagerItem{Number: nav.Number, Current: true})
	if next != "" {
		var back []string
		if nav.Cursor != "" {
			back = append(append(back, nav.Back...), nav.Cursor)
		}
		back = trimTrail(back)
		p.Next = pageURL(nav.Number+1, next, back)
		p.Items = append(p.Items, pagerItem{Number: nav.Number + 1, URL: p.Next})
	}
	return p
}

// trimTrail keeps the most recent earlier cursors that fit the bounds parsePageNav enforces.
func trimTrail(back []string) []string {
	if len(back) > maxBackPages {
		back = back[len(back)-maxBackPages:]
	}
	for i := len(back) - 1; i >= 0; i-- {
		if len(back[i]) > maxCursorLen {
			return back[i+1:]
		}
	}
	return back
}
