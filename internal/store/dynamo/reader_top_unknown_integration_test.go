//go:build integration

package dynamo_test

// Top unknowns (owner, 2026-11-11): Rudolph's ranker also writes the lists Unknown, UnknownBlocked and UnknownAllowed
// (pointer Top#<kind>#<days>, rows as the other lists), the binaries no rule decided (Santa's BLOCK_UNKNOWN and
// ALLOW_UNKNOWN). Their first 100 rows are ordered for deciding what to allow (no global rule first, then Macs) and
// may carry GlobalRule, the sort key of a global rule that now covers the binary (Binary#…, SigningID#… or TeamID#…).
// store.TopKindUnknown, TopKindUnknownBlocked and TopKindUnknownAllowed read them, with TopBinary.GlobalRule, in the
// same one GetItem and one Query as the other lists.

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

func TestTopBinaries_UnknownListsAndGlobalRule(t *testing.T) {
	st := newStore(t)
	for kind, prefix := range map[string]string{"Unknown": "unk", "UnknownBlocked": "ublk", "UnknownAllowed": "ualw"} {
		putSnapshot(t, kind, 7, "20261105T110000Z-a", prefix, 3, true)
	}
	putEventsItem(t, map[string]types.AttributeValue{
		"PK": s("TopSnapshot#Unknown#7#20261105T110000Z-a"), "SK": s("000003"),
		"FileSHA256": s("c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"), "Count": n(1), "Machines": n(1),
		"FileName": s("covered"), "SigningID": s("TEAM:covered"), "GlobalRule": s("SigningID#TEAM:covered"),
	})

	for kind, want := range map[store.TopKind]string{
		store.TopKindUnknown:        "unk-1,unk-2,covered",
		store.TopKindUnknownBlocked: "ublk-1,ublk-2,ublk-3",
		store.TopKindUnknownAllowed: "ualw-1,ualw-2,ualw-3",
	} {
		ddblocal.ResetCounts()
		list, err := st.TopBinaries(ctx, store.TopFilter{Kind: kind, WindowDays: 7}, store.PageRequest{Limit: 10})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if got := topNames(list.Items); got != want {
			t.Errorf("%s: %s, want %s", kind, got, want)
		}
		if c := ddblocal.Counts(); c["GetItem"] != 1 || c["Query"] != 1 {
			t.Errorf("%s: calls %v, want one GetItem and one Query", kind, c)
		}
	}
	list, _ := st.TopBinaries(ctx, store.TopFilter{Kind: store.TopKindUnknown, WindowDays: 7}, store.PageRequest{Limit: 10})
	if len(list.Items) == 3 && (list.Items[2].GlobalRule != "SigningID#TEAM:covered" || list.Items[0].GlobalRule != "") {
		t.Errorf("GlobalRule %q and %q; want the covered row's sort key and none on the first", list.Items[2].GlobalRule, list.Items[0].GlobalRule)
	}
}
