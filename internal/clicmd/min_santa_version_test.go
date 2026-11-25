package clicmd_test

// The minimum Santa version's command (Rudolph 05f9516, docs/cli.md "fleet min-santa-version"):
//
//	rudolph fleet min-santa-version (<version> | off | --default) [--yes|--dry-run]
//
// FleetMinSantaVersion(version) builds it for the /admin/config page, with the builder's ENV= prefix and --yes like
// every other command the web view shows. version is what the admin typed, checked and normalised by Rudolph's
// fleethealth.ValidateMinSantaVersion (surrounding spaces trimmed, OFF in any case as off); "" builds --default. A
// release of digits and dots is written as it is; any other release Rudolph accepts (one with a suffix such as
// 2026.10-beta) is single-quoted, so a shell never splits or expands it. A value Rudolph refuses builds nothing.
// Rudolph's cli-examples.txt has no line for it, so the format is docs/cli.md's.

import (
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/clicmd"
)

func TestFleetMinSantaVersionCommands(t *testing.T) {
	b := clicmd.New("prod")
	for _, c := range []struct{ version, want string }{
		{"2026.10", "ENV=prod rudolph fleet min-santa-version 2026.10 --yes"},
		{" 2026.10.1 ", "ENV=prod rudolph fleet min-santa-version 2026.10.1 --yes"},
		{"off", "ENV=prod rudolph fleet min-santa-version off --yes"},
		{"OFF", "ENV=prod rudolph fleet min-santa-version off --yes"},
		{"", "ENV=prod rudolph fleet min-santa-version --default --yes"},
		{"2026.10-beta", "ENV=prod rudolph fleet min-santa-version '2026.10-beta' --yes"},
		{"2026.10-it's", `ENV=prod rudolph fleet min-santa-version '2026.10-it'\''s' --yes`},
		{"2026.10-$HOME", "ENV=prod rudolph fleet min-santa-version '2026.10-$HOME' --yes"},
	} {
		got, ok := b.FleetMinSantaVersion(c.version)
		if !ok || got != c.want {
			t.Errorf("FleetMinSantaVersion(%q) = %q, %v; want %q", c.version, got, ok, c.want)
		}
	}
	if got, ok := clicmd.New("").FleetMinSantaVersion("2026.10"); !ok || got != "rudolph fleet min-santa-version 2026.10 --yes" {
		t.Errorf("without an environment: %q, %v", got, ok)
	}
	for _, bad := range []string{"latest", "2026", "2026.x", "  ", "2026.10\u202e", "2026.10\u200b", "2026.10\x1b[2J",
		"2026." + strings.Repeat("1", 30), "--default", "-y"} {
		if got, ok := b.FleetMinSantaVersion(bad); ok {
			t.Errorf("FleetMinSantaVersion(%q) built %q; Rudolph refuses the value", bad, got)
		}
	}
}
