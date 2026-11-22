package dynamo_test

// Decoding the fleet job's rows (testdata/webcontract/fleet-rows.json, vendored from Rudolph 6cdd274 or later; layout in
// Rudolph's docs/events-table.md "Fleet health and readiness rows") and the unknown marker eventupload writes
// (UnknownSeen#<machine> / <sha256> in rows.json):
//
//	dynamo.DecodeFleetSummary(item) (store.FleetSummary, error)       the pointer, FleetPointer / Current
//	dynamo.DecodeFleetHealthRow(item) (store.FleetHealthRow, error)   a row of any health list
//	dynamo.DecodeReadinessRow(item) (store.ReadinessRow, error)       a readiness row; the bucket is the sort key's first digit
//	dynamo.DecodeUnknownBinary(item) (store.UnknownBinary, error)     an unknown marker
//
// Modes in fleet rows are names (MONITOR), not Rudolph's numbers. A Locked readiness row has no unknown counts (the
// job does not read a locked Mac's markers): Counted is false there and true on every other row.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
)

const (
	fleetSnapshot = "20261122T120000.000000000Z-0a1b2c3d"
	contractFew   = "00000001-1111-4111-8111-000000000001"
	contractStale = "00000002-1111-4111-8111-000000000002"
)

var fleetGenerated = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

// loadFleetRows reads fleet-rows.json with the snapshot ID put in place of <snapshot>.
func loadFleetRows(t *testing.T, snapshot string) []item {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "webcontract", "fleet-rows.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("<snapshot>"), []byte(snapshot))
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

func fleetRow(t *testing.T, pk, sk string) item {
	t.Helper()
	for _, it := range loadFleetRows(t, fleetSnapshot) {
		if it["PK"].(*types.AttributeValueMemberS).Value == pk && it["SK"].(*types.AttributeValueMemberS).Value == sk {
			return it
		}
	}
	t.Fatalf("fleet-rows.json has no row %s / %s", pk, sk)
	return nil
}

func TestVendoredFleetFixtureMatchesRudolph(t *testing.T) {
	dir := os.Getenv("RUDOLPH_DIR")
	if dir == "" {
		dir = filepath.Join("..", "..", "..", "..", "rudolph")
	}
	upstream, err := os.ReadFile(filepath.Join(dir, "testdata", "webcontract", "fleet-rows.json"))
	if err != nil {
		t.Skipf("Rudolph checkout not found (set RUDOLPH_DIR): %v", err)
	}
	vendored, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "webcontract", "fleet-rows.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(upstream, vendored) {
		t.Errorf("testdata/webcontract/fleet-rows.json differs from Rudolph's copy in %s: copy it over and check what changed", dir)
	}
}

func TestDecodeFleetSummary_TheContractPointer(t *testing.T) {
	got, err := dynamo.DecodeFleetSummary(fleetRow(t, "FleetPointer", "Current"))
	if err != nil {
		t.Fatal(err)
	}
	want := store.FleetSummary{
		Snapshot:    fleetSnapshot,
		GeneratedAt: fleetGenerated,
		Macs:        2,
		Attention:   1,
		Health: map[store.FleetCategory]int{
			store.FleetCategoryStale: 1, store.FleetCategoryIncomplete: 0, store.FleetCategoryMode: 0,
			store.FleetCategoryRules: 0, store.FleetCategorySIP: 0, store.FleetCategoryVersion: 0,
		},
		VersionUnknown:  0,
		MinSantaVersion: "2026.9",
		StaleDays:       10,
		Holiday:         false,
		HolidayUntil:    "",
		ReadinessAll:    store.ReadinessCounts{Macs: 2, Few: 1, Unmeasured: 1},
		ReadinessGroups: map[string]store.ReadinessCounts{
			"eng":                {Macs: 1, Few: 1},
			store.UngroupedGroup: {Macs: 1, Unmeasured: 1},
		},
		Capped: 0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pointer:\n got %+v\nwant %+v", got, want)
	}
}

