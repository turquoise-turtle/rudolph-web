package deployments

// IAM review (2026-11-18, owner approved fixing every finding):
//   - The audit trail's PutItem cannot carry ExpiresAfter (dynamodb:Attributes): Rudolph's audit entries never have
//     one, and a PutItem replacing an entry with a past ExpiresAfter would let the store table's TTL delete it.
//   - Every ForAllValues condition on dynamodb:LeadingKeys has a Null check beside it, as ForAllValues is true when a
//     request carries no key at all.
//   - Comments, variable descriptions and the deploy doc describe what the role really has: the managed policy is
//     three logs actions on every group, not logs:*; the role also has BatchGetItem and, with editing, BatchWriteItem;
//     an audit entry can be replaced with PutItem, so the docs must not say the role cannot touch one.

import (
	"os"
	"strings"
	"testing"
)

func TestIAMReview_AuditPutCannotSetAnExpiry(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	found := false
	for _, doc := range blocks(module, "data", "aws_iam_policy_document") {
		for _, dyn := range nested(doc, "dynamic") {
			src := bodySource(t, module, dyn)
			if !strings.Contains(src, "AppendRudolphAudit") {
				continue
			}
			found = true
			for _, want := range []string{"dynamodb:Attributes", `"ExpiresAfter"`, "ForAllValues:StringNotEquals"} {
				if !strings.Contains(src, want) {
					t.Errorf("the audit statement lacks %s", want)
				}
			}
		}
	}
	if !found {
		t.Fatal("no AppendRudolphAudit statement found")
	}
}

func TestIAMReview_LeadingKeysConditionsRequireTheKey(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	checked := 0
	for _, doc := range blocks(module, "data", "aws_iam_policy_document") {
		var stmts []string
		for _, st := range nested(doc, "statement") {
			stmts = append(stmts, bodySource(t, module, st))
		}
		for _, dyn := range nested(doc, "dynamic") {
			stmts = append(stmts, bodySource(t, module, dyn))
		}
		for _, src := range stmts {
			if !strings.Contains(src, "dynamodb:LeadingKeys") || !strings.Contains(src, "ForAllValues:") {
				continue
			}
			checked++
			if !strings.Contains(src, `"Null"`) {
				t.Errorf("a ForAllValues LeadingKeys condition without a Null check beside it:\n%s", src)
			}
		}
	}
	if checked == 0 {
		t.Error("found no LeadingKeys conditions; the check is out of date")
	}
}

func TestIAMReview_DescriptionsMatchTheRole(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if strings.Contains(read(moduleDir+"/rudolph_web/iam.tf"), "logs:*") {
		t.Error("iam.tf says the managed policy allows logs:*; it allows CreateLogGroup, CreateLogStream and PutLogEvents on every group")
	}
	vars := read(moduleDir + "/rudolph_web/_variables.tf")
	for _, want := range []string{"BatchGetItem", "BatchWriteItem"} {
		if !strings.Contains(vars, want) {
			t.Errorf("_variables.tf descriptions do not mention %s, which the role has", want)
		}
	}
	if d := read("../docs/deploy.md"); strings.Contains(d, "cannot delete or update") || strings.Contains(d, "cannot update or delete") {
		t.Error("docs/deploy.md says the role cannot delete or update an audit entry; a PutItem can replace one")
	}
}
