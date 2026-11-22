package web_test

// Minimum Santa version (owner, 2026-11-22): Santa 2026.9 fixed two high-severity ways a local user could crash
// santad and pause enforcement (northpolesec/santa security advisories, 2026-10-27), so the owner wants to see which
// Macs still run an older release. web.Options.MinSantaVersion (RUDOLPH_WEB_MIN_SANTA_VERSION, Terraform
// min_santa_version, default "2026.9") names it; empty turns the check off. A Mac whose reported version parses and
// is below it gets an "update needed" marker, with the minimum named, next to its version on the machine list and
// its page; the "legacy client" marker below 2026.8 stays as it was. An empty or unparseable reported version gets
// no marker. A minimum that is not a version is refused by web.New.

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/fakeidp"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

func newAppWithMinVersion(t *testing.T, s *fake.Store, min string) *app {
	t.Helper()
	idp := fakeidp.New(t, tenantID, clientID, "secret")
	a, err := auth.New(auth.Config{
		Issuer: idp.Issuer, TenantID: tenantID, ClientID: clientID, ClientSecret: "secret",
		RedirectURL: origin + "/auth/callback", CookieKey: []byte("0123456789abcdef0123456789abcdef"),
		SessionTTL: time.Hour, Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	h, err := web.New(web.Options{Auth: a, Store: s, PageSize: 10, Now: func() time.Time { return fixedNow }, MinSantaVersion: min})
	if err != nil {
		t.Fatal(err)
	}
	return &app{h: h, auth: a, store: s, idp: idp}
}

func versionStore() *fake.Store {
	s := fake.New()
	for id, v := range map[string]string{
		"m-legacy": "2025.12.0", "m-old": "2026.8.1", "m-min": "2026.9", "m-new": "2026.10.2", "m-empty": "", "m-odd": "nightly",
	} {
		s.AddMachine(store.Machine{MachineID: id, SerialNum: "C02" + id, Hostname: "host-" + id, SantaVersion: v, LastSeen: fixedNow})
	}
	return s
}

func TestMinSantaVersion_MarkedBelowTheMinimum(t *testing.T) {
	a := newAppWithMinVersion(t, versionStore(), "2026.9")
	for id, want := range map[string]bool{"m-legacy": true, "m-old": true, "m-min": false, "m-new": false, "m-empty": false, "m-odd": false} {
		code, body := a.viewerGet(t, "/machines/"+id)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", id, code)
		}
		if want {
			mustContain(t, body, "update needed", "2026.9")
		} else {
			mustNotContain(t, body, "update needed")
		}
	}
	_, body := a.viewerGet(t, "/machines/m-legacy")
	mustContain(t, body, "legacy client")

	_, body = a.viewerGet(t, "/machines")
	mustContain(t, body, "update needed")
	if n := strings.Count(body, "update needed"); n != 2 {
		t.Errorf("the machine list marks %d Macs, want the 2 below 2026.9", n)
	}
}

func TestMinSantaVersion_EmptyTurnsItOff(t *testing.T) {
	a := newAppWithMinVersion(t, versionStore(), "")
	for _, path := range []string{"/machines", "/machines/m-old", "/machines/m-legacy"} {
		_, body := a.viewerGet(t, path)
		mustNotContain(t, body, "update needed")
	}
}

func TestMinSantaVersion_BadMinimumIsRefused(t *testing.T) {
	idp := fakeidp.New(t, tenantID, clientID, "secret")
	a, err := auth.New(auth.Config{Issuer: idp.Issuer, TenantID: tenantID, ClientID: clientID, ClientSecret: "secret",
		RedirectURL: origin + "/auth/callback", CookieKey: []byte("0123456789abcdef0123456789abcdef"), SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"latest", "2026", "2026.x", "<script>"} {
		if _, err := web.New(web.Options{Auth: a, Store: fake.New(), MinSantaVersion: bad}); err == nil {
			t.Errorf("MinSantaVersion %q accepted", bad)
		}
	}
	for _, good := range []string{"2026.9", "2026.10.1"} {
		if _, err := web.New(web.Options{Auth: a, Store: fake.New(), MinSantaVersion: good}); err != nil {
			t.Errorf("MinSantaVersion %q refused: %v", good, err)
		}
	}
}
