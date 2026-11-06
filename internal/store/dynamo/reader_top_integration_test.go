//go:build integration

package dynamo_test

// Top blocked and top allowed binaries from the snapshots Rudolph's hourly ranker writes to the events table
// (../rudolph/internal/ranker, docs/events-table.md "Top binaries"): pointer `Top#<Blocked|Allowed>#<days>` /
// `Current` with Snapshot and GeneratedAt (RFC 3339); rows `TopSnapshot#<kind>#<days>#<snapshot>` / `<rank, 6 digits>`
// with FileSHA256, Count, Machines (first 100 rows only), FileName, SigningID, TeamID.
//
// TopBinaries reads the pointer (one GetItem) and one page of the snapshot (one Query, Limit = page size). The cursor
// pins the snapshot, so paging stays consistent when the ranker moves the pointer meanwhile, and is bound to the kind
// and window. Windows are 1, 7, 30 and 90 days; kinds BLOCKED and ALLOWED; anything else is ErrInvalidArgument. No
// pointer yet means an empty list with a zero GeneratedAt; without an events table the method answers ErrUnavailable.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

var topGenerated = time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)

func putEventsItem(t *testing.T, it map[string]types.AttributeValue) {
	t.Helper()
	if _, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{TableName: aws.String(ddblocal.EventsTableName), Item: it}); err != nil {
		t.Fatal(err)
	}
}

func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

// putSnapshot writes rows named <prefix>-1..n with descending counts, Machines on the first two only, and points the
// pointer at it when point is true.
func putSnapshot(t *testing.T, kind string, days int, id, prefix string, rows int, point bool) {
	t.Helper()
	for i := 1; i <= rows; i++ {
		it := map[string]types.AttributeValue{
			"PK": s(fmt.Sprintf("TopSnapshot#%s#%d#%s", kind, days, id)), "SK": s(fmt.Sprintf("%06d", i)),
			"FileSHA256": s(fmt.Sprintf("%s-%064d", prefix, i)[:64]), "Count": n(100 - i),
			"FileName": s(fmt.Sprintf("%s-%d", prefix, i)), "TeamID": s("TEAM"), "SigningID": s("TEAM:" + prefix),
		}
		if i <= 2 {
			it["Machines"] = n(i)
		}
		putEventsItem(t, it)
	}
	if point {
		putEventsItem(t, map[string]types.AttributeValue{
			"PK": s(fmt.Sprintf("Top#%s#%d", kind, days)), "SK": s("Current"),
			"Snapshot": s(id), "GeneratedAt": s(topGenerated.Format(time.RFC3339)),
		})
	}
}

func topNames(items []store.TopBinary) string {
	var out []string
	for _, it := range items {
		out = append(out, it.FileName)
	}
	return strings.Join(out, ",")
}

func TestTopBinaries_PagesTheSnapshotThePointerNames(t *testing.T) {
	st := newStore(t)
	putSnapshot(t, "Blocked", 7, "20261105T110000Z-a", "blk", 3, true)
	putSnapshot(t, "Allowed", 7, "20261105T110000Z-a", "alw", 1, true)
	ddblocal.ResetCounts()
	limits := limitsUsed(t)

	f := store.TopFilter{Kind: store.TopKindBlocked, WindowDays: 7}
	first, err := st.TopBinaries(ctx, f, store.PageRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if topNames(first.Items) != "blk-1,blk-2" || first.NextCursor == "" || !first.GeneratedAt.Equal(topGenerated) {
		t.Fatalf("first page %+v", first)
	}
	b := first.Items[0]
	if b.Count != 99 || !b.MachinesKnown || b.Machines != 1 || b.TeamID != "TEAM" || b.SigningID != "TEAM:blk" {
		t.Errorf("first item %+v", b)
	}
	counts := ddblocal.Counts()
	if counts["GetItem"] != 1 || counts["Query"] != 1 || counts["Scan"] != 0 {
		t.Errorf("first page calls %v, want one GetItem and one Query", counts)
	}
	if len(*limits) != 1 || (*limits)[0] != 2 {
		t.Errorf("Query limits %v, want [2]", *limits)
	}

	// The ranker moves the pointer to a new snapshot between the two pages: the cursor keeps reading the old one.
	putSnapshot(t, "Blocked", 7, "20261105T120000Z-b", "new", 3, true)
	second, err := st.TopBinaries(ctx, f, store.PageRequest{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if topNames(second.Items) != "blk-3" || second.Items[0].MachinesKnown {
		t.Errorf("second page %+v, want blk-3 without a machine count", second.Items)
	}
}

func TestTopBinaries_CursorBoundToKindAndWindow(t *testing.T) {
	st := newStore(t)
	putSnapshot(t, "Blocked", 7, "s1", "blk", 3, true)
	putSnapshot(t, "Allowed", 7, "s1", "alw", 3, true)
	putSnapshot(t, "Blocked", 30, "s1", "b30", 3, true)
	first, err := st.TopBinaries(ctx, store.TopFilter{Kind: store.TopKindBlocked, WindowDays: 7}, store.PageRequest{Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("precondition: %v %+v", err, first)
	}
	for name, f := range map[string]store.TopFilter{
		"allowed": {Kind: store.TopKindAllowed, WindowDays: 7},
		"30 days": {Kind: store.TopKindBlocked, WindowDays: 30},
	} {
		if _, err := st.TopBinaries(ctx, f, store.PageRequest{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("%s: got %v, want ErrInvalidCursor", name, err)
		}
	}
}

func TestTopBinaries_NotComputedYetAndInvalidArguments(t *testing.T) {
	st := newStore(t)
	got, err := st.TopBinaries(ctx, store.TopFilter{Kind: store.TopKindAllowed, WindowDays: 90}, store.PageRequest{Limit: 10})
	if err != nil || len(got.Items) != 0 || got.NextCursor != "" || !got.GeneratedAt.IsZero() {
		t.Errorf("no pointer yet: %+v, %v; want an empty list", got, err)
	}
	for name, c := range map[string]struct {
		f store.TopFilter
		p store.PageRequest
	}{
		"window 5":     {store.TopFilter{Kind: store.TopKindBlocked, WindowDays: 5}, store.PageRequest{Limit: 10}},
		"unknown kind": {store.TopFilter{Kind: "SOMETIMES", WindowDays: 7}, store.PageRequest{Limit: 10}},
		"limit 0":      {store.TopFilter{Kind: store.TopKindBlocked, WindowDays: 7}, store.PageRequest{Limit: 0}},
	} {
		if _, err := st.TopBinaries(ctx, c.f, c.p); !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("%s: got %v, want ErrInvalidArgument", name, err)
		}
	}
}

func TestTopBinaries_UnavailableWithoutAnEventsTable(t *testing.T) {
	codec, _ := store.NewCursorCodec(store.RandomCursorKey())
	st, err := dynamo.New(dynamo.Options{Client: ddblocal.Raw(t), Table: ddblocal.TableName, Cursors: codec})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.TopBinaries(ctx, store.TopFilter{Kind: store.TopKindBlocked, WindowDays: 7}, store.PageRequest{Limit: 10}); !errors.Is(err, store.ErrUnavailable) {
		t.Errorf("got %v, want ErrUnavailable", err)
	}
}
