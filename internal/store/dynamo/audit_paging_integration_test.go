//go:build integration

package dynamo_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// ListAuditPage pages Rudolph's audit trail through the store's signed cursors.
func TestListAuditPage(t *testing.T) {
	st := newStore(t)
	for i := 0; i < 5; i++ {
		rule := store.NewRule{RuleType: store.RuleTypeBinary, Identifier: fmt.Sprintf("%064x", i+1), Policy: store.PolicyBlocklist}
		if err := st.AddRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetGlobal}, rule); err != nil {
			t.Fatal(err)
		}
	}
	since := time.Now().Add(-time.Hour)
	var sizes []int
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 5; page++ {
		p, err := st.ListAuditPage(ctx, since, store.PageRequest{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, len(p.Items))
		for _, e := range p.Items {
			if seen[e.Summary] {
				t.Errorf("entry %q repeated", e.Summary)
			}
			seen[e.Summary] = true
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if fmt.Sprint(sizes) != "[2 2 1]" || len(seen) != 5 {
		t.Errorf("page sizes %v, %d distinct entries; want [2 2 1] and 5", sizes, len(seen))
	}
	if _, err := st.ListAuditPage(ctx, since, store.PageRequest{Limit: 2, Cursor: "garbage"}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("garbage cursor: %v, want ErrInvalidCursor", err)
	}
}
