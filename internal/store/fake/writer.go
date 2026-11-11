package fake

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// The fake writes in memory, refusing what the DynamoDB writer refuses
// (rudolphrules, Rudolph's own validation) except checks against the clock:
// the fake has none, and tests run at a fixed time. Audit entries are stamped
// with the real time.

var _ store.Writer = (*Store)(nil)

// storedMachineID finds the spelling a machine ID is stored under, as the
// DynamoDB writer does: one with sensor data, else with configuration, else
// with machine rules; ErrNotFound when the fake has nothing for it. The
// caller holds s.mu.
func (s *Store) storedMachineID(id string) (string, error) {
	spellings := rudolphrules.Spellings(id)
	for _, sp := range spellings {
		if _, ok := s.machines[sp]; ok {
			return sp, nil
		}
	}
	for _, sp := range spellings {
		_, config := s.configs[sp]
		_, temp := s.tempModes[sp]
		_, clean := s.cleanSyncs[sp]
		if config || temp || clean {
			return sp, nil
		}
	}
	for _, sp := range spellings {
		if len(s.machineRules[sp]) > 0 {
			return sp, nil
		}
	}
	return "", rudolphrules.UnknownMachine(id)
}

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
	if add.Target.Kind == store.TargetMachine {
		if add.Target.MachineID, err = s.storedMachineID(add.Target.MachineID); err != nil {
			return err
		}
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
	if rm.Target.Kind == store.TargetMachine {
		if rm.Target.MachineID, err = s.storedMachineID(rm.Target.MachineID); err != nil {
			return err
		}
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
	if machineID, err = s.storedMachineID(machineID); err != nil {
		return err
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
	if machineID, err = s.storedMachineID(machineID); err != nil {
		return err
	}
	if _, ok := s.tempModes[machineID]; !ok {
		return nil
	}
	delete(s.tempModes, machineID)
	s.recordAudit(name, store.ActionEndTemporaryMode, store.MachineAuditTarget(machineID), rudolphrules.EndTemporaryModeSummary)
	return nil
}

// ListAudit returns up to limit entries recorded at or after since, newest
// first, for tests to check what a write recorded. It is not a store.Reader
// call, so like the fake's other inspection helpers it is not recorded.
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

// ListAuditPage implements store.AuditReader. An entry's key is its place in
// the trail counted from the oldest, so a cursor stays on the same entry as
// newer ones are recorded.
func (s *Store) ListAuditPage(_ context.Context, since time.Time, p store.PageRequest) (store.Page[store.AuditEntry], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record(Call{Method: "ListAuditPage", Page: p}); err != nil {
		return store.Page[store.AuditEntry]{}, err
	}
	var keyed []keyedItem[store.AuditEntry]
	for i := len(s.audit) - 1; i >= 0; i-- {
		if !s.audit[i].Time.Before(since) {
			keyed = append(keyed, keyedItem[store.AuditEntry]{key: fmt.Sprintf("%020d", math.MaxInt64-i), item: s.audit[i]})
		}
	}
	return paginate(s.cursors, keyed, fingerprint("ListAuditPage", "", nil), p)
}

// AddAuditEntry adds an entry to the audit trail as if another writer, such
// as Rudolph's CLI, had recorded it. It goes after every entry not newer than
// it, so the trail stays oldest first whatever order tests add entries in.
func (s *Store) AddAuditEntry(e store.AuditEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := len(s.audit)
	for i > 0 && s.audit[i-1].Time.After(e.Time) {
		i--
	}
	s.audit = slices.Insert(s.audit, i, e)
}

// recordAudit appends an entry; the caller holds s.mu. Entries are kept in
// the order they were recorded, which ListAudit reverses.
func (s *Store) recordAudit(actor, action, target, summary string) {
	s.audit = append(s.audit, store.AuditEntry{Time: time.Now().UTC(), Actor: actor, Action: action, Target: target, Summary: summary})
}

// CreateGroup implements store.Writer.
func (s *Store) CreateGroup(_ context.Context, actor store.Actor, name, description string, acceptReported bool) (string, error) {
	who, err := actor.AuditName()
	if err != nil {
		return "", err
	}
	if name, err = rudolphrules.GroupName(name); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	if _, ok := s.groups[name]; ok {
		return "", rudolphrules.GroupExists(name)
	}
	registered := len(s.groups)
	if _, ok := s.groups[store.UngroupedGroup]; ok {
		registered--
	}
	if registered >= rudolphrules.MaxGroups {
		return "", rudolphrules.TooManyGroups()
	}
	s.groups[name] = store.Group{Name: name, Description: description, AcceptReported: acceptReported}
	s.recordAudit(who, store.ActionGroupCreate, groupAuditTarget(name), rudolphrules.CreateGroupSummary(name, description, acceptReported))
	return name, nil
}

// UpdateGroup implements store.Writer.
func (s *Store) UpdateGroup(_ context.Context, actor store.Actor, name string, description *string, acceptReported *bool) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	if name, err = rudolphrules.ExistingGroupName(name, "changed"); err != nil {
		return err
	}
	if description == nil && acceptReported == nil {
		return store.ErrNothingToChange
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	g, ok := s.groups[name]
	if !ok {
		return store.ErrNotFound
	}
	description, acceptReported = rudolphrules.GroupChanges(g, description, acceptReported)
	if description == nil && acceptReported == nil {
		return store.ErrNothingToChange
	}
	if description != nil {
		g.Description = *description
	}
	if acceptReported != nil {
		g.AcceptReported = *acceptReported
	}
	s.groups[name] = g
	s.recordAudit(who, store.ActionGroupUpdate, groupAuditTarget(name), rudolphrules.UpdateGroupSummary(name, description, acceptReported))
	return nil
}

// DeleteGroup implements store.Writer. Like Rudolph, it leaves assigned
// membership on the Macs.
func (s *Store) DeleteGroup(_ context.Context, actor store.Actor, name string, force bool) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	if name, err = rudolphrules.ExistingGroupName(name, "deleted"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if _, ok := s.groups[name]; !ok {
		return store.ErrNotFound
	}
	if n := len(s.groupRules[name]); n > 0 && !force {
		return rudolphrules.GroupHasRules(name, n)
	}
	delete(s.groupRules, name)
	delete(s.groups, name)
	s.recordAudit(who, store.ActionGroupDelete, groupAuditTarget(name), rudolphrules.DeleteGroupSummary(name, force))
	return nil
}

// AssignGroup implements store.Writer.
func (s *Store) AssignGroup(_ context.Context, actor store.Actor, machineID, group string) error {
	return s.changeAssigned(actor, machineID, group, true)
}

// UnassignGroup implements store.Writer.
func (s *Store) UnassignGroup(_ context.Context, actor store.Actor, machineID, group string) error {
	return s.changeAssigned(actor, machineID, group, false)
}

func (s *Store) changeAssigned(actor store.Actor, machineID, group string, assign bool) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	if err := rudolphrules.MachineID(machineID); err != nil {
		return err
	}
	name, err := rudolphrules.GroupName(group)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if machineID, err = s.storedMachineID(machineID); err != nil {
		return err
	}
	if _, ok := s.groups[name]; assign && !ok {
		return rudolphrules.NotRegistered(name)
	}
	g := s.machineGrps[machineID]
	g.assigned = slices.DeleteFunc(slices.Clone(g.assigned), func(a string) bool { return a == name })
	action, summary := store.ActionGroupRemove, rudolphrules.UnassignGroupSummary(name)
	if assign {
		g.assigned = append(g.assigned, name)
		slices.Sort(g.assigned)
		action, summary = store.ActionGroupAdd, rudolphrules.AssignGroupSummary(name)
	}
	s.machineGrps[machineID] = g
	s.recordAudit(who, action, store.MachineAuditTarget(machineID), summary)
	return nil
}

func groupAuditTarget(name string) string {
	return store.RuleTarget{Kind: store.TargetGroup, Group: name}.AuditTarget()
}

// SetRuleCollection implements store.Writer.
func (s *Store) SetRuleCollection(_ context.Context, actor store.Actor, target store.RuleTarget, ruleType store.RuleType, identifier string, collect bool) error {
	ch, err := rudolphrules.CheckCollection(actor, target, ruleType, identifier)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	rules := s.globalRules
	if ch.Target.Kind == store.TargetMachine {
		if ch.Target.MachineID, err = s.storedMachineID(ch.Target.MachineID); err != nil {
			return err
		}
		rules = s.machineRules[ch.Target.MachineID]
	}
	key := ruleKey(store.Rule{RuleType: ruleType, Identifier: identifier})
	r, ok := rules[key]
	switch {
	case !ok || r.State == store.RuleStateExpired:
		return store.ErrNotFound
	case r.State == store.RuleStateRemoving:
		return store.Invalid("the rule is being removed from the Mac; add it again instead")
	case r.CollectAllowedEvents == collect:
		return store.ErrNothingToChange
	}
	r.CollectAllowedEvents = collect
	rules[key] = r
	s.recordAudit(ch.Actor, store.ActionRuleUpdate, ch.Target.AuditTarget(), rudolphrules.CollectionSummaryOf(r.RuleType, r.Identifier, r.Policy, collect))
	return nil
}

// MoveMachineRules implements store.Writer. Whether a rule is in force is its
// State, since the fake has no clock.
func (s *Store) MoveMachineRules(_ context.Context, actor store.Actor, from, to string, remove bool) (int, error) {
	who, err := rudolphrules.CheckMove(actor, from, to)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	if from, err = s.storedMachineID(from); err != nil {
		return 0, err
	}
	if to, err = s.storedMachineID(to); err != nil {
		return 0, err
	}
	var moving []store.Rule
	for _, r := range s.machineRules[from] {
		if r.State == store.RuleStateInForce {
			moving = append(moving, r)
		}
	}
	slices.SortFunc(moving, func(a, b store.Rule) int { return strings.Compare(ruleKey(a), ruleKey(b)) })
	switch {
	case len(moving) == 0:
		return 0, rudolphrules.NothingToMove(from)
	case len(moving) > store.MaxMovedRules:
		return 0, rudolphrules.TooManyToMove(len(moving))
	}
	var copies []store.Rule
	for _, r := range moving {
		if h, ok := s.machineRules[to][ruleKey(r)]; ok && h.State == store.RuleStateInForce {
			if diff := rudolphrules.MoveDifference(movedFields(h), movedFields(r)); diff != "" {
				return 0, rudolphrules.MoveDiffers(string(r.RuleType), r.Identifier, diff)
			}
			continue
		}
		copies = append(copies, r)
	}
	if s.machineRules[to] == nil {
		s.machineRules[to] = map[string]store.Rule{}
	}
	for _, r := range copies {
		s.machineRules[to][ruleKey(r)] = r
		s.recordAudit(who, store.ActionRuleAdd, store.MachineAuditTarget(to), rudolphrules.SummaryOf(r.RuleType, r.Identifier, r.Policy, rudolphrules.CopiedExtra(r.ExpiresAt, from)))
	}
	if remove {
		for _, r := range moving {
			// As RemoveRule: the row stays, marked, with the global rule's policy when there is one.
			marked := store.Rule{RuleType: r.RuleType, Identifier: r.Identifier, Policy: store.PolicyRemove, State: store.RuleStateRemoving}
			if g, ok := s.globalRules[ruleKey(r)]; ok {
				marked.Policy, marked.CustomMessage, marked.CustomURL, marked.CELExpr = g.Policy, g.CustomMessage, g.CustomURL, g.CELExpr
			}
			s.machineRules[from][ruleKey(r)] = marked
			s.recordAudit(who, store.ActionRuleRemove, store.MachineAuditTarget(from), rudolphrules.SummaryOf(r.RuleType, r.Identifier, r.Policy, rudolphrules.MovedExtra(to)))
		}
	}
	return len(copies), nil
}

// movedFields is what a move compares of a rule. The fake's rules have no
// notification app name or event detail button label, so those always match.
func movedFields(r store.Rule) rudolphrules.MovedRuleFields {
	return rudolphrules.MovedRuleFields{
		Policy:        string(r.Policy),
		ExpiresAt:     r.ExpiresAt,
		CustomMessage: r.CustomMessage,
		CustomURL:     r.CustomURL,
		CELExpr:       r.CELExpr,
	}
}
