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
	if _, err := st.CreateGroup(ctx, writerActor, "eng", "", true); err != nil {
		t.Fatal(err)
	}
	if err := st.AddRule(ctx, writerActor, store.RuleTarget{Kind: store.TargetGroup, Group: "eng"}, rule); err != nil {
		t.Fatal(err)
	}
	global, err := st.ListGlobalRules(ctx, store.RuleFilter{}, store.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	group, err := st.ListGroupRules(ctx, "eng", store.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for name, rules := range map[string][]store.Rule{"global": global.Items, "group": group.Items} {
		if len(rules) != 1 || rules[0].Policy != store.PolicyCEL || rules[0].CELExpr != expr {
			t.Errorf("%s rules %+v; want the CEL rule with its expression", name, rules)
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
	if after, _ := st.ListGlobalRules(ctx, store.RuleFilter{}, store.PageRequest{Limit: 10}); len(after.Items) != 1 {
		t.Errorf("refused rules were written: %+v", after.Items)
	}
}
