package web_test

// Times on pages are shown in UTC with the time in the configured display time zone beside them (owner feedback
// from the first production deployment). Without a display time zone, or with UTC, only UTC is shown.

import (
	"net/http"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/fakeidp"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

func newAppInZone(t *testing.T, s *fake.Store, zone *time.Location) *app {
	t.Helper()
	idp := fakeidp.New(t, tenantID, clientID, "secret")
	a, err := auth.New(auth.Config{
		Issuer:       idp.Issuer,
		TenantID:     tenantID,
		ClientID:     clientID,
		ClientSecret: "secret",
		RedirectURL:  origin + "/auth/callback",
		CookieKey:    []byte("0123456789abcdef0123456789abcdef"),
		SessionTTL:   time.Hour,
		Now:          func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	h, err := web.New(web.Options{Auth: a, Store: s, PageSize: 10, Now: func() time.Time { return fixedNow }, DisplayTimeZone: zone})
	if err != nil {
		t.Fatal(err)
	}
	return &app{h: h, auth: a, store: s, idp: idp}
}

func zoneMachineBody(t *testing.T, zone *time.Location) string {
	t.Helper()
	{
		s := fake.New()
		s.AddMachine(store.Machine{MachineID: "m-z", SerialNum: "C02Z", Hostname: "z.example.com", ClientMode: store.ClientModeMonitor, LastSeen: fixedNow.Add(-5 * time.Minute)})
		s.SetSyncState("m-z", store.SyncState{PreflightAt: fixedNow.Add(-5 * time.Minute), PostflightAt: fixedNow.Add(-5*time.Minute + 5*time.Second), LastSyncType: "NORMAL"})
		s.SetTemporaryClientMode("m-z", store.ClientModeLockdown, fixedNow.Add(90*time.Minute))
		a := newAppInZone(t, s, zone)
		code, body := a.viewerGet(t, "/machines/m-z")
		if code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		return body
	}
}

func TestDisplayTimeZone_MachinePageShowsLocalTimes(t *testing.T) {
	body := zoneMachineBody(t, time.FixedZone("TST", 11*3600))
	// fixedNow is 2026-09-17 12:00 UTC.
	mustContain(t, body,
		"2026-09-17 11:55:00 UTC (2026-09-17 22:55 TST)", // last seen and last preflight
		"2026-09-17 11:55:05 UTC (2026-09-17 22:55 TST)", // last postflight
		"2026-09-17 13:30:00 UTC (2026-09-18 00:30 TST)", // temporary mode end, in Client mode and in Sync health
	)
}

func TestDisplayTimeZone_UTCShowsOnlyUTC(t *testing.T) {
	for name, zone := range map[string]*time.Location{"unset": nil, "UTC": time.UTC} {
		t.Run(name, func(t *testing.T) {
			body := zoneMachineBody(t, zone)
			mustContain(t, body, "2026-09-17 11:55:00 UTC", "2026-09-17 13:30:00 UTC")
			mustNotContain(t, body, "UTC (2026-")
		})
	}
}
