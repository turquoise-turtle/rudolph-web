// Package store defines the read models and data-access interfaces used by
// the web layer. Implementations must read through bounded, paginated queries
// (DynamoDB Query with Limit + ExclusiveStartKey), never scans.
//
// The shapes follow Rudolph's single-table DynamoDB layout:
//
//	PK=Machine#<id>       SK=Current      sensor data      -> Machine
//	PK=Machine#<id>       SK=SyncState    sync state       -> SyncState
//	PK=Machine#<id>       SK=Config       machine config   -> MachineDetail.Config, ConfiguredClientMode
//	PK=GlobalConfig       SK=Config       global config    -> Config
//	PK=GlobalRules        SK=<Type>#<id>  global rules     -> Rule
//	PK=MachineRules#<id>  SK=<Type>#<id>  machine rules    -> Rule
//	PK=Machine#<id>       SK=Groups       a Mac's groups   -> MachineDetail.AssignedGroups, ReportedGroups
//	PK=Machine#<id>       SK=ReportedGroups  groups as sent -> MachineDetail.ReportedAsSent
//	PK=GroupRegistry      SK=Current      group registry   -> Group
//	PK=GroupRules#<name>  SK=<Type>#<id>  group rules      -> Rule
//	PK=GroupMembers#<name> SK=<machine id> a group's Macs  -> GroupMember
//
// Execution events come from Rudolph's separate events table:
//
//	PK=Exec#<id>          SK=<TS>#<sha256>#<pid>          -> ExecutionEvent
//
// with the indexes Fleet_TS, Decision_TS and TeamID_TS, and the top lists
// Rudolph's ranker writes to the same table:
//
//	PK=Top#<kind>#<days>                      SK=Current   pointer to the latest snapshot
//	PK=TopSnapshot#<kind>#<days>#<snapshot>   SK=<rank>    -> TopBinary
//
// File access events are not read yet; their interface exists so the web
// layer and fake can be built first.
package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// MaxPageSize is the largest page any implementation must serve.
const MaxPageSize = 100

var (
	// ErrNotFound is returned by point reads for a missing item.
	ErrNotFound = errors.New("store: not found")
	// ErrInvalidCursor is returned when a cursor is malformed, tampered with,
	// or was produced by a different query or filter.
	ErrInvalidCursor = errors.New("store: invalid cursor")
	// ErrInvalidArgument is returned for bad limits or unsupported filters.
	ErrInvalidArgument = errors.New("store: invalid argument")
)

// PageRequest asks for at most Limit items after Cursor ("" = first page).
type PageRequest struct {
	Limit  int
	Cursor string
}

// Page is one page of results. NextCursor is "" on the last page. Cursors are
// opaque to callers.
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// ClientMode is Santa's enforcement mode. Rudolph stores it as a number; a
// number this package does not know is shown as UNKNOWN(n).
type ClientMode string

const (
	ClientModeMonitor    ClientMode = "MONITOR"
	ClientModeLockdown   ClientMode = "LOCKDOWN"
	ClientModeStandalone ClientMode = "STANDALONE"
)

// RuleType is the kind of identifier a rule matches. Unknown numbers from
// Rudolph are shown as UNKNOWN(n).
type RuleType string

const (
	RuleTypeBinary      RuleType = "BINARY"
	RuleTypeCertificate RuleType = "CERTIFICATE"
	RuleTypeTeamID      RuleType = "TEAMID"
	RuleTypeSigningID   RuleType = "SIGNINGID"
	RuleTypeCDHash      RuleType = "CDHASH"
)

// RuleTypes lists every valid RuleType.
var RuleTypes = []RuleType{RuleTypeBinary, RuleTypeCertificate, RuleTypeTeamID, RuleTypeSigningID, RuleTypeCDHash}

// Policy is what Santa does when a rule matches. Unknown numbers from Rudolph
// are shown as UNKNOWN(n).
type Policy string

const (
	PolicyAllowlist         Policy = "ALLOWLIST"
	PolicyAllowlistCompiler Policy = "ALLOWLIST_COMPILER"
	// Santa creates transitive allowlist rules locally; Rudolph never sends
	// them, but a rule list can still hold one.
	PolicyAllowlistTransitive Policy = "ALLOWLIST_TRANSITIVE"
	PolicyBlocklist           Policy = "BLOCKLIST"
	PolicySilentBlocklist     Policy = "SILENT_BLOCKLIST"
	PolicySilentGUIBlocklist  Policy = "SILENT_GUI_BLOCKLIST"
	PolicySilentTTYBlocklist  Policy = "SILENT_TTY_BLOCKLIST"
	PolicyRemove              Policy = "REMOVE"
	PolicyCEL                 Policy = "CEL"
)

