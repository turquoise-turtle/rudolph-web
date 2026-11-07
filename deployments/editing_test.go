package deployments

// v2 editing (plan F2): the web role can write only when enable_editing is true (default false), only the write
// actions Rudolph's writers use, and only to Rudolph's rule, machine and audit partitions (dynamodb:LeadingKeys).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditing_WritesOnlyWhenEnabledAndOnlyToRudolphPartitions(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	vs := blocks(module, "variable", "enable_editing")
	if len(vs) != 1 {
		t.Fatalf("variable enable_editing declared %d times", len(vs))
	}
	if d, ok := vs[0].Body.Attributes["default"]; !ok || strings.TrimSpace(sourceOf(t, module, d.Expr)) != "false" {
		t.Error("enable_editing does not default to false")
	}

	writeActions := []string{"dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:TransactWriteItems", "dynamodb:BatchWriteItem"}
	found := 0
	for _, doc := range blocks(module, "data", "aws_iam_policy_document") {
		for _, dyn := range nested(doc, "dynamic") {
			if len(dyn.Labels) == 0 || dyn.Labels[0] != "statement" {
				continue
			}
			src := bodySource(t, module, dyn)
			writes := false
			for _, a := range writeActions {
				if strings.Contains(src, a) {
					writes = true
				}
			}
			if !writes {
				continue
			}
			found++
			fe, ok := dyn.Body.Attributes["for_each"]
			if !ok || !strings.Contains(sourceOf(t, module, fe.Expr), "var.enable_editing") {
				t.Errorf("a write statement is not conditional on var.enable_editing:\n%s", src)
			}
			if !strings.Contains(src, "dynamodb:LeadingKeys") || !strings.Contains(src, "ForAllValues:StringLike") {
				t.Errorf("the write statement does not restrict dynamodb:LeadingKeys with ForAllValues:StringLike:\n%s", src)
			}
			for _, prefix := range []string{"GlobalRules", "MachineRules#", "Machine#", "Audit#"} {
				if !strings.Contains(src, prefix) {
					t.Errorf("the write statement's leading keys do not include %s:\n%s", prefix, src)
				}
			}
			if strings.Contains(src, `"*"`) || strings.Contains(src, "dynamodb:Scan") || strings.Contains(src, "dynamodb:DeleteTable") {
				t.Errorf("the write statement is broader than it needs:\n%s", src)
			}
		}
	}
	if found == 0 {
		t.Error("no conditional write statement for editing")
	}

	env := false
	for _, f := range module {
		if strings.Contains(string(f.src), "RUDOLPH_WEB_EDITING") && strings.Contains(string(f.src), "var.enable_editing") {
			env = true
		}
	}
	if !env {
		t.Error("the function's environment does not set RUDOLPH_WEB_EDITING from var.enable_editing")
	}

	example := loadTerraform(t, exampleDir)
	var main string
	for _, m := range blocks(example, "module") {
		main += bodySource(t, example, m)
	}
	raw, err := os.ReadFile(filepath.Join(exampleDir, "config.auto.tfvars.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(main, "enable_editing") || cfg["enable_editing"] != false {
		t.Error("the example environment does not pass enable_editing, set to false")
	}
}
