package deployments

// Production readiness (owner, 2026-11-07): the first deployment is production, in an account that also holds other
// resources. rudolph-web gets its own monthly cost budget with email alerts, and environment directories (account
// ID, tenant and client IDs, domain) can never be committed.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebHasAMonthlyCostBudget(t *testing.T) {
	module := loadTerraform(t, moduleDir)

	for name, want := range map[string]string{"monthly_budget_usd": "0", "budget_alert_emails": "[]"} {
		vs := blocks(module, "variable", name)
		if len(vs) != 1 {
			t.Errorf("variable %q declared %d times, want once", name, len(vs))
			continue
		}
		d, ok := vs[0].Body.Attributes["default"]
		if got := ""; ok {
			got = strings.TrimSpace(sourceOf(t, module, d.Expr))
			if got != want {
				t.Errorf("variable %q default %s, want %s", name, got, want)
			}
		} else {
			t.Errorf("variable %q has no default, want %s", name, want)
		}
	}

	budgets := blocks(module, "resource", "aws_budgets_budget")
	if len(budgets) != 1 {
		t.Fatalf("found %d aws_budgets_budget resources, want 1", len(budgets))
	}
	b := budgets[0]
	src := bodySource(t, module, b)
	if c, ok := b.Body.Attributes["count"]; !ok || !strings.Contains(sourceOf(t, module, c.Expr), "var.monthly_budget_usd") {
		t.Error("the budget is not conditional on var.monthly_budget_usd")
	}
	for attr, want := range map[string]string{"budget_type": "COST", "time_unit": "MONTHLY", "limit_unit": "USD"} {
		a, ok := b.Body.Attributes[attr]
		if v, _ := literal(a); !ok || v != want {
			t.Errorf("budget %s = %q, want %q", attr, v, want)
		}
	}
	if a, ok := b.Body.Attributes["limit_amount"]; !ok || !strings.Contains(sourceOf(t, module, a.Expr), "var.monthly_budget_usd") {
		t.Error("budget limit_amount does not come from var.monthly_budget_usd")
	}
	if a, ok := b.Body.Attributes["name"]; !ok || !strings.Contains(sourceOf(t, module, a.Expr), "var.prefix") {
		t.Error("budget name does not include var.prefix")
	}
	// Only rudolph-web's own costs: every resource carries the provider's default tag Name = rudolph-web.
	if !strings.Contains(src, `"TagKeyValue"`) || !strings.Contains(src, `user:Name$rudolph-web`) {
		t.Errorf("the budget is not filtered to the Name = rudolph-web cost allocation tag:\n%s", src)
	}

	type note struct{ kind, threshold string }
	seen := map[note]bool{}
	for _, n := range nested(b, "notification") {
		kind, _ := literal(n.Body.Attributes["notification_type"])
		cmp, _ := literal(n.Body.Attributes["comparison_operator"])
		ttype, _ := literal(n.Body.Attributes["threshold_type"])
		th := ""
		if a, ok := n.Body.Attributes["threshold"]; ok {
			th = strings.TrimSpace(sourceOf(t, module, a.Expr))
		}
		if subs, ok := n.Body.Attributes["subscriber_email_addresses"]; !ok || !strings.Contains(sourceOf(t, module, subs.Expr), "var.budget_alert_emails") {
			t.Errorf("notification %s %s%% does not go to var.budget_alert_emails", kind, th)
		}
		if cmp != "GREATER_THAN" || ttype != "PERCENTAGE" {
			t.Errorf("notification %s %s: comparison %q, threshold type %q; want GREATER_THAN and PERCENTAGE", kind, th, cmp, ttype)
		}
		seen[note{kind, th}] = true
	}
	for _, want := range []note{{"ACTUAL", "80"}, {"ACTUAL", "100"}, {"FORECASTED", "100"}} {
		if !seen[want] {
			t.Errorf("no %s notification at %s%%", want.kind, want.threshold)
		}
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
	for _, v := range []string{"monthly_budget_usd", "budget_alert_emails"} {
		if !strings.Contains(main, v) {
			t.Errorf("the example environment does not pass %s to the module", v)
		}
		if _, ok := cfg[v]; !ok {
			t.Errorf("config.auto.tfvars.json has no %q", v)
		}
	}
}

// Real environments hold the account ID, tenant and client IDs and the domain. new_env.sh writes a .gitignore of `*`
// into each, but a directory made by hand has none, so the repository's own .gitignore must ignore them too.
func TestEnvironmentDirectoriesAreNeverCommitted(t *testing.T) {
	for path, ignored := range map[string]bool{
		"deployments/environments/prod/config.auto.tfvars.json":    true,
		"deployments/environments/prod/_backend.tf":                true,
		"deployments/environments/example/config.auto.tfvars.json": false,
		"deployments/environments/example/main.tf":                 false,
	} {
		err := exec.Command("git", "-C", "..", "check-ignore", "-q", "--no-index", path).Run()
		if got := err == nil; got != ignored {
			t.Errorf("%s: ignored %v, want %v", path, got, ignored)
		}
	}
}
