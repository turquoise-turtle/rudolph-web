package web_test

// Sync health on the machine page: signs that a Mac is not syncing as it should, worked out from what Rudolph
// stores, against the time the page is shown. The page lists each problem found in a "Sync health" section, or says
// none were found.
//   - Not seen for more than a day (sensor data Time).
//   - The last sync did not report completion: its postflight is missing or older than its preflight, 30 minutes or
//     more after the preflight (Santa sends postflight seconds after preflight).
//   - A clean sync is requested and has not happened yet (machine config CleanSync).
//   - A temporary client mode: running ("until" its end, and what the Mac returns to) or ended (and ignored). While one
//     runs the configured mode line does not claim the global config applies.

import (
	"net/http"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func healthApp(t *testing.T, seen time.Time, sync *store.SyncState, setup func(*fake.Store)) string {
	t.Helper()
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "m-h", SerialNum: "C02H", Hostname: "h.example.com", ClientMode: store.ClientModeLockdown, LastSeen: seen})
	if sync != nil {
		s.SetSyncState("m-h", *sync)
	}
	if setup != nil {
		setup(s)
	}
	a := newApp(t, s, 10)
	code, body := a.viewerGet(t, "/machines/m-h")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	return body
}

func healthy(at time.Time) *store.SyncState {
	return &store.SyncState{PreflightAt: at, PostflightAt: at.Add(5 * time.Second), LastSyncType: "NORMAL"}
}

func TestHealth_HealthyMac(t *testing.T) {
	body := healthApp(t, fixedNow.Add(-5*time.Minute), healthy(fixedNow.Add(-5*time.Minute)), nil)
	mustContain(t, body, "Sync health", "No problems found")
	mustNotContain(t, body, "Not seen for", "did not report completion", "Clean sync requested", "Temporary")
}

func TestHealth_NotSeenForMoreThanADay(t *testing.T) {
	body := healthApp(t, fixedNow.Add(-50*time.Hour), healthy(fixedNow.Add(-50*time.Hour)), nil)
	mustContain(t, body, "Not seen for 2 days")
	mustNotContain(t, body, "No problems found")
}

func TestHealth_LastSyncDidNotReportCompletion(t *testing.T) {
	pre := fixedNow.Add(-2 * time.Hour)
	for name, sync := range map[string]*store.SyncState{
		"postflight older":   {PreflightAt: pre, PostflightAt: pre.Add(-24 * time.Hour), LastSyncType: "NORMAL"},
		"postflight missing": {PreflightAt: pre, LastSyncType: "NORMAL"},
	} {
		t.Run(name, func(t *testing.T) {
			body := healthApp(t, pre, sync, nil)
			mustContain(t, body, "did not report completion")
		})
	}
	// A sync still in progress is not a problem yet.
	body := healthApp(t, fixedNow.Add(-time.Minute), &store.SyncState{PreflightAt: fixedNow.Add(-time.Minute), PostflightAt: fixedNow.Add(-11 * time.Minute)}, nil)
	mustNotContain(t, body, "did not report completion")
}

func TestHealth_CleanSyncRequested(t *testing.T) {
	body := healthApp(t, fixedNow.Add(-time.Minute), healthy(fixedNow.Add(-time.Minute)), func(s *fake.Store) { s.SetCleanSyncRequested("m-h", true) })
	mustContain(t, body, "Clean sync requested")
}

func TestHealth_TemporaryClientMode(t *testing.T) {
	until := fixedNow.Add(90 * time.Minute)
	body := healthApp(t, fixedNow.Add(-time.Minute), healthy(fixedNow.Add(-time.Minute)), func(s *fake.Store) {
		s.SetTemporaryClientMode("m-h", store.ClientModeMonitor, until)
	})
	mustContain(t, body, "Temporary MONITOR until", until.UTC().Format("2006-01-02 15:04:05 UTC"))
	mustNotContain(t, body, "not set: global config applies")

	ended := fixedNow.Add(-time.Hour)
	body = healthApp(t, fixedNow.Add(-time.Minute), healthy(fixedNow.Add(-time.Minute)), func(s *fake.Store) {
		s.SetTemporaryClientMode("m-h", store.ClientModeMonitor, ended)
	})
	mustContain(t, body, "Temporary MONITOR ended")
	mustNotContain(t, body, "Temporary MONITOR until")
}

// Values come from Macs and config rows: the health section escapes them like the rest of the page.
func TestHealth_IsEscaped(t *testing.T) {
	body := healthApp(t, fixedNow.Add(-50*time.Hour), healthy(fixedNow.Add(-50*time.Hour)), func(s *fake.Store) {
		s.SetTemporaryClientMode("m-h", store.ClientMode(`<script>alert(1)</script>`), fixedNow.Add(time.Hour))
	})
	mustNotContain(t, body, "<script>alert(1)</script>")
}
