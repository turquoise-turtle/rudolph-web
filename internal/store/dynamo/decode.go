// Package dynamo reads Rudolph's single-table DynamoDB layout into the store
// read models.
//
// Decoding is explicit rather than through attributevalue.UnmarshalMap so that
// a row Rudolph wrote with an unexpected attribute type yields an error naming
// the attribute, and attributes Rudolph adds later are ignored.
package dynamo

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// Sort keys of the rows in a Machine#<id> partition.
const (
	skCurrent   = "Current"
	skSyncState = "SyncState"
	skConfig    = "Config"
)

var ruleTypes = map[int64]store.RuleType{
	1: store.RuleTypeBinary,
	2: store.RuleTypeCertificate,
	3: store.RuleTypeSigningID,
	4: store.RuleTypeTeamID,
	5: store.RuleTypeCDHash,
}

var policies = map[int64]store.Policy{
	1: store.PolicyAllowlist,
	2: store.PolicyBlocklist,
	3: store.PolicySilentBlocklist,
	4: store.PolicyRemove,
	5: store.PolicyAllowlistCompiler,
	6: store.PolicyAllowlistTransitive,
	7: store.PolicyCEL,
	8: store.PolicySilentGUIBlocklist,
	9: store.PolicySilentTTYBlocklist,
}

var clientModes = map[int64]store.ClientMode{
	1: store.ClientModeMonitor,
	2: store.ClientModeLockdown,
	3: store.ClientModeStandalone,
}

// DecodeMachinePartition decodes every row of one Machine#<id> partition:
// sensor data (SK=Current), sync state (SK=SyncState) and machine config
// (SK=Config). Rows with other sort keys are ignored so that Rudolph can add
// row kinds without breaking the web layer. A partition without sensor data
// (expired, or holding only a machine config) is store.ErrNotFound.
func DecodeMachinePartition(items []map[string]types.AttributeValue) (store.MachineDetail, error) {
	var (
		d          store.MachineDetail
		hasCurrent bool
	)
	for _, it := range items {
		r := reader{item: it}
		sk := r.str("SK")
		switch sk {
		case skCurrent:
			hasCurrent = true
			d.Machine = r.machine()
		case skSyncState:
			st := r.syncState()
			d.Sync = &st
		case skConfig:
			d.ConfiguredClientMode = r.configClientMode()
		}
		if r.err != nil {
			return store.MachineDetail{}, fmt.Errorf("dynamo: decoding %s row %q: %w", r.str("PK"), sk, r.err)
		}
	}
	if !hasCurrent {
		return store.MachineDetail{}, store.ErrNotFound
	}
	return d, nil
}

// DecodeRule decodes one row of GlobalRules or MachineRules#<id>. now decides
// whether a rule with an expiry is still in force.
func DecodeRule(item map[string]types.AttributeValue, now time.Time) (store.Rule, error) {
	r := reader{item: item}
	rule := store.Rule{
		RuleType:      enum(r.requiredNum("RuleType"), ruleTypes),
		Policy:        enum(r.requiredNum("Policy"), policies),
		Identifier:    r.str("Identifier"),
		CustomMessage: r.str("CustomMessage"),
		CustomURL:     r.str("CustomURL"),
		CELExpr:       r.str("CELExpr"),
		Description:   r.str("Description"),
	}
	if rule.Identifier == "" {
		// Rows written by the original Rudolph carry the identifier as SHA256.
		rule.Identifier = r.str("SHA256")
	}
	if exp := r.num("ExpiresAfter"); exp != 0 {
		rule.ExpiresAt = time.Unix(exp, 0).UTC()
	}
	deleting := r.boolean("DeleteOnNextSync")
	if r.err != nil {
		return store.Rule{}, fmt.Errorf("dynamo: decoding rule %q: %w", r.str("SK"), r.err)
	}
	if rule.Identifier == "" {
		return store.Rule{}, fmt.Errorf("dynamo: decoding rule %q: no Identifier or SHA256", r.str("SK"))
	}
	switch {
	case deleting || rule.Policy == store.PolicyRemove:
		rule.State = store.RuleStateRemoving
	case !rule.ExpiresAt.IsZero() && !rule.ExpiresAt.After(now):
		rule.State = store.RuleStateExpired
	default:
		rule.State = store.RuleStateInForce
	}
	return rule, nil
}

// DecodeMachineListItem decodes one item of the machine list indexes, which
// project only what the list shows. SerialNum is shown as reported, not the
// upper-case SerialKey the serial index is keyed on. Its last seen is LastSeenDay, a UTC date
// Rudolph rewrites at most once a day so the indexes are not written on every
// sync; a missing or unreadable one is shown as unknown rather than failing
// the list.
func DecodeMachineListItem(item map[string]types.AttributeValue) (store.Machine, error) {
	r := reader{item: item}
	m := store.Machine{
		MachineID:       r.str("MachineID"),
		SerialNum:       r.str("SerialNum"),
		Hostname:        r.str("Hostname"),
		ModelIdentifier: r.str("ModelIdentifier"),
		OSVersion:       r.str("OSVersion"),
		SantaVersion:    r.str("SantaVersion"),
		ClientMode:      r.clientMode(),
		PrimaryUser:     r.str("PrimaryUser"),
	}
	if v, ok := item["LastSeenDay"].(*types.AttributeValueMemberS); ok {
		if day, err := time.Parse(time.DateOnly, v.Value); err == nil {
			m.LastSeen = day
		}
	}
	if m.MachineID == "" {
		m.MachineID = strings.TrimPrefix(r.str("PK"), "Machine#")
	}
	if r.err != nil {
		return store.Machine{}, fmt.Errorf("dynamo: decoding machine list item %q: %w", r.str("PK"), r.err)
	}
	return m, nil
}

