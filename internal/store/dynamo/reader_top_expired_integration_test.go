//go:build integration

package dynamo_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

// Snapshot rows expire 8 days after the ranker writes them. A page link (cursor) older than that pins a snapshot
// that is gone: the reader says the link has expired (ErrInvalidCursor, which the pages show as "invalid or has
// expired; start again from the first page") instead of showing an empty list as if nothing had been blocked.
func TestTopBinaries_CursorToAnExpiredSnapshotIsInvalid(t *testing.T) {
	st := newStore(t)
	id := "20261105T110000Z-a"
	putSnapshot(t, "Blocked", 7, id, "blk", 3, true)
	f := store.TopFilter{Kind: store.TopKindBlocked, WindowDays: 7}
	first, err := st.TopBinaries(ctx, f, store.PageRequest{Limit: 2})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page %+v, %v", first, err)
	}

	// TTL deletes the old snapshot; the ranker has since written a new one.
	for i := 1; i <= 3; i++ {
		if _, err := ddblocal.Raw(t).DeleteItem(ctx, &awsdynamodb.DeleteItemInput{
			TableName: aws.String(ddblocal.EventsTableName),
			Key: map[string]types.AttributeValue{
				"PK": s(fmt.Sprintf("TopSnapshot#Blocked#7#%s", id)), "SK": s(fmt.Sprintf("%06d", i)),
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	putSnapshot(t, "Blocked", 7, "20261113T110000Z-b", "new", 3, true)

	page, err := st.TopBinaries(ctx, f, store.PageRequest{Limit: 2, Cursor: first.NextCursor})
	if !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("page of an expired snapshot: %+v, %v; want ErrInvalidCursor", page, err)
	}

	// The first page still works and shows the new snapshot.
	fresh, err := st.TopBinaries(ctx, f, store.PageRequest{Limit: 2})
	if err != nil || topNames(fresh.Items) != "new-1,new-2" {
		t.Errorf("first page after expiry %+v, %v", fresh, err)
	}
}
