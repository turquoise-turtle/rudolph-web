package dynamo

import (
	"context"
	"errors"
	"fmt"
	"time"

	rudolphdynamodb "github.com/airbnb/rudolph/pkg/dynamodb"
	"github.com/airbnb/rudolph/pkg/model/audit"
	"github.com/airbnb/rudolph/pkg/model/eventcollection"
	"github.com/airbnb/rudolph/pkg/model/globalrules"
	"github.com/airbnb/rudolph/pkg/model/groups"
	"github.com/airbnb/rudolph/pkg/model/machineconfiguration"
	"github.com/airbnb/rudolph/pkg/model/machinerules"
	"github.com/airbnb/rudolph/pkg/model/rules"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// The writer makes every change through Rudolph's own packages
// (github.com/airbnb/rudolph pkg/model/...), with Rudolph's DynamoDB client,
// so the rows are the ones the CLI writes. Those packages take no context and
// read the clock themselves, so writes use the real time, never Options.Now:
// an audit entry or a temporary mode stamped with any other time would be
// wrong for every other reader of the table.

var _ store.Writer = (*Store)(nil)

// RudolphClient is what the writer needs of Rudolph's DynamoDB client: the
// client of pkg/dynamodb, which also writes group rules and the index of
// rules whose allowed executions are collected.
type RudolphClient interface {
	rudolphdynamodb.DynamoDBClient
	rudolphdynamodb.TransactUpdateExpressionAPI
	rudolphdynamodb.UpdateExpressionAPI
}

var (
	_ groups.RegistryWriter                         = RudolphClient(nil)
	_ eventcollection.Writer                        = RudolphClient(nil)
	_ machineconfiguration.TemporaryClientModeStore = RudolphClient(nil)
)

// rudolph returns Rudolph's client of the table, creating it on first use so
// that a read-only deployment never loads it.
func (s *Store) rudolph(ctx context.Context) (RudolphClient, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	if s.writer != nil {
		return s.writer, nil
	}
	if s.region == "" {
		return nil, errors.New("dynamo: no AWS region for Rudolph's client; set Options.Region")
	}
	c, err := rudolphdynamodb.GetClient(s.table, s.region)
	if err != nil {
		return nil, fmt.Errorf("dynamo: Rudolph's DynamoDB client: %w", err)
	}
	w, ok := c.(RudolphClient)
	if !ok {
		return nil, errors.New("dynamo: Rudolph's DynamoDB client cannot write group rules")
	}
	s.writer = w
	return w, nil
}

// AddRule implements store.Writer, as rudolph rule allow/deny/... does.
func (s *Store) AddRule(ctx context.Context, actor store.Actor, target store.RuleTarget, r store.NewRule) error {
	add, err := rudolphrules.CheckAdd(actor, target, r)
	if err != nil {
		return err
	}
	if !add.ExpiresAt.IsZero() && !add.ExpiresAt.After(s.writeClock.Now()) {
		return store.Invalid("the expiry %s is not in the future", rudolphrules.ExpiryText(add.ExpiresAt))
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	var options []rules.RuleOption
	if add.Rule.CustomMessage != "" {
		options = append(options, rules.WithCustomMessage(add.Rule.CustomMessage))
	}
	switch add.Target.Kind {
	case store.TargetGlobal:
		if err := globalrules.AddNewGlobalRule(s.writeClock, c, add.Rule.Identifier, add.Rule.RuleType, add.Rule.Policy, add.Description, options...); err != nil {
			return fmt.Errorf("dynamo: adding a global rule: %w", err)
		}
		// The new row replaced any earlier one without its collection flag.
		s.forgetCollected(c, "", add.SortKey)
	case store.TargetMachine:
		if err := machinerules.AddNewMachineRule(c, add.Target.MachineID, add.Rule.Identifier, add.Rule.RuleType, add.Rule.Policy, add.Description, add.ExpiresAt, options...); err != nil {
			return fmt.Errorf("dynamo: adding a machine rule: %w", err)
		}
		s.forgetCollected(c, add.Target.MachineID, add.SortKey)
	case store.TargetGroup:
		if err := s.addGroupRule(ctx, c, add); err != nil {
			return err
		}
	}
	return s.record(ctx, c, add.Actor, store.ActionRuleAdd, add.Target.AuditTarget(), add.Summary())
}

func (s *Store) addGroupRule(ctx context.Context, c RudolphClient, add rudolphrules.Add) error {
	group := add.Target.Group
	registry, err := groups.GetRegistry(c, true)
	if err != nil {
		return fmt.Errorf("dynamo: %w", err)
	}
	created := false
	if _, ok := registry.Groups[group]; !ok {
		if group != groups.Ungrouped {
			return store.Invalid("there is no group %q; create it first with rudolph group create", group)
		}
		// The reserved group exists as soon as it has a rule, as with the CLI.
		err := groups.CreateGroup(s.writeClock, c, groups.Ungrouped, "Macs in no group", false)
		if err != nil && !errors.Is(err, groups.ErrGroupExists) {
			return fmt.Errorf("dynamo: registering the group of Macs in no group: %w", err)
		}
		created = err == nil
	}
	err = groups.PutRule(c, group, add.Rule, add.Description)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, groups.ErrGroupNotFound):
		err = store.Invalid("there is no group %q", group)
	case errors.Is(err, groups.ErrTooManyRules):
		err = store.Invalid("%v", err)
	default:
		err = fmt.Errorf("dynamo: adding a group rule: %w", err)
	}
	if created {
		// Something was written before the rule failed: record it, as the CLI does.
		summary := fmt.Sprintf("%s (failed part-way: %s)", rudolphrules.Summary(add.Rule.RuleType, add.Rule.Identifier, add.Rule.Policy, "registered the reserved group of Macs in no group"), shortError(err))
		if rerr := s.record(ctx, c, add.Actor, store.ActionRuleAdd, add.Target.AuditTarget(), summary); rerr != nil {
			return errors.Join(err, rerr)
		}
	}
	return err
}

