package web_test

// When a Mac's machine config sets no client mode, the machine page names the mode the global config sets instead of
// only saying the global config applies (owner feedback from the first production deployment), and explains the
// difference between the reported and the configured client mode.

import (
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func TestMachinePage_NamesTheGlobalMode(t *testing.T) {
	body := healthApp(t, fixedNow.Add(-time.Minute), healthy(fixedNow.Add(-time.Minute)), func(s *fake.Store) {
		s.SetGlobalClientMode(store.ClientModeLockdown)
	})
	mustContain(t, body, "LOCKDOWN (from the global config)")
	mustNotContain(t, body, "not set: global config applies")
}

// Rudolph sends MONITOR when there is no global config row; the fake store starts that way.
func TestMachinePage_GlobalModeDefaultsToMonitor(t *testing.T) {
	body := healthApp(t, fixedNow.Add(-time.Minute), healthy(fixedNow.Add(-time.Minute)), nil)
	mustContain(t, body, "MONITOR (from the global config)")
}

func TestMachinePage_MachineModeIsNotAttributedToTheGlobalConfig(t *testing.T) {
	body := healthApp(t, fixedNow.Add(-time.Minute), healthy(fixedNow.Add(-time.Minute)), func(s *fake.Store) {
		s.SetGlobalClientMode(store.ClientModeMonitor)
		s.SetConfiguredClientMode("m-h", store.ClientModeLockdown)
	})
	mustNotContain(t, body, "(from the global config)")
}

func TestMachinePage_TemporaryModeNamesTheGlobalModeItReturnsTo(t *testing.T) {
	body := healthApp(t, fixedNow.Add(-time.Minute), healthy(fixedNow.Add(-time.Minute)), func(s *fake.Store) {
		s.SetGlobalClientMode(store.ClientModeLockdown)
		s.SetTemporaryClientMode("m-h", store.ClientModeMonitor, fixedNow.Add(90*time.Minute))
	})
	mustContain(t, body, "then LOCKDOWN (global config)", "returns to LOCKDOWN (the global config")
	mustNotContain(t, body, "then the global config", "returns to the global config")
}

func TestMachinePage_ExplainsReportedAndConfiguredMode(t *testing.T) {
	body := healthApp(t, fixedNow.Add(-time.Minute), healthy(fixedNow.Add(-time.Minute)), nil)
	mustContain(t, body, "at its last preflight", "what Rudolph sends")
}
