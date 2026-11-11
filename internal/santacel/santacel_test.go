package santacel_test

// Checking CEL expressions the way Santa will. Rules from Rudolph reach Santa over sync v1, so Santa evaluates their
// expressions with its CEL v1 environment (Santa 2026.8, Source/common/cel/Evaluator.mm with IsV2 false and the
// santa.cel.v1 protos of northpolesec/protos 68fc3ca):
//   - variables: target (signing_time, secure_signing_time, signing_id, is_platform_binary, team_id), args, envs,
//     euid, cwd and path, plus the return values ALLOWLIST, ALLOWLIST_COMPILER, BLOCKLIST, SILENT_BLOCKLIST,
//     SILENT_GUI_BLOCKLIST and SILENT_TTY_BLOCKLIST as ints;
//   - functions: cel-cpp 0.15.0's standard library and its strings extension (charAt, format, indexOf, join,
//     lastIndexOf, lowerAscii, replace, reverse, split, substring, trim, upperAscii), nothing else;
//   - the result is a bool (true allows, false blocks) or one of the return values.
// Workshop's CEL v2 adds ancestors, fds, target.entitlements, REQUIRE_TOUCHID, REQUIRE_TOUCHID_ONLY, SEATBELT, AUDIT,
// the FD_TYPE_* values and the functions today, now, days, weekdays, policy_for_range, kill_on_expiry,
// require_touchid_with_cooldown_minutes and require_touchid_only_with_cooldown_minutes; an expression using any of
// them is refused with a message saying it needs Workshop, because Santa would fail to compile it.
// Evaluate runs an expression against a sample execution and answers with the policy Santa would apply. Cacheable
// says whether Santa can cache the result: only when the expression reads nothing but target fields.

import (
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/santacel"
)

const profilesBlock = "args.exists(a, a in ['remove', '-R', '-D']) ? BLOCKLIST : ALLOWLIST"

func TestCheck_AcceptsWhatSantaV1Compiles(t *testing.T) {
	for _, expr := range []string{
		"true",
		"false",
		"ALLOWLIST",
		"ALLOWLIST_COMPILER",
		"SILENT_BLOCKLIST",
		"SILENT_GUI_BLOCKLIST",
		"SILENT_TTY_BLOCKLIST",
		"target.signing_time >= timestamp('2025-01-01T00:00:00Z')",
		"has(target.secure_signing_time) && target.secure_signing_time > timestamp('2024-06-01T00:00:00Z')",
		"target.signing_id == 'EQHXZ8M8AV:com.google.Chrome' && target.team_id == 'EQHXZ8M8AV'",
		"target.is_platform_binary ? ALLOWLIST : BLOCKLIST",
		profilesBlock,
		"envs['HOME'].startsWith('/Users/') && 'SUDO_USER' in envs",
		"euid == 0 ? BLOCKLIST : ALLOWLIST",
		"cwd.startsWith('/tmp') ? SILENT_BLOCKLIST : ALLOWLIST",
		"path.endsWith('/profiles') && args.size() > 1",
		"args.join(' ').lowerAscii().contains('removeframework') ? BLOCKLIST : ALLOWLIST",
		"args.exists(a, a.matches('^--remote-debugging-port=')) ? BLOCKLIST : ALLOWLIST",
		"  true\n",
	} {
		if err := santacel.Check(expr); err != nil {
			t.Errorf("Check(%q) = %v, want nil", expr, err)
		}
	}
}