// Policies lists every valid Policy.
var Policies = []Policy{
	PolicyAllowlist, PolicyAllowlistCompiler, PolicyAllowlistTransitive, PolicyBlocklist, PolicySilentBlocklist,
	PolicySilentGUIBlocklist, PolicySilentTTYBlocklist, PolicyRemove, PolicyCEL,
}

// Decision is Santa's execution decision as reported in event uploads.
type Decision string

const (
	DecisionAllowUnknown        Decision = "ALLOW_UNKNOWN"
	DecisionAllowBinary         Decision = "ALLOW_BINARY"
	DecisionAllowCertificate    Decision = "ALLOW_CERTIFICATE"
	DecisionAllowScope          Decision = "ALLOW_SCOPE"
	DecisionAllowTeamID         Decision = "ALLOW_TEAMID"
	DecisionAllowSigningID      Decision = "ALLOW_SIGNINGID"
	DecisionAllowCDHash         Decision = "ALLOW_CDHASH"
	DecisionBlockUnknown        Decision = "BLOCK_UNKNOWN"
	DecisionBlockBinary         Decision = "BLOCK_BINARY"
	DecisionBlockCertificate    Decision = "BLOCK_CERTIFICATE"
	DecisionBlockScope          Decision = "BLOCK_SCOPE"
	DecisionBlockTeamID         Decision = "BLOCK_TEAMID"
	DecisionBlockSigningID      Decision = "BLOCK_SIGNINGID"
	DecisionBlockCDHash         Decision = "BLOCK_CDHASH"
	DecisionBlockBinaryMismatch Decision = "BLOCK_BINARY_MISMATCH"
	DecisionBundleBinary        Decision = "BUNDLE_BINARY"
)

// Decisions lists every valid Decision.
var Decisions = []Decision{
	DecisionAllowUnknown, DecisionAllowBinary, DecisionAllowCertificate, DecisionAllowScope,
	DecisionAllowTeamID, DecisionAllowSigningID, DecisionAllowCDHash,
	DecisionBlockUnknown, DecisionBlockBinary, DecisionBlockCertificate, DecisionBlockScope,
	DecisionBlockTeamID, DecisionBlockSigningID, DecisionBlockCDHash, DecisionBlockBinaryMismatch,
	DecisionBundleBinary,
}

// IsBlock reports whether the decision prevented execution.
func (d Decision) IsBlock() bool {
	return len(d) > 6 && d[:6] == "BLOCK_"
}

// FileAccessDecision is Santa's file access authorization decision.
type FileAccessDecision string

const (
	FileAccessDecisionDenied                 FileAccessDecision = "DENIED"
	FileAccessDecisionDeniedInvalidSignature FileAccessDecision = "DENIED_INVALID_SIGNATURE"
	FileAccessDecisionAuditOnly              FileAccessDecision = "AUDIT_ONLY"
)

// RuleCounts mirrors the per-type counts Santa reports in preflight.
type RuleCounts struct {
	Binary      int
	Certificate int
	TeamID      int
	SigningID   int
	CDHash      int
	Compiler    int
	Transitive  int
}

// Machine is a host's latest sensor data (SK=Current).
type Machine struct {
	MachineID       string
	SerialNum       string
	Hostname        string // empty from older Rudolph versions
	ModelIdentifier string // empty from older Rudolph versions
	OSVersion       string
	OSBuild         string
	SantaVersion    string
	ClientMode      ClientMode // as last reported by the Mac
	PrimaryUser     string
	SIPStatus       string // "enabled" (flags 0, or no SIPStatus stored) or "reduced (0x<flags>)"
	RuleCounts      RuleCounts
	LastSeen        time.Time // sensor data Time; in ListMachines, midnight UTC of LastSeenDay (a date only)
}

// SantaRelease is a Santa year.month release, such as 2025.12 from "2025.12.0".
type SantaRelease struct{ Year, Month int }

