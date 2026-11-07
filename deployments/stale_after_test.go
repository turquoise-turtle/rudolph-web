package deployments

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTopListStaleAfterReachesTheFunction(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	vs := blocks(module, "variable", "top_list_stale_after")
	if len(vs) != 1 {
		t.Fatalf("variable top_list_stale_after declared %d times, want once", len(vs))
	}
	if d, ok := vs[0].Body.Attributes["default"]; !ok || strings.TrimSpace(sourceOf(t, module, d.Expr)) != `"3h"` {
		t.Error(`top_list_stale_after default is not "3h"`)
	}
	found := false
	for _, f := range module {
		if strings.Contains(string(f.src), "RUDOLPH_WEB_TOP_LIST_STALE_AFTER = var.top_list_stale_after") {
			found = true
		}
	}
	if !found {
		t.Error("the function's environment does not set RUDOLPH_WEB_TOP_LIST_STALE_AFTER = var.top_list_stale_after")
	}

	example := loadTerraform(t, exampleDir)
	var main string
	for _, m := range blocks(example, "module") {
		main += bodySource(t, example, m)
	}
	if !strings.Contains(main, "top_list_stale_after") {
		t.Error("the example environment does not pass top_list_stale_after")
	}
	raw, err := os.ReadFile(filepath.Join(exampleDir, "config.auto.tfvars.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg["top_list_stale_after"]; !ok {
		t.Error("config.auto.tfvars.json has no top_list_stale_after")
	}
}
