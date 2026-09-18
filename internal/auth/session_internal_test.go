package auth

// Internal tests for properties that cannot be reached with a correctly sealed
// payload through the public API alone: nonce uniqueness in the sealer, and
// each session check (AEAD cookie-name binding, tenant, subject) exercised in
// isolation with a payload that differs from a valid one in exactly one way.

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const (
	internalTenant      = "11111111-2222-3333-4444-555555555555"
	internalOtherTenant = "99999999-8888-7777-6666-555555555555"
)

var internalNow = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

func newInternalAuthenticator(t *testing.T) *Authenticator {
	t.Helper()
	a, err := New(Config{
		// Require never contacts the provider, so the issuer is never dialled.
		Issuer:       "https://login.microsoftonline.com/" + internalTenant + "/v2.0",
		TenantID:     internalTenant,
		ClientID:     "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		ClientSecret: "s3cret-value",
		RedirectURL:  "https://rudolph-web.example.com/auth/callback",
		CookieKey:    []byte("0123456789abcdef0123456789abcdef"),
		SessionTTL:   time.Hour,
		Now:          func() time.Time { return internalNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// validPayload is a session payload that Require accepts when sealed under
// SessionCookieName.
func validPayload() sessionPayload {
	return sessionPayload{
		Subject:  "00000000-0000-0000-0000-00000000a11c",
		Name:     "Alice",
		Username: "alice@example.com",
		TenantID: internalTenant,
		Roles:    []string{RoleAdmin},
		IssuedAt: internalNow.Add(-time.Minute).Unix(),
		Expires:  internalNow.Add(time.Hour).Unix(),
	}
}

// serveWithSessionValue presents value as the session cookie to a route that
// requires Rudolph.Viewer and reports whether the protected handler ran.
func serveWithSessionValue(t *testing.T, a *Authenticator, value string) (ran bool, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := a.Require(RoleViewer, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		_, _ = w.Write([]byte("protected"))
	}))
	req := httptest.NewRequest(http.MethodGet, "https://rudolph-web.example.com/viewer", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: value})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return ran, rec
}

func sealOrFatal(t *testing.T, a *Authenticator, name string, v any) string {
	t.Helper()
	value, err := a.sealer.seal(name, v)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return value
}

func assertSessionRejected(t *testing.T, a *Authenticator, value string) {
	t.Helper()
	ran, rec := serveWithSessionValue(t, a, value)
	if ran {
		t.Fatal("protected handler ran for a session that must be rejected")
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d, want 302 to login; body %q", rec.Code, rec.Body.String())
	}
}

// The control: the unmodified payload sealed under the session cookie name is
// accepted, so each rejection below is caused by its single difference.
func TestSessionChecks_ControlPayloadIsAccepted(t *testing.T) {
	a := newInternalAuthenticator(t)
	ran, rec := serveWithSessionValue(t, a, sealOrFatal(t, a, SessionCookieName, validPayload()))
	if !ran || rec.Code != http.StatusOK {
		t.Fatalf("valid sealed payload rejected: ran=%v status=%d body=%q", ran, rec.Code, rec.Body.String())
	}
}

// A valid session payload sealed for the flow cookie must not open as a
// session cookie: the cookie name is bound in as AEAD additional data.
func TestSessionChecks_CookieNameIsBoundAsAAD(t *testing.T) {
	a := newInternalAuthenticator(t)
	assertSessionRejected(t, a, sealOrFatal(t, a, FlowCookieName, validPayload()))

	// The same holds at the sealer level, in both directions.
	var p sessionPayload
	if err := a.sealer.open(SessionCookieName, sealOrFatal(t, a, FlowCookieName, validPayload()), &p); err == nil {
		t.Error("sealer.open accepted a value sealed under a different cookie name")
	}
	if err := a.sealer.open(FlowCookieName, sealOrFatal(t, a, SessionCookieName, validPayload()), &p); err == nil {
		t.Error("sealer.open accepted a session value as a flow cookie")
	}
}

func TestSessionChecks_OtherTenantRejected(t *testing.T) {
	a := newInternalAuthenticator(t)
	p := validPayload()
	p.TenantID = internalOtherTenant
	assertSessionRejected(t, a, sealOrFatal(t, a, SessionCookieName, p))

	p.TenantID = ""
	assertSessionRejected(t, a, sealOrFatal(t, a, SessionCookieName, p))
}

func TestSessionChecks_EmptySubjectRejected(t *testing.T) {
	a := newInternalAuthenticator(t)
	p := validPayload()
	p.Subject = ""
	assertSessionRejected(t, a, sealOrFatal(t, a, SessionCookieName, p))
}

// Sealing the same value twice must use a fresh random nonce each time. A
// fixed or zero nonce under AES-GCM leaks plaintext XORs and the GHASH key.
func TestSealer_NonceIsFreshPerSeal(t *testing.T) {
	a := newInternalAuthenticator(t)
	nonceSize := a.sealer.aead.NonceSize()
	zero := make([]byte, nonceSize)

	seen := map[string]bool{}
	nonces := map[string]bool{}
	for i := 0; i < 16; i++ {
		v := sealOrFatal(t, a, SessionCookieName, validPayload())
		if seen[v] {
			t.Fatalf("seal %d produced a ciphertext identical to an earlier one", i)
		}
		seen[v] = true

		raw, err := base64.RawURLEncoding.DecodeString(v)
		if err != nil {
			t.Fatalf("sealed value is not base64url: %v", err)
		}
		if len(raw) < nonceSize {
			t.Fatalf("sealed value too short: %d bytes", len(raw))
		}
		nonce := raw[:nonceSize]
		if bytes.Equal(nonce, zero) {
			t.Fatal("nonce is all zeros")
		}
		if nonces[string(nonce)] {
			t.Fatalf("seal %d reused a nonce", i)
		}
		nonces[string(nonce)] = true
	}

	// Public API: two session cookies for the same session at the same instant
	// must still differ.
	s := Session{Subject: "alice", TenantID: internalTenant, Roles: []string{RoleViewer}}
	c1, err1 := a.NewSessionCookie(s)
	c2, err2 := a.NewSessionCookie(s)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if c1.Value == c2.Value {
		t.Fatal("NewSessionCookie produced identical values for identical input")
	}
}
