package config_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/config"
)

const (
	tenant = "11111111-2222-3333-4444-555555555555"
	client = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	secret = "very-secret-client-secret"
)

var key32 = []byte("0123456789abcdef0123456789abcdef")

func validEnv() map[string]string {
	return map[string]string{
		"RUDOLPH_WEB_TENANT_ID":     tenant,
		"RUDOLPH_WEB_CLIENT_ID":     client,
		"RUDOLPH_WEB_CLIENT_SECRET": secret,
		"RUDOLPH_WEB_REDIRECT_URL":  "https://rudolph-web.example.com/auth/callback",
		"RUDOLPH_WEB_COOKIE_KEY":    base64.StdEncoding.EncodeToString(key32),
	}
}

func getenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnv_Valid(t *testing.T) {
	env := validEnv()
	env["RUDOLPH_WEB_POST_LOGOUT_REDIRECT_URL"] = "https://rudolph-web.example.com/"
	cfg, err := config.FromEnv(getenv(env))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.TenantID != tenant || cfg.ClientID != client || cfg.ClientSecret != secret {
		t.Errorf("identity fields not loaded: %+v", cfg)
	}
	if string(cfg.CookieKey) != string(key32) {
		t.Error("cookie key not decoded")
	}
	if cfg.SessionTTL != time.Hour {
		t.Errorf("default SessionTTL = %v, want 1h", cfg.SessionTTL)
	}
	if cfg.Addr != "127.0.0.1:8080" {
		t.Errorf("default Addr = %q, want 127.0.0.1:8080", cfg.Addr)
	}
	if want := "https://login.microsoftonline.com/" + tenant + "/v2.0"; cfg.Issuer() != want {
		t.Errorf("Issuer() = %q, want %q", cfg.Issuer(), want)
	}

	ac := cfg.AuthConfig()
	if ac.Issuer != cfg.Issuer() || ac.TenantID != tenant || ac.ClientID != client ||
		ac.ClientSecret != secret || ac.RedirectURL != env["RUDOLPH_WEB_REDIRECT_URL"] ||
		ac.PostLogoutRedirectURL != env["RUDOLPH_WEB_POST_LOGOUT_REDIRECT_URL"] ||
		string(ac.CookieKey) != string(key32) || ac.SessionTTL != time.Hour {
		t.Errorf("AuthConfig() does not mirror config: %+v", ac)
	}
}

func TestFromEnv_AcceptsURLSafeKeyAndLocalhostRedirect(t *testing.T) {
	env := validEnv()
	env["RUDOLPH_WEB_COOKIE_KEY"] = base64.RawURLEncoding.EncodeToString(key32)
	env["RUDOLPH_WEB_REDIRECT_URL"] = "http://localhost:8080/auth/callback"
	env["RUDOLPH_WEB_SESSION_TTL"] = "30m"
	env["RUDOLPH_WEB_ADDR"] = "127.0.0.1:9000"
	cfg, err := config.FromEnv(getenv(env))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if string(cfg.CookieKey) != string(key32) {
		t.Error("url-safe base64 key not decoded")
	}
	if cfg.SessionTTL != 30*time.Minute || cfg.Addr != "127.0.0.1:9000" {
		t.Errorf("optional values not loaded: ttl=%v addr=%q", cfg.SessionTTL, cfg.Addr)
	}
}

func TestFromEnv_Invalid(t *testing.T) {
	cases := map[string]struct {
		mutate  func(m map[string]string)
		wantVar string
	}{
		"missing tenant":        {func(m map[string]string) { delete(m, "RUDOLPH_WEB_TENANT_ID") }, "RUDOLPH_WEB_TENANT_ID"},
		"tenant not a GUID":     {func(m map[string]string) { m["RUDOLPH_WEB_TENANT_ID"] = "common" }, "RUDOLPH_WEB_TENANT_ID"},
		"tenant path injection": {func(m map[string]string) { m["RUDOLPH_WEB_TENANT_ID"] = "../evil" }, "RUDOLPH_WEB_TENANT_ID"},
		"missing client id":     {func(m map[string]string) { delete(m, "RUDOLPH_WEB_CLIENT_ID") }, "RUDOLPH_WEB_CLIENT_ID"},
		"missing secret":        {func(m map[string]string) { delete(m, "RUDOLPH_WEB_CLIENT_SECRET") }, "RUDOLPH_WEB_CLIENT_SECRET"},
		"missing redirect":      {func(m map[string]string) { delete(m, "RUDOLPH_WEB_REDIRECT_URL") }, "RUDOLPH_WEB_REDIRECT_URL"},
		"plain http redirect": {func(m map[string]string) {
			m["RUDOLPH_WEB_REDIRECT_URL"] = "http://rudolph-web.example.com/auth/callback"
		}, "RUDOLPH_WEB_REDIRECT_URL"},
		"redirect wrong path": {func(m map[string]string) {
			m["RUDOLPH_WEB_REDIRECT_URL"] = "https://rudolph-web.example.com/callback"
		}, "RUDOLPH_WEB_REDIRECT_URL"},
		"insecure post logout": {func(m map[string]string) {
			m["RUDOLPH_WEB_POST_LOGOUT_REDIRECT_URL"] = "http://rudolph-web.example.com/"
		}, "RUDOLPH_WEB_POST_LOGOUT_REDIRECT_URL"},
		"missing cookie key": {func(m map[string]string) { delete(m, "RUDOLPH_WEB_COOKIE_KEY") }, "RUDOLPH_WEB_COOKIE_KEY"},
		"cookie key not base64": {func(m map[string]string) {
			m["RUDOLPH_WEB_COOKIE_KEY"] = "not base64 at all!!"
		}, "RUDOLPH_WEB_COOKIE_KEY"},
		"cookie key too short": {func(m map[string]string) {
			m["RUDOLPH_WEB_COOKIE_KEY"] = base64.StdEncoding.EncodeToString([]byte("sixteen-bytes-ok"))
		}, "RUDOLPH_WEB_COOKIE_KEY"},
		"ttl not a duration": {func(m map[string]string) { m["RUDOLPH_WEB_SESSION_TTL"] = "forever" }, "RUDOLPH_WEB_SESSION_TTL"},
		"ttl too long":       {func(m map[string]string) { m["RUDOLPH_WEB_SESSION_TTL"] = "24h" }, "RUDOLPH_WEB_SESSION_TTL"},
		"ttl too short":      {func(m map[string]string) { m["RUDOLPH_WEB_SESSION_TTL"] = "30s" }, "RUDOLPH_WEB_SESSION_TTL"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			tc.mutate(env)
			_, err := config.FromEnv(getenv(env))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantVar) {
				t.Errorf("error %q should name %s", err, tc.wantVar)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), env["RUDOLPH_WEB_COOKIE_KEY"]) && env["RUDOLPH_WEB_COOKIE_KEY"] != "" {
				t.Errorf("error leaks a secret value: %q", err)
			}
		})
	}
}
