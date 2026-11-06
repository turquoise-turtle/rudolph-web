package clicmd_test

// Copy-and-paste Rudolph CLI commands (Rudolph's plan section F1). The strings are a contract with the CLI:
// testdata/webcontract/cli-examples.txt (vendored from Rudolph, where a test runs every line as a dry run through the
// real command tree) lists what the builder renders for its example values. Every value can come from a Mac, so each
// identifier is checked against its type's shape and no command is built for one that does not fit; arguments are
// single-quoted anyway.

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/clicmd"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

const machine = "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA"

func sha(last string) string { return strings.Repeat("0", 64-len(last)) + last }

func examples(t *testing.T) []string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	f, err := os.Open(filepath.Join(filepath.Dir(file), "..", "..", "testdata", "webcontract", "cli-examples.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		if l := strings.TrimSpace(s.Text()); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	return lines
}

// must(t)(b.Rule(…)) fails the test when no command was built.
func must(t *testing.T) func(string, bool) string {
	return func(cmd string, ok bool) string {
		t.Helper()
		if !ok {
			t.Fatalf("no command built")
		}
		return cmd
	}
}

func TestBuilderRendersExactlyTheContractExamples(t *testing.T) {
	b := clicmd.New("prod")
	got := []string{
		must(t)(b.Rule("deny", store.RuleTypeBinary, sha("e"), clicmd.Global())),
		must(t)(b.Rule("allow", store.RuleTypeBinary, sha("e"), clicmd.Global())),
		must(t)(b.Rule("allow", store.RuleTypeBinary, sha("e"), clicmd.Machine(machine, "24h"))),
		must(t)(b.Rule("deny", store.RuleTypeSigningID, "EQHXZ8M8AV:com.google.GeminiMacOS", clicmd.Global())),
		must(t)(b.Rule("deny", store.RuleTypeSigningID, "EQHXZ8M8AV:com.google.GeminiMacOS", clicmd.Group("eng"))),
		must(t)(b.Rule("allow", store.RuleTypeTeamID, "ABCDE12345", clicmd.Global())),
		must(t)(b.Rule("deny", store.RuleTypeCertificate, sha("f"), clicmd.Global())),
		must(t)(b.Rule("deny", store.RuleTypeCDHash, "ffeeddccbbaa99887766554433221100ffeeddcc", clicmd.Global())),
		must(t)(b.CollectAllowedEvents(store.RuleTypeTeamID, "EQHXZ8M8AV", clicmd.Global(), true)),
		must(t)(b.CollectAllowedEvents(store.RuleTypeTeamID, "EQHXZ8M8AV", clicmd.Global(), false)),
		must(t)(b.Remove(store.RuleTypeBinary, sha("1"), clicmd.Global())),
		must(t)(b.Remove(store.RuleTypeBinary, sha("b"), clicmd.Machine(machine, ""))),
		must(t)(b.MachineRules(machine)),
		must(t)(b.ConfigGet(machine)),
		must(t)(b.TemporaryMode(machine, store.ClientModeMonitor, "2h")),
		must(t)(b.EndTemporaryMode(machine)),
		must(t)(b.GroupAdd(machine, "eng")),
	}
	want := examples(t)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rendered commands differ from the contract\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestIdentifiersOfTheWrongShapeGetNoCommand(t *testing.T) {
	b := clicmd.New("prod")
	for name, c := range map[string]struct {
		t  store.RuleType
		id string
	}{
		"binary not hex":       {store.RuleTypeBinary, "$(rm -rf ~)" + strings.Repeat("a", 53)},
		"binary short":         {store.RuleTypeBinary, "abc"},
		"cdhash wrong length":  {store.RuleTypeCDHash, strings.Repeat("a", 64)},
		"team id with a quote": {store.RuleTypeTeamID, "ABCDE'1234"},
		"team id too long":     {store.RuleTypeTeamID, "ABCDE123456"},
		"signing id no team":   {store.RuleTypeSigningID, "com.google.Chrome"},
		"signing id injection": {store.RuleTypeSigningID, "EQHXZ8M8AV:com.x'; rm -rf ~; echo '"},
		"certificate not hex":  {store.RuleTypeCertificate, strings.Repeat("z", 64)},
	} {
		if cmd, ok := b.Rule("deny", c.t, c.id, clicmd.Global()); ok {
			t.Errorf("%s: built %q", name, cmd)
		}
	}
	if cmd, ok := b.ConfigGet("not-a-uuid"); ok {
		t.Errorf("a machine ID that is not a UUID: built %q", cmd)
	}
	if cmd, ok := b.Rule("deny", store.RuleTypeBinary, sha("e"), clicmd.Machine("AAAA'; rm -rf ~", "24h")); ok {
		t.Errorf("a hostile machine ID: built %q", cmd)
	}
}

// A platform binary's signing ID has the prefix "platform:" instead of a team ID.
func TestPlatformSigningIDs(t *testing.T) {
	cmd, ok := clicmd.New("prod").Rule("deny", store.RuleTypeSigningID, "platform:com.apple.Terminal", clicmd.Global())
	if !ok || !strings.Contains(cmd, "-i 'platform:com.apple.Terminal'") {
		t.Errorf("platform signing ID: %q, %v", cmd, ok)
	}
}

// Group names are registered in Rudolph; whatever they hold, the argument is quoted so a shell cannot split it.
func TestGroupNamesAreQuotedSafely(t *testing.T) {
	cmd, ok := clicmd.New("prod").GroupAdd(machine, "o'reilly team")
	if !ok || !strings.Contains(cmd, `'o'\''reilly team'`) {
		t.Errorf("group name with a quote: %q, %v", cmd, ok)
	}
}

// Without an environment name the commands start with `rudolph`, for an operator who exports ENV themselves.
func TestWithoutAnEnvironment(t *testing.T) {
	cmd, ok := clicmd.New("").ConfigGet(machine)
	if !ok || cmd != "rudolph config get --machine '"+machine+"'" {
		t.Errorf("no environment: %q", cmd)
	}
}
