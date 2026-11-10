package web_test

// Lists show numbered pages with Prev and Next (owner request, looking at 100 imported global rules: "Next page" and
// "First page" with no Prev or page number looked odd). Lists page with signed DynamoDB cursors, so the pager numbers
// the pages reached so far and the next one, e.g. "Prev 1 2 [3] 4 Next": the link to each earlier page carries that
// page's cursor (the `back` parameter), and `page` carries the current page's number. Only the most recent earlier
// pages keep a link, so the URL stays short: older ones collapse to an ellipsis after page 1. A cursor in `back` is
// still checked by the store when its link is followed, so editing one gets a 400, as with `cursor`.

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

var (
	pagerRe     = regexp.MustCompile(`(?s)<nav class="pager"[^>]*>(.*?)</nav>`)
	pagerLinkRe = regexp.MustCompile(`<a(?: rel="(prev|next)")? href="([^"]+)">([^<]+)</a>`)
	currentRe   = regexp.MustCompile(`<span aria-current="page">(\d+)</span>`)
)

type pagerView struct {
	prev, next string
	numbers    map[string]string // page number → URL
	current    string
	gap        bool
	raw        string
}

func readPager(t *testing.T, body string) pagerView {
	t.Helper()
	m := pagerRe.FindAllStringSubmatch(body, -1)
	if len(m) != 1 {
		t.Fatalf("found %d pagers, want 1\n%s", len(m), body)
	}
	p := pagerView{numbers: map[string]string{}, raw: m[0][1]}
	for _, l := range pagerLinkRe.FindAllStringSubmatch(p.raw, -1) {
		href, text := html.UnescapeString(l[2]), strings.TrimSpace(l[3])
		switch l[1] {
		case "prev":
			if text != "Prev" {
				t.Errorf("prev link text %q, want Prev", text)
			}
			p.prev = href
		case "next":
			if text != "Next" {
				t.Errorf("next link text %q, want Next", text)
			}
			p.next = href
		default:
			if _, err := fmt.Sscan(text, new(int)); err != nil {
				t.Errorf("pager link %q is not a page number", text)
			}
			p.numbers[text] = href
		}
	}
	if c := currentRe.FindAllStringSubmatch(p.raw, -1); len(c) == 1 {
		p.current = c[0][1]
	} else {
		t.Errorf("want exactly one current page marked aria-current, got %d in %q", len(c), p.raw)
	}
	p.gap = strings.Contains(p.raw, "…")
	return p
}

func machinesOn(body string) []string {
	var hosts []string
	for _, m := range regexp.MustCompile(`host-\d{3}`).FindAllString(body, -1) {
		if len(hosts) == 0 || hosts[len(hosts)-1] != m {
			hosts = append(hosts, m)
		}
	}
	return hosts
}

func TestPager_NoFirstPageLinkAnywhere(t *testing.T) {
	s := fake.New()
	seedMachines(s, 5)
	a := newApp(t, s, 2)
	_, body := a.viewerGet(t, "/machines")
	_, body2 := a.viewerGet(t, nextLink(t, body))
	for _, b := range []string{body, body2} {
		mustNotContain(t, b, "First page", "Next page")
	}
}