func TestDecodeFleetSummary_Holiday(t *testing.T) {
	it := clone(fleetRow(t, "FleetPointer", "Current"))
	it["StaleDays"] = n(60)
	it["Holiday"] = &types.AttributeValueMemberBOOL{Value: true}
	it["HolidayUntil"] = &types.AttributeValueMemberS{Value: "2027-02-01"}
	got, err := dynamo.DecodeFleetSummary(it)
	if err != nil {
		t.Fatal(err)
	}
	if got.StaleDays != 60 || !got.Holiday || got.HolidayUntil != "2027-02-01" {
		t.Errorf("holiday pointer: StaleDays %d, Holiday %v, HolidayUntil %q", got.StaleDays, got.Holiday, got.HolidayUntil)
	}
}

func TestDecodeFleetSummary_NeedsSnapshotAndGeneratedAt(t *testing.T) {
	for _, attr := range []string{"Snapshot", "GeneratedAt"} {
		it := clone(fleetRow(t, "FleetPointer", "Current"))
		delete(it, attr)
		if _, err := dynamo.DecodeFleetSummary(it); err == nil {
			t.Errorf("a pointer without %s decoded", attr)
		}
	}
}

func TestDecodeFleetHealthRow_TheContractStaleRow(t *testing.T) {
	got, err := dynamo.DecodeFleetHealthRow(fleetRow(t, "FleetSnapshot#"+fleetSnapshot+"#Health#Stale", "20261102T120100Z#"+contractStale))
	if err != nil {
		t.Fatal(err)
	}
	want := store.FleetHealthRow{
		MachineID:     contractStale,
		Hostname:      "contract-stale",
		SerialNum:     "C02STALE",
		LastSeen:      time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
		LastCompleted: time.Date(2026, 11, 2, 12, 1, 0, 0, time.UTC),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stale row:\n got %+v\nwant %+v", got, want)
	}
}

// One row of each other category, as docs/events-table.md lists their attributes.
func TestDecodeFleetHealthRow_EveryCategorysDetail(t *testing.T) {
	s := func(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
	base := func(category, sk string) item {
		return item{"PK": s("FleetSnapshot#" + fleetSnapshot + "#Health#" + category), "SK": s(sk), "MachineID": s(machineA),
			"LastSeen": s("2026-11-21T08:00:00Z"), "ExpiresAfter": n(1795608000)}
	}
	cases := map[string]struct {
		it   item
		want store.FleetHealthRow
	}{
		"never completed": {
			it:   with(base("Stale", "00000000T000000Z#"+machineA), "Never", &types.AttributeValueMemberBOOL{Value: true}),
			want: store.FleetHealthRow{Never: true},
		},
		"incomplete": {
			it: with(with(base("Incomplete", "20261121T080000Z#"+machineA), "SyncsWithoutPostflight", n(4)),
				"Never", &types.AttributeValueMemberBOOL{Value: false}),
			want: store.FleetHealthRow{SyncsWithoutPostflight: 4},
		},
		"mode": {
			it:   with(with(with(base("Mode", machineA), "Reported", s("MONITOR")), "Intended", s("LOCKDOWN")), "Source", s("group:pilot")),
			want: store.FleetHealthRow{Reported: store.ClientModeMonitor, Intended: store.ClientModeLockdown, Source: "group:pilot"},
		},
		"rules": {
			it: with(with(with(with(with(base("Rules", machineA), "Reason", s("Outlier")), "RulesReceived", n(12)), "RulesProcessed", n(12)),
				"Hash", s("abc")), "ExpectedHash", s("def")),
			want: store.FleetHealthRow{Reason: "Outlier", RulesReceived: 12, RulesProcessed: 12, Hash: "abc", ExpectedHash: "def"},
		},
		"sip": {
			it:   with(base("SIP", machineA), "Flags", s("0x7f")),
			want: store.FleetHealthRow{Flags: "0x7f"},
		},
		"version": {
			it:   with(base("Version", "2026.08#"+machineA), "SantaVersion", s("2026.8.1")),
			want: store.FleetHealthRow{SantaVersion: "2026.8.1"},
		},
	}
	for name, c := range cases {
		got, err := dynamo.DecodeFleetHealthRow(c.it)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		c.want.MachineID = machineA
		c.want.LastSeen = time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, c.want)
		}
	}
	if _, err := dynamo.DecodeFleetHealthRow(item{"PK": s("FleetSnapshot#x#Health#SIP"), "SK": s("y")}); err == nil {
		t.Error("a health row without a MachineID decoded")
	}
}

