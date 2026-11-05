package dynamo_test

// Decoding Rudolph's DynamoDB rows into the web read models, checked against the web contract fixture
// (testdata/webcontract/rows.json, vendored from Rudolph). The fixture replaces values that differ on every run with
// "<timestamp>" and "<number>"; loadRows puts concrete values back.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
)

const (
	machineA = "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA"
	machineB = "BBBBBBBB-2222-4222-8222-BBBBBBBBBBBB"
	sha1     = "0000000000000000000000000000000000000000000000000000000000000001"
	shaB     = "000000000000000000000000000000000000000000000000000000000000000b"
	shaC     = "000000000000000000000000000000000000000000000000000000000000000c"
	shaD     = "000000000000000000000000000000000000000000000000000000000000000d"
)

var (
	fixtureTime = time.Date(2026, 11, 1, 9, 30, 15, 0, time.UTC)
	now         = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
)

type item = map[string]types.AttributeValue

// expiresAfter gives each fixture row's ExpiresAfter (Unix seconds); rows not listed get one far in the future.
var expiresAfter = map[string]int64{
	"MachineRules#" + machineA + "|Binary#" + shaB: now.Add(48 * time.Hour).Unix(),
	"MachineRules#" + machineA + "|Binary#" + shaD: now.Add(-time.Hour).Unix(),
}

func loadRows(t *testing.T) []item {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "webcontract", "rows.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	out := make([]item, 0, len(rows))
	for _, r := range rows {
		key := r["PK"].(map[string]any)["S"].(string) + "|" + r["SK"].(map[string]any)["S"].(string)
		it := item{}
		for name, v := range r {
			it[name] = toAV(t, key, name, v)
		}
		out = append(out, it)
	}
	return out
}

func toAV(t *testing.T, key, name string, v any) types.AttributeValue {
	t.Helper()
	m := v.(map[string]any)
	for kind, val := range m {
		switch kind {
		case "S":
			s := val.(string)
			s = strings.ReplaceAll(s, "<timestamp>", fixtureTime.Format(time.RFC3339))
			return &types.AttributeValueMemberS{Value: s}
		case "N":
			n := val.(string)
			if n == "<number>" {
				if name == "ExpiresAfter" {
					if e, ok := expiresAfter[key]; ok {
						n = strconv.FormatInt(e, 10)
					} else {
						n = strconv.FormatInt(now.Add(90*24*time.Hour).Unix(), 10)
					}
				} else {
					n = strconv.FormatInt(fixtureTime.UnixMilli(), 10)
				}
			}
			return &types.AttributeValueMemberN{Value: n}
		case "BOOL":
			return &types.AttributeValueMemberBOOL{Value: val.(bool)}
		case "NULL":
			return &types.AttributeValueMemberNULL{Value: true}
		case "L":
			var l []types.AttributeValue
			for _, e := range val.([]any) {
				l = append(l, toAV(t, key, name, e))
			}
			return &types.AttributeValueMemberL{Value: l}
		case "M":
			mm := map[string]types.AttributeValue{}
			for k, e := range val.(map[string]any) {
				mm[k] = toAV(t, key, k, e)
			}
			return &types.AttributeValueMemberM{Value: mm}
		case "SS":
			var ss []string
			for _, e := range val.([]any) {
				ss = append(ss, e.(string))
			}
			return &types.AttributeValueMemberSS{Value: ss}
		}
	}
	t.Fatalf("%s %s: unsupported DynamoDB JSON %v", key, name, v)
	return nil
}

func rowsWithPK(t *testing.T, pk string) []item {
	t.Helper()
	var out []item
	for _, it := range loadRows(t) {
		if it["PK"].(*types.AttributeValueMemberS).Value == pk {
			out = append(out, it)
		}
	}
	if len(out) == 0 {
		t.Fatalf("fixture has no rows with PK %s", pk)
	}
	return out
}

