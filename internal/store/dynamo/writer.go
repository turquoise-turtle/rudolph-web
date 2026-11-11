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
	"github.com/airbnb/rudolph/pkg/model/sensordata"
	"github.com/airbnb/rudolph/pkg/types"
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

// storedMachineID returns the spelling a machine ID is stored under, as the
// CLI's flags.ResolveMachineID finds it (that function is internal to
// Rudolph's CLI, so its steps are repeated here with Rudolph's keys): the
// spelling a Mac syncs under, else one with a machine configuration, else one
// with machine rules, trying the ID as given, in upper case and in lower case.
// Unlike the CLI, which then falls back to upper case, a machine Rudolph has
// nothing for is store.ErrNotFound: the web view only edits machines it shows.
// Point reads only (GetItem, and a Query of at most one item), which the
// read-only role already allows.
func (s *Store) storedMachineID(c RudolphClient, id string) (string, error) {
	if err := rudolphrules.MachineID(id); err != nil {
		return "", err
	}
	spellings := rudolphrules.Spellings(id)
	exists := func(key rudolphdynamodb.PrimaryKey) (bool, error) {
		out, err := c.GetItem(key, false)
		if err != nil {
			return false, fmt.Errorf("dynamo: looking up machine %s: %w", id, err)
		}
		return len(out.Item) > 0, nil
	}
	for _, sp := range spellings {
		pk, sk := sensordata.MachineIDSensorDataPKSK(sp)
		if ok, err := exists(rudolphdynamodb.PrimaryKey{PartitionKey: pk, SortKey: sk}); err != nil || ok {
			return sp, err
		}
	}
	for _, sp := range spellings {
		if ok, err := exists(machineconfiguration.MachineConfigurationKey(sp)); err != nil || ok {
			return sp, err
		}
	}
	for _, sp := range spellings {
		ok, err := machinerules.HasMachineRules(c, sp)
		if err != nil {
			return "", fmt.Errorf("dynamo: looking up machine %s: %w", id, err)
		}
		if ok {
			return sp, nil
		}
	}
	return "", rudolphrules.UnknownMachine(id)
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
	if add.Target.Kind == store.TargetMachine {
		if add.Target.MachineID, err = s.storedMachineID(c, add.Target.MachineID); err != nil {
			return err
		}
	}
	var options []rules.RuleOption
	if add.Rule.CustomMessage != "" {
		options = append(options, rules.WithCustomMessage(add.Rule.CustomMessage))
	}
	if add.Rule.CELExpr != "" {
		options = append(options, rules.WithCELExpr(add.Rule.CELExpr))
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
	if rm.Target.Kind == store.TargetMachine {
		if rm.Target.MachineID, err = s.storedMachineID(c, rm.Target.MachineID); err != nil {
			return err
		}
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
		s.forgetIfRemoved(c, "", rm.SortKey)
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
		s.forgetIfRemoved(c, rm.Target.MachineID, rm.SortKey)
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
	until = withMinimumSlack(until, now)
	tm, err := rudolphrules.CheckTemporaryMode(actor, machineID, mode, until, now)
	if err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	if machineID, err = s.storedMachineID(c, machineID); err != nil {
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
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	if machineID, err = s.storedMachineID(c, machineID); err != nil {
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

// minimumSlack is how much shorter than Rudolph's one-minute minimum a
// temporary mode may arrive: the handler computes the end from the duration
// asked for a moment before the store writes it.
const minimumSlack = 5 * time.Second

// withMinimumSlack moves an end that falls just short of the one-minute
// minimum, by at most minimumSlack, to exactly a minute from now, so that the
// shortest mode the form offers is not refused for the time the request took.
// Rudolph's own check is unchanged.
func withMinimumSlack(until, now time.Time) time.Time {
	if left := until.Sub(now); left < machineconfiguration.MinTemporaryClientModeDuration && left >= machineconfiguration.MinTemporaryClientModeDuration-minimumSlack {
		return now.Add(machineconfiguration.MinTemporaryClientModeDuration)
	}
	return until
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

// forgetIfRemoved takes a removed rule out of the collection index only when
// it is really gone: a global rule with no row, or a machine rule marked for
// removal, as the CLI's ruleRemoved checks. A rule written again since the
// removal keeps its collection. The reads are strongly consistent, since the
// removal was written moments ago.
func (s *Store) forgetIfRemoved(c RudolphClient, machineID, sortKey string) {
	removed := false
	if machineID == "" {
		out, err := c.GetItem(rudolphdynamodb.PrimaryKey{PartitionKey: "GlobalRules", SortKey: sortKey}, true)
		if err != nil {
			s.log.Warn("could not check that the global rule is gone; its allowed executions may still be collected", "rule", sortKey, "err", err)
			return
		}
		removed = len(out.Item) == 0
	} else {
		row, err := machinerules.GetMachineRuleConsistently(c, machineID, sortKey)
		if err != nil {
			s.log.Warn("could not check that the machine rule is removed; its allowed executions may still be collected", "machine", machineID, "rule", sortKey, "err", err)
			return
		}
		removed = row == nil || row.DeleteOnNextSync || row.Policy == types.RulePolicyRemove
	}
	if removed {
		s.forgetCollected(c, machineID, sortKey)
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
