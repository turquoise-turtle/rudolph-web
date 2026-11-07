package config_test

import (
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/config"
)

// RUDOLPH_WEB_EDITING turns on the admin write paths (plan F2); off unless exactly "true", and it needs the store
// table to write to.
func TestFromEnv_Editing(t *testing.T) {
	cfg, err := config.FromEnv(getenv(validEnv()))
	if err != nil || cfg.Editing {
		t.Errorf("default Editing %v, %v; want false", cfg.Editing, err)
	}
	env := validEnv()
	env["RUDOLPH_WEB_EDITING"] = "true"
	env["RUDOLPH_WEB_DYNAMODB_TABLE"] = "prod_rudolph_store"
	if cfg, err := config.FromEnv(getenv(env)); err != nil || !cfg.Editing {
		t.Errorf("RUDOLPH_WEB_EDITING=true: %v, %v", cfg.Editing, err)
	}
	env = validEnv()
	env["RUDOLPH_WEB_EDITING"] = "true"
	if _, err := config.FromEnv(getenv(env)); err == nil {
		t.Error("editing without a store table accepted")
	}
	env = validEnv()
	env["RUDOLPH_WEB_EDITING"] = "yes please"
	if _, err := config.FromEnv(getenv(env)); err == nil {
		t.Error("RUDOLPH_WEB_EDITING=\"yes please\" accepted; want true, false or empty")
	}
}
