//go:build integration

package dynamo_test

// A Mac's unknown binaries, from the markers eventupload writes to the events table (UnknownSeen#<machine ID> /
// <file SHA-256>, with LastDay, the UTC date of the Mac's latest upload of an unknown execution of it, and FileName,
// SigningID and TeamID when usable; they expire 35 days after that upload, and TTL deletion lags):
//
//	MachineUnknown(ctx, machineID) (store.UnknownCounts{Last30, Last7 int; More bool}, error)
//	    one Query of the Mac's partition, Limit 1,000, counting in Go the markers whose LastDay is within the last 30
//	    and the last 7 days, today included (LastDay >= today - (N - 1), today being the UTC date of Options.Now).
//	    More is set when DynamoDB returns a LastEvaluatedKey: the Mac has more than 1,000 markers and the page shows
//	    "1,000+". A Mac without markers is zero counts, not an error.
//	ListMachineUnknown(ctx, machineID, store.PageRequest) (store.Page[store.UnknownBinary], error)
//	    one Query a page, Limit = page size, in SHA-256 order; markers outside the window are listed with their date,
//	    not filtered (a filter would shorten pages). The cursor is bound to the Mac.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

func unknownMarker(machineID, sha string, lastDay time.Time) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": s("UnknownSeen#" + machineID), "SK": s(sha), "LastDay": s(lastDay.Format("2006-01-02")),
		"FileName": s("tool-" + sha[60:]), "ExpiresAfter": n(int(lastDay.Add(35 * 24 * time.Hour).Unix())),
	}
}

func markerSHA(i int) string { return fmt.Sprintf("%064x", i) }

func TestMachineUnknown_CountsBothWindowsInOneQuery(t *testing.T) {
	st := newStore(t)
	today := now.UTC().Truncate(24 * time.Hour)
	// 0, 6, 7, 29, 30 and 40 days old: within 7 days 0 and 6; within 30 days those and 7 and 29.
	for i, age := range []int{0, 6, 7, 29, 30, 40} {
		putEventsItem(t, unknownMarker(machineA, markerSHA(i+1), today.AddDate(0, 0, -age)))
	}
	putEventsItem(t, unknownMarker(machineB, markerSHA(99), today))
	ddblocal.ResetCounts()
	limits := limitsUsed(t)

	got, err := st.MachineUnknown(ctx, machineA)
	if err != nil {
		t.Fatal(err)
	}
	if got != (store.UnknownCounts{Last30: 4, Last7: 2}) {
		t.Errorf("counts %+v, want 4 in 30 days and 2 in 7", got)
	}
	queriesOnly(t, 1)
	if len(*limits) != 1 || (*limits)[0] != 1000 {
		t.Errorf("Query limits %v, want [1000]", *limits)
	}

	none, err := st.MachineUnknown(ctx, "CCCCCCCC-3333-4333-8333-CCCCCCCCCCCC")
	if err != nil || none != (store.UnknownCounts{}) {
		t.Errorf("a Mac without markers: %+v, %v; want zero counts", none, err)
	}
}

func TestMachineUnknown_CutAtAThousand(t *testing.T) {
	st := newStore(t)
	today := now.UTC().Truncate(24 * time.Hour)
	var batch []types.WriteRequest
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if _, err := ddblocal.Raw(t).BatchWriteItem(ctx, &awsdynamodb.BatchWriteItemInput{
			RequestItems: map[string][]types.WriteRequest{ddblocal.EventsTableName: batch},
		}); err != nil {
			t.Fatal(err)
		}
		batch = nil
	}
	for i := 1; i <= 1001; i++ {
		batch = append(batch, types.WriteRequest{PutRequest: &types.PutRequest{Item: unknownMarker(machineA, markerSHA(i), today)}})
		if len(batch) == 25 {
			flush()
		}
	}
	flush()
	ddblocal.ResetCounts()

	got, err := st.MachineUnknown(ctx, machineA)
	if err != nil {
		t.Fatal(err)
	}
	if !got.More || got.Last30 != 1000 {
		t.Errorf("counts %+v; want More, with the 1,000 read counted", got)
	}
	queriesOnly(t, 1)
}

func TestMachineUnknown_UnavailableWithoutAnEventsTable(t *testing.T) {
	st := newStoreWithoutEvents(t)
	if _, err := st.MachineUnknown(ctx, machineA); !errors.Is(err, store.ErrUnavailable) {
		t.Errorf("MachineUnknown: %v, want ErrUnavailable", err)
	}
	if _, err := st.ListMachineUnknown(ctx, machineA, store.PageRequest{Limit: 10}); !errors.Is(err, store.ErrUnavailable) {
		t.Errorf("ListMachineUnknown: %v, want ErrUnavailable", err)
	}
}

func TestListMachineUnknown_Pages(t *testing.T) {
	st := newStore(t)
	today := now.UTC().Truncate(24 * time.Hour)
	putEventsItem(t, unknownMarker(machineA, markerSHA(1), today))
	putEventsItem(t, unknownMarker(machineA, markerSHA(2), today.AddDate(0, 0, -3)))
	old := unknownMarker(machineA, markerSHA(3), today.AddDate(0, 0, -40))
	old["SigningID"], old["TeamID"] = s("EQHXZ8M8AV:com.example.old"), s("EQHXZ8M8AV")
	putEventsItem(t, old)
	putEventsItem(t, unknownMarker(machineB, markerSHA(4), today))
	ddblocal.ResetCounts()
	limits := limitsUsed(t)

	first, err := st.ListMachineUnknown(ctx, machineA, store.PageRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].FileSHA256 != markerSHA(1) || first.NextCursor == "" ||
		!first.Items[0].LastDay.Equal(today) || first.Items[0].FileName != "tool-"+markerSHA(1)[60:] {
		t.Fatalf("first page %+v", first)
	}
	queriesOnly(t, 1)
	if (*limits)[0] != 2 {
		t.Errorf("Query limits %v, want 2", *limits)
	}

	if _, err := st.ListMachineUnknown(ctx, machineB, store.PageRequest{Limit: 2, Cursor: first.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("machine A's cursor on machine B's list: %v, want ErrInvalidCursor", err)
	}
	second, err := st.ListMachineUnknown(ctx, machineA, store.PageRequest{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	// The 40-day-old marker is listed, with its date and fields, not filtered out.
	if len(second.Items) != 1 || second.Items[0].FileSHA256 != markerSHA(3) || !second.Items[0].LastDay.Equal(today.AddDate(0, 0, -40)) ||
		second.Items[0].SigningID != "EQHXZ8M8AV:com.example.old" || second.Items[0].TeamID != "EQHXZ8M8AV" {
		t.Errorf("second page %+v", second)
	}
	for _, it := range append(first.Items, second.Items...) {
		if it.FileSHA256 == markerSHA(4) {
			t.Error("machine B's marker listed for machine A")
		}
	}
	if _, err := st.ListMachineUnknown(ctx, machineA, store.PageRequest{Limit: 0}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("limit 0: %v, want ErrInvalidArgument", err)
	}
	if strings.Contains(fmt.Sprint(ddblocal.Counts()), "Scan") {
		t.Error("the unknown list scanned")
	}
}

// newStoreWithoutEvents is a store over the store table only, as a deployment without the events table.
func newStoreWithoutEvents(t *testing.T) *dynamo.Store {
	t.Helper()
	codec, _ := store.NewCursorCodec(store.RandomCursorKey())
	st, err := dynamo.New(dynamo.Options{Client: ddblocal.Raw(t), Table: ddblocal.TableName, Cursors: codec})
	if err != nil {
		t.Fatal(err)
	}
	return st
}