func row(t *testing.T, pk, sk string) item {
	t.Helper()
	for _, it := range rowsWithPK(t, pk) {
		if it["SK"].(*types.AttributeValueMemberS).Value == sk {
			return it
		}
	}
	t.Fatalf("fixture has no row %s / %s", pk, sk)
	return nil
}

func clone(it item) item {
	out := item{}
	for k, v := range it {
		out[k] = v
	}
	return out
}

func n(v int) types.AttributeValue { return &types.AttributeValueMemberN{Value: strconv.Itoa(v)} }

func TestVendoredFixtureMatchesRudolph(t *testing.T) {
	dir := os.Getenv("RUDOLPH_DIR")
	if dir == "" {
		dir = filepath.Join("..", "..", "..", "..", "rudolph")
	}
	upstream, err := os.ReadFile(filepath.Join(dir, "testdata", "webcontract", "rows.json"))
	if err != nil {
		t.Skipf("Rudolph checkout not found (set RUDOLPH_DIR): %v", err)
	}
	vendored, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "webcontract", "rows.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(upstream, vendored) {
		t.Errorf("testdata/webcontract/rows.json differs from Rudolph's copy in %s: copy it over and check what changed", dir)
	}
}

func TestDecodeMachinePartition_TheFixtureMac(t *testing.T) {
	got, err := dynamo.DecodeMachinePartition(rowsWithPK(t, "Machine#"+machineA))
	if err != nil {
		t.Fatal(err)
	}
	want := store.Machine{
		MachineID:       machineA,
		SerialNum:       "C02AAAAAAAA1",
		Hostname:        "mac-aaaaaaaa.example.com",
		ModelIdentifier: "Mac15,7",
		OSVersion:       "15.6",
		OSBuild:         "24G84",
		SantaVersion:    "2026.8.0",
		ClientMode:      store.ClientModeMonitor,
		PrimaryUser:     "alice",
		SIPStatus:       "reduced (0x7f)",
		RuleCounts:      store.RuleCounts{Binary: 8, Certificate: 1, TeamID: 1, SigningID: 1, CDHash: 1},
		LastSeen:        fixtureTime,
	}
	if got.Machine != want {
		t.Errorf("machine:\n got %+v\nwant %+v", got.Machine, want)
	}
	if got.Sync == nil {
		t.Fatal("sync state missing")
	}
	wantSync := store.SyncState{PreflightAt: fixtureTime, PostflightAt: fixtureTime, LastSyncType: "NORMAL", RulesProcessed: 13}
	if *got.Sync != wantSync {
		t.Errorf("sync state:\n got %+v\nwant %+v", *got.Sync, wantSync)
	}
	if got.ConfiguredClientMode != "" {
		t.Errorf("a Mac without a machine config follows the global config, got configured mode %q", got.ConfiguredClientMode)
	}
}

// Sensor data expires after 90 days; a partition holding only a machine config is not a machine to show.
func TestDecodeMachinePartition_ConfigWithoutSensorDataIsNotFound(t *testing.T) {
	_, err := dynamo.DecodeMachinePartition(rowsWithPK(t, "Machine#"+machineB))
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("got %v, want store.ErrNotFound", err)
	}
	if _, err := dynamo.DecodeMachinePartition(nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("empty partition: got %v, want store.ErrNotFound", err)
	}
}

// The configured mode (machine config) is shown apart from the mode the Mac last reported (sensor data).
func TestDecodeMachinePartition_ConfiguredModeIsSeparateFromReported(t *testing.T) {
	current := clone(row(t, "Machine#"+machineA, "Current"))
	current["PK"] = &types.AttributeValueMemberS{Value: "Machine#" + machineB}
	current["MachineID"] = &types.AttributeValueMemberS{Value: machineB}
	items := append(rowsWithPK(t, "Machine#"+machineB), current)
	got, err := dynamo.DecodeMachinePartition(items)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientMode != store.ClientModeMonitor || got.ConfiguredClientMode != store.ClientModeLockdown {
		t.Errorf("reported %q, configured %q; want MONITOR reported and LOCKDOWN configured", got.ClientMode, got.ConfiguredClientMode)
	}
	if got.Sync != nil {
		t.Errorf("no sync state row, want Sync nil, got %+v", *got.Sync)
	}
}

