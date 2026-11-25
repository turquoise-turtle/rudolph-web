//go:build integration

package dynamo_test

// The editable minimum Santa version (owner decisions, rudolph-web after Rudolph 05f9516). It is one stored value,
// Rudolph's: the attribute MinSantaVersion of the fleet health setting (PK FleetHealth, SK Config; Rudolph's
// pkg/model/fleethealth and testdata/webcontract/README.md), a release such as 2026.10, or "off". The web contract's
// rows hold the item with MinSantaVersion 2026.10, so newStore starts with it stored.
//
//	FleetHealthConfig(ctx) (store.FleetHealthConfig, error)
//	    still one GetItem; MinSantaVersion is what fleethealth.FromItem decodes: the stored release or "off", "" when the
//	    attribute is absent or holds a value Rudolph cannot use.
//	SetMinSantaVersion(ctx, actor, version string) error
//	    fleethealth.SetMinSantaVersion, which validates (fleethealth.ValidateMinSantaVersion: trimmed, OFF as off) and
//	    writes only that attribute, creating the item when there is none. A refusal is store.ErrInvalidArgument with
//	    Rudolph's message; nothing is written or audited.
//	ClearMinSantaVersion(ctx, actor) error
//	    fleethealth.ClearMinSantaVersion, removing only that attribute; store.ErrNothingToChange when none is stored
//	    (fleethealth.ErrNoMinSantaVersion), nothing written or audited and no item created.
//
// Each change is audited through Rudolph's audit package exactly as `rudolph fleet min-santa-version` records it
// (Rudolph's internal/cli/fleet/fleet.go): action fleet.min-santa-version, target fleet, by the web actor, with the
// summary "set the minimum Santa version to <v> (was <before>)", "turned the minimum Santa version check off (was
// <before>)" or "cleared the stored minimum Santa version (was <stored>); the default 2026.9 applies". <before> is the
// value in force before the change, "the default 2026.9" when none (or none usable) was stored; <stored> is the
// removed value, "an unusable value" when it could not be used. No Scan or Query.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/airbnb/rudolph/pkg/model/fleethealth"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

func deleteFleetHealthItem(t *testing.T) {
	t.Helper()
	_, err := ddblocal.Raw(t).DeleteItem(ctx, &awsdynamodb.DeleteItemInput{
		TableName: aws.String(ddblocal.TableName),
		Key:       map[string]types.AttributeValue{"PK": s("FleetHealth"), "SK": s("Config")},
	})
	if err != nil {
		t.Fatal(err)
	}
	ddblocal.ResetCounts()
}

