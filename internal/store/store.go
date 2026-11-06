// Package store defines the read models and data-access interfaces used by
// the web layer. Implementations must read through bounded, paginated queries
// (DynamoDB Query with Limit + ExclusiveStartKey), never scans.
//
// The shapes follow Rudolph's single-table DynamoDB layout:
//
//	PK=Machine#<id>       SK=Current      sensor data      -> Machine
//	PK=Machine#<id>       SK=SyncState    sync state       -> SyncState
//	PK=Machine#<id>       SK=Config       machine config   -> MachineDetail.ConfiguredClientMode
//	PK=GlobalRules        SK=<Type>#<id>  global rules     -> Rule
//	PK=MachineRules#<id>  SK=<Type>#<id>  machine rules    -> Rule
//
// Execution and file access events are not yet stored queryably by Rudolph;
// their interfaces exist so the web layer and fake can be built first.
package store

import (
	"context"
	"errors"
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

// SyncState is a host's sync progress (SK=SyncState).
type SyncState struct {
	PreflightAt    time.Time
	PostflightAt   time.Time
	LastSyncType   string // NORMAL, CLEAN or CLEAN_ALL
	RulesProcessed int
}

// MachineDetail combines sensor data with sync state and machine config. Sync
// is nil when the machine has no sync state row (never synced, or expired).
type MachineDetail struct {
	Machine
	Sync *SyncState
	// ConfiguredClientMode is the mode the machine config (SK=Config) sets,
	// which can differ from the mode the Mac last reported until it syncs.
	// Empty when the machine config does not set one, so the global config
	// applies.
	ConfiguredClientMode ClientMode
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
}

// EventFilter narrows ListExecutionEvents. Zero values match everything.
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

// BlockedBinary is one row of the "top blocked binaries" summary.
type BlockedBinary struct {
	FileSHA256   string
	FileName     string
	TeamID       string
	SigningID    string
	BlockCount   int
	MachineCount int
	LastBlocked  time.Time
}

// TopBlockedFilter selects the aggregation window.
type TopBlockedFilter struct {
	Since time.Time
}

// MachineReader reads machines and their machine-specific rules.
type MachineReader interface {
	// ListMachines returns machines ordered by the index implied by the filter:
	// serial (no filter, or a serial prefix) or hostname. Prefixes are trimmed;
	// serial prefixes match upper case and hostname prefixes ignore case.
	ListMachines(ctx context.Context, f MachineFilter, p PageRequest) (Page[Machine], error)
	// GetMachine reads one machine's sensor data and sync state
	// (a single Query on PK=Machine#<id>). Returns ErrNotFound.
	GetMachine(ctx context.Context, machineID string) (MachineDetail, error)
	// ListMachineRules returns rules in PK=MachineRules#<id>, in SK order.
	ListMachineRules(ctx context.Context, machineID string, p PageRequest) (Page[Rule], error)
}

// RuleReader reads global rules.
type RuleReader interface {
	// ListGlobalRules returns rules in PK=GlobalRules, in SK order.
	ListGlobalRules(ctx context.Context, f RuleFilter, p PageRequest) (Page[Rule], error)
}

// EventReader reads uploaded events and summaries.
type EventReader interface {
	// ListExecutionEvents returns events newest first.
	ListExecutionEvents(ctx context.Context, f EventFilter, p PageRequest) (Page[ExecutionEvent], error)
	// TopBlockedBinaries returns binaries by descending block count since f.Since.
	TopBlockedBinaries(ctx context.Context, f TopBlockedFilter, p PageRequest) (Page[BlockedBinary], error)
	// ListFileAccessEvents returns file access events newest first.
	ListFileAccessEvents(ctx context.Context, f FileAccessEventFilter, p PageRequest) (Page[FileAccessEvent], error)
}

// Reader is everything the web layer reads.
type Reader interface {
	MachineReader
	RuleReader
	EventReader
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

func (Unavailable) ListGlobalRules(context.Context, RuleFilter, PageRequest) (Page[Rule], error) {
	return Page[Rule]{}, ErrUnavailable
}

func (Unavailable) ListExecutionEvents(context.Context, EventFilter, PageRequest) (Page[ExecutionEvent], error) {
	return Page[ExecutionEvent]{}, ErrUnavailable
}

func (Unavailable) TopBlockedBinaries(context.Context, TopBlockedFilter, PageRequest) (Page[BlockedBinary], error) {
	return Page[BlockedBinary]{}, ErrUnavailable
}

func (Unavailable) ListFileAccessEvents(context.Context, FileAccessEventFilter, PageRequest) (Page[FileAccessEvent], error) {
	return Page[FileAccessEvent]{}, ErrUnavailable
}
