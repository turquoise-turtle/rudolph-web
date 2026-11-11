//go:build integration

package dynamo_test

// Collecting a rule's allowed executions from the web view (step S2 of the admin plan; admins, editing on), as
// `rudolph rule update ... --collect-allowed-events=true|false` does: store.Writer.SetRuleCollection sets or clears
// CollectAllowedEvents on the global or machine rule and its entry in the EventCollection index in one transaction
// (Rudolph's eventcollection.SetCollectAllowedEvents), recording the CLI's audit entry (rule.update, summary ending
// "collect allowed events true|false"). Reads return the flag as Rule.CollectAllowedEvents.
//   - Setting it to what it already is changes nothing and records nothing: store.ErrNothingToChange.
//   - A missing rule (or an expired machine rule) is store.ErrNotFound; group rules can't be collected (Rudolph's
//     index has no group scope): store.ErrInvalidArgument.

import (
	"errors"
	"slices"
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

func collectionIndex(t *testing.T, attr string) []string {
	t.Helper()
	out, err := ddblocal.Raw(t).GetItem(ctx, &awsdynamodb.GetItemInput{
		TableName:      aws.String(ddblocal.TableName),
		Key:            map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: "EventCollection"}, "SK": &types.AttributeValueMemberS{Value: "Rules"}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ss, ok := out.Item[attr].(*types.AttributeValueMemberSS); ok {
		return ss.Value
	}
	return nil
}

func ruleCollecting(t *testing.T, st *dynamo.Store, target store.RuleTarget) bool {
	t.Helper()
	var p store.Page[store.Rule]
	var err error
	if target.Kind == store.TargetGlobal {
		p, err = st.ListGlobalRules(ctx, store.RuleFilter{}, store.PageRequest{Limit: 100})
	} else {
		p, err = st.ListMachineRules(ctx, target.MachineID, store.PageRequest{Limit: 100})
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range p.Items {
		if r.Identifier == writerSHA && r.RuleType == store.RuleTypeBinary {
			return r.CollectAllowedEvents
		}
	}
	t.Fatalf("rule %s not found for %+v", writerSHA, target)
	return false
}

func TestWriter_SetRuleCollection(t *testing.T) {
	st := newStore(t)
	global := store.RuleTarget{Kind: store.TargetGlobal}
	machine := store.RuleTarget{Kind: store.TargetMachine, MachineID: machineA}
	for _, target := range []store.RuleTarget{global, machine} {
		rule := store.NewRule{RuleType: store.RuleTypeBinary, Identifier: writerSHA, Policy: store.PolicyAllowlist}
		if target.Kind == store.TargetMachine {
			rule.ExpiresAt = time.Now().Add(time.Hour).UTC()
		}
		if err := st.AddRule(ctx, writerActor, target, rule); err != nil {
			t.Fatal(err)
		}
	}

	if err := st.SetRuleCollection(ctx, writerActor, global, store.RuleTypeBinary, writerSHA, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRuleCollection(ctx, writerActor, machine, store.RuleTypeBinary, writerSHA, true); err != nil {
		t.Fatal(err)
	}
	if !ruleCollecting(t, st, global) || !ruleCollecting(t, st, machine) {
		t.Error("rules not read back as collecting")
	}
	if got := collectionIndex(t, "Global"); !slices.Contains(got, "Binary#"+writerSHA) {
		t.Errorf("index Global %v, want Binary#%s", got, writerSHA)
	}
	if got := collectionIndex(t, "Machine#"+machineA); !slices.Contains(got, "Binary#"+writerSHA) {
		t.Errorf("index for the Mac %v, want Binary#%s", got, writerSHA)
	}

	if err := st.SetRuleCollection(ctx, writerActor, global, store.RuleTypeBinary, writerSHA, true); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("collecting a rule already collected: %v, want ErrNothingToChange", err)
	}

	if err := st.SetRuleCollection(ctx, writerActor, global, store.RuleTypeBinary, writerSHA, false); err != nil {
		t.Fatal(err)
	}
	if ruleCollecting(t, st, global) || slices.Contains(collectionIndex(t, "Global"), "Binary#"+writerSHA) {
		t.Error("global rule still collecting after it was turned off")
	}
	if !ruleCollecting(t, st, machine) {
		t.Error("turning off the global rule's collection turned off the machine rule's")
	}

	entries, err := st.ListAudit(ctx, time.Now().Add(-time.Hour), 20)
	if err != nil {
		t.Fatal(err)
	}
	var updates []string
	for i := len(entries) - 1; i >= 0; i-- {
		if e := entries[i]; e.Action == "rule.update" {
			if !strings.HasPrefix(e.Actor, "web:") {
				t.Errorf("audit actor %q, want the web actor", e.Actor)
			}
			updates = append(updates, e.Target+"|"+e.Summary)
		}
	}
	if len(updates) != 3 || !strings.HasPrefix(updates[0], "global|") || !strings.HasSuffix(updates[0], "collect allowed events true") ||
		!strings.HasPrefix(updates[1], "machine:"+machineA+"|") || !strings.HasSuffix(updates[2], "collect allowed events false") {
		t.Errorf("rule.update audit entries %q; want global on, machine on, global off (the no-op unrecorded)", updates)
	}
}

func TestWriter_SetRuleCollectionRefusals(t *testing.T) {
	st := newStore(t)
	other := strings.Repeat("cd", 32)
	if err := st.SetRuleCollection(ctx, writerActor, store.RuleTarget{Kind: store.TargetGlobal}, store.RuleTypeBinary, other, true); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing global rule: %v, want ErrNotFound", err)
	}
	if err := st.SetRuleCollection(ctx, writerActor, store.RuleTarget{Kind: store.TargetGroup, Group: "eng"}, store.RuleTypeBinary, other, true); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("group rule: %v, want ErrInvalidArgument", err)
	}
	if entries, _ := st.ListAudit(ctx, time.Now().Add(-time.Hour), 5); len(entries) != 0 {
		t.Errorf("refusals were audited: %+v", entries)
	}
}
