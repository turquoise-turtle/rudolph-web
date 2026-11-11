package deployments

// Group members (owner approved, 2026-11-11): assigning or removing a group writes the Mac's Groups item and its
// GroupMembers#<group> row in one transaction (Rudolph's groups.AddAssigned and RemoveAssigned). IAM checks each item
// of a transaction against UpdateItem or DeleteItem and the leading keys, so the editing write statement must cover the
// GroupMembers# partitions, or every assignment from the web fails.

import (
	"strings"
	"testing"
)

func TestEditing_AssigningAGroupCanWriteItsMemberRow(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	found := false
	for _, doc := range blocks(module, "data", "aws_iam_policy_document") {
		for _, dyn := range nested(doc, "dynamic") {
			if len(dyn.Labels) == 0 || dyn.Labels[0] != "statement" {
				continue
			}
			src := bodySource(t, module, dyn)
			if !strings.Contains(src, "WriteRudolphRules") {
				continue
			}
			found = true
			if !strings.Contains(src, `"GroupMembers#*"`) {
				t.Errorf("the editing write statement's leading keys do not include \"GroupMembers#*\":\n%s", src)
			}
		}
	}
	if !found {
		t.Fatal("no WriteRudolphRules statement found")
	}
}
