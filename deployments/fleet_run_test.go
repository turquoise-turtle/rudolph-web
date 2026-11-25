package deployments

// "Run the fleet job now" (owner request, 2026-11-25): with enable_editing, admins can start Rudolph's fleet Lambda
// from /fleet. The module does not know Rudolph's prefix (it takes Rudolph's table by name), so it takes the
// function's name:
//   - variable fleet_function_name: type string, default "" (no button, no grant), a Lambda function name such as
//     production_rudolph_fleet (Rudolph names it ${prefix}_rudolph_fleet), checked by a validation block.
//   - With enable_editing and a name, and only then, the role gets lambda:InvokeFunction and nothing else on exactly
//     arn:aws:lambda:${var.region}:${var.aws_account_id}:function:${var.fleet_function_name}: no wildcard and no
//     qualifier (":*" would allow every version and alias).
//   - The function's environment sets RUDOLPH_WEB_FLEET_FUNCTION from var.fleet_function_name, empty unless
//     var.enable_editing (the app refuses a function without editing).
//   - The example environment declares the variable, passes it through and sets it in config.auto.tfvars.json, which
//     cmd/envcheck then holds existing environments to.
// The rerun guard's marker (FleetHealth / RunRequested) is in the FleetHealth partition the editing statement
// already grants, so no DynamoDB change.

import (
	"regexp"
	"strings"
	"testing"
)

const fleetFunctionARN = "arn:aws:lambda:${var.region}:${var.aws_account_id}:function:${var.fleet_function_name}"

func TestFleetRun_FunctionNameVariable(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	v := requireVariable(t, module, "module", "fleet_function_name", "string", `""`)
	if v == nil {
		return
	}
	if _, ok := v.Body.Attributes["description"]; !ok {
		t.Error("fleet_function_name has no description")
	}
	if len(nested(v, "validation")) == 0 {
		t.Error("fleet_function_name has no validation block: a name that is not a Lambda function name would build a broken ARN")
	}

	example := loadTerraform(t, exampleDir)
	requireVariable(t, example, "example", "fleet_function_name", "string", `""`)
	requirePassedThrough(t, "fleet_function_name")
}

func TestFleetRun_InvokeOnlyTheFleetFunctionWithEditing(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	found := 0
	for _, doc := range blocks(module, "data", "aws_iam_policy_document") {
		for _, st := range nested(doc, "statement") {
			if strings.Contains(bodySource(t, module, st), "lambda:") {
				t.Errorf("an unconditional statement grants a Lambda action:\n%s", bodySource(t, module, st))
			}
		}
		for _, dyn := range nested(doc, "dynamic") {
			src := bodySource(t, module, dyn)
			if !strings.Contains(src, "lambda:") {
				continue
			}
			found++
			g := gate(t, module, dyn)
			if !refersToVar(g, "enable_editing") || !refersToVar(g, "fleet_function_name") {
				t.Errorf("the Lambda statement exists without both enable_editing and fleet_function_name: for_each is %s", g)
			}
			for _, content := range nested(dyn, "content") {
				actions := content.Body.Attributes["actions"]
				resources := content.Body.Attributes["resources"]
				if actions == nil || resources == nil {
					t.Fatalf("the Lambda statement has no actions or resources:\n%s", src)
				}
				if a := strings.Join(strings.Fields(sourceOf(t, module, actions.Expr)), ""); a != `["lambda:InvokeFunction"]` {
					t.Errorf("the Lambda statement's actions are %s, want exactly [\"lambda:InvokeFunction\"]", a)
				}
				if !strings.Contains(sourceOf(t, module, resources.Expr), "statement.value") {
					r := strings.Join(strings.Fields(expandLocals(t, module, sourceOf(t, module, resources.Expr))), "")
					r = regexp.MustCompile(`\(("[^"]*")\)`).ReplaceAllString(r, "$1")
					if r != `["`+fleetFunctionARN+`"]` {
						t.Errorf("the Lambda statement's resources are %s, want exactly [%q]", r, fleetFunctionARN)
					}
				}
			}
			// The ARN may come through for_each (resources = [statement.value]) or a local; either way the block names
			// that one unqualified function and nothing else.
			whole := strings.Join(strings.Fields(expandLocals(t, module, src)), "")
			if strings.Count(whole, fleetFunctionARN) != 1 || strings.Count(whole, "arn:") != 1 {
				t.Errorf("the Lambda statement does not name exactly %s:\n%s", fleetFunctionARN, src)
			}
			if strings.Contains(whole, fleetFunctionARN+":") || strings.Contains(whole, `*"`) {
				t.Errorf("the Lambda statement allows more than the one unqualified function:\n%s", src)
			}
		}
	}
	if found != 1 {
		t.Errorf("%d conditional statements grant a Lambda action, want exactly 1", found)
	}
}

func TestFleetRun_FunctionNameReachesTheFunction(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	line := regexp.MustCompile(`(?m)^\s*RUDOLPH_WEB_FLEET_FUNCTION\s*=\s*(.+)$`)
	var found []string
	for _, f := range module {
		for _, m := range line.FindAllStringSubmatch(string(f.src), -1) {
			found = append(found, expandLocals(t, module, m[1]))
		}
	}
	if len(found) != 1 {
		t.Fatalf("RUDOLPH_WEB_FLEET_FUNCTION is set %d times in the module, want once in the function's environment", len(found))
	}
	if !refersToVar(found[0], "fleet_function_name") || !refersToVar(found[0], "enable_editing") {
		t.Errorf("RUDOLPH_WEB_FLEET_FUNCTION = %s; want var.fleet_function_name, empty unless var.enable_editing", found[0])
	}
}