// ParseSantaVersion reads the release from a reported Santa version. ok is
// false for an empty or unparseable version.
func ParseSantaVersion(v string) (r SantaRelease, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 3)
	if len(parts) < 2 {
		return SantaRelease{}, false
	}
	year, err1 := strconv.Atoi(parts[0])
	month, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || year < 0 || month < 1 || month > 12 {
		return SantaRelease{}, false
	}
	return SantaRelease{Year: year, Month: month}, true
}

// LegacySantaVersion reports whether v is below Santa 2026.8, the oldest
// release Rudolph fully supports; 2025.12 is accepted only for macOS 13 Macs,
// with limits. An empty or unparseable version is not called legacy, since
// nothing is known about it.
func LegacySantaVersion(v string) bool {
	r, ok := ParseSantaVersion(v)
	if !ok {
		return false
	}
	return r.Year < 2026 || (r.Year == 2026 && r.Month < 8)
}

// SyncState is a host's sync progress (SK=SyncState).
type SyncState struct {
	PreflightAt    time.Time
	PostflightAt   time.Time
	LastSyncType   string // NORMAL, CLEAN or CLEAN_ALL
	RulesProcessed int
}

// Config is a global or machine config row: the settings `rudolph config
// update` sets, by Rudolph's attribute names. A nil setting (or an empty
// ClientMode or OverrideFileAccessAction) is one the row does not store with
// a readable value, so a Mac takes it from the global config, or Santa keeps
// its own when the global config does not store it either. USB settings are
// left out: Santa 2026.8 replaces them with a removable media policy Rudolph
// does not model yet.
type Config struct {
	// Exists says whether the row is there at all. Without a global config
	// Rudolph sends its defaults, so GlobalConfig then reports MONITOR.
	Exists                    bool
	ClientMode                ClientMode
	AllowedPathRegex          *string
	BlockedPathRegex          *string
	BatchSize                 *int
	EnableBundles             *bool
	EnableTransitiveRules     *bool
	FullSyncInterval          *int    // seconds
	UploadLogsURL             *string // attribute UploadLogsUrl
	EnableAllEventUpload      *bool
	DisableUnknownEventUpload *bool
	OverrideFileAccessAction  string // NONE, AUDIT_ONLY or DISABLE
	EventDetailURL            *string
	EventDetailText           *string
}

// MachineDetail combines sensor data with sync state and machine config. Sync
// is nil when the machine has no sync state row (never synced, or expired).
type MachineDetail struct {
	Machine
	Sync *SyncState
	// Config is the Mac's own config (SK=Config): only the settings it
	// overrides; the global config supplies the rest.
	Config Config
	// ConfiguredClientMode is the mode the machine config (SK=Config) sets,
	// which can differ from the mode the Mac last reported until it syncs.
	// Empty when the machine config does not set one, so the global config
	// applies.
	ConfiguredClientMode ClientMode
	// TemporaryClientMode is a temporary mode from the machine config, sent
	// instead of the usual mode until TemporaryClientModeUntil. Both are left
	// empty unless the row holds a known mode and an expiry. The expiry may
	// already be past: Rudolph leaves ended values on the row, so readers
	// compare it with the current time.
	TemporaryClientMode      ClientMode
	TemporaryClientModeUntil time.Time
	// CleanSyncRequested is the machine config's CleanSync flag: a clean sync
	// was requested and has not completed yet.
	CleanSyncRequested bool
	// AssignedGroups are the groups assigned to the Mac in Rudolph (rudolph
	// group add), sorted.
	AssignedGroups []string
	// ReportedGroups are the groups the Mac reported that count: registered,
	// accepting reported membership and normalised, as of its last preflight.
	ReportedGroups []string
	// ReportedAsSent is the primary user's groups as Santa last sent them,
	// unnormalised and including ones Rudolph does not know.
	ReportedAsSent []string
}

// UngroupedGroup is Rudolph's reserved group of the Macs in no group. It can
// have rules without being registered; Rudolph registers it when it first gets
// a rule or a client mode.
const UngroupedGroup = "rudolph:ungrouped"

// Group is a registered device group.
type Group struct {
	Name        string
	Description string
	// AcceptReported is whether a Mac reporting the group (through its
	// primary user's directory groups) becomes a member; when false only
	// assignment in Rudolph counts.
	AcceptReported bool
	// ClientMode is the mode the group's Macs get when they have no temporary
	// mode or mode of their own (the strictest of their groups' wins), ahead
	// of the global config's. "" when the group has none, or stores one
	// Rudolph cannot decode and so ignores.
	ClientMode ClientMode
}