func minVersionEntries(t *testing.T, st *dynamo.Store) []store.AuditEntry {
	t.Helper()
	entries, err := st.ListAudit(ctx, time.Now().Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEntry
	for _, e := range entries {
		if e.Action == "fleet.min-santa-version" {
			out = append(out, e)
		}
	}
	return out
}

func storedAttribute(t *testing.T) (string, bool) {
	t.Helper()
	v, ok := fleetHealthItem(t)["MinSantaVersion"]
	if !ok {
		return "", false
	}
	str, isString := v.(*types.AttributeValueMemberS)
	if !isString {
		t.Fatalf("MinSantaVersion is %T, want a string", v)
	}
	return str.Value, true
}

func TestFleetHealthConfig_ReadsTheStoredMinimum(t *testing.T) {
	st := newStore(t)
	got, err := st.FleetHealthConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.MinSantaVersion != "2026.10" {
		t.Errorf("the contract's item: MinSantaVersion %q, want 2026.10", got.MinSantaVersion)
	}
	if c := ddblocal.Counts(); c["GetItem"] != 1 || len(c) != 1 {
		t.Errorf("calls %v, want one GetItem", c)
	}

	for raw, want := range map[string]string{"off": "off", "2026.10.1": "2026.10.1", "nightly": "", "2026.10\u202e": "", "": ""} {
		putItem(t, map[string]types.AttributeValue{"PK": s("FleetHealth"), "SK": s("Config"), "StaleDays": n(14), "MinSantaVersion": s(raw)})
		got, err := st.FleetHealthConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got.MinSantaVersion != want || got.StaleDays != 14 {
			t.Errorf("stored %q: read %+v, want MinSantaVersion %q and the threshold of 14 days", raw, got, want)
		}
	}
	putItem(t, map[string]types.AttributeValue{"PK": s("FleetHealth"), "SK": s("Config"), "MinSantaVersion": n(2026)})
	if got, err := st.FleetHealthConfig(ctx); err != nil || got.MinSantaVersion != "" {
		t.Errorf("a number stored: %+v, %v; want it ignored, as Rudolph ignores it", got, err)
	}
}

func TestMinSantaVersion_SetOffAndClearThroughRudolph(t *testing.T) {
	st := newStore(t)
	until := holidayDay(20)
	putItem(t, map[string]types.AttributeValue{"PK": s("FleetHealth"), "SK": s("Config"), "StaleDays": n(14),
		"HolidayStaleDays": n(30), "HolidayUntil": s(until), "MinSantaVersion": s("2026.10")})
	ddblocal.ResetCounts()

	steps := []struct {
		name    string
		do      func() error
		stored  string // "" for no attribute
		summary string
	}{
		{"set", func() error { return st.SetMinSantaVersion(ctx, writerActor, " 2026.11 ") }, "2026.11",
			"set the minimum Santa version to 2026.11 (was 2026.10)"},
		{"off", func() error { return st.SetMinSantaVersion(ctx, writerActor, "Off") }, "off",
			"turned the minimum Santa version check off (was 2026.11)"},
		{"set from off", func() error { return st.SetMinSantaVersion(ctx, writerActor, "2026.12") }, "2026.12",
			"set the minimum Santa version to 2026.12 (was off)"},
		{"default", func() error { return st.ClearMinSantaVersion(ctx, writerActor) }, "",
			"cleared the stored minimum Santa version (was 2026.12); the default 2026.9 applies"},
		{"set from the default", func() error { return st.SetMinSantaVersion(ctx, writerActor, "2026.10.1") }, "2026.10.1",
			"set the minimum Santa version to 2026.10.1 (was the default 2026.9)"},
	}
	for i, step := range steps {
		if err := step.do(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		got, ok := storedAttribute(t)
		if (step.stored == "") == ok || got != step.stored {
			t.Errorf("%s: MinSantaVersion %q (present %v), want %q", step.name, got, ok, step.stored)
		}
		it := fleetHealthItem(t)
		if d, ok := it["StaleDays"].(*types.AttributeValueMemberN); !ok || d.Value != "14" {
			t.Errorf("%s: StaleDays %#v, want 14 kept", step.name, it["StaleDays"])
		}
		if d, ok := it["HolidayStaleDays"].(*types.AttributeValueMemberN); !ok || d.Value != "30" {
			t.Errorf("%s: HolidayStaleDays %#v, want 30 kept", step.name, it["HolidayStaleDays"])
		}
		if u, ok := it["HolidayUntil"].(*types.AttributeValueMemberS); !ok || u.Value != until {
			t.Errorf("%s: HolidayUntil %#v, want %s kept", step.name, it["HolidayUntil"], until)
		}
		entries := minVersionEntries(t, st)
		if len(entries) != i+1 {
			t.Fatalf("%s: %d fleet.min-santa-version entries, want %d", step.name, len(entries), i+1)
		}
		e := entries[0]
		if e.Target != "fleet" || e.Summary != step.summary || !strings.HasPrefix(e.Actor, "web:") || !strings.Contains(e.Actor, "admin@example.com") {
			t.Errorf("%s: audit entry %+v; want target fleet by the web actor, summary %q", step.name, e, step.summary)
		}
	}
	if c := ddblocal.Counts(); c["Scan"] != 0 || c["Query"] != 0 {
		t.Errorf("calls %v; the setting is read and written by key only", c)
	}
}

func TestMinSantaVersion_ClearWithNothingStored(t *testing.T) {
	st := newStore(t)
	deleteFleetHealthItem(t)
	if err := st.ClearMinSantaVersion(ctx, writerActor); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("no item: %v, want ErrNothingToChange", err)
	}
	if fleetHealthItem(t) != nil {
		t.Error("clearing with no item created one")
	}
	putItem(t, map[string]types.AttributeValue{"PK": s("FleetHealth"), "SK": s("Config"), "StaleDays": n(14)})
	if err := st.ClearMinSantaVersion(ctx, writerActor); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("an item without the attribute: %v, want ErrNothingToChange", err)
	}
	if entries := minVersionEntries(t, st); len(entries) != 0 {
		t.Errorf("clearing nothing was audited: %+v", entries)
	}

	// Setting with no item creates it.
	deleteFleetHealthItem(t)
	if err := st.SetMinSantaVersion(ctx, writerActor, "2026.10"); err != nil {
		t.Fatal(err)
	}
	if got, ok := storedAttribute(t); !ok || got != "2026.10" {
		t.Errorf("after setting with no item: %q, %v", got, ok)
	}
}

