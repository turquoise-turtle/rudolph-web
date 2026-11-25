package deployments

// "Run the ranker now" (owner request, 2026-11-25): with enable_editing, admins can start Rudolph's ranker Lambda
// from /admin, as they can the fleet job (fleet_run_test.go). The module takes the function's name:
//   - variable ranker_function_name: type string, default "" (no button, no grant), a Lambda function name such as
//     production_rudolph_ranker (Rudolph names it ${prefix}_rudolph_ranker), checked by a validation block.
//   - With enable_editing and a name, and only then, a dynamic statement of its own gives the role
//     lambda:InvokeFunction and nothing else on exactly
//     arn:aws:lambda:${var.region}:${var.aws_account_id}:function:${var.ranker_function_name}: no wildcard and no
//     qualifier. It is separate from the fleet job's statement, so either function can be set alone.
//   - Every Lambda statement of the role names exactly one of the two functions.
//   - The function's environment sets RUDOLPH_WEB_RANKER_FUNCTION from var.ranker_function_name, empty unless
//     var.enable_editing.
//   - The example environment declares the variable, passes it through and sets it in config.auto.tfvars.json, which
//     cmd/envcheck then holds existing environments to.
// The rerun guard's marker (FleetHealth / RankerRunRequested) is in the FleetHealth partition the editing statement
// already grants, so no DynamoDB change.

import (
	"regexp"
	"strings"
	"testing"
)

const rankerFunctionARN = "arn:aws:lambda:${var.region}:${var.aws_account_id}:function:${var.ranker_function_name}"

func TestRankerRun_FunctionNameVariable(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	v := requireVariable(t, module, "module", "ranker_function_name", "string", `""`)
	if v == nil {
		return
	}
	if _, ok := v.Body.Attributes["description"]; !ok {
		t.Error("ranker_function_name has no description")
	}
	if len(nested(v, "validation")) == 0 {
		t.Error("ranker_function_name has no validation block: a name that is not a Lambda function name would build a broken ARN")
	}

	example := loadTerraform(t, exampleDir)
	requireVariable(t, example, "example", "ranker_function_name", "string", `""`)
	requirePassedThrough(t, "ranker_function_name")
}

func TestRankerRun_InvokeOnlyTheRankerFunctionWithEditing(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	found := 0
	for _, doc := range blocks(module, "data", "aws_iam_policy_document") {
		for _, dyn := range nested(doc, "dynamic") {
			src := bodySource(t, module, dyn)
			if !strings.Contains(src, "lambda:") {
				continue
			}
			fleet, ranker := strings.Contains(src, "fleet_function_name"), strings.Contains(src, "ranker_function_name")
			if fleet == ranker {
				t.Errorf("a Lambda statement names %s; want exactly one of fleet_function_name and ranker_function_name:\n%s",
					map[bool]string{true: "both functions", false: "neither function"}[fleet], src)
				continue
			}
			if !ranker {
				continue
			}
			found++
			g := gate(t, module, dyn)
			if !refersToVar(g, "enable_editing") || !refersToVar(g, "ranker_function_name") {
				t.Errorf("the ranker's Lambda statement exists without both enable_editing and ranker_function_name: for_each is %s", g)
			}
			for _, content := range nested(dyn, "content") {
				actions := content.Body.Attributes["actions"]
				resources := content.Body.Attributes["resources"]
				if actions == nil || resources == nil {
					t.Fatalf("the ranker's Lambda statement has no actions or resources:\n%s", src)
				}
				if a := strings.Join(strings.Fields(sourceOf(t, module, actions.Expr)), ""); a != `["lambda:InvokeFunction"]` {
					t.Errorf("the ranker's Lambda statement's actions are %s, want exactly [\"lambda:InvokeFunction\"]", a)
				}
				if !strings.Contains(sourceOf(t, module, resources.Expr), "statement.value") {
					r := strings.Join(strings.Fields(expandLocals(t, module, sourceOf(t, module, resources.Expr))), "")
					r = regexp.MustCompile(`\(("[^"]*")\)`).ReplaceAllString(r, "$1")
					if r != `["`+rankerFunctionARN+`"]` {
						t.Errorf("the ranker's Lambda statement's resources are %s, want exactly [%q]", r, rankerFunctionARN)
					}
				}
			}
			whole := strings.Join(strings.Fields(expandLocals(t, module, src)), "")
			if strings.Count(whole, rankerFunctionARN) != 1 || strings.Count(whole, "arn:") != 1 {
				t.Errorf("the ranker's Lambda statement does not name exactly %s:\n%s", rankerFunctionARN, src)
			}
			if strings.Contains(whole, rankerFunctionARN+":") || strings.Contains(whole, `*"`) {
				t.Errorf("the ranker's Lambda statement allows more than the one unqualified function:\n%s", src)
			}
		}
	}
	if found != 1 {
		t.Errorf("%d conditional statements grant a Lambda action on the ranker function, want exactly 1", found)
	}
}

func TestRankerRun_FunctionNameReachesTheFunction(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	line := regexp.MustCompile(`(?m)^\s*RUDOLPH_WEB_RANKER_FUNCTION\s*=\s*(.+)$`)
	var found []string
	for _, f := range module {
		for _, m := range line.FindAllStringSubmatch(string(f.src), -1) {
			found = append(found, expandLocals(t, module, m[1]))
		}
	}
	if len(found) != 1 {
		t.Fatalf("RUDOLPH_WEB_RANKER_FUNCTION is set %d times in the module, want once in the function's environment", len(found))
	}
	if !refersToVar(found[0], "ranker_function_name") || !refersToVar(found[0], "enable_editing") {
		t.Errorf("RUDOLPH_WEB_RANKER_FUNCTION = %s; want var.ranker_function_name, empty unless var.enable_editing", found[0])
	}
}