func TestPager_NumbersPrevAndNextAsYouGo(t *testing.T) {
	s := fake.New()
	seedMachines(s, 9) // 5 pages of 2
	a := newApp(t, s, 2)

	_, body := a.viewerGet(t, "/machines")
	p1 := readPager(t, body)
	if p1.current != "1" || p1.prev != "" || p1.next == "" {
		t.Fatalf("page 1 pager: current %q prev %q next %q; want current 1, no Prev, a Next\n%s", p1.current, p1.prev, p1.next, p1.raw)
	}
	if p1.numbers["2"] != p1.next || len(p1.numbers) != 1 {
		t.Errorf("page 1 links %v; want only 2, the same URL as Next", p1.numbers)
	}

	_, body = a.viewerGet(t, p1.next)
	p2 := readPager(t, body)
	if p2.current != "2" {
		t.Errorf("page 2 marks %q current", p2.current)
	}
	if u, _ := url.Parse(p2.prev); u == nil || u.Path != "/machines" || u.Query().Get("cursor") != "" {
		t.Errorf("page 2 Prev %q; want the first page, /machines without a cursor", p2.prev)
	}
	if p2.numbers["1"] != p2.prev || p2.numbers["3"] != p2.next {
		t.Errorf("page 2 links %v (prev %q next %q); want 1 = Prev and 3 = Next", p2.numbers, p2.prev, p2.next)
	}

	_, body = a.viewerGet(t, p2.next)
	_, body = a.viewerGet(t, readPager(t, body).next)
	p4 := readPager(t, body)
	if p4.current != "4" || p4.numbers["1"] == "" || p4.numbers["2"] == "" || p4.numbers["3"] != p4.prev || p4.numbers["5"] != p4.next {
		t.Fatalf("page 4 pager: current %q links %v prev %q next %q; want 1 2 3 [4] 5 with 3 = Prev, 5 = Next", p4.current, p4.numbers, p4.prev, p4.next)
	}
	if got := machinesOn(body); strings.Join(got, ",") != "host-007,host-008" {
		t.Errorf("page 4 shows %v, want host-007, host-008", got)
	}

	// Every number and Prev lead back to the right page, which numbers itself the same way.
	for n, want := range map[string]string{"1": "host-001,host-002", "2": "host-003,host-004", "3": "host-005,host-006"} {
		code, b := a.viewerGet(t, p4.numbers[n])
		if code != http.StatusOK {
			t.Fatalf("page %s link from page 4: status %d", n, code)
		}
		if got := strings.Join(machinesOn(b), ","); got != want {
			t.Errorf("page %s link from page 4 shows %s, want %s", n, got, want)
		}
		if readPager(t, b).current != n {
			t.Errorf("page %s link from page 4 marks %q current", n, readPager(t, b).current)
		}
	}
	_, b := a.viewerGet(t, p4.prev)
	if back := readPager(t, b); back.current != "3" || back.prev == "" || back.numbers["4"] != back.next {
		t.Errorf("Prev from page 4: current %q links %v; want page 3 with its own Prev and 4 = Next", back.current, back.numbers)
	}

	_, body = a.viewerGet(t, p4.next)
	p5 := readPager(t, body)
	if p5.current != "5" || p5.next != "" || p5.numbers["6"] != "" || p5.numbers["4"] != p5.prev {
		t.Errorf("last page: current %q links %v next %q; want [5] with no Next and 4 = Prev", p5.current, p5.numbers, p5.next)
	}
}

func TestPager_KeepsFilters(t *testing.T) {
	s := fake.New()
	seedMachines(s, 9)
	a := newApp(t, s, 2)
	_, body := a.viewerGet(t, "/machines?hostname=host-")
	_, body = a.viewerGet(t, readPager(t, body).next)
	_, body = a.viewerGet(t, readPager(t, body).next)
	p := readPager(t, body)
	for label, link := range map[string]string{"prev": p.prev, "next": p.next, "1": p.numbers["1"], "2": p.numbers["2"]} {
		u, err := url.Parse(link)
		if err != nil || u.Query().Get("hostname") != "host-" {
			t.Errorf("%s link %q loses the hostname filter", label, link)
		}
	}
}

func TestPager_SinglePageHasNoLinks(t *testing.T) {
	s := fake.New()
	seedMachines(s, 2)
	a := newApp(t, s, 2)
	_, body := a.viewerGet(t, "/machines")
	if m := pagerRe.FindStringSubmatch(body); m != nil && pagerLinkRe.MatchString(m[1]) {
		t.Errorf("a single page shows pager links: %q", m[1])
	}
}

