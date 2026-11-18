package web_test

import (
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

// A multi-line CEL expression keeps its line breaks where rules are listed (review, 2026-11-18), rather than showing
// \n; other control characters in it are still escaped.
func TestRules_MultiLineCELKeepsLineBreaks(t *testing.T) {
	s := fake.New()
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeSigningID, Policy: store.PolicyCEL, Identifier: "EQHXZ8M8AV:com.x",
		CELExpr: "target.signing_time >= timestamp('2025-01-01T00:00:00Z') &&\n  args.size() < 3"})
	a := newApp(t, s, 10)
	_, body := a.viewerGet(t, "/rules")
	if strings.Contains(body, `&amp;&amp;\n`) {
		t.Errorf("CEL line break shown as an escape")
	}
	mustContain(t, body, "&amp;&amp;\n  args.size() &lt; 3")
}

// An audit entry's machine target is linked only when it is a machine ID Rudolph would accept.
func TestAudit_LinksOnlyValidMachineTargets(t *testing.T) {
	s := fake.New()
	s.AddAuditEntry(store.AuditEntry{Time: fixedNow, Actor: "cli:a@b", Action: "machine.forget", Target: "machine:..", Summary: "s"})
	s.AddAuditEntry(store.AuditEntry{Time: fixedNow, Actor: "cli:a@b", Action: "machine.forget", Target: "machine:" + editMachine, Summary: "s"})
	a := newEditApp(t, s, true)
	body := a.get(t, "/admin/audit", a.cookie(t, auth.RoleAdmin)).Body.String()
	if strings.Contains(body, `href="/machines/.."`) || strings.Contains(body, `href="/machines/%2e%2e"`) {
		t.Errorf("audit links a machine target that is not a UUID")
	}
	mustContain(t, body, `href="/machines/`+editMachine+`"`)
}
