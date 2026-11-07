package deployments

import (
	"strings"
	"testing"
)

// The web role may add audit entries but never change or delete them (review of v2-editing): Audit# appears only in a
// PutItem-only write statement.
func TestEditing_AuditEntriesArePutOnly(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	sawAudit := false
	for _, doc := range blocks(module, "data", "aws_iam_policy_document") {
		for _, dyn := range nested(doc, "dynamic") {
			if len(dyn.Labels) == 0 || dyn.Labels[0] != "statement" {
				continue
			}
			src := bodySource(t, module, dyn)
			if !strings.Contains(src, "Audit#") {
				continue
			}
			sawAudit = true
			// Each for_each element is one statement; check the element that names Audit#.
			for _, part := range strings.Split(src, "}") {
				if !strings.Contains(part, "Audit#") {
					continue
				}
				if strings.Contains(part, "UpdateItem") || strings.Contains(part, "DeleteItem") {
					t.Errorf("a statement granting UpdateItem or DeleteItem covers Audit#:\n%s", part)
				}
			}
		}
	}
	if !sawAudit {
		t.Fatal("no write statement covers Audit#")
	}
}
