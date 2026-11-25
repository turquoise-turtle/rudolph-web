package config_test

// "Run the fleet job now" (owner request, 2026-11-25): RUDOLPH_WEB_FLEET_FUNCTION (config.EnvFleetFunction) names
// Rudolph's fleet Lambda (${prefix}_rudolph_fleet) that admins may start from /fleet; config.Config.FleetFunction
// holds it. Unset or empty (the default) turns the button and the route off. Set, it must be a Lambda function name
// (letters, digits, '-' and '_', 1 to 64), not an ARN: the deployment grants lambda:InvokeFunction on the ARN it builds
// from that name. It needs RUDOLPH_WEB_EDITING=true, since starting the job is an admin action; set without editing
// it is an error naming both, so a deployment never believes the button is there when it is not.

import (
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/config"
)

func editingEnv() map[string]string {
	env := validEnv()
	env["RUDOLPH_WEB_DYNAMODB_TABLE"] = "example_rudolph_store"
	env["RUDOLPH_WEB_EDITING"] = "true"
	return env
}

func TestFromEnv_FleetFunction(t *testing.T) {
	if config.EnvFleetFunction != "RUDOLPH_WEB_FLEET_FUNCTION" {
		t.Errorf("EnvFleetFunction = %q", config.EnvFleetFunction)
	}

	cfg, err := config.FromEnv(getenv(editingEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FleetFunction != "" {
		t.Errorf("default FleetFunction = %q, want empty (off)", cfg.FleetFunction)
	}

	for _, name := range []string{"example_rudolph_fleet", "Prod-rudolph_fleet", strings.Repeat("a", 64)} {
		env := editingEnv()
		env["RUDOLPH_WEB_FLEET_FUNCTION"] = "  " + name + " "
		cfg, err := config.FromEnv(getenv(env))
		if err != nil || cfg.FleetFunction != name {
			t.Errorf("%q: FleetFunction %q, %v; want it, trimmed", name, cfg.FleetFunction, err)
		}
	}

	for _, bad := range []string{
		"arn:aws:lambda:us-east-1:000000000000:function:example_rudolph_fleet",
		"example_rudolph_fleet:1",
		"has space",
		"example/rudolph_fleet",
		strings.Repeat("a", 65),
	} {
		env := editingEnv()
		env["RUDOLPH_WEB_FLEET_FUNCTION"] = bad
		if _, err := config.FromEnv(getenv(env)); err == nil || !strings.Contains(err.Error(), "RUDOLPH_WEB_FLEET_FUNCTION") {
			t.Errorf("%q: error %v, want one naming RUDOLPH_WEB_FLEET_FUNCTION", bad, err)
		}
	}
}

func TestFromEnv_FleetFunctionNeedsEditing(t *testing.T) {
	for _, editing := range []string{"", "false"} {
		env := validEnv()
		env["RUDOLPH_WEB_DYNAMODB_TABLE"] = "example_rudolph_store"
		env["RUDOLPH_WEB_EDITING"] = editing
		env["RUDOLPH_WEB_FLEET_FUNCTION"] = "example_rudolph_fleet"
		_, err := config.FromEnv(getenv(env))
		if err == nil || !strings.Contains(err.Error(), "RUDOLPH_WEB_FLEET_FUNCTION") || !strings.Contains(err.Error(), "RUDOLPH_WEB_EDITING") {
			t.Errorf("editing %q: error %v, want one naming RUDOLPH_WEB_FLEET_FUNCTION and RUDOLPH_WEB_EDITING", editing, err)
		}
	}
}
