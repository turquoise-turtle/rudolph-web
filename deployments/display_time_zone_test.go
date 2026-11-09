package deployments

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisplayTimeZoneReachesTheFunction(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	vs := blocks(module, "variable", "display_time_zone")
	if len(vs) != 1 {
		t.Fatalf("variable display_time_zone declared %d times, want once", len(vs))
	}
	if d, ok := vs[0].Body.Attributes["default"]; !ok || strings.TrimSpace(sourceOf(t, module, d.Expr)) != `"UTC"` {
		t.Error(`display_time_zone default is not "UTC"`)
	}
	found := false
	for _, f := range module {
		if strings.Contains(string(f.src), "RUDOLPH_WEB_DISPLAY_TIME_ZONE") && strings.Contains(string(f.src), "var.display_time_zone") {
			found = true
		}
	}
	if !found {
		t.Error("the function's environment does not set RUDOLPH_WEB_DISPLAY_TIME_ZONE from var.display_time_zone")
	}

	example := loadTerraform(t, exampleDir)
	var main string
	for _, m := range blocks(example, "module") {
		main += bodySource(t, example, m)
	}
	if !strings.Contains(main, "display_time_zone") {
		t.Error("the example environment does not pass display_time_zone")
	}
	raw, err := os.ReadFile(filepath.Join(exampleDir, "config.auto.tfvars.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if v, ok := cfg["display_time_zone"]; !ok || v != "UTC" {
		t.Errorf("config.auto.tfvars.json display_time_zone = %v, want \"UTC\" in the example", v)
	}
}

// provided.al2023 does not guarantee a time zone database, so the Lambda binary embeds one: either cmd/lambda
// imports time/tzdata or the build uses the timetzdata tag.
func TestLambdaEmbedsTimeZoneDatabase(t *testing.T) {
	main, err := os.ReadFile(filepath.Join("..", "cmd", "lambda", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	build, err := os.ReadFile(filepath.Join("..", "scripts", "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(main), `"time/tzdata"`) && !strings.Contains(string(build), "timetzdata") {
		t.Error("the Lambda binary does not embed the time zone database (import _ \"time/tzdata\" in cmd/lambda, or build with -tags timetzdata)")
	}
}