// A machine config without ClientMode inherits the global one: shown as not configured.
func TestDecodeMachinePartition_ConfigWithoutClientMode(t *testing.T) {
	config := clone(row(t, "Machine#"+machineB, "Config"))
	delete(config, "ClientMode")
	config["PK"] = &types.AttributeValueMemberS{Value: "Machine#" + machineA}
	got, err := dynamo.DecodeMachinePartition(append(rowsWithPK(t, "Machine#"+machineA), config))
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfiguredClientMode != "" {
		t.Errorf("configured mode %q, want \"\" (inherits the global config)", got.ConfiguredClientMode)
	}
}

func TestDecodeMachinePartition_Encodings(t *testing.T) {
	cases := []struct {
		name   string
		change func(item)
		check  func(*testing.T, store.MachineDetail)
	}{
		{"SIP flags 0 mean every protection is on", func(it item) { it["SIPStatus"] = n(0) }, func(t *testing.T, m store.MachineDetail) {
			if m.SIPStatus != "enabled" {
				t.Errorf("SIPStatus %q, want enabled", m.SIPStatus)
			}
		}},
		{"some SIP protections off", func(it item) { it["SIPStatus"] = n(0x10) }, func(t *testing.T, m store.MachineDetail) {
			if m.SIPStatus != "reduced (0x10)" {
				t.Errorf("SIPStatus %q, want reduced (0x10)", m.SIPStatus)
			}
		}},
		{"no SIP status reported", func(it item) { delete(it, "SIPStatus") }, func(t *testing.T, m store.MachineDetail) {
			if m.SIPStatus != "" {
				t.Errorf("SIPStatus %q, want empty", m.SIPStatus)
			}
		}},
		{"lockdown", func(it item) { it["ClientMode"] = n(2) }, func(t *testing.T, m store.MachineDetail) {
			if m.ClientMode != store.ClientModeLockdown {
				t.Errorf("ClientMode %q", m.ClientMode)
			}
		}},
		{"standalone", func(it item) { it["ClientMode"] = n(3) }, func(t *testing.T, m store.MachineDetail) {
			if m.ClientMode != store.ClientModeStandalone {
				t.Errorf("ClientMode %q", m.ClientMode)
			}
		}},
		{"unknown client mode", func(it item) { it["ClientMode"] = n(9) }, func(t *testing.T, m store.MachineDetail) {
			if m.ClientMode != "UNKNOWN(9)" {
				t.Errorf("ClientMode %q, want UNKNOWN(9)", m.ClientMode)
			}
		}},
		{"no hostname or model (older Rudolph, serial-less Macs)", func(it item) {
			delete(it, "Hostname")
			delete(it, "ModelIdentifier")
			delete(it, "SerialNum")
		}, func(t *testing.T, m store.MachineDetail) {
			if m.Hostname != "" || m.ModelIdentifier != "" || m.SerialNum != "" || m.MachineID != machineA {
				t.Errorf("got %+v", m.Machine)
			}
		}},
		{"new attributes are tolerated", func(it item) {
			it["SomethingNew"] = &types.AttributeValueMemberS{Value: "x"}
			it["PrimaryUserGroups"] = &types.AttributeValueMemberL{Value: []types.AttributeValue{&types.AttributeValueMemberS{Value: "staff"}}}
		}, func(t *testing.T, m store.MachineDetail) {
			if m.PrimaryUser != "alice" {
				t.Errorf("got %+v", m.Machine)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			current := clone(row(t, "Machine#"+machineA, "Current"))
			c.change(current)
			got, err := dynamo.DecodeMachinePartition([]item{current})
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, got)
		})
	}
}

