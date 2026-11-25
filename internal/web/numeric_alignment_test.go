package web_test

// Headings of numeric columns (owner report, 2026-11-25): on /fleet/readiness the headings Macs, Ready, Few, Many,
// Unmeasured and Locked were left-aligned while their numbers, <td class="num">, are right-aligned (style.css:
// td.num { text-align: right }), so each number sat under the next column's heading.
//
// Every table whose cells right-align numbers gets the same alignment on the column's heading: in each column whose
// body cells are class="num", the <th> is class="num" too, and in a column whose cells are not, the <th> is not. The
// stylesheet right-aligns th.num as it does td.num. Only the class and the stylesheet: the CSP allows no inline style
// (style-src 'self'), so a style attribute would be ignored. Checked on every page with class="num" cells: /fleet,
// /fleet/health/<each list>, /fleet/readiness, /fleet/readiness/macs (with and without the cohort column), the top
// lists, and /machines/{id}/unknown (no numeric column today, so its headings must not be num either).

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

var (
	tableRe = regexp.MustCompile(`(?s)<table>(.*?)</table>`)
	theadRe = regexp.MustCompile(`(?s)<thead>(.*?)</thead>`)
	tbodyRe = regexp.MustCompile(`(?s)<tbody>(.*?)</tbody>`)
	trRe    = regexp.MustCompile(`(?s)<tr[ >](.*?)</tr>`)
	thRe    = regexp.MustCompile(`<th([^>]*)>`)
	tdRe    = regexp.MustCompile(`<td([^>]*)>`)
	numRe   = regexp.MustCompile(`\bclass="(?:[^"]* )?num(?: [^"]*)?"`)
	styleRe = regexp.MustCompile(`style=`)
)

// checkNumericHeadings checks each table of a page: column by column, the heading is num exactly when the column's
// cells are. A column's cells must agree with each other, too. It returns how many numeric columns it saw.
func checkNumericHeadings(t *testing.T, page, body string) int {
	t.Helper()
	numeric := 0
	tables := tableRe.FindAllStringSubmatch(body, -1)
	if len(tables) == 0 {
		t.Errorf("%s: no table to check", page)
	}
	for ti, table := range tables {
		head := theadRe.FindStringSubmatch(table[1])
		tbody := tbodyRe.FindStringSubmatch(table[1])
		if head == nil || tbody == nil {
			continue
		}
		var headNum []bool
		for _, th := range thRe.FindAllStringSubmatch(head[1], -1) {
			headNum = append(headNum, numRe.MatchString(th[1]))
			if styleRe.MatchString(th[1]) {
				t.Errorf("%s table %d: a heading has a style attribute, which the CSP ignores", page, ti)
			}
		}
		cellNum := make([]*bool, len(headNum))
		for _, tr := range trRe.FindAllStringSubmatch(tbody[1], -1) {
			cells := tdRe.FindAllStringSubmatch(tr[1], -1)
			if len(cells) != len(headNum) {
				t.Errorf("%s table %d: a row has %d cells under %d headings", page, ti, len(cells), len(headNum))
				continue
			}
			for i, td := range cells {
				n := numRe.MatchString(td[1])
				if cellNum[i] == nil {
					cellNum[i] = &n
				} else if *cellNum[i] != n {
					t.Errorf("%s table %d column %d: some cells are num and some are not", page, ti, i+1)
				}
			}
		}
		for i, h := range headNum {
			if cellNum[i] == nil {
				continue
			}
			if *cellNum[i] {
				numeric++
			}
			switch {
			case *cellNum[i] && !h:
				t.Errorf("%s table %d column %d: the cells are right-aligned (num) but the heading is not", page, ti, i+1)
			case !*cellNum[i] && h:
				t.Errorf("%s table %d column %d: the heading is num but its cells are not", page, ti, i+1)
			}
		}
	}
	return numeric
}

func TestNumericHeadings_FleetPages(t *testing.T) {
	s := fleetStore()
	s.AddUnknownBinary(fleetMacA, store.UnknownBinary{FileSHA256: strings.Repeat("ab", 32), FileName: "tool", SigningID: "EQHXZ8M8AV:com.example.tool",
		TeamID: "EQHXZ8M8AV", LastDay: fixedNow.Add(-24 * time.Hour)})
	a := newFleetApp(t, s, s, nil)
	for page, wantNumeric := range map[string]int{
		"/fleet":                              1,
		"/fleet/health/stale":                 0,
		"/fleet/health/incomplete":            1,
		"/fleet/health/mode":                  0,
		"/fleet/health/rules":                 2,
		"/fleet/health/sip":                   0,
		"/fleet/health/version":               0,
		"/fleet/readiness":                    6,
		"/fleet/readiness/macs":               5,
		"/fleet/readiness/macs?group=pilot":   5,
		"/fleet/readiness/macs?bucket=ready":  5,
		"/machines/" + fleetMacA + "/unknown": 0,
	} {
		for _, role := range []string{auth.RoleViewer, auth.RoleAdmin} {
			rec := a.get(t, page, a.cookie(t, role))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s as %s: status %d", page, role, rec.Code)
			}
			if got := checkNumericHeadings(t, page+" as "+role, rec.Body.String()); got != wantNumeric {
				t.Errorf("%s as %s: %d numeric columns, want %d", page, role, got, wantNumeric)
			}
		}
	}
}

func TestNumericHeadings_TopLists(t *testing.T) {
	s := sectionTabsStore()
	gen := fixedNow.Add(-40 * time.Minute)
	for _, kind := range []store.TopKind{store.TopKindBlocked, store.TopKindAllowed, store.TopKindUnknown} {
		s.SetTopList(kind, 7, gen, []store.TopBinary{
			{FileSHA256: strings.Repeat("cd", 32), FileName: "tool", TeamID: "EQHXZ8M8AV", Count: 50, Machines: 5, MachinesKnown: true},
		})
	}
	a := newApp(t, s, 10)
	for _, page := range []string{"/events/top-blocked", "/events/top-allowed", "/events/top-unknown"} {
		code, body := a.viewerGet(t, page)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", page, code)
		}
		if got := checkNumericHeadings(t, page, body); got != 2 {
			t.Errorf("%s: %d numeric columns, want 2 (Executions, Machines)", page, got)
		}
	}
}

// The heading's class must do something: the stylesheet right-aligns th.num as it does td.num. "th, td" sets
// text-align: left on every cell, which th.num overrides by being more specific.
func TestNumericHeadings_StylesheetRightAlignsThem(t *testing.T) {
	css, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	rule := regexp.MustCompile(`(?m)^([^{}]*)\{([^}]*)\}`)
	found := false
	for _, m := range rule.FindAllStringSubmatch(string(css), -1) {
		for _, sel := range strings.Split(m[1], ",") {
			sel = strings.TrimSpace(sel)
			if (sel == "th.num" || sel == ".num") && regexp.MustCompile(`text-align:\s*right`).MatchString(m[2]) {
				found = true
			}
		}
	}
	if !found {
		t.Error("style.css has no rule right-aligning th.num (or .num)")
	}
}
