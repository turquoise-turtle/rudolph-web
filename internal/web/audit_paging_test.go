package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

// /admin/audit pages like every other list (owner, 2026-11-07): one page of the app's page size, newest first, a
// signed next-page link, 400 for a tampered cursor.
func TestAuditPage_Pages(t *testing.T) {
	s := fake.New()
	a := newEditApp(t, s, true) // page size 10
	admin := a.cookie(t, auth.RoleAdmin)
	for i := 0; i < 25; i++ {
		form := globalRule()
		form.Set("identifier", auditPagingID(i))
		if rec := a.post(t, "/admin/rules", form, admin, true); rec.Code != http.StatusSeeOther {
			t.Fatalf("write %d: status %d", i, rec.Code)
		}
	}
	target := "/admin/audit"
	var counts []int
	seen := map[string]bool{}
	for page := 0; page < 5 && target != ""; page++ {
		rec := a.get(t, target, admin)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", target, rec.Code)
		}
		body := rec.Body.String()
		counts = append(counts, rows(body))
		for i := 0; i < 25; i++ {
			id := auditPagingID(i)
			if strings.Contains(body, id) {
				if seen[id] {
					t.Errorf("entry %d appears on two pages", i)
				}
				seen[id] = true
			}
		}
		target = nextLink(t, body)
	}
	if fmt.Sprint(counts) != "[10 10 5]" {
		t.Errorf("rows per page %v, want [10 10 5]", counts)
	}
	if len(seen) != 25 {
		t.Errorf("saw %d of 25 entries across the pages", len(seen))
	}
	if rec := a.get(t, "/admin/audit?cursor="+url.QueryEscape("garbage"), admin); rec.Code != http.StatusBadRequest {
		t.Errorf("tampered cursor: status %d, want 400", rec.Code)
	}
}

// auditPagingID gives entry i a distinct SHA-256-shaped identifier. The audit page shows identifiers in full and all
// have the same length, so the page an entry is on can be found by substring.
func auditPagingID(i int) string {
	return fmt.Sprintf("%02x", i+1) + strings.Repeat("e", 62)
}
