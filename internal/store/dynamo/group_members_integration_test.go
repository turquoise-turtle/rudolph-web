//go:build integration

package dynamo_test

// Group members (Rudolph's docs/groups.md, "Group members"): `GroupMembers#<group>` / `<machine id>` with Assigned and
// Reported (BOOL true, or absent), UpdatedAt and DataType "GroupMember", kept by Rudolph in step with each Mac's
// `Machine#<id>`/`Groups` item.
//
// ListGroupMembers pages a group's rows by machine ID with one Query a page (AssignedOnly filters on Assigned, so
// reported-only rows are skipped), then reads the page's Macs' sensor data (`Machine#<id>`/`Current`) with at most
// one BatchGetItem (none for an empty page). A Mac without sensor data comes back with only its MachineID. Cursors
// are bound to the group and the filter. Assigning or removing a group on the web goes through Rudolph's
// groups.AddAssigned/RemoveAssigned, so the rows follow.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	awstypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

func putMember(t *testing.T, group, machineID string, assigned, reported bool) {
	t.Helper()
	item := map[string]awstypes.AttributeValue{
		"PK":        &awstypes.AttributeValueMemberS{Value: "GroupMembers#" + group},
		"SK":        &awstypes.AttributeValueMemberS{Value: machineID},
		"DataType":  &awstypes.AttributeValueMemberS{Value: "GroupMember"},
		"UpdatedAt": &awstypes.AttributeValueMemberS{Value: "2026-11-11T00:00:00Z"},
	}
	if assigned {
		item["Assigned"] = &awstypes.AttributeValueMemberBOOL{Value: true}
	}
	if reported {
		item["Reported"] = &awstypes.AttributeValueMemberBOOL{Value: true}
	}
	if _, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{TableName: aws.String(ddblocal.TableName), Item: item}); err != nil {
		t.Fatal(err)
	}
}

func memberExists(t *testing.T, group, machineID string) (exists, assigned bool) {
	t.Helper()
	out, err := ddblocal.Raw(t).GetItem(ctx, &awsdynamodb.GetItemInput{
		TableName:      aws.String(ddblocal.TableName),
		Key:            map[string]awstypes.AttributeValue{"PK": &awstypes.AttributeValueMemberS{Value: "GroupMembers#" + group}, "SK": &awstypes.AttributeValueMemberS{Value: machineID}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Item) == 0 {
		return false, false
	}
	b, _ := out.Item["Assigned"].(*awstypes.AttributeValueMemberBOOL)
	return true, b != nil && b.Value
}

func TestListGroupMembers(t *testing.T) {
	s := newStore(t)
	putMember(t, "eng", machineA, true, true)
	putMember(t, "eng", machineB, false, true) // machine B has no sensor data
	ddblocal.ResetCounts()

	page, err := s.ListGroupMembers(ctx, "eng", store.GroupMemberFilter{}, store.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if c := ddblocal.Counts(); c["Query"] != 1 || c["BatchGetItem"] > 1 || c["Scan"] != 0 || c["GetItem"] != 0 {
		t.Errorf("calls %v, want one Query and at most one BatchGetItem", c)
	}
	if len(page.Items) != 2 {
		t.Fatalf("members %+v, want machines A and B", page.Items)
	}
	a, b := page.Items[0], page.Items[1]
	if a.Machine.MachineID != machineA || !a.Assigned || !a.Reported || a.Machine.Hostname != "mac-aaaaaaaa.example.com" || a.Machine.SerialNum != "C02AAAAAAAA1" || a.Machine.ModelIdentifier != "Mac15,7" || a.Machine.LastSeen.IsZero() {
		t.Errorf("machine A: %+v", a)
	}
	if b.Machine.MachineID != machineB || b.Assigned || !b.Reported || b.Machine.Hostname != "" {
		t.Errorf("machine B: %+v", b)
	}

	only, err := s.ListGroupMembers(ctx, "eng", store.GroupMemberFilter{AssignedOnly: true}, store.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(only.Items) != 1 || only.Items[0].Machine.MachineID != machineA {
		t.Errorf("assigned only: %+v, want machine A", only.Items)
	}

	ddblocal.ResetCounts()
	empty, err := s.ListGroupMembers(ctx, "nobody", store.GroupMemberFilter{}, store.PageRequest{Limit: 10})
	if err != nil || len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Errorf("a group with no rows: %+v, %v", empty, err)
	}
	if c := ddblocal.Counts(); c["BatchGetItem"] != 0 {
		t.Errorf("an empty page read sensor data: %v", c)
	}
}

func TestListGroupMembers_PagesAndCursors(t *testing.T) {
	s := newStore(t)
	for i := 0; i < 25; i++ {
		putMember(t, "ops", fmt.Sprintf("%08d-0000-4000-8000-000000000000", i), i%2 == 0, i%2 == 1)
	}
	var seen []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("more than 3 pages of 10 for 25 members")
		}
		page, err := s.ListGroupMembers(ctx, "ops", store.GroupMemberFilter{}, store.PageRequest{Limit: 10, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 10 {
			t.Fatalf("a page of %d, limit 10", len(page.Items))
		}
		for _, m := range page.Items {
			seen = append(seen, m.Machine.MachineID)
		}
		if page.NextCursor == "" {
			break
		}
		if pages == 0 {
			if _, err := s.ListGroupMembers(ctx, "eng", store.GroupMemberFilter{}, store.PageRequest{Limit: 10, Cursor: page.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
				t.Errorf("another group's cursor: %v, want store.ErrInvalidCursor", err)
			}
			if _, err := s.ListGroupMembers(ctx, "ops", store.GroupMemberFilter{AssignedOnly: true}, store.PageRequest{Limit: 10, Cursor: page.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
				t.Errorf("a cursor with another filter: %v, want store.ErrInvalidCursor", err)
			}
		}
		cursor = page.NextCursor
	}
	if len(seen) != 25 {
		t.Fatalf("%d members over all pages, want 25", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i-1] >= seen[i] {
			t.Errorf("not in machine ID order: %s then %s", seen[i-1], seen[i])
		}
	}
}

// The web's assign and remove go through Rudolph's code, which keeps the member rows in step.
func TestAssignGroupKeepsTheMemberRow(t *testing.T) {
	s := newStore(t)
	if err := s.AssignGroup(ctx, writerActor, machineB, "eng"); err != nil {
		t.Fatal(err)
	}
	if exists, assigned := memberExists(t, "eng", machineB); !exists || !assigned {
		t.Errorf("after AssignGroup: row exists %v, assigned %v", exists, assigned)
	}
	if err := s.UnassignGroup(ctx, writerActor, machineB, "eng"); err != nil {
		t.Fatal(err)
	}
	if exists, _ := memberExists(t, "eng", machineB); exists {
		t.Error("after UnassignGroup: the row is still there")
	}
}
