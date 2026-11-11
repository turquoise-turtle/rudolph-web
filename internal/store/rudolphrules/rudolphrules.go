// Package rudolphrules checks web edits with Rudolph's own validation
// (github.com/airbnb/rudolph pkg/types, pkg/model/rules and pkg/model/groups),
// so the DynamoDB writer and the fake refuse exactly what Rudolph's CLI
// refuses, and builds the audit summaries the CLI writes.
package rudolphrules

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/airbnb/rudolph/pkg/model/groups"
	"github.com/airbnb/rudolph/pkg/model/machineconfiguration"
	"github.com/airbnb/rudolph/pkg/model/rules"
	"github.com/airbnb/rudolph/pkg/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// RuleType converts a rule type to Rudolph's.
func RuleType(t store.RuleType) (types.RuleType, error) {
	var rt types.RuleType
	if err := rt.UnmarshalText([]byte(t)); err != nil {
		return 0, store.Invalid("unknown rule type %q", t)
	}
	return rt, nil
}

// ClientMode converts a client mode to Rudolph's.
func ClientMode(m store.ClientMode) (types.ClientMode, error) {
	var cm types.ClientMode
	if err := cm.UnmarshalText([]byte(m)); err != nil {
		return 0, store.Invalid("unknown client mode %q", m)
	}
	return cm, nil
}

// MachineID refuses a machine ID that is not a UUID in its canonical form, as
// Rudolph's API and CLI do.
func MachineID(id string) error {
	if err := types.ValidateMachineID(id); err != nil {
		return store.Invalid("machine %q is not a machine UUID (8-4-4-4-12 hexadecimal digits)", id)
	}
	return nil
}

// Target checks a rule target and returns it with the group name normalised.
func Target(t store.RuleTarget) (store.RuleTarget, error) {
	switch t.Kind {
	case store.TargetGlobal:
		return store.RuleTarget{Kind: store.TargetGlobal}, nil
	case store.TargetMachine:
		if err := MachineID(t.MachineID); err != nil {
			return store.RuleTarget{}, err
		}
		return store.RuleTarget{Kind: store.TargetMachine, MachineID: t.MachineID}, nil
	case store.TargetGroup:
		name, err := groups.NormaliseTarget(t.Group)
		if err != nil {
			return store.RuleTarget{}, store.Invalid("group: %v", err)
		}
		return store.RuleTarget{Kind: store.TargetGroup, Group: name}, nil
	}
	return store.RuleTarget{}, store.Invalid("unknown rule target %q", t.Kind)
}

// Add is a checked rule to add.
type Add struct {
	Actor   string
	Target  store.RuleTarget
	Rule    rules.SantaRule
	SortKey string
	// Description is stored with the rule; Santa never sees it.
	Description string
	ExpiresAt   time.Time
}

// CheckAdd checks a rule to add: the actor, the target, and the rule as
// Rudolph validates it when it writes one.
func CheckAdd(actor store.Actor, target store.RuleTarget, r store.NewRule) (Add, error) {
	name, err := actor.AuditName()
	if err != nil {
		return Add{}, err
	}
	target, err = Target(target)
	if err != nil {
		return Add{}, err
	}
	ruleType, err := RuleType(r.RuleType)
	if err != nil {
		return Add{}, err
	}
	if !slices.Contains(store.AddablePolicies, r.Policy) {
		return Add{}, store.Invalid("a rule cannot be added with the policy %q", r.Policy)
	}
	var policy types.Policy
	if err := policy.UnmarshalText([]byte(r.Policy)); err != nil {
		return Add{}, store.Invalid("unknown policy %q", r.Policy)
	}
	rule := rules.SantaRule{RuleType: ruleType, Policy: policy, Identifier: r.Identifier, CustomMessage: r.CustomMessage}
	if !rules.ValidIdentifier(ruleType, r.Identifier) {
		return Add{}, store.Invalid("identifier %q is not a valid %s identifier (%s)", r.Identifier, r.RuleType, identifierShape(ruleType))
	}
	if err := rules.ValidatePolicy(rule); err != nil {
		return Add{}, store.Invalid("%v", err)
	}
	if err := rules.ValidateFieldSizes(rule); err != nil {
		return Add{}, store.Invalid("%v", err)
	}
	if target.Kind == store.TargetGroup {
		if err := groups.ValidateRule(rule); err != nil {
			return Add{}, store.Invalid("%v", err)
		}
	}
	if !r.ExpiresAt.IsZero() && target.Kind != store.TargetMachine {
		return Add{}, store.Invalid("only machine rules expire; global and group rules never do")
	}
	if !r.ExpiresAt.IsZero() && r.ExpiresAt.Unix() <= 0 {
		return Add{}, store.Invalid("the expiry is not a valid time")
	}
	return Add{
		Actor:       name,
		Target:      target,
		Rule:        rule,
		SortKey:     rules.RuleSortKeyFromTypeIdentifier(r.Identifier, ruleType),
		Description: r.Description,
		ExpiresAt:   r.ExpiresAt,
	}, nil
}

