package web_test

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

func newAppStaleAfter(t *testing.T, s *fake.Store, staleAfter time.Duration) *app {
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
	h, err := web.New(web.Options{Auth: a, Store: s, PageSize: 10, Now: func() time.Time { return fixedNow }, TopListStaleAfter: staleAfter})
	if err != nil {
		t.Fatal(err)
	}
	return &app{h: h, auth: a, store: s, idp: idp}
}

// With the ranker on a working-hours schedule, a list from last night is expected, not a fault.
func TestTopLists_StaleAfterIsConfigurable(t *testing.T) {
	s := fake.New()
	s.SetTopList(store.TopKindBlocked, 7, fixedNow.Add(-14*time.Hour), []store.TopBinary{{FileSHA256: "sha-a", FileName: "a", Count: 1}})

	code, body := newAppStaleAfter(t, s, 15*time.Hour).viewerGet(t, "/events/top-blocked")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if strings.Contains(body, "Warning") {
		t.Error("a 14-hour-old list warns although the configured limit is 15 hours")
	}

	code, body = newAppStaleAfter(t, s, 0).viewerGet(t, "/events/top-blocked")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "Warning", "3 hours")
	mustNotContain(t, body, "every hour")

	code, body = newAppStaleAfter(t, s, 12*time.Hour).viewerGet(t, "/events/top-blocked")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "Warning", "12 hours")
}

func TestNew_RejectsANegativeStaleAfter(t *testing.T) {
	idp := fakeidp.New(t, tenantID, clientID, "secret")
	a, err := auth.New(auth.Config{Issuer: idp.Issuer, TenantID: tenantID, ClientID: clientID, ClientSecret: "secret",
		RedirectURL: origin + "/auth/callback", CookieKey: []byte("0123456789abcdef0123456789abcdef"), SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := web.New(web.Options{Auth: a, Store: fake.New(), TopListStaleAfter: -time.Hour}); err == nil {
		t.Error("New accepted a negative TopListStaleAfter")
	}
}
