//go:build integration

package dynamo_test

// CEL rules from the web reach Rudolph's rows with their expression (CELExpr), for global, group and machine rules,
// and an expression Santa's CEL v1 would refuse is ErrInvalidArgument with nothing written.

import (
	"errors"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

func TestAddRule_CELExpression(t *testing.T) {
	st := newStore(t)
	const expr = "args.exists(a, a in ['remove', '-R', '-D']) ? BLOCKLIST : ALLOWLIST"
	rule := store.NewRule{RuleType: store.RuleTypeSigningID, Identifier: "platform:com.apple.profiles", Policy: store.PolicyCEL, CELExpr: expr}
	if err := st.AddRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetGlobal}, rule); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateGroup(ctx, writerActor, "cel", "", true); err != nil {
		t.Fatal(err)
	}
	if err := st.AddRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetGroup, Group: "cel"}, rule); err != nil {
		t.Fatal(err)
	}
	global, err := st.ListGlobalRules(ctx, store.RuleFilter{}, store.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	group, err := st.ListGroupRules(ctx, "cel", store.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	// The web contract fixture already holds other rules, so look the new one up by its identifier.
	for name, rules := range map[string][]store.Rule{"global": global.Items, "group": group.Items} {
		found := false
		for _, r := range rules {
			if r.Identifier == rule.Identifier {
				found = r.Policy == store.PolicyCEL && r.CELExpr == expr
			}
		}
		if !found {
			t.Errorf("%s rules %+v; want %s as a CEL rule with its expression", name, rules, rule.Identifier)
		}
	}

	for _, bad := range []store.NewRule{
		{RuleType: store.RuleTypeTeamID, Identifier: "EQHXZ8M8AV", Policy: store.PolicyCEL, CELExpr: "AUDIT"},
		{RuleType: store.RuleTypeTeamID, Identifier: "EQHXZ8M8AV", Policy: store.PolicyCEL, CELExpr: "args.exists(a,"},
		{RuleType: store.RuleTypeTeamID, Identifier: "EQHXZ8M8AV", Policy: store.PolicyCEL},
		{RuleType: store.RuleTypeTeamID, Identifier: "EQHXZ8M8AV", Policy: store.PolicyBlocklist, CELExpr: "true"},
	} {
		if err := st.AddRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetGlobal}, bad); !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("%+v: %v, want ErrInvalidArgument", bad, err)
		}
	}
	if after, _ := st.ListGlobalRules(ctx, store.RuleFilter{}, store.PageRequest{Limit: 100}); len(after.Items) != len(global.Items) {
		t.Errorf("refused rules were written: %+v", after.Items)
	}
}
