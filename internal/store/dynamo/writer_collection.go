package dynamo

import (
	"context"
	"errors"
	"fmt"

	rudolphdynamodb "github.com/airbnb/rudolph/pkg/dynamodb"
	"github.com/airbnb/rudolph/pkg/model/eventcollection"
	"github.com/airbnb/rudolph/pkg/model/globalrules"
	"github.com/airbnb/rudolph/pkg/model/groups"
	"github.com/airbnb/rudolph/pkg/model/machinerules"
	"github.com/airbnb/rudolph/pkg/model/rules"
	"github.com/airbnb/rudolph/pkg/types"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// SetRuleCollection implements store.Writer, as rudolph rule update
// --collect-allowed-events does: the rule row's flag and its entry in the
// index of collected rules change in one transaction. A group rule goes
// through Rudolph's groups.SetCollectAllowedEvents, which keeps the index's
// Group#<group> set in step and leaves the group's RulesVersion alone, since
// Santa never sees the flag.
func (s *Store) SetRuleCollection(ctx context.Context, actor store.Actor, target store.RuleTarget, ruleType store.RuleType, identifier string, collect bool) error {
	ch, err := rudolphrules.CheckCollection(actor, target, ruleType, identifier)
	if err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	if ch.Target.Kind == store.TargetGroup {
		return s.setGroupRuleCollection(ctx, c, ch, collect)
	}
	// The index keys a Mac's rules by its stored machine ID, and the global
	// rules by none.
	indexMachineID := ""
	if ch.Target.Kind == store.TargetMachine {
		if ch.Target.MachineID, err = s.storedMachineID(c, ch.Target.MachineID); err != nil {
			return err
		}
		indexMachineID = ch.Target.MachineID
	}
	var (
		key        rudolphdynamodb.PrimaryKey
		rule       rules.SantaRule
		collecting bool
	)
	now := s.writeClock.Now()
	switch ch.Target.Kind {
	case store.TargetGlobal:
		// Strongly consistent, so a change made moments ago is not taken for
		// nothing to change.
		key = rudolphdynamodb.PrimaryKey{PartitionKey: "GlobalRules", SortKey: ch.SortKey}
		out, err := c.GetItem(key, true)
		if err != nil {
			return fmt.Errorf("dynamo: reading the global rule: %w", err)
		}
		if len(out.Item) == 0 {
			return store.ErrNotFound
		}
		var row globalrules.GlobalRuleRow
		if err := attributevalue.UnmarshalMap(out.Item, &row); err != nil {
			return fmt.Errorf("dynamo: reading the global rule: %w", err)
		}
		rule, collecting = row.SantaRule, row.CollectAllowedEvents
	case store.TargetMachine:
		row, err := machinerules.GetMachineRuleConsistently(c, ch.Target.MachineID, ch.SortKey)
		if err != nil {
			return fmt.Errorf("dynamo: reading the machine rule: %w", err)
		}
		// As the CLI: an expired rule allows nothing, and changing it would
		// look successful while changing nothing on any Mac.
		if row == nil || (row.ExpiresAfter != 0 && row.ExpiresAfter <= now.Unix()) {
			return store.ErrNotFound
		}
		if row.DeleteOnNextSync || row.Policy == types.RulePolicyRemove {
			return store.Invalid("the rule is being removed from the Mac; add it again instead")
		}
		key, rule, collecting = row.PrimaryKey, row.SantaRule, row.CollectAllowedEvents
	}
	if collecting == collect {
		return store.ErrNothingToChange
	}
	err = eventcollection.SetCollectAllowedEvents(c, indexMachineID, key, collect, now)
	if errors.Is(err, eventcollection.ErrRuleNotFound) {
		return store.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("dynamo: changing whether the rule's allowed executions are collected: %w", err)
	}
	return s.record(ctx, c, ch.Actor, store.ActionRuleUpdate, ch.Target.AuditTarget(), rudolphrules.CollectionSummary(rule.RuleType, rule.Identifier, rule.Policy, collect))
}

// setGroupRuleCollection is SetRuleCollection for a group rule, checking what
// the CLI's rule update --group checks before the write.
func (s *Store) setGroupRuleCollection(ctx context.Context, c RudolphClient, ch rudolphrules.Remove, collect bool) error {
	group := ch.Target.Group
	registry, err := groups.GetRegistry(c, true)
	if err != nil {
		return fmt.Errorf("dynamo: %w", err)
	}
	if _, ok := registry.Groups[group]; !ok {
		return store.ErrNotFound
	}
	// Read for the audit summary, and so that a flag already as asked is told apart from a change.
	row, err := groups.GetRule(c, group, ch.SortKey)
	if err != nil {
		return fmt.Errorf("dynamo: %w", err)
	}
	if row == nil {
		return store.ErrNotFound
	}
	if row.CollectAllowedEvents == collect {
		return store.ErrNothingToChange
	}
	err = groups.SetCollectAllowedEvents(c, group, ch.SortKey, collect)
	switch {
	case errors.Is(err, groups.ErrNotAnAllowRule):
		return rudolphrules.NotAnAllowRule()
	case errors.Is(err, groups.ErrRuleNotFound), errors.Is(err, groups.ErrGroupNotFound):
		return store.ErrNotFound
	case errors.Is(err, groups.ErrRegistryChanged):
		// The rule changed between Rudolph's check and its write; nothing was written.
		return store.Invalid("the rule changed while it was being updated; try again")
	case err != nil:
		return fmt.Errorf("dynamo: changing whether the group rule's allowed executions are collected: %w", err)
	}
	return s.record(ctx, c, ch.Actor, store.ActionRuleUpdate, ch.Target.AuditTarget(), rudolphrules.CollectionSummary(row.RuleType, row.Identifier, row.Policy, collect))
}
