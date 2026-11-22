package config_test

import (
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/config"
)

// Santa 2026.9 fixed ways for a local user to pause enforcement, so it is the default minimum (owner, 2026-11-22).
func TestFromEnv_MinSantaVersion(t *testing.T) {
	cfg, err := config.FromEnv(getenv(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinSantaVersion != "2026.9" {
		t.Errorf("default MinSantaVersion = %q, want 2026.9", cfg.MinSantaVersion)
	}
	for v, want := range map[string]string{"2026.10": "2026.10", "2026.10.1": "2026.10.1", "off": ""} {
		env := validEnv()
		env["RUDOLPH_WEB_MIN_SANTA_VERSION"] = v
		cfg, err := config.FromEnv(getenv(env))
		if err != nil || cfg.MinSantaVersion != want {
			t.Errorf("%q: %q, %v; want %q", v, cfg.MinSantaVersion, err, want)
		}
	}
	for _, bad := range []string{"latest", "2026", "9", "2026.9-beta"} {
		env := validEnv()
		env["RUDOLPH_WEB_MIN_SANTA_VERSION"] = bad
		if _, err := config.FromEnv(getenv(env)); err == nil || !strings.Contains(err.Error(), "RUDOLPH_WEB_MIN_SANTA_VERSION") {
			t.Errorf("%q: error %v, want one naming RUDOLPH_WEB_MIN_SANTA_VERSION", bad, err)
		}
	}
}
