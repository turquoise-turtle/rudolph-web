package config_test

// Times are converted to each viewer's time zone in the browser, so the server no longer has a display time zone. A
// leftover RUDOLPH_WEB_DISPLAY_TIME_ZONE (a function deployed before the upgrade) is ignored, not an error.

import (
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/config"
)

func TestFromEnv_IgnoresAFormerDisplayTimeZone(t *testing.T) {
	env := validEnv()
	env["RUDOLPH_WEB_DISPLAY_TIME_ZONE"] = "Not/A_Zone"
	if _, err := config.FromEnv(getenv(env)); err != nil {
		t.Errorf("a leftover RUDOLPH_WEB_DISPLAY_TIME_ZONE stops the function: %v", err)
	}
}
