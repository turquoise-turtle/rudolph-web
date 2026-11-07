package fake

import (
	"context"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// The fake writes in memory, refusing what the DynamoDB writer refuses
// (rudolphrules, Rudolph's own validation) except checks against the clock:
// the fake has none, and tests run at a fixed time. Audit entries are stamped
// with the real time.

var _ store.Writer = (*Store)(nil)

// AddRule implements store.Writer.
func (s *Store) AddRule(_ context.Context, actor store.Actor, target store.RuleTarget, r store.NewRule) error {
	add, err := rudolphrules.CheckAdd(actor, target, r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	rule := store.Rule{
		RuleType: r.RuleType, Policy: r.Policy, Identifier: r.Identifier, CustomMessage: r.CustomMessage,
		Description: r.Description, ExpiresAt: r.ExpiresAt, State: store.RuleStateInForce,
	}
	switch add.Target.Kind {
	case store.TargetGlobal:
		s.globalRules[ruleKey(rule)] = rule
	case store.TargetMachine:
		if s.machineRules[add.Target.MachineID] == nil {
			s.machineRules[add.Target.MachineID] = map[string]store.Rule{}
		}
		s.machineRules[add.Target.MachineID][ruleKey(rule)] = rule
	case store.TargetGroup:
		if _, ok := s.groups[add.Target.Group]; !ok && add.Target.Group != store.UngroupedGroup {
			return store.Invalid("there is no group %q; create it first with rudolph group create", add.Target.Group)
		}
		if s.groupRules[add.Target.Group] == nil {
			s.groupRules[add.Target.Group] = map[string]store.Rule{}
		}
		s.groupRules[add.Target.Group][ruleKey(rule)] = rule
	}
	extra := ""
	if add.Target.Kind == store.TargetMachine {
		extra = "expires " + rudolphrules.ExpiryText(r.ExpiresAt)
	}
	s.recordAudit(add.Actor, store.ActionRuleAdd, add.Target.AuditTarget(), rudolphrules.SummaryOf(r.RuleType, r.Identifier, r.Policy, extra))
	return nil
}

// RemoveRule implements store.Writer. Like Rudolph, a removed machine rule
// is kept, marked for removal, with the global rule's policy when there is one.
func (s *Store) RemoveRule(_ context.Context, actor store.Actor, target store.RuleTarget, ruleType store.RuleType, identifier string) error {
	rm, err := rudolphrules.CheckRemove(actor, target, ruleType, identifier)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	key := ruleKey(store.Rule{RuleType: ruleType, Identifier: identifier})
	var rules map[string]store.Rule
	switch rm.Target.Kind {
	case store.TargetGlobal:
		rules = s.globalRules
	case store.TargetMachine:
		rules = s.machineRules[rm.Target.MachineID]
	case store.TargetGroup:
		rules = s.groupRules[rm.Target.Group]
	}
	old, ok := rules[key]
	if !ok {
		return store.ErrNotFound
	}
	if rm.Target.Kind == store.TargetMachine {
		marked := store.Rule{RuleType: old.RuleType, Identifier: old.Identifier, Policy: store.PolicyRemove, State: store.RuleStateRemoving}
		if g, ok := s.globalRules[key]; ok {
			marked.Policy, marked.CustomMessage, marked.CustomURL, marked.CELExpr = g.Policy, g.CustomMessage, g.CustomURL, g.CELExpr
		}
		rules[key] = marked
	} else {
		delete(rules, key)
	}
	s.recordAudit(rm.Actor, store.ActionRuleRemove, rm.Target.AuditTarget(), rudolphrules.SummaryOf(old.RuleType, old.Identifier, old.Policy, ""))
	return nil
}

// SetTemporaryMode implements store.Writer.
func (s *Store) SetTemporaryMode(_ context.Context, actor store.Actor, machineID string, mode store.ClientMode, until time.Time) error {
	tm, err := rudolphrules.CheckTemporaryMode(actor, machineID, mode, until, time.Time{})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	// Rudolph stores the end in whole seconds.
	until = time.Unix(until.Unix(), 0).UTC()
	s.tempModes[machineID] = tempMode{mode: mode, until: until}
	s.recordAudit(tm.Actor, store.ActionTemporaryMode, store.MachineAuditTarget(machineID), rudolphrules.TemporaryModeSummary(mode, until))
	return nil
}

// EndTemporaryMode implements store.Writer.
func (s *Store) EndTemporaryMode(_ context.Context, actor store.Actor, machineID string) error {
	name, err := actor.AuditName()
	if err != nil {
		return err
	}
	if err := rudolphrules.MachineID(machineID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if _, ok := s.tempModes[machineID]; !ok {
		return nil
	}
	delete(s.tempModes, machineID)
	s.recordAudit(name, store.ActionEndTemporaryMode, store.MachineAuditTarget(machineID), rudolphrules.EndTemporaryModeSummary)
	return nil
}

// ListAudit implements store.Writer.
func (s *Store) ListAudit(_ context.Context, since time.Time, limit int) ([]store.AuditEntry, error) {
	if limit < 1 || limit > store.MaxAuditEntries {
		return nil, store.Invalid("limit %d outside 1..%d", limit, store.MaxAuditEntries)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	var out []store.AuditEntry
	for i := len(s.audit) - 1; i >= 0 && len(out) < limit; i-- {
		if !s.audit[i].Time.Before(since) {
			out = append(out, s.audit[i])
		}
	}
	return out, nil
}

// recordAudit appends an entry; the caller holds s.mu. Entries are kept in
// the order they were recorded, which ListAudit reverses.
func (s *Store) recordAudit(actor, action, target, summary string) {
	s.audit = append(s.audit, store.AuditEntry{Time: time.Now().UTC(), Actor: actor, Action: action, Target: target, Summary: summary})
}
