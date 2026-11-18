package clicmd_test

import (
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/clicmd"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// The page shows a command's invisible characters as escapes, so a command naming a group with one would not name it
// once copied; none is offered (stored-XSS review, 2026-11-18).
func TestNoCommandForAGroupWithInvisibleCharacters(t *testing.T) {
	b := clicmd.New("")
	for _, g := range []string{"eng\u202e", "en\u200bg", "eng\n", "eng\x1b"} {
		if cmd, ok := b.Rule("deny", store.RuleTypeTeamID, "EQHXZ8M8AV", clicmd.Group(g)); ok {
			t.Errorf("group %q: built %q", g, cmd)
		}
	}
	if _, ok := b.Rule("deny", store.RuleTypeTeamID, "EQHXZ8M8AV", clicmd.Group("工程")); !ok {
		t.Error("a group with ordinary Unicode got no command")
	}
}