func TestCheck_RefusesWhatOnlyWorkshopHas(t *testing.T) {
	for _, expr := range []string{
		"ancestors.exists(a, a.team_id == 'EQHXZ8M8AV')",
		"fds.size() > 3",
		"'com.apple.security.get-task-allow' in target.entitlements",
		"AUDIT",
		"REQUIRE_TOUCHID",
		"euid == 0 ? REQUIRE_TOUCHID_ONLY : ALLOWLIST",
		"SEATBELT",
		"fds.exists(f, f.type == FD_TYPE_SOCKET)",
		"target.secure_signing_time > today() - days(90)",
		"policy_for_range(weekdays(), '09:00', '17:00', ALLOWLIST, BLOCKLIST)",
		"require_touchid_with_cooldown_minutes(60)",
	} {
		err := santacel.Check(expr)
		if err == nil {
			t.Errorf("Check(%q) = nil; Santa refuses it without Workshop", expr)
			continue
		}
		if !strings.Contains(err.Error(), "Workshop") {
			t.Errorf("Check(%q) = %q; want it to say this needs Workshop", expr, err)
		}
	}
}

func TestCheck_RefusesTheRest(t *testing.T) {
	for expr, want := range map[string]string{
		"":                                    "empty",
		"   ":                                 "empty",
		"args.exists(a,":                      "Syntax error",
		"foo == 1":                            "foo",
		"target.bundle_id == 'x'":             "bundle_id",
		"euid == '0'":                         "no matching overload",
		"target.team_id":                      "ALLOWLIST",
		"args":                                "ALLOWLIST",
		"strings.quote('x') == \"'x'\"":       "quote",
		"target.signing_id.startsWith(1)":     "startsWith",
		"args.exists(a, a == 'x') && ancestr": "ancestr",
	} {
		err := santacel.Check(expr)
		if err == nil {
			t.Errorf("Check(%q) = nil, want an error", expr)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Check(%q) = %q; want it to mention %q", expr, err, want)
		}
		if strings.Contains(err.Error(), "Workshop") {
			t.Errorf("Check(%q) = %q; Workshop would not help here", expr, err)
		}
	}
}

func TestEvaluate_GivesThePolicySantaApplies(t *testing.T) {
	signed := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		expr string
		ctx  santacel.Context
		want string
	}{
		{"true", santacel.Context{}, "ALLOWLIST"},
		{"false", santacel.Context{}, "BLOCKLIST"},
		{"SILENT_GUI_BLOCKLIST", santacel.Context{}, "SILENT_GUI_BLOCKLIST"},
		{"3", santacel.Context{}, "BLOCKLIST"},
		{profilesBlock, santacel.Context{Path: "/usr/bin/profiles", Args: []string{"/usr/bin/profiles", "remove", "-identifier", "com.example"}}, "BLOCKLIST"},
		{profilesBlock, santacel.Context{Path: "/usr/bin/profiles", Args: []string{"profiles", "-R", "-p", "com.example"}}, "BLOCKLIST"},
		{profilesBlock, santacel.Context{Path: "/usr/bin/profiles", Args: []string{"profiles", "list"}}, "ALLOWLIST"},
		{"target.signing_time >= timestamp('2025-01-01T00:00:00Z')", santacel.Context{SigningTime: signed}, "ALLOWLIST"},
		{"target.signing_time >= timestamp('2025-06-01T00:00:00Z')", santacel.Context{SigningTime: signed}, "BLOCKLIST"},
		{"has(target.signing_time) ? ALLOWLIST : SILENT_BLOCKLIST", santacel.Context{}, "SILENT_BLOCKLIST"},
		{"has(target.secure_signing_time)", santacel.Context{SecureSigningTime: signed}, "ALLOWLIST"},
		{"euid == 0 ? BLOCKLIST : ALLOWLIST", santacel.Context{EUID: 0}, "BLOCKLIST"},
		{"euid == 0 ? BLOCKLIST : ALLOWLIST", santacel.Context{EUID: 501}, "ALLOWLIST"},
		{"envs['HOME'] == '/var/root'", santacel.Context{Envs: map[string]string{"HOME": "/var/root"}}, "ALLOWLIST"},
		{"cwd == '/tmp' || path == '/tmp/x'", santacel.Context{CWD: "/private"}, "BLOCKLIST"},
		{"target.is_platform_binary && target.signing_id == 'platform:com.apple.profiles' && target.team_id == ''",
			santacel.Context{PlatformBinary: true, SigningID: "platform:com.apple.profiles"}, "ALLOWLIST"},
		{"target.team_id == 'EQHXZ8M8AV' ? ALLOWLIST_COMPILER : BLOCKLIST", santacel.Context{TeamID: "EQHXZ8M8AV"}, "ALLOWLIST_COMPILER"},
	} {
		got, err := santacel.Evaluate(c.expr, c.ctx)
		if err != nil || got != c.want {
			t.Errorf("Evaluate(%q, %+v) = %q, %v; want %q", c.expr, c.ctx, got, err, c.want)
		}
	}
}