func identifierShape(t types.RuleType) string {
	switch t {
	case types.RuleTypeBinary, types.RuleTypeCertificate:
		return "64 lower-case hexadecimal digits"
	case types.RuleTypeTeamID:
		return "up to 10 upper-case letters and digits"
	case types.RuleTypeSigningID:
		return "a Team ID or platform, a colon, then the signing ID"
	case types.RuleTypeCDHash:
		return "40 lower-case hexadecimal digits"
	}
	return "unknown rule type"
}

// Summary describes the added rule for the audit trail, as the CLI does.
func (a Add) Summary() string {
	if a.Target.Kind == store.TargetMachine {
		return Summary(a.Rule.RuleType, a.Rule.Identifier, a.Rule.Policy, "expires "+ExpiryText(a.ExpiresAt))
	}
	return Summary(a.Rule.RuleType, a.Rule.Identifier, a.Rule.Policy, "")
}

// Remove is a checked rule to remove.
type Remove struct {
	Actor    string
	Target   store.RuleTarget
	RuleType types.RuleType
	// Identifier as given: removal also accepts the identifiers older Rudolph
	// versions stored, such as an upper-case SHA-256.
	Identifier string
	SortKey    string
}

// CheckRemove checks a rule to remove.
func CheckRemove(actor store.Actor, target store.RuleTarget, ruleType store.RuleType, identifier string) (Remove, error) {
	name, err := actor.AuditName()
	if err != nil {
		return Remove{}, err
	}
	target, err = Target(target)
	if err != nil {
		return Remove{}, err
	}
	rt, err := RuleType(ruleType)
	if err != nil {
		return Remove{}, err
	}
	if !rules.ValidRemovalIdentifier(rt, identifier) {
		return Remove{}, store.Invalid("identifier %q is not a valid %s identifier", identifier, ruleType)
	}
	return Remove{Actor: name, Target: target, RuleType: rt, Identifier: identifier, SortKey: rules.RuleSortKeyFromTypeIdentifier(identifier, rt)}, nil
}

// TemporaryMode is a checked temporary client mode to set.
type TemporaryMode struct {
	Actor string
	Mode  types.ClientMode
}

// CheckTemporaryMode checks a temporary mode for a Mac. With a non-zero now
// it also checks, as Rudolph does, that until is between a minute and 30
// days after now.
func CheckTemporaryMode(actor store.Actor, machineID string, mode store.ClientMode, until, now time.Time) (TemporaryMode, error) {
	name, err := actor.AuditName()
	if err != nil {
		return TemporaryMode{}, err
	}
	if err := MachineID(machineID); err != nil {
		return TemporaryMode{}, err
	}
	cm, err := ClientMode(mode)
	if err != nil {
		return TemporaryMode{}, err
	}
	if until.IsZero() {
		return TemporaryMode{}, store.Invalid("a temporary client mode needs an end")
	}
	if !now.IsZero() {
		if err := machineconfiguration.ValidateTemporaryClientMode(cm, until, now); err != nil {
			return TemporaryMode{}, store.Invalid("%v", err)
		}
	}
	return TemporaryMode{Actor: name, Mode: cm}, nil
}

// TemporaryModeSummary describes a temporary mode for the audit trail, as the
// CLI does.
func TemporaryModeSummary(mode store.ClientMode, until time.Time) string {
	return fmt.Sprintf("temporary client mode %s until %s", mode, until.UTC().Format(time.RFC3339))
}

// EndTemporaryModeSummary is the CLI's audit summary of ending a temporary mode.
const EndTemporaryModeSummary = "ended the temporary client mode"

// Summary describes a rule for the audit trail as the CLI's ruleSummary does,
// e.g. "BINARY rule ab12... BLOCKLIST"; extra is appended after a comma.
func Summary(ruleType types.RuleType, identifier string, policy types.Policy, extra string) string {
	policyName, err := policy.MarshalText()
	if err != nil {
		policyName = []byte(fmt.Sprint(policy))
	}
	typeName, err := ruleType.MarshalText()
	if err != nil {
		typeName = []byte(fmt.Sprint(ruleType))
	}
	summary := fmt.Sprintf("%s rule %s %s", typeName, identifier, policyName)
	if extra != "" {
		summary += ", " + extra
	}
	return summary
}

// ExpiryText is how the CLI writes a machine rule's expiry; the zero time is
// never.
func ExpiryText(expires time.Time) string {
	if expires.IsZero() {
		return "never"
	}
	return expires.UTC().Format(time.RFC3339)
}

