package deployments

// The minimum Santa version the pages mark Macs below (owner, 2026-11-22): a module variable, default "2026.9",
// passed to the function, the example environment and its config.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMinSantaVersionReachesTheFunction(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	vs := blocks(module, "variable", "min_santa_version")
	if len(vs) != 1 {
		t.Fatalf("variable min_santa_version declared %d times, want once", len(vs))
	}
	if d, ok := vs[0].Body.Attributes["default"]; !ok || strings.TrimSpace(sourceOf(t, module, d.Expr)) != `"2026.9"` {
		t.Error(`min_santa_version default is not "2026.9"`)
	}
	found := false
	for _, f := range module {
		if strings.Contains(string(f.src), "RUDOLPH_WEB_MIN_SANTA_VERSION = var.min_santa_version") {
			found = true
		}
	}
	if !found {
		t.Error("the function's environment does not set RUDOLPH_WEB_MIN_SANTA_VERSION = var.min_santa_version")
	}

	example := loadTerraform(t, exampleDir)
	var main string
	for _, m := range blocks(example, "module") {
		main += bodySource(t, example, m)
	}
	if !strings.Contains(main, "min_santa_version") {
		t.Error("the example environment does not pass min_santa_version")
	}
	raw, err := os.ReadFile(filepath.Join(exampleDir, "config.auto.tfvars.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg["min_santa_version"]; !ok {
		t.Error("config.auto.tfvars.json has no min_santa_version")
	}
}