func TestPager_LongWalksCollapseOldPages(t *testing.T) {
	s := fake.New()
	seedMachines(s, 30) // 15 pages of 2
	a := newApp(t, s, 2)
	_, body := a.viewerGet(t, "/machines")
	for i := 1; i < 12; i++ {
		_, body = a.viewerGet(t, readPager(t, body).next)
	}
	p := readPager(t, body)
	if p.current != "12" {
		t.Fatalf("after 11 Nexts, current page %q, want 12", p.current)
	}
	for _, n := range []string{"1", "9", "10", "11", "13"} {
		if p.numbers[n] == "" {
			t.Errorf("page 12 has no link to page %s: %v", n, p.numbers)
		}
	}
	if p.numbers["2"] != "" || !p.gap {
		t.Errorf("page 12 should collapse early pages to an ellipsis after 1: links %v, ellipsis %v", p.numbers, p.gap)
	}
	if len(p.next) > 4000 {
		t.Errorf("Next URL is %d bytes; the trail of earlier pages must stay bounded", len(p.next))
	}
	_, b := a.viewerGet(t, p.numbers["9"])
	if got := strings.Join(machinesOn(b), ","); got != "host-017,host-018" {
		t.Errorf("page 9 link from page 12 shows %s", got)
	}
}

func TestPager_ForgedTrailIsBadRequest(t *testing.T) {
	s := fake.New()
	seedMachines(s, 9)
	a := newApp(t, s, 2)
	_, body := a.viewerGet(t, "/machines")
	_, body = a.viewerGet(t, readPager(t, body).next)
	_, body = a.viewerGet(t, readPager(t, body).next)
	p3 := readPager(t, body)
	u, _ := url.Parse(p3.next)
	q := u.Query()
	if q.Get("back") == "" || q.Get("page") != "4" {
		t.Fatalf("Next from page 3 %q; want back= (earlier cursors) and page=4", p3.next)
	}

	bad := map[string]func(url.Values){
		"page not a number":   func(v url.Values) { v.Set("page", "four") },
		"page zero":           func(v url.Values) { v.Set("page", "0") },
		"page too small":      func(v url.Values) { v.Set("page", "2") }, // two earlier cursors make this at least page 4
		"back without cursor": func(v url.Values) { v.Del("cursor") },
		"back too long":       func(v url.Values) { v.Set("back", strings.Repeat(v.Get("back")+".", 20)) },
	}
	for name, edit := range bad {
		v := url.Values{}
		for k, vs := range q {
			v[k] = append([]string(nil), vs...)
		}
		edit(v)
		if code, _ := a.viewerGet(t, "/machines?"+v.Encode()); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}

	// A garbled earlier cursor is caught when its link is followed.
	v := url.Values{}
	for k, vs := range q {
		v[k] = append([]string(nil), vs...)
	}
	v.Set("back", "bm90LWEtY3Vyc29y")
	_, b := a.viewerGet(t, "/machines?"+v.Encode())
	prev := readPager(t, b).prev
	if code, _ := a.viewerGet(t, prev); code != http.StatusBadRequest {
		t.Errorf("Prev built from a forged back cursor: status %d, want 400", code)
	}
}

// Every paginated list uses the same pager.
func TestPager_OnEveryList(t *testing.T) {
	s := fake.New()
	seedMachines(s, 5)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("rule-%d", i)
		s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: id})
		s.AddMachineRule("machine-001", store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: id})
	}
	a := newApp(t, s, 2)
	for _, list := range []string{"/machines", "/rules", "/machines/machine-001"} {
		_, body := a.viewerGet(t, list)
		next := readPager(t, body).next
		if next == "" {
			t.Errorf("%s: no Next", list)
			continue
		}
		code, body := a.viewerGet(t, next)
		if code != http.StatusOK {
			t.Errorf("%s page 2: status %d", list, code)
			continue
		}
		if p := readPager(t, body); p.current != "2" || p.prev == "" || p.numbers["1"] == "" {
			t.Errorf("%s page 2: current %q prev %q links %v; want Prev 1 [2]", list, p.current, p.prev, p.numbers)
		}
	}
}
