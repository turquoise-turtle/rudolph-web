package config_test

// "Run the ranker now" (owner request, 2026-11-25): RUDOLPH_WEB_RANKER_FUNCTION (config.EnvRankerFunction) names
// Rudolph's ranker Lambda (${prefix}_rudolph_ranker) that admins may start from /admin; config.Config.RankerFunction
// holds it. It is checked exactly as RUDOLPH_WEB_FLEET_FUNCTION is (fleet_function_test.go): unset or empty (the
// default) is off; set, a Lambda function name (letters, digits, '-' and '_', 1 to 64), trimmed, not an ARN or a
// qualified name; set without RUDOLPH_WEB_EDITING=true it is an error naming both. Either function can be set without
// the other.

import (
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/config"
)

func TestFromEnv_RankerFunction(t *testing.T) {
	if config.EnvRankerFunction != "RUDOLPH_WEB_RANKER_FUNCTION" {
		t.Errorf("EnvRankerFunction = %q", config.EnvRankerFunction)
	}

	cfg, err := config.FromEnv(getenv(editingEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RankerFunction != "" {
		t.Errorf("default RankerFunction = %q, want empty (off)", cfg.RankerFunction)
	}

	for _, name := range []string{"example_rudolph_ranker", "Prod-rudolph_ranker", strings.Repeat("a", 64)} {
		env := editingEnv()
		env["RUDOLPH_WEB_RANKER_FUNCTION"] = "  " + name + " "
		cfg, err := config.FromEnv(getenv(env))
		if err != nil || cfg.RankerFunction != name {
			t.Errorf("%q: RankerFunction %q, %v; want it, trimmed", name, cfg.RankerFunction, err)
		}
		if cfg.FleetFunction != "" {
			t.Errorf("%q: the ranker function set FleetFunction %q", name, cfg.FleetFunction)
		}
	}

	for _, bad := range []string{
		"arn:aws:lambda:us-east-1:000000000000:function:example_rudolph_ranker",
		"example_rudolph_ranker:1",
		"has space",
		"example/rudolph_ranker",
		strings.Repeat("a", 65),
	} {
		env := editingEnv()
		env["RUDOLPH_WEB_RANKER_FUNCTION"] = bad
		if _, err := config.FromEnv(getenv(env)); err == nil || !strings.Contains(err.Error(), "RUDOLPH_WEB_RANKER_FUNCTION") {
			t.Errorf("%q: error %v, want one naming RUDOLPH_WEB_RANKER_FUNCTION", bad, err)
		}
	}
}

func TestFromEnv_RankerFunctionNeedsEditing(t *testing.T) {
	for _, editing := range []string{"", "false"} {
		env := validEnv()
		env["RUDOLPH_WEB_DYNAMODB_TABLE"] = "example_rudolph_store"
		env["RUDOLPH_WEB_EDITING"] = editing
		env["RUDOLPH_WEB_RANKER_FUNCTION"] = "example_rudolph_ranker"
		_, err := config.FromEnv(getenv(env))
		if err == nil || !strings.Contains(err.Error(), "RUDOLPH_WEB_RANKER_FUNCTION") || !strings.Contains(err.Error(), "RUDOLPH_WEB_EDITING") {
			t.Errorf("editing %q: error %v, want one naming RUDOLPH_WEB_RANKER_FUNCTION and RUDOLPH_WEB_EDITING", editing, err)
		}
	}
}

func TestFromEnv_BothFunctions(t *testing.T) {
	env := editingEnv()
	env["RUDOLPH_WEB_FLEET_FUNCTION"] = "example_rudolph_fleet"
	env["RUDOLPH_WEB_RANKER_FUNCTION"] = "example_rudolph_ranker"
	cfg, err := config.FromEnv(getenv(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FleetFunction != "example_rudolph_fleet" || cfg.RankerFunction != "example_rudolph_ranker" {
		t.Errorf("FleetFunction %q, RankerFunction %q", cfg.FleetFunction, cfg.RankerFunction)
	}
}
