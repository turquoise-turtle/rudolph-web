package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNotAudited is returned when a write succeeded but its audit entry could
// not be recorded. The change is not rolled back, as with Rudolph's CLI.
var ErrNotAudited = errors.New("store: the change was made but not recorded in the audit trail")

// Invalid returns an error wrapping ErrInvalidArgument whose message, after
// the ErrInvalidArgument prefix, is fit to show the person who sent the value.
func Invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidArgument, fmt.Sprintf(format, args...))
}

// Audit actions, exactly as Rudolph's CLI records them.
const (
	ActionRuleAdd             = "rule.add"
	ActionRuleRemove          = "rule.remove"
	ActionTemporaryMode       = "config.temporary-mode"
	ActionEndTemporaryMode    = "config.end-temporary-mode"
	auditActorPrefix          = "web:"
	maxActorUsernameBytes     = 256
	maxActorSubjectBytes      = 128
	auditTargetGlobal         = "global"
	auditTargetMachinePrefix  = "machine:"
	auditTargetGroupPrefix    = "group:"
	auditTargetUngroupedGroup = "ungrouped"
)

// Actor is the signed-in person a write is made for, from the Entra session.
type Actor struct {
	Username string // preferred_username
	Name     string
	Subject  string // Entra object ID
}

// AuditName is how the audit trail names the actor: "web:<username>
// (<object ID>)". The username is what people recognise; the object ID stays
// the same when the username changes.
func (a Actor) AuditName() (string, error) {
	switch {
	case a.Username == "":
		return "", Invalid("a change needs a signed-in user with a username")
	case len(a.Username) > maxActorUsernameBytes || len(a.Subject) > maxActorSubjectBytes || hasControl(a.Username) || hasControl(a.Subject):
		return "", Invalid("the signed-in user's username or object ID is not usable")
	case a.Subject == "":
		return auditActorPrefix + a.Username, nil
	}
	return fmt.Sprintf("%s%s (%s)", auditActorPrefix, a.Username, a.Subject), nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// TargetKind is what a rule applies to.
type TargetKind string

const (
	TargetGlobal  TargetKind = "global"
	TargetMachine TargetKind = "machine"
	TargetGroup   TargetKind = "group"
)

// RuleTarget is where a rule is added or removed: every Mac, one Mac (by its
// stored machine ID) or a device group (UngroupedGroup for the Macs in none).
type RuleTarget struct {
	Kind      TargetKind
	MachineID string
	Group     string
}

// AuditTarget is the target as Rudolph's CLI records it: "global",
// "machine:<machine ID>", "group:<name>", or "ungrouped". The group name must
// already be normalised.
func (t RuleTarget) AuditTarget() string {
	switch t.Kind {
	case TargetMachine:
		return auditTargetMachinePrefix + t.MachineID
	case TargetGroup:
		if t.Group == UngroupedGroup {
			return auditTargetUngroupedGroup
		}
		return auditTargetGroupPrefix + t.Group
	}
	return auditTargetGlobal
}

// MachineAuditTarget is the audit target of a machine's configuration.
func MachineAuditTarget(machineID string) string {
	return auditTargetMachinePrefix + machineID
}

// NewRule is a rule to add. ExpiresAt is for machine rules only; zero means
// the rule never expires.
type NewRule struct {
	RuleType      RuleType
	Identifier    string
	Policy        Policy
	CustomMessage string
	Description   string
	ExpiresAt     time.Time
}

// AddablePolicies are the policies a rule can be added with from the web
// view. REMOVE is what removing a rule sends, ALLOWLIST_TRANSITIVE is never
// sent to Santa, and CEL needs an expression the web view does not take.
var AddablePolicies = []Policy{
	PolicyAllowlist, PolicyAllowlistCompiler, PolicyBlocklist, PolicySilentBlocklist,
	PolicySilentGUIBlocklist, PolicySilentTTYBlocklist,
}

// ClientModes lists every valid ClientMode.
var ClientModes = []ClientMode{ClientModeMonitor, ClientModeLockdown, ClientModeStandalone}

// AuditEntry is one change recorded in Rudolph's audit trail, by the CLI or
// the web view.
type AuditEntry struct {
	Time    time.Time
	Actor   string
	Action  string
	Target  string
	Summary string
}

// MaxAuditEntries is the most entries one ListAudit returns.
const MaxAuditEntries = 1000

// Writer changes rules and temporary client modes the way Rudolph's CLI does,
// and records every change in Rudolph's audit trail with the actor. Invalid
// input is refused with ErrInvalidArgument before anything is written, and
// nothing is audited unless something was written.
type Writer interface {
	// AddRule creates or replaces a rule.
	AddRule(ctx context.Context, actor Actor, target RuleTarget, rule NewRule) error
	// RemoveRule removes a rule: a global or group rule is deleted, a machine
	// rule is marked so that the Mac drops it (or gets the global rule back)
	// at its next sync. ErrNotFound when there is no such rule.
	RemoveRule(ctx context.Context, actor Actor, target RuleTarget, ruleType RuleType, identifier string) error
	// SetTemporaryMode sends mode to the Mac instead of its usual mode until
	// until, which must be between a minute and 30 days ahead.
	SetTemporaryMode(ctx context.Context, actor Actor, machineID string, mode ClientMode, until time.Time) error
	// EndTemporaryMode ends a Mac's temporary mode now. Nothing is written or
	// recorded when it has none.
	EndTemporaryMode(ctx context.Context, actor Actor, machineID string) error
	// ListAudit returns at most limit (1..MaxAuditEntries) audit entries
	// recorded at or after since, newest first.
	ListAudit(ctx context.Context, since time.Time, limit int) ([]AuditEntry, error)
}
