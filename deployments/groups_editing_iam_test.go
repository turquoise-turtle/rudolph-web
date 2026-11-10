package deployments

// Group management from the web view (owner request) deletes a group's rules with Rudolph's groups.DeleteGroup, which
// removes them with BatchWriteItem (its own IAM action, unlike TransactWriteItems), so the editing write statement
// grants it, still limited to Rudolph's partitions by dynamodb:LeadingKeys.

import (
	"strings"
	"testing"
)

func TestEditing_GroupDeleteCanRemoveTheGroupsRules(t *testing.T) {
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
			if !strings.Contains(src, `"dynamodb:BatchWriteItem"`) {
				t.Errorf("the editing write statement does not grant dynamodb:BatchWriteItem, which deleting a group with its rules needs:\n%s", src)
			}
			for _, key := range []string{`"GroupRules#*"`, `"GroupRegistry"`, `"Machine#*"`} {
				if !strings.Contains(src, key) {
					t.Errorf("the editing write statement's leading keys do not include %s", key)
				}
			}
			return
		}
	}
	t.Fatal("no WriteRudolphRules statement")
}
