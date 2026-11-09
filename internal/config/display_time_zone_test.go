package config_test

// Pages show times in UTC with the time in a configured display time zone beside them (owner feedback from the first
// production deployment: UTC-only times were confusing). Pages run no scripts (CSP script-src 'none'), so the
// browser's time zone is not available: the zone is configured, and defaults to UTC (no second time shown).

import (
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/config"
)

func TestFromEnv_DisplayTimeZone(t *testing.T) {
	cfg, err := config.FromEnv(getenv(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DisplayTimeZone != time.UTC {
		t.Errorf("default DisplayTimeZone = %v, want time.UTC", cfg.DisplayTimeZone)
	}

	env := validEnv()
	env["RUDOLPH_WEB_DISPLAY_TIME_ZONE"] = "Asia/Tokyo"
	cfg, err = config.FromEnv(getenv(env))
	if err != nil || cfg.DisplayTimeZone == nil || cfg.DisplayTimeZone.String() != "Asia/Tokyo" {
		t.Errorf("Asia/Tokyo: %v, %v", cfg.DisplayTimeZone, err)
	}

	env["RUDOLPH_WEB_DISPLAY_TIME_ZONE"] = "UTC"
	if cfg, err = config.FromEnv(getenv(env)); err != nil || cfg.DisplayTimeZone != time.UTC {
		t.Errorf("UTC: %v, %v; want time.UTC", cfg.DisplayTimeZone, err)
	}

	for _, bad := range []string{"Mars/Olympus_Mons", "Local", "+11:00", "../etc/passwd"} {
		env := validEnv()
		env["RUDOLPH_WEB_DISPLAY_TIME_ZONE"] = bad
		if _, err := config.FromEnv(getenv(env)); err == nil || !strings.Contains(err.Error(), "RUDOLPH_WEB_DISPLAY_TIME_ZONE") {
			t.Errorf("%q: error %v, want one naming RUDOLPH_WEB_DISPLAY_TIME_ZONE (an IANA time zone name)", bad, err)
		}
	}
}