// SummaryOf is Summary for a rule in the store's terms, whose names are
// Santa's, as Rudolph's are.
func SummaryOf(ruleType store.RuleType, identifier string, policy store.Policy, extra string) string {
	summary := fmt.Sprintf("%s rule %s %s", ruleType, identifier, policy)
	if extra != "" {
		summary += ", " + extra
	}
	return summary
}

// Spellings are the spellings of a machine ID to look for, in the order the
// CLI's ResolveMachineID tries them: as given, upper case, lower case. Rudolph
// keeps a machine ID exactly as the Mac sends it and matches it exactly, so a
// rule or configuration under another spelling never reaches the Mac.
func Spellings(id string) []string {
	out := []string{id}
	for _, s := range []string{strings.ToUpper(id), strings.ToLower(id)} {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// UnknownMachine is the error for a machine ID Rudolph has nothing stored
// under in any spelling: no sensor data, configuration or machine rules.
func UnknownMachine(id string) error {
	return fmt.Errorf("%w: Rudolph has nothing for machine %s", store.ErrNotFound, id)
}

// CheckCollection checks a change to whether a rule's allowed executions are
// collected. Rudolph's index of collected rules has global and machine scopes
// only, so group rules are refused.
func CheckCollection(actor store.Actor, target store.RuleTarget, ruleType store.RuleType, identifier string) (Remove, error) {
	if target.Kind == store.TargetGroup {
		return Remove{}, store.Invalid("Rudolph collects the allowed executions of global and machine rules only, not group rules")
	}
	return CheckRemove(actor, target, ruleType, identifier)
}

// CollectionSummary describes a change to a rule's collection for the audit
// trail, as the CLI's rule update does.
func CollectionSummary(ruleType types.RuleType, identifier string, policy types.Policy, collect bool) string {
	return Summary(ruleType, identifier, policy, fmt.Sprintf("collect allowed events %t", collect))
}

// CollectionSummaryOf is CollectionSummary for a rule in the store's terms.
func CollectionSummaryOf(ruleType store.RuleType, identifier string, policy store.Policy, collect bool) string {
	return SummaryOf(ruleType, identifier, policy, fmt.Sprintf("collect allowed events %t", collect))
}

// CheckMove checks a move of machine rules from one Mac to another and
// returns the actor's audit name. Machine IDs that differ only in case are
// the same Mac to the person moving rules, whatever spellings Rudolph holds.
func CheckMove(actor store.Actor, from, to string) (string, error) {
	name, err := actor.AuditName()
	if err != nil {
		return "", err
	}
	if err := MachineID(from); err != nil {
		return "", err
	}
	if err := MachineID(to); err != nil {
		return "", err
	}
	if strings.EqualFold(from, to) {
		return "", store.Invalid("the rules are already on machine %s; choose another Mac", from)
	}
	return name, nil
}

// TooManyToMove refuses a move of more than store.MaxMovedRules rules.
func TooManyToMove(n int) error {
	return store.Invalid("this Mac has %d rules in force, more than the %d the web view moves at once; use the CLI", n, store.MaxMovedRules)
}

// NothingToMove refuses a move from a Mac with no rules in force.
func NothingToMove(machineID string) error {
	return store.Invalid("machine %s has no rules in force to move", machineID)
}

// MoveConflict refuses a move that would replace a rule the new Mac has with
// another policy: which one should win is the person's call.
func MoveConflict(ruleType, identifier, has, moving string) error {
	return store.Invalid("the new Mac already has the %s rule %s with the policy %s, not %s; change or remove one of them first", ruleType, identifier, has, moving)
}

// CheckCopy checks a stored machine rule as Rudolph checks one it writes, so
// that a move is refused before anything is written rather than part-way.
func CheckCopy(rule rules.SantaRule) error {
	typeName, err := rule.RuleType.MarshalText()
	if err != nil {
		return store.Invalid("a rule of unknown type %d cannot be moved", rule.RuleType)
	}
	if !rules.ValidIdentifier(rule.RuleType, rule.Identifier) {
		return store.Invalid("the %s rule %q cannot be moved: its identifier is not one Rudolph accepts now (%s); add it again on the new Mac", typeName, rule.Identifier, identifierShape(rule.RuleType))
	}
	if err := rules.ValidatePolicy(rule); err != nil {
		return store.Invalid("the %s rule %s cannot be moved: %v", typeName, rule.Identifier, err)
	}
	if err := rules.ValidateFieldSizes(rule); err != nil {
		return store.Invalid("the %s rule %s cannot be moved: %v", typeName, rule.Identifier, err)
	}
	return nil
}

// CopiedExtra is the end of the audit summary of a rule copied to the new
// Mac: the CLI's rule add, saying where it came from.
func CopiedExtra(expires time.Time, from string) string {
	return "expires " + ExpiryText(expires) + ", copied from machine " + from
}

// MovedExtra is the end of the audit summary of a rule removed from the old
// Mac: the CLI's rule remove, saying where it went.
func MovedExtra(to string) string {
	return "moved to machine " + to
}