// RemoveRule implements store.Writer, as rudolph rule remove does.
func (s *Store) RemoveRule(ctx context.Context, actor store.Actor, target store.RuleTarget, ruleType store.RuleType, identifier string) error {
	rm, err := rudolphrules.CheckRemove(actor, target, ruleType, identifier)
	if err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	var rule rules.SantaRule
	switch rm.Target.Kind {
	case store.TargetGlobal:
		row, err := globalrules.GetGlobalRuleBySortKey(c, rm.SortKey)
		if err != nil {
			return fmt.Errorf("dynamo: reading the global rule: %w", err)
		}
		if row == nil {
			return store.ErrNotFound
		}
		rule = row.SantaRule
		if err := globalrules.RemoveGlobalRule(s.writeClock, c, c, rm.SortKey, ""); err != nil {
			return fmt.Errorf("dynamo: removing the global rule: %w", err)
		}
		s.forgetCollected(c, "", rm.SortKey)
	case store.TargetMachine:
		row, err := machinerules.GetMachineRuleConsistently(c, rm.Target.MachineID, rm.SortKey)
		if err != nil {
			return fmt.Errorf("dynamo: reading the machine rule: %w", err)
		}
		if row == nil {
			return store.ErrNotFound
		}
		rule = row.SantaRule
		err = machinerules.RemoveMachineRule(c, c, rm.Target.MachineID, rm.SortKey)
		if errors.Is(err, machinerules.ErrRuleNotFound) {
			return store.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("dynamo: removing the machine rule: %w", err)
		}
		s.forgetCollected(c, rm.Target.MachineID, rm.SortKey)
	case store.TargetGroup:
		// One point read for the audit summary; groups.GetRules would read every rule of the group.
		out, err := c.GetItem(rudolphdynamodb.PrimaryKey{PartitionKey: "GroupRules#" + rm.Target.Group, SortKey: rm.SortKey}, true)
		if err != nil {
			return fmt.Errorf("dynamo: reading the group rule: %w", err)
		}
		if len(out.Item) == 0 {
			return store.ErrNotFound
		}
		var row groups.RuleRow
		if err := attributevalue.UnmarshalMap(out.Item, &row); err != nil {
			return fmt.Errorf("dynamo: reading the group rule: %w", err)
		}
		rule = row.SantaRule
		err = groups.RemoveRule(c, rm.Target.Group, rm.SortKey)
		if errors.Is(err, groups.ErrRuleNotFound) || errors.Is(err, groups.ErrGroupNotFound) {
			return store.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("dynamo: removing the group rule: %w", err)
		}
	}
	return s.record(ctx, c, rm.Actor, store.ActionRuleRemove, rm.Target.AuditTarget(), rudolphrules.Summary(rule.RuleType, rule.Identifier, rule.Policy, ""))
}

