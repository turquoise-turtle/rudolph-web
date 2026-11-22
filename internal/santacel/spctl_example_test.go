package santacel_test

// The spctl example (owner, 2026-11-22): a SIGNINGID rule on platform:com.apple.spctl (codesign: Identifier
// com.apple.spctl, a platform binary) that blocks turning Gatekeeper off or loosening it from the command line, and
// allows everything that only reads or assesses. Blocked: --global-disable, the older --master-disable and --disable
// spellings, the deprecated --add and --remove rule edits, and developer-mode enable-terminal (which lets Terminal run
// unnotarised code). Allowed: --status, --disable-status, --assess and developer-mode status.

import (
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/santacel"
)

func TestExamples_SpctlGlobalDisable(t *testing.T) {
	var ex *santacel.Example
	for i, e := range santacel.Examples {
		if e.Identifier == "platform:com.apple.spctl" {
			ex = &santacel.Examples[i]
		}
	}
	if ex == nil {
		t.Fatal("no example for /usr/sbin/spctl (identifier platform:com.apple.spctl)")
	}
	if ex.RuleType != "SIGNINGID" {
		t.Errorf("rule type %q, want SIGNINGID", ex.RuleType)
	}
	for _, word := range []string{"Gatekeeper", "System Settings"} {
		if !strings.Contains(ex.Explanation, word) {
			t.Errorf("the explanation does not mention %s", word)
		}
	}
	for args, want := range map[string]string{
		"spctl --global-disable":                     "BLOCKLIST",
		"spctl --master-disable":                     "BLOCKLIST",
		"spctl --disable":                            "BLOCKLIST",
		"spctl --add --label x /Applications/X.app":  "BLOCKLIST",
		"spctl --remove --label x":                   "BLOCKLIST",
		"spctl developer-mode enable-terminal":       "BLOCKLIST",
		"spctl --status":                             "ALLOWLIST",
		"spctl --disable-status":                     "ALLOWLIST",
		"spctl --assess -v /Applications/Safari.app": "ALLOWLIST",
		"spctl -a -vv /Applications/Safari.app":      "ALLOWLIST",
		"spctl developer-mode status":                "ALLOWLIST",
	} {
		got, err := santacel.Evaluate(ex.Expression, santacel.Context{
			SigningID: "platform:com.apple.spctl", PlatformBinary: true, Path: "/usr/sbin/spctl", Args: strings.Fields(args), EUID: 0,
		})
		if err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", args, got, err, want)
		}
	}
}
