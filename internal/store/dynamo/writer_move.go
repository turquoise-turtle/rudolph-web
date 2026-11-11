package dynamo

import (
	"context"
	"errors"
	"fmt"
	"time"

	rudolphdynamodb "github.com/airbnb/rudolph/pkg/dynamodb"
	"github.com/airbnb/rudolph/pkg/model/eventcollection"
	"github.com/airbnb/rudolph/pkg/model/machinerules"
	"github.com/airbnb/rudolph/pkg/model/rules"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// maxMachineRuleRows bounds how many rule rows of one Mac a move reads.
// Expired and removed rows stay until DynamoDB's TTL or a sync clears them,
// so a Mac can hold more rows than rules in force; past this many the move is
// left to the CLI rather than reading without end. It is not tied to
// store.MaxMovedRules: reading is a few paged Queries, while the limit on
// moved rules comes from the writes, one rule at a time.
const maxMachineRuleRows = 2000

// MoveMachineRules implements store.Writer. The rules are written with
// Rudolph's machinerules.AddNewMachineRule, as the CLI's rule add writes
// them, so the new Mac downloads them at its next sync, and removed with
// machinerules.RemoveMachineRule, as the CLI's rule remove does.
func (s *Store) MoveMachineRules(ctx context.Context, actor store.Actor, from, to string, remove bool) (int, error) {
	who, err := rudolphrules.CheckMove(actor, from, to)
	if err != nil {
		return 0, err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return 0, err
	}
	if from, err = s.storedMachineID(c, from); err != nil {
		return 0, err
	}
	if to, err = s.storedMachineID(c, to); err != nil {
		return 0, err
	}
	// Which rules are in force is decided by the reader's clock, so a move
	// takes exactly the rules the Mac's page shows in force.
	inForceAt := s.now()

	fromRows, err := s.machineRuleRows(ctx, c, from)
	if err != nil {
		return 0, err
	}
	var moving []machinerules.MachineRuleRow
	for _, row := range fromRows {
		if row.AppliesAt(inForceAt) {
			if row.Identifier == "" {
				// Rows written by the original Rudolph carry the identifier as SHA256.
				row.Identifier = row.SHA256
			}
			moving = append(moving, row)
		}
	}
	switch {
	case len(moving) == 0:
		return 0, rudolphrules.NothingToMove(from)
	case len(moving) > store.MaxMovedRules:
		return 0, rudolphrules.TooManyToMove(len(moving))
	}

	toRows, err := s.machineRuleRows(ctx, c, to)
	if err != nil {
		return 0, err
	}
	// Hash identifiers stored by older versions may be upper case, so rules
	// are matched as events are.
	held := map[string]machinerules.MachineRuleRow{}
	for _, row := range toRows {
		if row.AppliesAt(inForceAt) {
			held[eventcollection.MatchKey(row.SortKey)] = row
		}
	}
	var copies []machinerules.MachineRuleRow
	for _, row := range moving {
		if h, ok := held[eventcollection.MatchKey(row.SortKey)]; ok {
			// Skipped only when it is the same rule, so removing the old Mac's
			// copy loses nothing, and a retry after a failure part-way skips
			// exactly the rules it copied.
			if diff := rudolphrules.MoveDifference(movedFields(h), movedFields(row)); diff != "" {
				return 0, rudolphrules.MoveDiffers(enumText(row.RuleType), row.Identifier, diff)
			}
			continue
		}
		if err := rudolphrules.CheckCopy(row.SantaRule); err != nil {
			return 0, err
		}
		copies = append(copies, row)
	}
	index, err := eventcollection.ReadIndex(c)
	if err != nil {
		return 0, fmt.Errorf("dynamo: %w", err)
	}

	copied, removed := 0, 0
	partial := func(err error) (int, error) {
		if copied == 0 && removed == 0 {
			return 0, err
		}
		return copied, &store.PartialMoveError{Copied: copied, Removed: removed, Err: err}
	}
	for _, row := range copies {
		written, err := s.copyMachineRule(ctx, c, who, index, from, to, row)
		if written {
			copied++
		}
		if err != nil {
			return partial(err)
		}
	}
	if remove {
		for _, row := range moving {
			done, err := s.removeMovedRule(ctx, c, who, index, from, to, row)
			if done {
				removed++
			}
			if err != nil {
				return partial(err)
			}
		}
	}
	return copied, nil
}

// movedFields is what a move compares of a stored machine rule.
func movedFields(row machinerules.MachineRuleRow) rudolphrules.MovedRuleFields {
	var expires time.Time
	if row.ExpiresAfter != 0 {
		expires = time.Unix(row.ExpiresAfter, 0).UTC()
	}
	return rudolphrules.MovedRuleFields{
		Policy:                 enumText(row.Policy),
		ExpiresAt:              expires,
		CustomMessage:          row.CustomMessage,
		CustomURL:              row.CustomURL,
		CELExpr:                row.CELExpr,
		NotificationAppName:    row.NotificationAppName,
		EventDetailButtonLabel: row.EventDetailButtonLabel,
	}
}

// copyMachineRule writes one rule of the old Mac on the new one and records
// it. written says the rule is on the new Mac, even when a later step failed.
func (s *Store) copyMachineRule(ctx context.Context, c RudolphClient, who string, index eventcollection.Index, from, to string, row machinerules.MachineRuleRow) (written bool, err error) {
	rule := row.SantaRule
	var expires time.Time
	if row.ExpiresAfter != 0 {
		expires = time.Unix(row.ExpiresAfter, 0).UTC()
	}
	err = machinerules.AddNewMachineRule(c, to, rule.Identifier, rule.RuleType, rule.Policy, row.Description, expires,
		rules.WithCustomMessage(rule.CustomMessage),
		rules.WithCustomURL(rule.CustomURL),
		rules.WithNotificationAppName(rule.NotificationAppName),
		rules.WithCELExpr(rule.CELExpr),
		rules.WithEventDetailButtonLabel(rule.EventDetailButtonLabel),
	)
	if err != nil {
		return false, fmt.Errorf("dynamo: copying a machine rule: %w", err)
	}
	sortKey := rules.RuleSortKeyFromTypeIdentifier(rule.Identifier, rule.RuleType)
	switch {
	case row.CollectAllowedEvents:
		key := rudolphdynamodb.PrimaryKey{PartitionKey: machinerules.MachineRulesPK(to), SortKey: sortKey}
		if err := eventcollection.SetCollectAllowedEvents(c, to, key, true, s.writeClock.Now()); err != nil {
			// The rule is on the new Mac, so its add is recorded with the failure, as the CLI records a partial change.
			summary := rudolphrules.Summary(rule.RuleType, rule.Identifier, rule.Policy, rudolphrules.CopiedExtra(expires, from)+fmt.Sprintf(" (failed part-way: its allowed executions are not collected: %s)", shortError(err)))
			if rerr := s.record(ctx, c, who, store.ActionRuleAdd, store.MachineAuditTarget(to), summary); rerr != nil {
				return true, errors.Join(err, rerr)
			}
			return true, fmt.Errorf("dynamo: collecting the copied rule's allowed executions: %w", err)
		}
	case index.Machines[to][sortKey]:
		// The new row replaced an expired or removed one that was collected.
		s.forgetCollected(c, to, sortKey)
	}
	return true, s.record(ctx, c, who, store.ActionRuleAdd, store.MachineAuditTarget(to), rudolphrules.Summary(rule.RuleType, rule.Identifier, rule.Policy, rudolphrules.CopiedExtra(expires, from)))
}

// removeMovedRule removes one moved rule from the old Mac and records it.
// removed says the rule is off the old Mac, even when recording it failed.
func (s *Store) removeMovedRule(ctx context.Context, c RudolphClient, who string, index eventcollection.Index, from, to string, row machinerules.MachineRuleRow) (removed bool, err error) {
	err = machinerules.RemoveMachineRule(c, c, from, row.SortKey)
	if errors.Is(err, machinerules.ErrRuleNotFound) {
		// Removed since it was read: nothing left to do for it.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("dynamo: removing a moved rule from the old Mac: %w", err)
	}
	if index.Machines[from][row.SortKey] {
		s.forgetIfRemoved(c, from, row.SortKey)
	}
	return true, s.record(ctx, c, who, store.ActionRuleRemove, store.MachineAuditTarget(from), rudolphrules.Summary(row.RuleType, row.Identifier, row.Policy, rudolphrules.MovedExtra(to)))
}

// machineRuleRows reads every rule row of a Mac, strongly consistent so that
// a rule changed moments ago is moved as it is now, and at most
// maxMachineRuleRows of them.
func (s *Store) machineRuleRows(ctx context.Context, c RudolphClient, machineID string) ([]machinerules.MachineRuleRow, error) {
	var rows []machinerules.MachineRuleRow
	var start map[string]types.AttributeValue
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out, err := c.Query(&awsdynamodb.QueryInput{
			KeyConditionExpression:    aws.String("PK = :pk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":pk": &types.AttributeValueMemberS{Value: machinerules.MachineRulesPK(machineID)}},
			ConsistentRead:            aws.Bool(true),
			Limit:                     aws.Int32(int32(maxMachineRuleRows + 1 - len(rows))),
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return nil, fmt.Errorf("dynamo: reading the rules of machine %s: %w", machineID, err)
		}
		var page []machinerules.MachineRuleRow
		if err := attributevalue.UnmarshalListOfMaps(out.Items, &page); err != nil {
			return nil, fmt.Errorf("dynamo: reading the rules of machine %s: %w", machineID, err)
		}
		rows = append(rows, page...)
		if len(rows) > maxMachineRuleRows {
			return nil, store.Invalid("machine %s has more than %d rule rows, too many for the web view to move; use the CLI", machineID, maxMachineRuleRows)
		}
		if len(out.LastEvaluatedKey) == 0 {
			return rows, nil
		}
		start = out.LastEvaluatedKey
	}
}

// enumText is a Rudolph rule type or policy as the CLI names it.
func enumText(v interface{ MarshalText() ([]byte, error) }) string {
	text, err := v.MarshalText()
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(text)
}
