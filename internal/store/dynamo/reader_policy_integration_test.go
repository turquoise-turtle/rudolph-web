//go:build integration

package dynamo_test

// Step 7 of docs/data-layer-design.md: global rules by policy, from Rudolph's GSI GlobalRulePolicy_SK (hash
// GlobalRulePolicy, range SK, projection ALL; GlobalRulePolicy is set on global rule rows only). One Query per page,
// never a FilterExpression, which would return short pages. A rule type narrows the same Query with begins_with(SK).
//
// Fixture global rules by policy: ALLOWLIST (1) are one Binary rule, the Cert rule and the TeamID rule; BLOCKLIST (2)
// are one Binary rule and the CDHash rule.

import (
	"errors"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

// indexesUsed records the IndexName of every Query from now on ("" for the table).
func indexesUsed(t *testing.T) *[]string {
	t.Helper()
	var names []string
	ddblocal.AddHook(t, ddblocal.Hook{Before: func(c *ddblocal.Call) *ddblocal.Fault {
		if c.Op == "Query" {
			n, _ := c.Input["IndexName"].(string)
			names = append(names, n)
		}
		return nil
	}})
	return &names
}

func allGlobalRules(t *testing.T, f store.RuleFilter, limit int) ([]store.Rule, int, *[]string, *[]int) {
	t.Helper()
	s := newStore(t)
	indexes := indexesUsed(t)
	limits := limitsUsed(t)
	var got []store.Rule
	cursor := ""
	pages := 0
	for {
		page, err := s.ListGlobalRules(ctx, f, store.PageRequest{Limit: limit, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", pages+1, err)
		}
		pages++
		got = append(got, page.Items...)
		if page.NextCursor == "" || pages > 20 {
			return got, pages, indexes, limits
		}
		cursor = page.NextCursor
	}
}

func ruleKeys(rs []store.Rule) string {
	var out []string
	for _, r := range rs {
		out = append(out, string(r.RuleType)+":"+string(r.Policy))
	}
	return strings.Join(out, ",")
}

func TestListGlobalRules_ByPolicy(t *testing.T) {
	got, pages, indexes, limits := allGlobalRules(t, store.RuleFilter{Policy: store.PolicyAllowlist}, 1)
	if want := "BINARY:ALLOWLIST,CERTIFICATE:ALLOWLIST,TEAMID:ALLOWLIST"; ruleKeys(got) != want {
		t.Errorf("allowlist rules %s, want %s (in SK order)", ruleKeys(got), want)
	}
	queriesOnly(t, pages)
	for i, n := range *indexes {
		if n != "GlobalRulePolicy_SK" {
			t.Errorf("query %d used index %q, want GlobalRulePolicy_SK", i, n)
		}
	}
	for _, l := range *limits {
		if l != 1 {
			t.Errorf("Query Limit %d, want the page size 1", l)
		}
	}
	// The index projects every attribute, so a rule from it is as complete as one from the table.
	all, _, _, _ := allGlobalRules(t, store.RuleFilter{RuleType: store.RuleTypeTeamID}, 10)
	if len(got) == 3 && len(all) == 1 && got[2] != all[0] {
		t.Errorf("rule from the policy index %+v differs from the table's %+v", got[2], all[0])
	}
}

func TestListGlobalRules_ByPolicyAndType(t *testing.T) {
	for _, c := range []struct {
		f    store.RuleFilter
		want string
	}{
		{store.RuleFilter{Policy: store.PolicyBlocklist}, "BINARY:BLOCKLIST,CDHASH:BLOCKLIST"},
		{store.RuleFilter{Policy: store.PolicyBlocklist, RuleType: store.RuleTypeBinary}, "BINARY:BLOCKLIST"},
		{store.RuleFilter{Policy: store.PolicyAllowlist, RuleType: store.RuleTypeCertificate}, "CERTIFICATE:ALLOWLIST"},
		{store.RuleFilter{Policy: store.PolicyRemove}, ""},
	} {
		got, pages, _, _ := allGlobalRules(t, c.f, 10)
		if ruleKeys(got) != c.want {
			t.Errorf("%+v: %s, want %s", c.f, ruleKeys(got), c.want)
		}
		if pages != 1 {
			t.Errorf("%+v: %d pages, want 1", c.f, pages)
		}
	}
}

func TestListGlobalRules_PolicyCursorBoundToTheFilter(t *testing.T) {
	s := newStore(t)
	page, err := s.ListGlobalRules(ctx, store.RuleFilter{Policy: store.PolicyAllowlist}, store.PageRequest{Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("precondition: %v %+v", err, page)
	}
	plain, err := s.ListGlobalRules(ctx, store.RuleFilter{}, store.PageRequest{Limit: 1})
	if err != nil || plain.NextCursor == "" {
		t.Fatalf("precondition: %v %+v", err, plain)
	}
	for name, c := range map[string]struct {
		f      store.RuleFilter
		cursor string
	}{
		"allowlist cursor for blocklist":       {store.RuleFilter{Policy: store.PolicyBlocklist}, page.NextCursor},
		"allowlist cursor without a policy":    {store.RuleFilter{}, page.NextCursor},
		"allowlist cursor with a type as well": {store.RuleFilter{Policy: store.PolicyAllowlist, RuleType: store.RuleTypeBinary}, page.NextCursor},
		"table cursor for a policy":            {store.RuleFilter{Policy: store.PolicyAllowlist}, plain.NextCursor},
	} {
		if _, err := s.ListGlobalRules(ctx, c.f, store.PageRequest{Limit: 1, Cursor: c.cursor}); !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("%s: got %v, want store.ErrInvalidCursor", name, err)
		}
	}
	if _, err := s.ListGlobalRules(ctx, store.RuleFilter{Policy: store.PolicyAllowlist}, store.PageRequest{Limit: 2, Cursor: page.NextCursor}); err != nil {
		t.Errorf("the same filter with another page size may continue: %v", err)
	}
}
