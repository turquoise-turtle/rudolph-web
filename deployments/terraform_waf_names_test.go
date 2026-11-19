package deployments

// Terraform audit, review round 2 (2026-11-19): AWS WAFv2 refuses an apostrophe in a web ACL description, and the
// provider does not check, so such a description passes plan and fails apply. The patterns are the ones the WAFv2
// API documents for each field.

import (
	"regexp"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

var wafFieldPatterns = map[string]*regexp.Regexp{
	"description": regexp.MustCompile(`^[\w+=:#@/\-,\.][\w+=:#@/\-,\.\s]+[\w+=:#@/\-,\.]$`),
	"name":        regexp.MustCompile(`^[\w\-]+$`),
	"metric_name": regexp.MustCompile(`^[\w#:\.\-/]+$`),
}

func TestWAF_NamesAndDescriptionsAreAcceptedByWAFv2(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	ctx := &hcl.EvalContext{Variables: map[string]cty.Value{
		"var": cty.ObjectVal(map[string]cty.Value{"prefix": cty.StringVal("prod")}),
	}}
	var walk func(owner string, b *hclsyntax.Body)
	walk = func(owner string, b *hclsyntax.Body) {
		for name, a := range b.Attributes {
			re, ok := wafFieldPatterns[name]
			if !ok {
				continue
			}
			v, diags := a.Expr.Value(ctx)
			if diags.HasErrors() || !v.IsKnown() || v.IsNull() || v.Type() != cty.String {
				t.Errorf("%s: %s = %s cannot be evaluated with prefix prod; it should depend on var.prefix at most", owner, name, sourceOf(t, module, a.Expr))
				continue
			}
			if !re.MatchString(v.AsString()) {
				t.Errorf("%s: %s %q does not match %s, so WAFv2 would refuse it at apply", owner, name, v.AsString(), re)
			}
		}
		for _, nb := range b.Blocks {
			walk(owner, nb.Body)
		}
	}
	found := false
	for _, f := range module {
		for _, b := range f.body.Blocks {
			if b.Type == "resource" && len(b.Labels) == 2 && regexp.MustCompile(`^aws_wafv2_`).MatchString(b.Labels[0]) {
				found = true
				walk(label(b), b.Body)
			}
		}
	}
	if !found {
		t.Fatal("the module defines no aws_wafv2_* resource")
	}
}