// GroupMember is one of a group's Macs. Machine always has MachineID; the
// rest comes from the Mac's sensor data and is empty when it has none (a Mac
// assigned before it ever synced). Assigned and Reported say how it is a
// member: assigned in Rudolph, or reporting the group (which then counts only
// when the group accepts reported membership).
type GroupMember struct {
	Machine  Machine
	Assigned bool
	Reported bool
}

// GroupMemberFilter narrows ListGroupMembers. AssignedOnly leaves out Macs
// that only report the group, for a group that counts only assigned Macs.
type GroupMemberFilter struct {
	AssignedOnly bool
}

// MachineFilter narrows ListMachines. At most one prefix may be set; each maps
// to a different index.
type MachineFilter struct {
	SerialPrefix   string
	HostnamePrefix string
}

// RuleState is whether a rule currently applies.
type RuleState string

const (
	RuleStateInForce RuleState = "IN_FORCE"
	// RuleStateRemoving: Rudolph is telling Macs to drop the rule at their
	// next sync.
	RuleStateRemoving RuleState = "REMOVING"
	// RuleStateExpired: the rule's expiry has passed but Rudolph has not yet
	// deleted the row.
	RuleStateExpired RuleState = "EXPIRED"
)

// Rule is a global or machine-specific Santa rule.
type Rule struct {
	RuleType      RuleType
	Policy        Policy
	Identifier    string
	CustomMessage string
	CustomURL     string
	CELExpr       string
	Description   string
	ExpiresAt     time.Time // machine rules only; zero if none
	State         RuleState
	// CollectAllowedEvents: Rudolph stores the executions this global or
	// machine rule allows, from Macs that upload allowed executions.
	CollectAllowedEvents bool
}

// RuleFilter narrows ListGlobalRules. Zero values match everything.
type RuleFilter struct {
	RuleType RuleType
	Policy   Policy
}

// ExecutionEvent is one uploaded execution event.
type ExecutionEvent struct {
	MachineID     string
	ExecutionTime time.Time
	Decision      Decision
	FileSHA256    string
	FilePath      string
	FileName      string
	ExecutingUser string
	TeamID        string
	SigningID     string
	CDHash        string
	// IdentityUnverified is set when Santa could not confirm the file it
	// evaluated was the image that ran, so the hash and file details are
	// not verified (Santa 2026.9 identity_unverified).
	IdentityUnverified bool
}

// EventFilter narrows ListExecutionEvents. Zero values match everything. The
// DynamoDB reader serves one filter at a time (each is a different key) and
// refuses a combination with ErrInvalidArgument.
type EventFilter struct {
	MachineID string
	Decision  Decision
	TeamID    string
}

// Process is one entry in a file access event's process chain.
type Process struct {
	FilePath   string
	FileSHA256 string
	CDHash     string
	SigningID  string
	TeamID     string
	PID        int32
}

// FileAccessEvent is one uploaded file access authorization event.
type FileAccessEvent struct {
	MachineID    string
	AccessTime   time.Time
	RuleVersion  string
	RuleName     string
	Target       string
	Decision     FileAccessDecision
	ProcessChain []Process
}

// FileAccessEventFilter narrows ListFileAccessEvents.
type FileAccessEventFilter struct {
	MachineID string
	Decision  FileAccessDecision
}

// TopKind selects one of the ranker's lists: binaries blocked or allowed most
// often, or the unknown ones among them, which no rule decided (Santa's
// BLOCK_UNKNOWN and ALLOW_UNKNOWN). Unknown is UnknownBlocked and
// UnknownAllowed together.
type TopKind string

const (
	TopKindBlocked        TopKind = "BLOCKED"
	TopKindAllowed        TopKind = "ALLOWED"
	TopKindUnknown        TopKind = "UNKNOWN"
	TopKindUnknownBlocked TopKind = "UNKNOWN_BLOCKED"
	TopKindUnknownAllowed TopKind = "UNKNOWN_ALLOWED"
)

// TopKinds lists every valid TopKind.
var TopKinds = []TopKind{TopKindBlocked, TopKindAllowed, TopKindUnknown, TopKindUnknownBlocked, TopKindUnknownAllowed}

