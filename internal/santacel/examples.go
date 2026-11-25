package santacel

import "time"

// Example is a CEL rule worth starting from: the rule type and identifier to
// add it on, and its expression.
type Example struct {
	Name        string
	Explanation string
	RuleType    string
	// Identifier is empty when the example fits any binary: the explanation
	// says what to add it on.
	Identifier string
	Expression string
	// Sample is an execution the expression blocks, for the help page's
	// "Try it" link: trying the example then shows the case it exists for,
	// with the sample filled in as a pattern for the person's own.
	Sample Context
}

// Examples are the CEL rules the help page offers. Each passes Check.
var Examples = []Example{
	{
		Name: "Stop the profiles command removing profiles",
		Explanation: "On a SIGNINGID rule for /usr/bin/profiles (signing ID com.apple.profiles, a platform binary with no team ID), " +
			"this blocks the profiles command when it is asked to remove profiles (remove, or the older -R and -D flags), " +
			"so an admin user cannot take profiles off from the command line with sudo profiles remove. " +
			"Listing and showing profiles and enrolment status still work. " +
			"It does not stop removal in System Settings, which does not run the profiles command. " +
			"The real protection against MDM unenrolment is enrolling Macs through Automated Device Enrolment with a non-removable MDM profile; this rule only closes the command-line route. " +
			"IT can still use profiles on one Mac by adding a machine rule for the same signing ID (for example ALLOWLIST, for an hour), which takes precedence over this global or group rule there.",
		RuleType:   "SIGNINGID",
		Identifier: "platform:com.apple.profiles",
		Expression: "args.exists(a, a in ['remove', '-R', '-D']) ? BLOCKLIST : ALLOWLIST",
		Sample: Context{
			SigningID: "platform:com.apple.profiles", PlatformBinary: true, Path: "/usr/bin/profiles",
			Args: []string{"profiles", "remove", "-identifier", "com.example.wifi"},
		},
	},
	{
		Name: "Stop the spctl command turning Gatekeeper off",
		Explanation: "On a SIGNINGID rule for /usr/sbin/spctl (signing ID com.apple.spctl, a platform binary with no team ID), " +
			"this blocks spctl when it is asked to turn Gatekeeper off or loosen it from the command line: --global-disable, the older --master-disable and --disable spellings, the deprecated --add and --remove rule edits, and developer-mode enable-terminal, which lets Terminal run unnotarised code. " +
			"Reading the status (--status, --disable-status, developer-mode status) and assessing apps (--assess, -a) still work. " +
			"It does not stop the Gatekeeper setting in System Settings (Privacy & Security), which does not run spctl: an admin user can still turn Gatekeeper off there unless an MDM profile enforces it, such as Jamf's Security & Privacy payload. " +
			"IT can still use spctl on one Mac by adding a machine rule for the same signing ID (for example ALLOWLIST, for an hour), which takes precedence over this global or group rule there.",
		RuleType:   "SIGNINGID",
		Identifier: "platform:com.apple.spctl",
		Expression: "args.exists(a, a in ['--global-disable', '--master-disable', '--disable', '--add', '--remove', 'enable-terminal']) ? BLOCKLIST : ALLOWLIST",
		Sample: Context{
			SigningID: "platform:com.apple.spctl", PlatformBinary: true, Path: "/usr/sbin/spctl",
			Args: []string{"spctl", "--global-disable"},
		},
	},
	{
		Name: "Allow a team's binaries only when signed recently",
		Explanation: "On a TEAMID rule, this allows the team's binaries only when Apple's timestamp server vouches that they were signed on or after the date, " +
			"and blocks older builds, such as an outdated browser with known vulnerabilities. " +
			"secure_signing_time comes from Apple's timestamp server; signing_time is the signer's own clock and can be set to anything. " +
			"It reads only target, so Santa caches the result for each binary. " +
			"The example uses Google's team ID; change the date as new releases come out.",
		RuleType:   "TEAMID",
		Identifier: "EQHXZ8M8AV",
		Expression: "has(target.secure_signing_time) && target.secure_signing_time >= timestamp('2025-01-01T00:00:00Z')",
		Sample: Context{
			SigningID: "EQHXZ8M8AV:com.google.Chrome", TeamID: "EQHXZ8M8AV",
			SecureSigningTime: time.Date(2024, time.June, 1, 0, 0, 0, 0, time.UTC),
		},
	},
	{
		Name: "Block Chrome started with remote debugging",
		Explanation: "On a SIGNINGID rule for Google Chrome, this blocks Chrome when it is started with --remote-debugging-port, " +
			"which malware uses to read a user's cookies and saved sessions, and allows it otherwise. " +
			"People who need remote debugging can be given a machine rule that allows Chrome on their Mac.",
		RuleType:   "SIGNINGID",
		Identifier: "EQHXZ8M8AV:com.google.Chrome",
		Expression: "args.exists(a, a.startsWith('--remote-debugging-port')) ? BLOCKLIST : ALLOWLIST",
		Sample: Context{
			SigningID: "EQHXZ8M8AV:com.google.Chrome", TeamID: "EQHXZ8M8AV",
			Path: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			Args: []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "--remote-debugging-port=9222"},
			EUID: 501,
		},
	},
	{
		Name: "Allow a tool for users, not as root",
		Explanation: "On a SIGNINGID or TEAMID rule for a tool people should run as themselves, this blocks it when it runs as root (for example with sudo) and allows it otherwise. " +
			"Add it on the signing ID or team ID of the tool; check them with codesign -dv on a Mac.",
		RuleType:   "SIGNINGID",
		Expression: "euid == 0 ? BLOCKLIST : ALLOWLIST",
		Sample:     Context{Args: []string{"sometool"}, EUID: 0},
	},
}
