package config_test

// The minimum Santa version is now one stored value, Rudolph's fleet health setting (FleetHealth / Config,
// MinSantaVersion), which the web view reads at run time (owner decision 1). The web view's own setting is gone:
// config.Config has no MinSantaVersion, and the package has no default of its own (Rudolph's
// fleethealth.DefaultMinSantaVersion is the one default).
//
// A function deployed before the upgrade may still have RUDOLPH_WEB_MIN_SANTA_VERSION in its environment (owner
// decision 3). Whatever it holds, it is ignored and never stops the function: FromEnv and Load accept it.
// config.IgnoredEnvironment(getenv) returns the lines cmd/lambda and cmd/local log once at start-up: one line per
// retired setting that is set (not empty after trimming), naming the variable, saying it is ignored, and where the
// setting went (Rudolph's `rudolph fleet min-santa-version`, or the form on /admin/config). Nothing when it is unset.

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/config"
)

func TestConfig_HasNoMinSantaVersionOfItsOwn(t *testing.T) {
	if _, ok := reflect.TypeOf(config.Config{}).FieldByName("MinSantaVersion"); ok {
		t.Error("config.Config still has a MinSantaVersion field; the minimum is Rudolph's stored setting, read by the web view at run time")
	}
}

func TestFromEnv_IgnoresAFormerMinSantaVersion(t *testing.T) {
	for _, v := range []string{"2026.10", "off", "latest", "2026.x", "<script>", " "} {
		env := validEnv()
		env["RUDOLPH_WEB_MIN_SANTA_VERSION"] = v
		if _, err := config.FromEnv(getenv(env)); err != nil {
			t.Errorf("RUDOLPH_WEB_MIN_SANTA_VERSION=%q stops the function: %v", v, err)
		}
		if _, err := config.Load(context.Background(), getenv(env), nil); err != nil {
			t.Errorf("Load with RUDOLPH_WEB_MIN_SANTA_VERSION=%q: %v", v, err)
		}
	}
}

func TestIgnoredEnvironment_NamesTheRetiredMinSantaVersion(t *testing.T) {
	env := validEnv()
	if lines := config.IgnoredEnvironment(getenv(env)); len(lines) != 0 {
		t.Errorf("nothing retired is set, but IgnoredEnvironment returns %q", lines)
	}
	env["RUDOLPH_WEB_MIN_SANTA_VERSION"] = "   "
	if lines := config.IgnoredEnvironment(getenv(env)); len(lines) != 0 {
		t.Errorf("a blank RUDOLPH_WEB_MIN_SANTA_VERSION is not set, but IgnoredEnvironment returns %q", lines)
	}
	env["RUDOLPH_WEB_MIN_SANTA_VERSION"] = "2026.10"
	lines := config.IgnoredEnvironment(getenv(env))
	if len(lines) != 1 {
		t.Fatalf("IgnoredEnvironment returns %d lines %q, want one for RUDOLPH_WEB_MIN_SANTA_VERSION", len(lines), lines)
	}
	for _, want := range []string{"RUDOLPH_WEB_MIN_SANTA_VERSION", "ignored", "rudolph fleet min-santa-version", "/admin/config"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("the line %q does not mention %q", lines[0], want)
		}
	}
}
