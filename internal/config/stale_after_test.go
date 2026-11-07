package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/config"
)

// The ranker can run on a schedule (for example weekday working hours only), so how old a top list may be before
// the page warns is configurable (owner, 2026-11-07).
func TestFromEnv_TopListStaleAfter(t *testing.T) {
	cfg, err := config.FromEnv(getenv(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TopListStaleAfter != 3*time.Hour {
		t.Errorf("default TopListStaleAfter = %v, want 3h", cfg.TopListStaleAfter)
	}

	env := validEnv()
	env["RUDOLPH_WEB_TOP_LIST_STALE_AFTER"] = "15h"
	cfg, err = config.FromEnv(getenv(env))
	if err != nil || cfg.TopListStaleAfter != 15*time.Hour {
		t.Errorf("15h: %v, %v", cfg.TopListStaleAfter, err)
	}

	for _, bad := range []string{"soon", "30m", "0", "-1h", "200h"} {
		env := validEnv()
		env["RUDOLPH_WEB_TOP_LIST_STALE_AFTER"] = bad
		if _, err := config.FromEnv(getenv(env)); err == nil || !strings.Contains(err.Error(), "RUDOLPH_WEB_TOP_LIST_STALE_AFTER") {
			t.Errorf("%q: error %v, want one naming RUDOLPH_WEB_TOP_LIST_STALE_AFTER (1h to 168h)", bad, err)
		}
	}
}