func TestDecodeMachinePartition_LastSyncTypes(t *testing.T) {
	for stored, want := range map[string]string{"normal": "NORMAL", "clean": "CLEAN", "clean_all": "CLEAN_ALL", "CLEAN": "CLEAN"} {
		state := clone(row(t, "Machine#"+machineA, "SyncState"))
		state["LastSyncType"] = &types.AttributeValueMemberS{Value: stored}
		got, err := dynamo.DecodeMachinePartition([]item{row(t, "Machine#"+machineA, "Current"), state})
		if err != nil {
			t.Fatal(err)
		}
		if got.Sync == nil || got.Sync.LastSyncType != want {
			t.Errorf("stored %q: got %+v, want %s", stored, got.Sync, want)
		}
	}
}

// New row kinds in the machine partition (Rudolph may add some) do not break the detail page.
func TestDecodeMachinePartition_IgnoresUnknownRows(t *testing.T) {
	extra := item{
		"PK": &types.AttributeValueMemberS{Value: "Machine#" + machineA},
		"SK": &types.AttributeValueMemberS{Value: "SomethingNew"},
		"X":  n(1),
	}
	got, err := dynamo.DecodeMachinePartition(append(rowsWithPK(t, "Machine#"+machineA), extra))
	if err != nil {
		t.Fatal(err)
	}
	if got.MachineID != machineA {
		t.Errorf("got %+v", got)
	}
}

func TestDecodeMachinePartition_WrongTypeIsAnErrorNotAPanic(t *testing.T) {
	current := clone(row(t, "Machine#"+machineA, "Current"))
	current["ClientMode"] = &types.AttributeValueMemberS{Value: "MONITOR"}
	if _, err := dynamo.DecodeMachinePartition([]item{current}); err == nil || errors.Is(err, store.ErrNotFound) {
		t.Errorf("ClientMode stored as a string: got %v, want a decoding error", err)
	}
}

