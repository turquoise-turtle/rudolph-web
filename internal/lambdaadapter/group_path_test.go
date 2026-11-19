package lambdaadapter_test

// Rudolph's group names may contain "/", "?" and "%" (groups.Normalise keeps them), and the pages link to a group as
// /groups/<name> with the name percent-encoded. The Function URL passes rawPath still encoded, so the adapter must
// keep the encoding through to the router: decoding %2F early would send /groups/x%2Fmembers to group x's members
// page, and %3F would cut the name at a query (path traversal review, 2026-11-19).

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/lambdaadapter"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/fakeidp"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

func TestAdapter_EncodedGroupNameReachesTheGroupPage(t *testing.T) {
	const tenantID, clientID = "11111111-2222-3333-4444-555555555555", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := fakeidp.New(t, tenantID, clientID, "secret")
	a, err := auth.New(auth.Config{
		Issuer: idp.Issuer, TenantID: tenantID, ClientID: clientID, ClientSecret: "secret",
		RedirectURL: "https://" + lambdaHost + "/auth/callback", CookieKey: []byte("0123456789abcdef0123456789abcdef"),
		SessionTTL: time.Hour, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	s := fake.New()
	for _, name := range []string{"x", "x/members", "a", "a?b", "50", "50%"} {
		s.AddGroup(store.Group{Name: name})
	}
	h, err := web.New(web.Options{Auth: a, Store: s, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	c, err := a.NewSessionCookie(auth.Session{Subject: "oid-vera", Name: "Vera Viewer", Username: "vera@example.com", TenantID: tenantID, Roles: []string{auth.RoleViewer}})
	if err != nil {
		t.Fatal(err)
	}

	for rawPath, want := range map[string]string{
		"/groups/x%2Fmembers": "x/members",
		"/groups/x%2fmembers": "x/members",
		"/groups/a%3Fb":       "a?b",
		"/groups/50%25":       "50%",
	} {
		t.Run(rawPath, func(t *testing.T) {
			s.ResetCalls()
			ev := baseEvent(http.MethodGet, rawPath)
			ev.Cookies = []string{c.Name + "=" + c.Value}
			resp, err := lambdaadapter.New(h)(context.Background(), ev)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d, want 200; body:\n%s", resp.StatusCode, resp.Body)
			}
			var groups []string
			for _, call := range s.Calls() {
				switch call.Method {
				case "ListGroupRules":
					groups = append(groups, call.Filter.(string))
				case "ListGroupMembers":
					t.Errorf("reached a members page (%+v), want group %q's own page", call, want)
				}
			}
			if strings.Join(groups, ",") != want {
				t.Errorf("group page read the rules of %q, want %q alone", groups, want)
			}
		})
	}
}
