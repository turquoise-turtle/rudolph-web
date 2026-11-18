package clicmd_test

import (
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/clicmd"
)

// GroupAdd, like every other command naming a group, offers nothing for a name with invisible characters.
func TestGroupAdd_NoCommandForAGroupWithInvisibleCharacters(t *testing.T) {
	b := clicmd.New("")
	const mac = "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA"
	for _, g := range []string{"eng" + string(rune(0x202e)), "en" + string(rune(0x200b)) + "g", "eng\n"} {
		if cmd, ok := b.GroupAdd(mac, g); ok {
			t.Errorf("group %q: built %q", g, cmd)
		}
	}
	if _, ok := b.GroupAdd(mac, "eng"); !ok {
		t.Error("an ordinary group got no command")
	}
}