// A value edited in by hand that Rudolph cannot use is removed by clearing, and named as the CLI names it.
func TestMinSantaVersion_ClearAnUnusableValue(t *testing.T) {
	st := newStore(t)
	putItem(t, map[string]types.AttributeValue{"PK": s("FleetHealth"), "SK": s("Config"), "MinSantaVersion": s("nightly")})
	if err := st.ClearMinSantaVersion(ctx, writerActor); err != nil {
		t.Fatal(err)
	}
	if _, ok := storedAttribute(t); ok {
		t.Error("the unusable value is still stored")
	}
	want := "cleared the stored minimum Santa version (was an unusable value); the default 2026.9 applies"
	if entries := minVersionEntries(t, st); len(entries) != 1 || entries[0].Summary != want {
		t.Errorf("audit %+v; want one entry %q", entries, want)
	}
}

// The refusals are Rudolph's (fleethealth.ValidateMinSantaVersion), message and all.
func TestMinSantaVersion_RefusesWhatTheCLIRefuses(t *testing.T) {
	st := newStore(t)
	for _, bad := range []string{"", "latest", "2026", "9", "2026.x", "v2026.10", "2026.10\u202e", "2026.10\u200b",
		"2026.10\x1b[2J", "2026." + strings.Repeat("1", 30)} {
		_, rudolph := fleethealth.ValidateMinSantaVersion(bad)
		if rudolph == nil {
			t.Fatalf("Rudolph accepts %q; pick another refused value", bad)
		}
		err := st.SetMinSantaVersion(ctx, writerActor, bad)
		if !errors.Is(err, store.ErrInvalidArgument) || !strings.Contains(err.Error(), rudolph.Error()) {
			t.Errorf("%q: %v; want ErrInvalidArgument with Rudolph's message %q", bad, err, rudolph)
		}
	}
	if err := st.SetMinSantaVersion(ctx, store.Actor{}, "2026.11"); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("no actor: %v, want ErrInvalidArgument", err)
	}
	if err := st.ClearMinSantaVersion(ctx, store.Actor{}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("no actor clearing: %v, want ErrInvalidArgument", err)
	}
	if got, ok := storedAttribute(t); !ok || got != "2026.10" {
		t.Errorf("after refusals: %q (present %v), want the contract's 2026.10 untouched", got, ok)
	}
	if entries := minVersionEntries(t, st); len(entries) != 0 {
		t.Errorf("refusals were audited: %+v", entries)
	}
}

// Setting a holiday or ending one leaves the minimum as it is.
func TestMinSantaVersion_HolidayLeavesIt(t *testing.T) {
	st := newStore(t)
	if err := st.SetFleetHoliday(ctx, writerActor, 30, holidayDay(5)); err != nil {
		t.Fatal(err)
	}
	if err := st.EndFleetHoliday(ctx, writerActor); err != nil {
		t.Fatal(err)
	}
	if got, ok := storedAttribute(t); !ok || got != "2026.10" {
		t.Errorf("after a holiday: %q (present %v), want the contract's 2026.10", got, ok)
	}
	if got, err := st.FleetHealthConfig(ctx); err != nil || got.MinSantaVersion != "2026.10" {
		t.Errorf("FleetHealthConfig after a holiday: %+v, %v", got, err)
	}
}
