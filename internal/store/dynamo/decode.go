// Package dynamo reads Rudolph's single-table DynamoDB layout into the store
// read models.
//
// Decoding is explicit rather than through attributevalue.UnmarshalMap so that
// a row Rudolph wrote with an unexpected attribute type yields an error naming
// the attribute, and attributes Rudolph adds later are ignored.
package dynamo

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// Sort keys of the rows in a Machine#<id> partition.
const (
	skCurrent        = "Current"
	skSyncState      = "SyncState"
	skConfig         = "Config"
	skGroups         = "Groups"
	skReportedGroups = "ReportedGroups"
)

// pkGlobalConfig is the partition of the global config, whose one row has
// SK=Config like a machine config.
const pkGlobalConfig = "GlobalConfig"

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
// sensor data (SK=Current), sync state (SK=SyncState), machine config
// (SK=Config), the Mac's groups (SK=Groups) and the groups Santa reported
// (SK=ReportedGroups). Rows with other sort keys are ignored so that Rudolph can add
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
			d.Config = r.config()
			d.ConfiguredClientMode = d.Config.ClientMode
			d.TemporaryClientMode, d.TemporaryClientModeUntil = r.configTemporaryClientMode()
			d.CleanSyncRequested = r.configCleanSync()
		case skGroups:
			d.AssignedGroups = sortedStrings(stringList(it["Assigned"]))
			d.ReportedGroups = sortedStrings(stringList(it["Reported"]))
		case skReportedGroups:
			d.ReportedAsSent = stringList(it["PrimaryUserGroups"])
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

// DecodeGlobalClientMode decodes ClientMode from the global config row. With
// no row (a nil or empty item) it is MONITOR, the mode Rudolph sends then
// (machineconfiguration.GetUniversalDefaultConfig). Rudolph unmarshals the
// whole row, so a mistyped ClientMode fails its syncs too, and is an error
// here rather than a guess.
func DecodeGlobalClientMode(item map[string]types.AttributeValue) (store.ClientMode, error) {
	if len(item) == 0 {
		return store.ClientModeMonitor, nil
	}
	r := reader{item: item}
	mode := r.clientMode()
	if r.err != nil {
		return "", fmt.Errorf("dynamo: decoding the global config: %w", r.err)
	}
	return mode, nil
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

// DecodeConfig decodes a global or machine config row. With no row (a nil or
// empty item) Exists is false and ClientMode is MONITOR, as Rudolph then
// sends its defaults (machineconfiguration.GetUniversalDefaultConfig); the
// Mac page reads a machine's row through DecodeMachinePartition instead.
func DecodeConfig(item map[string]types.AttributeValue) store.Config {
	if len(item) == 0 {
		return store.Config{ClientMode: store.ClientModeMonitor}
	}
	r := reader{item: item}
	return r.config()
}

// config reads the settings of a config row. Rudolph leaves out an attribute
// whose stored type does not fit its setting rather than failing every sync
// that reads the row, and a Mac then follows the global value, so a mistyped
// one is shown as not set instead of failing the page.
func (r *reader) config() store.Config {
	c := store.Config{
		Exists:                    true,
		AllowedPathRegex:          r.configStr("AllowedPathRegex"),
		BlockedPathRegex:          r.configStr("BlockedPathRegex"),
		BatchSize:                 r.configInt("BatchSize"),
		EnableBundles:             r.configBool("EnableBundles"),
		EnableTransitiveRules:     r.configBool("EnableTransitiveRules"),
		FullSyncInterval:          r.configInt("FullSyncInterval"),
		UploadLogsURL:             r.configStr("UploadLogsUrl"),
		EnableAllEventUpload:      r.configBool("EnableAllEventUpload"),
		DisableUnknownEventUpload: r.configBool("DisableUnknownEventUpload"),
		EventDetailURL:            r.configStr("EventDetailURL"),
		EventDetailText:           r.configStr("EventDetailText"),
	}
	if n, ok := r.configNum("ClientMode"); ok {
		c.ClientMode = enum(n, clientModes)
	}
	if v := r.configStr("OverrideFileAccessAction"); v != nil {
		c.OverrideFileAccessAction = *v
	}
	return c
}

func (r *reader) configStr(name string) *string {
	v, ok := r.item[name].(*types.AttributeValueMemberS)
	if !ok {
		return nil
	}
	return &v.Value
}

func (r *reader) configBool(name string) *bool {
	v, ok := r.item[name].(*types.AttributeValueMemberBOOL)
	if !ok {
		return nil
	}
	return &v.Value
}

func (r *reader) configInt(name string) *int {
	n, ok := r.configNum(name)
	if !ok || int64(int(n)) != n {
		return nil
	}
	i := int(n)
	return &i
}

// configTemporaryClientMode reads TemporaryClientMode and
// TemporaryClientModeExpiresAt (Unix seconds) from a machine config row.
// Rudolph applies a temporary mode only when both decode and the mode is a
// known one and the expiry is positive, so anything else is shown as no
// temporary mode.
func (r *reader) configTemporaryClientMode() (store.ClientMode, time.Time) {
	mode, ok1 := r.configNum("TemporaryClientMode")
	until, ok2 := r.configNum("TemporaryClientModeExpiresAt")
	known, ok3 := clientModes[mode]
	if !ok1 || !ok2 || !ok3 || until <= 0 {
		return "", time.Time{}
	}
	return known, time.Unix(until, 0).UTC()
}

// configCleanSync reads CleanSync from a machine config row; a mistyped value
// is shown as no request rather than failing the machine page.
func (r *reader) configCleanSync() bool {
	v, ok := r.item["CleanSync"].(*types.AttributeValueMemberBOOL)
	return ok && v.Value
}

// configNum reads an integer from a machine config row without failing the
// row, for settings Rudolph ignores when they are mistyped.
func (r *reader) configNum(name string) (int64, bool) {
	v, ok := r.item[name].(*types.AttributeValueMemberN)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v.Value, 10, 64)
	return n, err == nil
}

