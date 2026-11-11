//go:build integration

package dynamo_test

// Client mode per device group (Rudolph's docs/groups.md "Client mode per group"): the registry entry's ClientMode
// (number 1–3). ListGroups returns it as store.Group.ClientMode, "" when absent or undecodable (a hand-edited string or
// 99 must not break the list). SetGroupClientMode writes it through Rudolph's groups.SetClientMode (one conditional
// update that keeps the rest of the entry and bumps MembershipVersion) and records config.update with the web actor;
// "" removes it. rudolph:ungrouped is registered first when it isn't. An unknown group is store.ErrNotFound; the mode
// it already has is store.ErrNothingToChange, with nothing written or audited.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	awstypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

func setRawGroupMode(t *testing.T, group string, v awstypes.AttributeValue) {
	t.Helper()
	_, err := ddblocal.Raw(t).UpdateItem(context.Background(), &awsdynamodb.UpdateItemInput{
		TableName:                 aws.String(ddblocal.TableName),
		Key:                       map[string]awstypes.AttributeValue{"PK": &awstypes.AttributeValueMemberS{Value: "GroupRegistry"}, "SK": &awstypes.AttributeValueMemberS{Value: "Current"}},
		UpdateExpression:          aws.String("SET #g.#n.ClientMode = :m"),
		ExpressionAttributeNames:  map[string]string{"#g": "Groups", "#n": group},
		ExpressionAttributeValues: map[string]awstypes.AttributeValue{":m": v},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGroupMode_ListGroupsReadsTheMode(t *testing.T) {
	st := newStore(t)
	for _, name := range []string{"pilot", "bad"} {
		if _, err := st.CreateGroup(ctx, writerActor, name, "", true); err != nil {
			t.Fatal(err)
		}
	}
	setRawGroupMode(t, "pilot", &awstypes.AttributeValueMemberN{Value: "2"})
	setRawGroupMode(t, "bad", &awstypes.AttributeValueMemberS{Value: "LOCKDOWN"})
	groups, err := st.ListGroups(ctx)
	if err != nil {
		t.Fatalf("an undecodable mode must not break the list: %v", err)
	}
	if g := findGroup(t, groups, "pilot"); g == nil || g.ClientMode != store.ClientModeLockdown {
		t.Errorf("pilot %+v, want LOCKDOWN", g)
	}
	if g := findGroup(t, groups, "bad"); g == nil || g.ClientMode != "" {
		t.Errorf("bad %+v, want no mode", g)
	}
}

func TestGroupMode_SetGroupClientMode(t *testing.T) {
	st := newStore(t)
	if _, err := st.CreateGroup(ctx, writerActor, "pilot", "Pilot", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupClientMode(ctx, writerActor, "pilot", store.ClientModeLockdown); err != nil {
		t.Fatal(err)
	}
	groups, _ := st.ListGroups(ctx)
	if g := findGroup(t, groups, "pilot"); g == nil || g.ClientMode != store.ClientModeLockdown || g.Description != "Pilot" {
		t.Errorf("pilot %+v, want LOCKDOWN with its description kept", g)
	}
	if err := st.SetGroupClientMode(ctx, writerActor, "pilot", store.ClientModeLockdown); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("same mode again: %v, want ErrNothingToChange", err)
	}
	if err := st.SetGroupClientMode(ctx, writerActor, "pilot", ""); err != nil {
		t.Fatal(err)
	}
	groups, _ = st.ListGroups(ctx)
	if g := findGroup(t, groups, "pilot"); g == nil || g.ClientMode != "" {
		t.Errorf("pilot %+v after clearing", g)
	}
	if err := st.SetGroupClientMode(ctx, writerActor, "nope", store.ClientModeMonitor); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown group: %v, want ErrNotFound", err)
	}
	if err := st.SetGroupClientMode(ctx, writerActor, store.UngroupedGroup, store.ClientModeMonitor); err != nil {
		t.Fatalf("ungrouped: %v", err)
	}
	groups, _ = st.ListGroups(ctx)
	if g := findGroup(t, groups, store.UngroupedGroup); g == nil || g.ClientMode != store.ClientModeMonitor {
		t.Errorf("ungrouped %+v, want MONITOR (registered on demand)", g)
	}

	entries, err := st.ListAudit(ctx, time.Now().Add(-time.Hour), 20)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		if e.Action == "config.update" {
			got = append(got, e.Target+" "+e.Summary)
		}
	}
	if len(got) != 3 {
		t.Errorf("config.update entries %v, want 3 (set, clear, ungrouped)", got)
	}
}
