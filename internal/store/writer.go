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
	ActionConfigUpdate        = "config.update"
	ActionGroupCreate         = "group.create"
	ActionGroupUpdate         = "group.update"
	ActionGroupDelete         = "group.delete"
	ActionGroupAdd            = "group.add"
	ActionGroupRemove         = "group.remove"
	auditActorPrefix          = "web:"
	maxActorUsernameBytes     = 256
	maxActorSubjectBytes      = 128
	auditTargetGlobal         = "global"
	auditTargetMachinePrefix  = "machine:"
	auditTargetGroupPrefix    = "group:"
	auditTargetUngroupedGroup = "ungrouped"
)

// ErrNothingToChange is the refusal of a change that asks for nothing:
// UpdateGroup's when the group already has the description and reported
// membership asked for, and UpdateMachineConfig's and UpdateGlobalConfig's for
// an empty ConfigChange. It wraps ErrInvalidArgument, so a caller that does
// not tell it apart refuses it like any other invalid change.
var ErrNothingToChange = Invalid("nothing to change")

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

// Writer changes rules, temporary client modes and device groups the way Rudolph's CLI does,
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
	// CreateGroup registers a group, as rudolph group create does, and
	// returns its name normalised (lower case, trimmed). An empty or reserved
	// name, one over 128 bytes, an existing group and more than 64 groups are
	// refused with ErrInvalidArgument.
	CreateGroup(ctx context.Context, actor Actor, name, description string, acceptReported bool) (string, error)
	// UpdateGroup changes a group's description and whether Macs that report
	// it are members, as rudolph group update does. A nil field is left as it
	// is, and only the fields that differ from the group's are written and
	// recorded; when none do it is ErrNothingToChange. ErrNotFound when the
	// group is not registered.
	UpdateGroup(ctx context.Context, actor Actor, name string, description *string, acceptReported *bool) error
	// DeleteGroup deletes a group, as rudolph group delete does. A group with
	// rules is refused unless force, which deletes its rules too. ErrNotFound
	// when the group is not registered.
	DeleteGroup(ctx context.Context, actor Actor, name string, force bool) error
	// AssignGroup assigns a registered group to a Mac, as rudolph group add
	// does.
	AssignGroup(ctx context.Context, actor Actor, machineID, group string) error
	// UnassignGroup removes a group assigned to a Mac, as rudolph group
	// remove does. Groups the Mac reports are not affected.
	UnassignGroup(ctx context.Context, actor Actor, machineID, group string) error
	// UpdateMachineConfig changes a Mac's own configuration, as rudolph
	// config update --machine does: the given settings are written, even when
	// they equal the stored or global value, and the inherited ones removed,
	// so the Mac follows the global config for them again. A running
	// temporary mode is left alone. An empty change is ErrNothingToChange;
	// ErrNotFound when Rudolph has nothing for the Mac.
	UpdateMachineConfig(ctx context.Context, actor Actor, machineID string, change ConfigChange) error
	// UpdateGlobalConfig changes the global configuration, as rudolph config
	// update --global does. Nothing can be inherited, and clean syncs are per
	// Mac. A client mode other than MONITOR moves every Mac without its own:
	// the caller must have had it confirmed, as the store confirms it to
	// Rudolph. An empty change is ErrNothingToChange.
	UpdateGlobalConfig(ctx context.Context, actor Actor, change ConfigChange) error
}

// ConfigChange is a change to a global or machine configuration. A nil
// setting is left as it is. Settings are named as in Config; Inherit lists
// settings by Rudolph's attribute names (as InheritableSettings), and only a
// Mac's configuration inherits. USB settings are left out, as in Config.
type ConfigChange struct {
	ClientMode                *ClientMode
	AllowedPathRegex          *string
	BlockedPathRegex          *string
	BatchSize                 *int
	EnableBundles             *bool
	EnableTransitiveRules     *bool
	FullSyncInterval          *int
	UploadLogsURL             *string
	EnableAllEventUpload      *bool
	DisableUnknownEventUpload *bool
	OverrideFileAccessAction  *string
	EventDetailURL            *string
	EventDetailText           *string
	// CleanSync true requests one clean sync of a Mac; false cancels a
	// request.
	CleanSync *bool
	Inherit   []string
}

// IsEmpty reports whether the change asks for nothing.
func (c ConfigChange) IsEmpty() bool {
	return c.ClientMode == nil && c.AllowedPathRegex == nil && c.BlockedPathRegex == nil && c.BatchSize == nil &&
		c.EnableBundles == nil && c.EnableTransitiveRules == nil && c.FullSyncInterval == nil && c.UploadLogsURL == nil &&
		c.EnableAllEventUpload == nil && c.DisableUnknownEventUpload == nil && c.OverrideFileAccessAction == nil &&
		c.EventDetailURL == nil && c.EventDetailText == nil && c.CleanSync == nil && len(c.Inherit) == 0
}

// InheritableSettings are the attribute names of the settings a Mac's
// configuration can inherit from here: Rudolph's, without the USB settings.
var InheritableSettings = []string{
	"ClientMode", "AllowedPathRegex", "BlockedPathRegex", "BatchSize", "EnableBundles", "EnableTransitiveRules",
	"FullSyncInterval", "UploadLogsUrl", "EnableAllEventUpload", "DisableUnknownEventUpload",
	"OverrideFileAccessAction", "EventDetailURL", "EventDetailText",
}

// FileAccessActions lists the override file access actions, as Santa names
// them.
var FileAccessActions = []string{"NONE", "AUDIT_ONLY", "DISABLE"}