// stringList reads a string set or a list of strings. The groups rows are
// optional extras on the machine page, so a mistyped value, or a mistyped
// element, is left out instead of failing the page.
func stringList(v types.AttributeValue) []string {
	var out []string
	switch v := v.(type) {
	case *types.AttributeValueMemberSS:
		out = append(out, v.Value...)
	case *types.AttributeValueMemberL:
		for _, e := range v.Value {
			if s, ok := e.(*types.AttributeValueMemberS); ok {
				out = append(out, s.Value)
			}
		}
	}
	return out
}

// sortedStrings sorts a set DynamoDB returns in no particular order.
func sortedStrings(s []string) []string {
	slices.Sort(s)
	return s
}

// DecodeGroupRegistry decodes the GroupRegistry/Current item: its Groups map,
// name to entry, sorted by name. An entry that is not a map is skipped, and a
// mistyped Description or AcceptReported is shown as empty or false, so one
// bad entry does not hide the others.
func DecodeGroupRegistry(item map[string]types.AttributeValue) []store.Group {
	m, ok := item["Groups"].(*types.AttributeValueMemberM)
	if !ok {
		return nil
	}
	groups := make([]store.Group, 0, len(m.Value))
	for name, v := range m.Value {
		entry, ok := v.(*types.AttributeValueMemberM)
		if !ok {
			continue
		}
		g := store.Group{Name: name}
		if d, ok := entry.Value["Description"].(*types.AttributeValueMemberS); ok {
			g.Description = d.Value
		}
		if a, ok := entry.Value["AcceptReported"].(*types.AttributeValueMemberBOOL); ok {
			g.AcceptReported = a.Value
		}
		groups = append(groups, g)
	}
	slices.SortFunc(groups, func(a, b store.Group) int { return strings.Compare(a.Name, b.Name) })
	return groups
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