func with(it item, name string, v types.AttributeValue) item {
	out := clone(it)
	out[name] = v
	return out
}

func TestDecodeReadinessRow_TheContractRows(t *testing.T) {
	few, err := dynamo.DecodeReadinessRow(fleetRow(t, "FleetSnapshot#"+fleetSnapshot+"#Ready#All", "1#000001#"+contractFew))
	if err != nil {
		t.Fatal(err)
	}
	want := store.ReadinessRow{
		MachineID: contractFew, Hostname: "contract-few", SerialNum: "C02FEW", Bucket: store.ReadinessFew, Counted: true,
		Unknown30: 1, Pending30: 1, Unknown7: 1, Pending7: 1, ReportedMode: store.ClientModeMonitor, IntendedMode: store.ClientModeMonitor,
		ObservedDays: 40,
	}
	if !reflect.DeepEqual(few, want) {
		t.Errorf("few row:\n got %+v\nwant %+v", few, want)
	}

	stale, err := dynamo.DecodeReadinessRow(fleetRow(t, "FleetSnapshot#"+fleetSnapshot+"#Ready#Group#rudolph:ungrouped", "3#000000#"+contractStale))
	if err != nil {
		t.Fatal(err)
	}
	if stale.Bucket != store.ReadinessUnmeasured || stale.Why != "stale" || !stale.Counted || stale.MachineID != contractStale {
		t.Errorf("unmeasured row %+v; want bucket unmeasured, Why stale", stale)
	}
}

func TestDecodeReadinessRow_LockedAndCapped(t *testing.T) {
	s := func(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
	locked := item{"PK": s("FleetSnapshot#" + fleetSnapshot + "#Ready#All"), "SK": s("4#000000#" + machineA), "MachineID": s(machineA),
		"ReportedMode": s("LOCKDOWN"), "IntendedMode": s("LOCKDOWN"), "ObservedDays": n(3), "Capped": &types.AttributeValueMemberBOOL{Value: false}}
	got, err := dynamo.DecodeReadinessRow(locked)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bucket != store.ReadinessLocked || got.Counted || got.IntendedMode != store.ClientModeLockdown {
		t.Errorf("locked row %+v; want bucket locked, not counted", got)
	}

	capped := clone(locked)
	capped["SK"] = s("2#000412#" + machineA)
	capped["Capped"] = &types.AttributeValueMemberBOOL{Value: true}
	for _, a := range []string{"Unknown30", "Pending30", "Unknown7", "Pending7"} {
		capped[a] = n(412)
	}
	got, err = dynamo.DecodeReadinessRow(capped)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bucket != store.ReadinessMany || !got.Capped || !got.Counted || got.Pending30 != 412 {
		t.Errorf("capped row %+v; want bucket many, capped, counted", got)
	}

	for name, sk := range map[string]string{"bucket 7": "7#000000#" + machineA, "no bucket": machineA, "empty": ""} {
		bad := clone(locked)
		bad["SK"] = s(sk)
		if _, err := dynamo.DecodeReadinessRow(bad); err == nil {
			t.Errorf("%s: sort key %q decoded", name, sk)
		}
	}
}

func TestDecodeUnknownBinary_TheContractMarker(t *testing.T) {
	sha := "0000000000000000000000000000000000000000000000000000000000000015"
	got, err := dynamo.DecodeUnknownBinary(row(t, "UnknownSeen#"+machineA, sha))
	if err != nil {
		t.Fatal(err)
	}
	want := store.UnknownBinary{
		FileSHA256: sha,
		FileName:   "tool",
		SigningID:  "EQHXZ8M8AV:com.google.Chrome",
		TeamID:     "EQHXZ8M8AV",
		LastDay:    time.Date(fixtureTime.Year(), fixtureTime.Month(), fixtureTime.Day(), 0, 0, 0, 0, time.UTC),
	}
	if got != want {
		t.Errorf("marker:\n got %+v\nwant %+v", got, want)
	}
}
