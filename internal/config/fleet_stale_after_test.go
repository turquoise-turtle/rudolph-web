package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/config"
)

// The fleet job runs once a day (owner, 2026-11-22), so the fleet health page warns when its snapshot is older than
// RUDOLPH_WEB_FLEET_STALE_AFTER: about a day and two hours by default (26h), a Go duration from 1h to 336h (two weeks).
func TestFromEnv_FleetStaleAfter(t *testing.T) {
	cfg, err := config.FromEnv(getenv(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FleetStaleAfter != 26*time.Hour {
		t.Errorf("default FleetStaleAfter = %v, want 26h", cfg.FleetStaleAfter)
	}

	env := validEnv()
	env["RUDOLPH_WEB_FLEET_STALE_AFTER"] = "50h"
	cfg, err = config.FromEnv(getenv(env))
	if err != nil || cfg.FleetStaleAfter != 50*time.Hour {
		t.Errorf("50h: %v, %v", cfg.FleetStaleAfter, err)
	}

	for _, bad := range []string{"daily", "30m", "0", "-1h", "400h"} {
		env := validEnv()
		env["RUDOLPH_WEB_FLEET_STALE_AFTER"] = bad
		if _, err := config.FromEnv(getenv(env)); err == nil || !strings.Contains(err.Error(), "RUDOLPH_WEB_FLEET_STALE_AFTER") {
			t.Errorf("%q: error %v, want one naming RUDOLPH_WEB_FLEET_STALE_AFTER (1h to 336h)", bad, err)
		}
	}
	if config.EnvFleetStaleAfter != "RUDOLPH_WEB_FLEET_STALE_AFTER" {
		t.Errorf("EnvFleetStaleAfter = %q", config.EnvFleetStaleAfter)
	}
}
