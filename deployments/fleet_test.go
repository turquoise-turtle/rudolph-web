package deployments

// Fleet health pages (owner decisions, 2026-11-22):
//   - fleet_stale_after (default "26h", the fleet job running daily) reaches the function as
//     RUDOLPH_WEB_FLEET_STALE_AFTER, and the example environment passes it, as top_list_stale_after does.
//   - Admins set and end holiday mode from the fleet health page through Rudolph's pkg/model/fleethealth, which
//     writes the store-table item PK FleetHealth, SK Config with UpdateItem. The editing write statement's leading
//     keys therefore include "FleetHealth", exactly (the IAM review test keeps the Null check beside the condition).
//     Everything else the fleet pages read (FleetPointer, FleetSnapshot#*, UnknownSeen#* on the events table) and the
//     cohort action's group assignments are already granted.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFleetStaleAfterReachesTheFunction(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	vs := blocks(module, "variable", "fleet_stale_after")
	if len(vs) != 1 {
		t.Fatalf("variable fleet_stale_after declared %d times, want once", len(vs))
	}
	if d, ok := vs[0].Body.Attributes["default"]; !ok || strings.TrimSpace(sourceOf(t, module, d.Expr)) != `"26h"` {
		t.Error(`fleet_stale_after default is not "26h"`)
	}
	found := false
	for _, f := range module {
		if strings.Contains(string(f.src), "RUDOLPH_WEB_FLEET_STALE_AFTER = var.fleet_stale_after") {
			found = true
		}
	}
	if !found {
		t.Error("the function's environment does not set RUDOLPH_WEB_FLEET_STALE_AFTER = var.fleet_stale_after")
	}

	example := loadTerraform(t, exampleDir)
	var main string
	for _, m := range blocks(example, "module") {
		main += bodySource(t, example, m)
	}
	if !strings.Contains(main, "fleet_stale_after") {
		t.Error("the example environment does not pass fleet_stale_after")
	}
	if len(blocks(example, "variable", "fleet_stale_after")) != 1 {
		t.Error("the example environment does not declare the fleet_stale_after variable")
	}
	raw, err := os.ReadFile(filepath.Join(exampleDir, "config.auto.tfvars.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if v, ok := cfg["fleet_stale_after"]; !ok || v != "26h" {
		t.Errorf("config.auto.tfvars.json fleet_stale_after = %v, want \"26h\"", v)
	}
}

func TestEditing_FleetHealthCanBeWritten(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	for _, doc := range blocks(module, "data", "aws_iam_policy_document") {
		for _, dyn := range nested(doc, "dynamic") {
			if len(dyn.Labels) == 0 || dyn.Labels[0] != "statement" {
				continue
			}
			src := bodySource(t, module, dyn)
			if !strings.Contains(src, "WriteRudolphRules") {
				continue
			}
			if !strings.Contains(src, `"FleetHealth"`) {
				t.Errorf("the editing write statement's leading keys do not include \"FleetHealth\":\n%s", src)
			}
			if strings.Contains(src, `"FleetHealth*"`) || strings.Contains(src, `"Fleet*"`) {
				t.Error("the FleetHealth leading key must be exact, not a wildcard")
			}
			if strings.Contains(src, `"FleetPointer"`) || strings.Contains(src, `"FleetSnapshot`) {
				t.Error("the web view must not be able to write the fleet job's rows")
			}
			return
		}
	}
	t.Fatal("no WriteRudolphRules statement")
}