// SetTemporaryMode implements store.Writer, as rudolph config update --for/--until does.
func (s *Store) SetTemporaryMode(ctx context.Context, actor store.Actor, machineID string, mode store.ClientMode, until time.Time) error {
	now := s.writeClock.Now()
	tm, err := rudolphrules.CheckTemporaryMode(actor, machineID, mode, until, now)
	if err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	if err := machineconfiguration.SetTemporaryClientMode(c, machineID, tm.Mode, until, now); err != nil {
		return fmt.Errorf("dynamo: setting the temporary client mode: %w", err)
	}
	return s.record(ctx, c, tm.Actor, store.ActionTemporaryMode, store.MachineAuditTarget(machineID), rudolphrules.TemporaryModeSummary(mode, until))
}

// EndTemporaryMode implements store.Writer, as rudolph config update --end-temporary-mode does.
func (s *Store) EndTemporaryMode(ctx context.Context, actor store.Actor, machineID string) error {
	name, err := actor.AuditName()
	if err != nil {
		return err
	}
	if err := rudolphrules.MachineID(machineID); err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	err = machineconfiguration.EndTemporaryClientMode(c, machineID)
	if errors.Is(err, machineconfiguration.ErrNoTemporaryClientMode) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("dynamo: ending the temporary client mode: %w", err)
	}
	return s.record(ctx, c, name, store.ActionEndTemporaryMode, store.MachineAuditTarget(machineID), rudolphrules.EndTemporaryModeSummary)
}

// ListAudit implements store.Writer with Rudolph's audit.ListContext: one
// bounded Query per UTC day, never a Scan.
func (s *Store) ListAudit(ctx context.Context, since time.Time, limit int) ([]store.AuditEntry, error) {
	if limit < 1 || limit > store.MaxAuditEntries {
		return nil, store.Invalid("limit %d outside 1..%d", limit, store.MaxAuditEntries)
	}
	entries, err := audit.ListContext(ctx, s.client, s.table, since, limit)
	if err != nil {
		return nil, fmt.Errorf("dynamo: %w", err)
	}
	out := make([]store.AuditEntry, len(entries))
	for i, e := range entries {
		out[i] = store.AuditEntry{Time: e.Time, Actor: e.Actor, Action: e.Action, Target: e.Target, Summary: e.Summary}
	}
	return out, nil
}

// record adds a change that was just written to the audit trail. When that
// fails the change stays, as with the CLI, and the error says so.
func (s *Store) record(ctx context.Context, c RudolphClient, actor, action, target, summary string) error {
	err := audit.Record(s.writeClock, c, audit.Entry{Actor: actor, Action: action, Target: target, Summary: summary})
	if err != nil {
		s.log.ErrorContext(ctx, "change made but not audited", "action", action, "target", target, "summary", summary, "err", err)
		return fmt.Errorf("%w: %v", store.ErrNotAudited, err)
	}
	return nil
}

// forgetCollected stops collecting the allowed executions of a rule whose row
// was replaced or removed. A failure only leaves the rule collected, so it is
// logged, as the CLI warns.
func (s *Store) forgetCollected(c RudolphClient, machineID, sortKey string) {
	if err := eventcollection.Forget(c, machineID, sortKey); err != nil {
		s.log.Warn("the rule's allowed executions may still be collected", "machine", machineID, "rule", sortKey, "err", err)
	}
}

// shortError is the first line of err, at most 200 characters, as the CLI
// keeps it for an audit entry that is never deleted.
func shortError(err error) string {
	text := err.Error()
	for i, r := range text {
		if r == '\n' {
			text = text[:i]
			break
		}
	}
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return text
}