func (r *reader) machine() store.Machine {
	m := store.Machine{
		MachineID:       r.str("MachineID"),
		SerialNum:       r.str("SerialNum"),
		Hostname:        r.str("Hostname"),
		ModelIdentifier: r.str("ModelIdentifier"),
		OSVersion:       r.str("OSVersion"),
		OSBuild:         r.str("OSBuild"),
		SantaVersion:    r.str("SantaVersion"),
		ClientMode:      r.clientMode(),
		PrimaryUser:     r.str("PrimaryUser"),
		RuleCounts: store.RuleCounts{
			Binary:      int(r.num("BinaryRuleCount")),
			Certificate: int(r.num("CertificateRuleCount")),
			TeamID:      int(r.num("TeamIDRuleCount")),
			SigningID:   int(r.num("SigningIDRuleCount")),
			CDHash:      int(r.num("CDHashRuleCount")),
			Compiler:    int(r.num("CompilerRuleCount")),
			Transitive:  int(r.num("TransitiveRuleCount")),
		},
		LastSeen: r.timestamp("Time"),
	}
	if m.MachineID == "" {
		m.MachineID = strings.TrimPrefix(r.str("PK"), "Machine#")
	}
	// SIPStatus is the raw csrutil configuration flags: 0 means every
	// protection is on, and each set bit turns one off. Santa's sip_status is a
	// plain proto3 uint32, so a 0 is left out of the preflight and Rudolph
	// stores no attribute: a missing SIPStatus is a Mac with SIP fully on.
	if flags := r.num("SIPStatus"); flags == 0 {
		m.SIPStatus = "enabled"
	} else {
		m.SIPStatus = fmt.Sprintf("reduced (0x%x)", flags)
	}
	return m
}

func (r *reader) syncState() store.SyncState {
	return store.SyncState{
		PreflightAt:    r.timestamp("PreflightAt"),
		PostflightAt:   r.timestamp("PostflightAt"),
		LastSyncType:   strings.ToUpper(r.str("LastSyncType")),
		RulesProcessed: int(r.num("RulesProcessed")),
	}
}

func (r *reader) clientMode() store.ClientMode {
	if !r.present("ClientMode") {
		return ""
	}
	return enum(r.num("ClientMode"), clientModes)
}

// configClientMode reads ClientMode from a machine config row. Rudolph skips a
// machine config attribute it cannot decode and applies the global value, so a
// mistyped one is shown as not set instead of failing the machine page.
func (r *reader) configClientMode() store.ClientMode {
	v, ok := r.item["ClientMode"].(*types.AttributeValueMemberN)
	if !ok {
		return ""
	}
	n, err := strconv.ParseInt(v.Value, 10, 64)
	if err != nil {
		return ""
	}
	return enum(n, clientModes)
}

func enum[T ~string](n int64, known map[int64]T) T {
	if v, ok := known[n]; ok {
		return v
	}
	return T(fmt.Sprintf("UNKNOWN(%d)", n))
}

// reader reads attributes of one item, keeping the first error so a row can
// be decoded in one pass and checked once.
type reader struct {
	item map[string]types.AttributeValue
	err  error
}

func (r *reader) present(name string) bool {
	switch r.item[name].(type) {
	case nil, *types.AttributeValueMemberNULL:
		return false
	}
	return true
}

func (r *reader) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

// str returns a string attribute, or "" if it is absent or NULL.
func (r *reader) str(name string) string {
	switch v := r.item[name].(type) {
	case nil, *types.AttributeValueMemberNULL:
		return ""
	case *types.AttributeValueMemberS:
		return v.Value
	default:
		r.fail(fmt.Errorf("%s: want a string, got %T", name, v))
		return ""
	}
}

// num returns an integer attribute, or 0 if it is absent or NULL.
func (r *reader) num(name string) int64 {
	switch v := r.item[name].(type) {
	case nil, *types.AttributeValueMemberNULL:
		return 0
	case *types.AttributeValueMemberN:
		n, err := strconv.ParseInt(v.Value, 10, 64)
		if err != nil {
			r.fail(fmt.Errorf("%s: want an integer, got %q", name, v.Value))
		}
		return n
	default:
		r.fail(fmt.Errorf("%s: want a number, got %T", name, v))
		return 0
	}
}

func (r *reader) requiredNum(name string) int64 {
	if !r.present(name) {
		r.fail(fmt.Errorf("%s: missing", name))
		return 0
	}
	return r.num(name)
}

// boolean returns a boolean attribute, or false if it is absent or NULL.
func (r *reader) boolean(name string) bool {
	switch v := r.item[name].(type) {
	case nil, *types.AttributeValueMemberNULL:
		return false
	case *types.AttributeValueMemberBOOL:
		return v.Value
	default:
		r.fail(fmt.Errorf("%s: want a boolean, got %T", name, v))
		return false
	}
}

// timestamp returns an RFC 3339 time attribute in UTC, or the zero time if it is
// absent, NULL or empty.
func (r *reader) timestamp(name string) time.Time {
	s := r.str(name)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		r.fail(fmt.Errorf("%s: want an RFC 3339 time, got %q", name, s))
		return time.Time{}
	}
	return t.UTC()
}