func TestDecodeRule_GlobalRules(t *testing.T) {
	cases := map[string]store.Rule{
		"Binary#" + sha1: {RuleType: store.RuleTypeBinary, Policy: store.PolicyBlocklist, Identifier: sha1,
			CustomMessage: "Blocked", CustomURL: "https://it.example.com/%file_sha%", Description: "web contract", State: store.RuleStateInForce},
		"Cert#0000000000000000000000000000000000000000000000000000000000000007": {RuleType: store.RuleTypeCertificate, Policy: store.PolicyAllowlist,
			Identifier: "0000000000000000000000000000000000000000000000000000000000000007", Description: "web contract", State: store.RuleStateInForce},
		"SigningID#EQHXZ8M8AV:com.google.Chrome": {RuleType: store.RuleTypeSigningID, Policy: store.PolicyCEL, Identifier: "EQHXZ8M8AV:com.google.Chrome",
			CELExpr: "target.signing_time >= timestamp('2025-01-01T00:00:00Z')", Description: "web contract", State: store.RuleStateInForce},
		"TeamID#EQHXZ8M8AV": {RuleType: store.RuleTypeTeamID, Identifier: "EQHXZ8M8AV", Description: "web contract", State: store.RuleStateInForce},
		"CDHash#a1b2c3d4e5f60718293a4b5c6d7e8f9001122334": {RuleType: store.RuleTypeCDHash, Identifier: "a1b2c3d4e5f60718293a4b5c6d7e8f9001122334",
			Description: "web contract", State: store.RuleStateInForce},
	}
	for sk, want := range cases {
		t.Run(sk, func(t *testing.T) {
			it := row(t, "GlobalRules", sk)
			got, err := dynamo.DecodeRule(it, now)
			if err != nil {
				t.Fatal(err)
			}
			if want.Policy == "" {
				want.Policy = got.Policy // the fixture chooses these policies; checked by TestDecodeRule_Policies
			}
			if got != want {
				t.Errorf("\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

// Every global rule row in the fixture decodes, with a known rule type and policy.
func TestDecodeRule_EveryFixtureGlobalRule(t *testing.T) {
	for _, it := range rowsWithPK(t, "GlobalRules") {
		got, err := dynamo.DecodeRule(it, now)
		if err != nil {
			t.Errorf("%s: %v", it["SK"].(*types.AttributeValueMemberS).Value, err)
			continue
		}
		if strings.HasPrefix(string(got.RuleType), "UNKNOWN") || strings.HasPrefix(string(got.Policy), "UNKNOWN") || got.Identifier == "" {
			t.Errorf("%s decoded as %+v", it["SK"].(*types.AttributeValueMemberS).Value, got)
		}
	}
}

func TestDecodeRule_Policies(t *testing.T) {
	want := map[int]store.Policy{
		1: store.PolicyAllowlist, 2: store.PolicyBlocklist, 3: store.PolicySilentBlocklist, 4: store.PolicyRemove,
		5: store.PolicyAllowlistCompiler, 6: store.PolicyAllowlistTransitive, 7: store.PolicyCEL,
		8: store.PolicySilentGUIBlocklist, 9: store.PolicySilentTTYBlocklist, 0: "UNKNOWN(0)", 42: "UNKNOWN(42)",
	}
	for code, policy := range want {
		it := clone(row(t, "GlobalRules", "Binary#"+sha1))
		it["Policy"] = n(code)
		got, err := dynamo.DecodeRule(it, now)
		if err != nil {
			t.Fatalf("policy %d: %v", code, err)
		}
		if got.Policy != policy {
			t.Errorf("policy %d decoded as %q, want %q", code, got.Policy, policy)
		}
	}
}

func TestDecodeRule_RuleTypes(t *testing.T) {
	want := map[int]store.RuleType{
		1: store.RuleTypeBinary, 2: store.RuleTypeCertificate, 3: store.RuleTypeSigningID, 4: store.RuleTypeTeamID,
		5: store.RuleTypeCDHash, 9: "UNKNOWN(9)",
	}
	for code, ruleType := range want {
		it := clone(row(t, "GlobalRules", "Binary#"+sha1))
		it["RuleType"] = n(code)
		got, err := dynamo.DecodeRule(it, now)
		if err != nil {
			t.Fatalf("rule type %d: %v", code, err)
		}
		if got.RuleType != ruleType {
			t.Errorf("rule type %d decoded as %q, want %q", code, got.RuleType, ruleType)
		}
	}
}

func TestDecodeRule_MachineRuleStates(t *testing.T) {
	pk := "MachineRules#" + machineA
	cases := []struct {
		sk        string
		state     store.RuleState
		policy    store.Policy
		expiresAt time.Time
	}{
		// Marked for deletion: the Mac is being told to drop it; Policy shows what it falls back to.
		{"Binary#" + sha1, store.RuleStateRemoving, store.PolicyBlocklist, time.Unix(now.Add(90*24*time.Hour).Unix(), 0)},
		{"Binary#" + shaB, store.RuleStateInForce, store.PolicyAllowlist, time.Unix(now.Add(48*time.Hour).Unix(), 0)},
		{"Binary#" + shaC, store.RuleStateRemoving, store.PolicyRemove, time.Unix(now.Add(90*24*time.Hour).Unix(), 0)},
		{"Binary#" + shaD, store.RuleStateExpired, store.PolicyAllowlist, time.Unix(now.Add(-time.Hour).Unix(), 0)},
	}
	for _, c := range cases {
		t.Run(c.sk, func(t *testing.T) {
			got, err := dynamo.DecodeRule(row(t, pk, c.sk), now)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != c.state || got.Policy != c.policy || !got.ExpiresAt.Equal(c.expiresAt) {
				t.Errorf("got state %q policy %q expires %v; want %q %q %v", got.State, got.Policy, got.ExpiresAt, c.state, c.policy, c.expiresAt)
			}
		})
	}
}

func TestDecodeRule_ExpiryEdgeCases(t *testing.T) {
	base := row(t, "MachineRules#"+machineA, "Binary#"+shaB)
	cases := []struct {
		name      string
		change    func(item)
		state     store.RuleState
		expiresAt time.Time
	}{
		{"no expiry", func(it item) { delete(it, "ExpiresAfter") }, store.RuleStateInForce, time.Time{}},
		{"expiry 0 means none", func(it item) { it["ExpiresAfter"] = n(0) }, store.RuleStateInForce, time.Time{}},
		{"expires exactly now", func(it item) { it["ExpiresAfter"] = n(int(now.Unix())) }, store.RuleStateExpired, time.Unix(now.Unix(), 0)},
		{"REMOVE without DeleteOnNextSync", func(it item) { it["Policy"] = n(4) }, store.RuleStateRemoving, time.Unix(now.Add(48*time.Hour).Unix(), 0)},
		{"DeleteOnNextSync false", func(it item) { it["DeleteOnNextSync"] = &types.AttributeValueMemberBOOL{Value: false} }, store.RuleStateInForce, time.Unix(now.Add(48*time.Hour).Unix(), 0)},
		{"marked for deletion and expired", func(it item) {
			it["DeleteOnNextSync"] = &types.AttributeValueMemberBOOL{Value: true}
			it["ExpiresAfter"] = n(int(now.Add(-time.Hour).Unix()))
		}, store.RuleStateRemoving, time.Unix(now.Add(-time.Hour).Unix(), 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it := clone(base)
			c.change(it)
			got, err := dynamo.DecodeRule(it, now)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != c.state || !got.ExpiresAt.Equal(c.expiresAt) {
				t.Errorf("got state %q expires %v; want %q %v", got.State, got.ExpiresAt, c.state, c.expiresAt)
			}
		})
	}
}

// Rows written by the original Rudolph carry the identifier as SHA256.
func TestDecodeRule_LegacySHA256Attribute(t *testing.T) {
	it := clone(row(t, "GlobalRules", "Binary#"+sha1))
	delete(it, "Identifier")
	it["SHA256"] = &types.AttributeValueMemberS{Value: sha1}
	got, err := dynamo.DecodeRule(it, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Identifier != sha1 {
		t.Errorf("Identifier %q, want %s", got.Identifier, sha1)
	}
}

// Empty optional strings written by Rudolph (CELExpr "" and so on) decode as empty, not as an error.
func TestDecodeRule_EmptyOptionalFields(t *testing.T) {
	got, err := dynamo.DecodeRule(row(t, "MachineRules#"+machineA, "Binary#"+shaC), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.CELExpr != "" || got.CustomMessage != "" || got.CustomURL != "" || got.Identifier != shaC {
		t.Errorf("got %+v", got)
	}
}

func TestDecodeRule_MalformedRowsAreErrors(t *testing.T) {
	cases := map[string]func(item){
		"rule type as a string": func(it item) { it["RuleType"] = &types.AttributeValueMemberS{Value: "BINARY"} },
		"policy not a number":   func(it item) { it["Policy"] = &types.AttributeValueMemberN{Value: "two"} },
		"no rule type":          func(it item) { delete(it, "RuleType") },
		"no identifier at all":  func(it item) { delete(it, "Identifier") },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			it := clone(row(t, "GlobalRules", "Binary#"+sha1))
			change(it)
			if _, err := dynamo.DecodeRule(it, now); err == nil {
				t.Error("want an error")
			}
		})
	}
}

// The web layer shows every policy, including ones a rule list can hold but Rudolph never sends to Macs.
func TestPoliciesIncludeTransitive(t *testing.T) {
	found := false
	for _, p := range store.Policies {
		if p == store.PolicyAllowlistTransitive {
			found = true
		}
	}
	if !found {
		t.Error(fmt.Sprintf("store.Policies lacks %s", store.PolicyAllowlistTransitive))
	}
}