// IsUnknown reports whether the kind is one of the unknown lists, whose first
// 100 rows the ranker orders for deciding what rule to add and marks with
// GlobalRule.
func (k TopKind) IsUnknown() bool {
	return k == TopKindUnknown || k == TopKindUnknownBlocked || k == TopKindUnknownAllowed
}

// TopWindowDays are the windows, in days ending today (UTC), that Rudolph's
// ranker computes. No other window exists in the table.
var TopWindowDays = []int{1, 7, 30, 90}

// TopFilter selects one ranker list.
type TopFilter struct {
	Kind       TopKind
	WindowDays int
}

// Validate returns ErrInvalidArgument for a kind or window the ranker does
// not compute.
func (f TopFilter) Validate() error {
	if !slices.Contains(TopKinds, f.Kind) {
		return fmt.Errorf("%w: unknown top list kind %q", ErrInvalidArgument, f.Kind)
	}
	if !slices.Contains(TopWindowDays, f.WindowDays) {
		return fmt.Errorf("%w: top list window %d days is not one of %v", ErrInvalidArgument, f.WindowDays, TopWindowDays)
	}
	return nil
}

// TopBinary is one row of a top list. Count is the number of executions of
// the list's kind in the window, by execution date. Machines is the number of
// Macs that uploaded any stored execution of the binary in the window, by
// upload date; the ranker counts it for the first 100 rows only, so
// MachinesKnown is false further down. GlobalRule, on the first 100 rows of
// the unknown lists only, is the sort key of a global rule added since the
// executions (Binary#<SHA-256>, SigningID#<signing ID> or TeamID#<team ID>).
type TopBinary struct {
	FileSHA256    string
	FileName      string
	TeamID        string
	SigningID     string
	Count         int
	Machines      int
	MachinesKnown bool
	GlobalRule    string
}

// TopList is one page of a ranker snapshot, in the snapshot's order (Count,
// highest first; in the unknown lists the first 100 rows put binaries without
// a global rule first, then order by Machines). GeneratedAt is when the ranker built the snapshot; zero
// when the list has not been computed yet, in which case Items is empty.
// Every page of one listing comes from the same snapshot.
type TopList struct {
	Items       []TopBinary
	NextCursor  string
	GeneratedAt time.Time
}

// MachineReader reads machines and their machine-specific rules.
type MachineReader interface {
	// ListMachines returns machines ordered by the index implied by the filter:
	// serial (no filter, or a serial prefix) or hostname. Prefixes are trimmed;
	// serial prefixes match upper case and hostname prefixes ignore case.
	ListMachines(ctx context.Context, f MachineFilter, p PageRequest) (Page[Machine], error)
	// GetMachine reads one machine's sensor data, sync state and own config
	// (a single Query on PK=Machine#<id>). Returns ErrNotFound.
	GetMachine(ctx context.Context, machineID string) (MachineDetail, error)
	// ListMachineRules returns rules in PK=MachineRules#<id>, in SK order.
	ListMachineRules(ctx context.Context, machineID string, p PageRequest) (Page[Rule], error)
	// GlobalClientMode returns the client mode of the global config (one
	// GetItem of PK=GlobalConfig, SK=Config), which a Mac follows when its
	// machine config sets none. MONITOR when there is no global config, as
	// Rudolph then sends; "" when the row sets no client mode.
	GlobalClientMode(ctx context.Context) (ClientMode, error)
	// GlobalConfig returns the global config (one GetItem of
	// PK=GlobalConfig, SK=Config). Without the row, Exists is false and
	// ClientMode is MONITOR, as Rudolph then sends.
	GlobalConfig(ctx context.Context) (Config, error)
}

// RuleReader reads global rules.
type RuleReader interface {
	// ListGlobalRules returns rules in PK=GlobalRules, in SK order.
	ListGlobalRules(ctx context.Context, f RuleFilter, p PageRequest) (Page[Rule], error)
}

// GroupReader reads device groups and their rules.
type GroupReader interface {
	// ListGroups returns the registered groups sorted by name; none when
	// there is no registry. It honours Consistent.
	ListGroups(ctx context.Context) ([]Group, error)
	// ListGroupRules returns rules in PK=GroupRules#<group>, in SK order.
	ListGroupRules(ctx context.Context, group string, p PageRequest) (Page[Rule], error)
	// ListGroupMembers returns the group's Macs from PK=GroupMembers#<group>,
	// in machine ID order, with each page's sensor data read in one batch.
	ListGroupMembers(ctx context.Context, group string, f GroupMemberFilter, p PageRequest) (Page[GroupMember], error)
}

