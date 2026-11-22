package clicmd_test

// Commands for a device group's settings and its rules' collection (Rudolph 019bf42):
//   - CollectAllowedEvents takes a group target too: `rule update ... --group '<name>' --collect-allowed-events=true|false`,
//     or `--ungrouped` for rudolph:ungrouped (the contract lines are checked in clicmd_test.go). An empty group name, or
//     one with invisible characters, gets no command, as for the other group commands.
//   - GroupFileAccessOverride(group, action) sets a group's file access override, as `rudolph config update
//     (--group <name>|--ungrouped) --override-file-access-action <none|audit_only|disable>` does, and removes it with
//     `--inherit override-file-access-action` when action is "". Rudolph's cli-examples.txt has no line for it, so the
//     format is docs/cli.md's ("Configuration"), with the value in lower case as there and the builder's --yes. Values
//     are the store's names (NONE, AUDIT_ONLY, DISABLE); anything else gets no command.

import (
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/clicmd"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

func TestGroupCollectionCommands(t *testing.T) {
	b := clicmd.New("prod")
	if cmd, ok := b.CollectAllowedEvents(store.RuleTypeBinary, sha("e"), clicmd.Group("lab macs"), false); !ok || cmd != "ENV=prod rudolph rule update -t binary -i '"+sha("e")+"' --group 'lab macs' --collect-allowed-events=false --yes" {
		t.Errorf("group with a space: %q, %v", cmd, ok)
	}
	if cmd, ok := b.CollectAllowedEvents(store.RuleTypeTeamID, "ABCDE12345", clicmd.Group(""), true); ok {
		t.Errorf("an empty group name: built %q", cmd)
	}
	if cmd, ok := b.CollectAllowedEvents(store.RuleTypeTeamID, "ABCDE'1234", clicmd.Group("eng"), true); ok {
		t.Errorf("a team ID of the wrong shape: built %q", cmd)
	}
}

func TestGroupFileAccessOverrideCommands(t *testing.T) {
	b := clicmd.New("prod")
	for _, c := range []struct {
		group, action, want string
	}{
		{"eng", "NONE", "ENV=prod rudolph config update --group 'eng' --override-file-access-action none --yes"},
		{"eng", "AUDIT_ONLY", "ENV=prod rudolph config update --group 'eng' --override-file-access-action audit_only --yes"},
		{"eng", "DISABLE", "ENV=prod rudolph config update --group 'eng' --override-file-access-action disable --yes"},
		{"eng", "", "ENV=prod rudolph config update --group 'eng' --inherit override-file-access-action --yes"},
		{store.UngroupedGroup, "AUDIT_ONLY", "ENV=prod rudolph config update --ungrouped --override-file-access-action audit_only --yes"},
		{store.UngroupedGroup, "", "ENV=prod rudolph config update --ungrouped --inherit override-file-access-action --yes"},
		{"o'reilly team", "DISABLE", `ENV=prod rudolph config update --group 'o'\''reilly team' --override-file-access-action disable --yes`},
	} {
		got, ok := b.GroupFileAccessOverride(c.group, c.action)
		if !ok || got != c.want {
			t.Errorf("GroupFileAccessOverride(%q, %q) = %q, %v; want %q", c.group, c.action, got, ok, c.want)
		}
	}
	for _, c := range []struct{ group, action string }{
		{"eng", "PERMISSIVE"},
		{"eng", "audit_only; rm -rf ~"},
		{"eng\u200b", "NONE"},
		{"", "NONE"},
	} {
		if got, ok := b.GroupFileAccessOverride(c.group, c.action); ok {
			t.Errorf("GroupFileAccessOverride(%q, %q) built %q", c.group, c.action, got)
		}
	}
	if got, ok := clicmd.New("").GroupFileAccessOverride("eng", "NONE"); !ok || got != "rudolph config update --group 'eng' --override-file-access-action none --yes" {
		t.Errorf("no environment: %q, %v", got, ok)
	}
}
