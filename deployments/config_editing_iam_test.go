package deployments

// Global configuration editing from the web view (step S4 of the admin plan, owner approved 2026-11-11) updates the
// row PK=GlobalConfig, SK=Config, so the editing write statement's leading keys include "GlobalConfig". Machine
// configuration lives under Machine#*, already granted.

import (
	"strings"
	"testing"
)

func TestEditing_GlobalConfigCanBeWritten(t *testing.T) {
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
			if !strings.Contains(src, `"GlobalConfig"`) {
				t.Errorf("the editing write statement's leading keys do not include \"GlobalConfig\":\n%s", src)
			}
			if strings.Contains(src, `"GlobalConfig*"`) || strings.Contains(src, `"Global*"`) {
				t.Error("the GlobalConfig leading key must be exact, not a wildcard")
			}
			return
		}
	}
	t.Fatal("no WriteRudolphRules statement")
}