// EventReader reads uploaded events and summaries.
type EventReader interface {
	// ListExecutionEvents returns events newest first.
	ListExecutionEvents(ctx context.Context, f EventFilter, p PageRequest) (Page[ExecutionEvent], error)
	// TopBinaries returns a page of the ranker's latest list for f, or of the
	// snapshot a cursor pinned. Returns ErrInvalidArgument for a kind or
	// window that does not exist, and ErrInvalidCursor for a cursor whose
	// snapshot has expired (Rudolph deletes snapshot rows 8 days after
	// writing them) rather than an empty page.
	TopBinaries(ctx context.Context, f TopFilter, p PageRequest) (TopList, error)
	// ListFileAccessEvents returns file access events newest first.
	ListFileAccessEvents(ctx context.Context, f FileAccessEventFilter, p PageRequest) (Page[FileAccessEvent], error)
}

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

// AuditReader reads Rudolph's audit trail. It is a read, not part of Writer,
// because admins see who changed what (with the CLI as well as the web view)
// whether or not editing is on.
type AuditReader interface {
	// ListAuditPage returns one page (1..MaxPageSize entries) of the audit
	// entries recorded at or after since, newest first, with a signed cursor
	// to the next page. A cursor issued for another list is ErrInvalidCursor.
	ListAuditPage(ctx context.Context, since time.Time, p PageRequest) (Page[AuditEntry], error)
}

// Reader is everything the web layer reads.
type Reader interface {
	MachineReader
	RuleReader
	GroupReader
	EventReader
	AuditReader
}

// ErrUnavailable is returned by Unavailable. The web layer renders it as 503.
var ErrUnavailable = errors.New("store: data layer not configured")

// Unavailable is a Reader that fails every call with ErrUnavailable. It
// stands in for the DynamoDB implementation until that exists.
type Unavailable struct{}

var _ Reader = Unavailable{}

func (Unavailable) ListMachines(context.Context, MachineFilter, PageRequest) (Page[Machine], error) {
	return Page[Machine]{}, ErrUnavailable
}

func (Unavailable) GetMachine(context.Context, string) (MachineDetail, error) {
	return MachineDetail{}, ErrUnavailable
}

func (Unavailable) ListMachineRules(context.Context, string, PageRequest) (Page[Rule], error) {
	return Page[Rule]{}, ErrUnavailable
}

func (Unavailable) GlobalClientMode(context.Context) (ClientMode, error) {
	return "", ErrUnavailable
}

func (Unavailable) GlobalConfig(context.Context) (Config, error) {
	return Config{}, ErrUnavailable
}

func (Unavailable) ListGlobalRules(context.Context, RuleFilter, PageRequest) (Page[Rule], error) {
	return Page[Rule]{}, ErrUnavailable
}

func (Unavailable) ListGroups(context.Context) ([]Group, error) {
	return nil, ErrUnavailable
}

func (Unavailable) ListGroupRules(context.Context, string, PageRequest) (Page[Rule], error) {
	return Page[Rule]{}, ErrUnavailable
}

func (Unavailable) ListGroupMembers(context.Context, string, GroupMemberFilter, PageRequest) (Page[GroupMember], error) {
	return Page[GroupMember]{}, ErrUnavailable
}

func (Unavailable) ListExecutionEvents(context.Context, EventFilter, PageRequest) (Page[ExecutionEvent], error) {
	return Page[ExecutionEvent]{}, ErrUnavailable
}

func (Unavailable) TopBinaries(context.Context, TopFilter, PageRequest) (TopList, error) {
	return TopList{}, ErrUnavailable
}

func (Unavailable) ListFileAccessEvents(context.Context, FileAccessEventFilter, PageRequest) (Page[FileAccessEvent], error) {
	return Page[FileAccessEvent]{}, ErrUnavailable
}

func (Unavailable) ListAuditPage(context.Context, time.Time, PageRequest) (Page[AuditEntry], error) {
	return Page[AuditEntry]{}, ErrUnavailable
}
