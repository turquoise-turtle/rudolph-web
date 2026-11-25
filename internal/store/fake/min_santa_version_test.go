package fake_test

// The fake's minimum Santa version, which the web tests and the local demo use (owner decisions for the editable
// minimum Santa version). It behaves as the DynamoDB store does (internal/store/dynamo/min_santa_version_integration_test.go):
//
//   - Test setup: SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: v}) stores v as the raw attribute.
//     FleetHealthConfig reads it back as Rudolph's fleethealth.FromItem would: a value ValidateMinSantaVersion refuses
//     (edited by hand) reads as "", as if none were stored.
//   - SetMinSantaVersion(ctx, actor, version) validates with fleethealth.ValidateMinSantaVersion and stores what it
//     returns (trimmed; OFF as off). A refusal is store.ErrInvalidArgument with Rudolph's message, nothing stored or
//     audited. The stale threshold and the holiday are left as they are.
//   - ClearMinSantaVersion(ctx, actor) removes the stored value, usable or not; store.ErrNothingToChange, with nothing
//     audited, when none is stored.
//   - Each change records Rudolph's CLI audit entry exactly (internal/cli/fleet/fleet.go in Rudolph): action
//     fleet.min-santa-version, target fleet, and the summary
//       "set the minimum Santa version to <v> (was <before>)"
//       "turned the minimum Santa version check off (was <before>)"
//       "cleared the stored minimum Santa version (was <stored>); the default 2026.9 applies"
//     where <before> is the value in force before the change, or "the default 2026.9" when none was stored, and
//     <stored> is the value removed, or "an unusable value" when what was stored could not be used.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func minVersionAudit(t *testing.T, s *fake.Store) []store.AuditEntry {
	t.Helper()
	entries, err := s.ListAudit(ctx, time.Now().Add(-time.Hour), 100)
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

func storedMinVersion(t *testing.T, s *fake.Store) string {
	t.Helper()
	cfg, err := s.FleetHealthConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.MinSantaVersion
}

func TestMinSantaVersion_SetOffAndClearAsTheCLI(t *testing.T) {
	s := fake.New()
	if got := storedMinVersion(t, s); got != "" {
		t.Fatalf("a new store holds %q, want nothing stored", got)
	}

	steps := []struct {
		name    string
		do      func() error
		stored  string
		summary string
	}{
		{"set from the default", func() error { return s.SetMinSantaVersion(ctx, fleetActor, " 2026.10 ") }, "2026.10",
			"set the minimum Santa version to 2026.10 (was the default 2026.9)"},
		{"turn off", func() error { return s.SetMinSantaVersion(ctx, fleetActor, "OFF") }, "off",
			"turned the minimum Santa version check off (was 2026.10)"},
		{"set from off", func() error { return s.SetMinSantaVersion(ctx, fleetActor, "2026.11.1") }, "2026.11.1",
			"set the minimum Santa version to 2026.11.1 (was off)"},
		{"set the same again", func() error { return s.SetMinSantaVersion(ctx, fleetActor, "2026.11.1") }, "2026.11.1",
			"set the minimum Santa version to 2026.11.1 (was 2026.11.1)"},
		{"back to the default", func() error { return s.ClearMinSantaVersion(ctx, fleetActor) }, "",
			"cleared the stored minimum Santa version (was 2026.11.1); the default 2026.9 applies"},
	}
	for i, step := range steps {
		if err := step.do(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got := storedMinVersion(t, s); got != step.stored {
			t.Errorf("%s: stored %q, want %q", step.name, got, step.stored)
		}
		entries := minVersionAudit(t, s)
		if len(entries) != i+1 {
			t.Fatalf("%s: %d fleet.min-santa-version entries, want %d", step.name, len(entries), i+1)
		}
		e := entries[0]
		if e.Target != "fleet" || e.Summary != step.summary || !strings.HasPrefix(e.Actor, "web:") || !strings.Contains(e.Actor, "admin@example.com") {
			t.Errorf("%s: audit entry %+v; want target fleet, by the web actor, summary %q", step.name, e, step.summary)
		}
	}

	if err := s.ClearMinSantaVersion(ctx, fleetActor); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("clearing with nothing stored: %v, want ErrNothingToChange", err)
	}
	if n := len(minVersionAudit(t, s)); n != len(steps) {
		t.Errorf("clearing with nothing stored was audited (%d entries)", n)
	}
}