// UNSPECIFIED and the values Santa reserves for CEL v2 are evaluation errors, as are runtime errors.
func TestEvaluate_Errors(t *testing.T) {
	for _, expr := range []string{"0", "5", "8", "11", "envs['MISSING'] == 'x'", "args[3] == 'x'", "ancestors.size() > 0", "args.exists(a,"} {
		if got, err := santacel.Evaluate(expr, santacel.Context{Args: []string{"a"}}); err == nil {
			t.Errorf("Evaluate(%q) = %q, nil; want an error", expr, got)
		}
	}
}

func TestCacheable(t *testing.T) {
	for expr, want := range map[string]bool{
		"true": true,
		"target.signing_time >= timestamp('2025-01-01T00:00:00Z')": true,
		"target.is_platform_binary ? ALLOWLIST : BLOCKLIST":        true,
		profilesBlock:                          false,
		"euid == 0":                            false,
		"envs['HOME'] == ''":                   false,
		"cwd == '/'":                           false,
		"path == '/x' || target.team_id == ''": false,
	} {
		got, err := santacel.Cacheable(expr)
		if err != nil || got != want {
			t.Errorf("Cacheable(%q) = %v, %v; want %v", expr, got, err, want)
		}
	}
	if _, err := santacel.Cacheable("args.exists(a,"); err == nil {
		t.Error("Cacheable of an invalid expression did not fail")
	}
}

func TestExamples(t *testing.T) {
	if len(santacel.Examples) < 3 {
		t.Fatalf("%d examples, want at least 3", len(santacel.Examples))
	}
	var profiles *santacel.Example
	for i, e := range santacel.Examples {
		if e.Name == "" || e.Explanation == "" || e.Expression == "" || e.RuleType == "" {
			t.Errorf("example %d is missing a name, explanation, expression or rule type: %+v", i, e)
		}
		if err := santacel.Check(e.Expression); err != nil {
			t.Errorf("example %q does not pass Check: %v", e.Name, err)
		}
		if e.Identifier == "platform:com.apple.profiles" {
			profiles = &santacel.Examples[i]
		}
	}
	if profiles == nil {
		t.Fatal("no example for /usr/bin/profiles (identifier platform:com.apple.profiles)")
	}
	if profiles.RuleType != "SIGNINGID" {
		t.Errorf("the profiles example's rule type is %q, want SIGNINGID", profiles.RuleType)
	}
	for args, want := range map[string]string{
		"profiles remove -identifier com.example.wifi": "BLOCKLIST",
		"profiles remove -all":                         "BLOCKLIST",
		"profiles -R -p com.example.wifi":              "BLOCKLIST",
		"profiles -D":                                  "BLOCKLIST",
		"profiles list":                                "ALLOWLIST",
		"profiles status -type enrollment":             "ALLOWLIST",
		"profiles show -type enrollment":               "ALLOWLIST",
	} {
		got, err := santacel.Evaluate(profiles.Expression, santacel.Context{
			SigningID: "platform:com.apple.profiles", PlatformBinary: true, Path: "/usr/bin/profiles", Args: strings.Fields(args), EUID: 0,
		})
		if err != nil || got != want {
			t.Errorf("the profiles example on %q = %q, %v; want %q", args, got, err, want)
		}
	}
}