func TestMinSantaVersion_RefusesWhatRudolphRefuses(t *testing.T) {
	s := fake.New()
	s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: "2026.10"})
	for _, bad := range []string{"", "  ", "latest", "2026", "9", "2026.x", "v2026.10", "2026.10\u202e", "2026.10\u200b",
		"2026." + strings.Repeat("1", 30)} {
		err := s.SetMinSantaVersion(ctx, fleetActor, bad)
		if !errors.Is(err, store.ErrInvalidArgument) || !strings.Contains(err.Error(), "must be a release such as 2026.10") {
			t.Errorf("%q: %v; want ErrInvalidArgument with Rudolph's message", bad, err)
		}
	}
	if err := s.SetMinSantaVersion(ctx, store.Actor{}, "2026.11"); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("no actor: %v, want ErrInvalidArgument", err)
	}
	if err := s.ClearMinSantaVersion(ctx, store.Actor{}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("no actor clearing: %v, want ErrInvalidArgument", err)
	}
	if got := storedMinVersion(t, s); got != "2026.10" {
		t.Errorf("after refusals the store holds %q, want 2026.10 untouched", got)
	}
	if n := len(minVersionAudit(t, s)); n != 0 {
		t.Errorf("refusals were audited (%d entries)", n)
	}
}

// A value edited into the item by hand that Rudolph cannot use counts as none stored when read, and clearing removes
// it, naming it as Rudolph's CLI does.
func TestMinSantaVersion_UnusableStoredValue(t *testing.T) {
	s := fake.New()
	s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: "nightly"})
	if got := storedMinVersion(t, s); got != "" {
		t.Errorf("an unusable stored value reads as %q, want \"\" (the default applies)", got)
	}
	if err := s.ClearMinSantaVersion(ctx, fleetActor); err != nil {
		t.Fatalf("clearing an unusable value: %v", err)
	}
	entries := minVersionAudit(t, s)
	if want := "cleared the stored minimum Santa version (was an unusable value); the default 2026.9 applies"; len(entries) != 1 || entries[0].Summary != want {
		t.Errorf("audit %+v; want one entry %q", entries, want)
	}
	if err := s.ClearMinSantaVersion(ctx, fleetActor); !errors.Is(err, store.ErrNothingToChange) {
		t.Errorf("clearing again: %v, want ErrNothingToChange", err)
	}

	s = fake.New()
	s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: "nightly"})
	if err := s.SetMinSantaVersion(ctx, fleetActor, "2026.10"); err != nil {
		t.Fatal(err)
	}
	if entries := minVersionAudit(t, s); len(entries) != 1 || entries[0].Summary != "set the minimum Santa version to 2026.10 (was the default 2026.9)" {
		t.Errorf("replacing an unusable value: audit %+v; the value in force before was the default", entries)
	}
}

func TestMinSantaVersion_LeavesTheThresholdAndHolidayAlone(t *testing.T) {
	s := fake.New()
	until := time.Now().UTC().AddDate(0, 0, 20).Format("2006-01-02")
	s.SetFleetHealthConfig(store.FleetHealthConfig{StaleDays: 14, HolidayStaleDays: 30, HolidayUntil: until})
	want := store.FleetHealthConfig{StaleDays: 14, HolidayStaleDays: 30, HolidayUntil: until, MinSantaVersion: "2026.10"}
	if err := s.SetMinSantaVersion(ctx, fleetActor, "2026.10"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := s.FleetHealthConfig(ctx); cfg != want {
		t.Errorf("after setting: %+v, want %+v", cfg, want)
	}
	if err := s.EndFleetHoliday(ctx, fleetActor); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := s.FleetHealthConfig(ctx); cfg.MinSantaVersion != "2026.10" {
		t.Errorf("ending the holiday changed the minimum: %+v", cfg)
	}
	if err := s.ClearMinSantaVersion(ctx, fleetActor); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := s.FleetHealthConfig(ctx); cfg != (store.FleetHealthConfig{StaleDays: 14}) {
		t.Errorf("after clearing: %+v, want the threshold of 14 days kept", cfg)
	}
}

func TestMinSantaVersion_StoreErrors(t *testing.T) {
	s := fake.New()
	boom := errors.New("boom")
	s.SetError(boom)
	if err := s.SetMinSantaVersion(ctx, fleetActor, "2026.10"); !errors.Is(err, boom) {
		t.Errorf("set with a failing store: %v, want its error", err)
	}
	if err := s.ClearMinSantaVersion(ctx, fleetActor); !errors.Is(err, boom) {
		t.Errorf("clear with a failing store: %v, want its error", err)
	}
}

// The local demo (cmd/local) starts with the fleet job's minimum and the value in force agreeing, so /fleet shows no
// version warning, and its minimum can be changed like any store's.
func TestMinSantaVersion_Demo(t *testing.T) {
	now := time.Now()
	s := fake.NewDemo(now)
	cfg, err := s.FleetHealthConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := s.FleetSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inForce, _, _ := cfg.MinSantaVersionInForce()
	if inForce == "" || sum.MinSantaVersion != inForce {
		t.Errorf("demo: the snapshot's minimum %q, in force %q; want them equal", sum.MinSantaVersion, inForce)
	}
	if err := s.SetMinSantaVersion(ctx, fleetActor, "2026.10"); err != nil {
		t.Fatalf("demo: %v", err)
	}
	if got := storedMinVersion(t, s); got != "2026.10" {
		t.Errorf("demo stores %q, want 2026.10", got)
	}
}
