// Package clicmd builds Rudolph CLI commands for operators to copy from the web
// view. The strings are a contract with Rudolph's CLI: Rudolph runs every line of
// testdata/webcontract/cli-examples.txt as a dry run through its command tree.
//
// Identifiers come from Macs, so each is checked against its type's shape and no
// command is built for one that does not fit; every argument from a Mac is
// single-quoted as well, so a shell never splits or expands it.
package clicmd

import (
	"regexp"
	"strings"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

var (
	sha256Re    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	cdhashRe    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	teamIDRe    = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	signingIDRe = regexp.MustCompile(`^(?:[A-Z0-9]{10}|platform):[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)
	machineRe   = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	durationRe  = regexp.MustCompile(`^[1-9][0-9]{0,4}[smh]$`)
	// The environment name is written unquoted after ENV=, so it must not need quoting.
	envRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// Builder renders commands for one Rudolph environment.
type Builder struct {
	prefix string
	ok     bool
}

// New returns a Builder whose commands start with ENV=<env>, or with plain
// `rudolph` when env is empty. An env that would need quoting builds nothing.
func New(env string) Builder {
	switch {
	case env == "":
		return Builder{prefix: "rudolph", ok: true}
	case envRe.MatchString(env):
		return Builder{prefix: "ENV=" + env + " rudolph", ok: true}
	default:
		return Builder{}
	}
}

// Target is where a rule applies: globally, on one Mac, or in a group.
type Target struct {
	kind    string // "global", "machine" or "group"
	value   string
	expires string
}

// Global targets every Mac.
func Global() Target { return Target{kind: "global"} }

// Machine targets one Mac; expires (a duration such as "24h") is added to
// rules it creates, and may be empty.
func Machine(id, expires string) Target { return Target{kind: "machine", value: id, expires: expires} }

// Group targets a registered group, or Macs in no group for store.UngroupedGroup.
func Group(name string) Target { return Target{kind: "group", value: name} }

// quote single-quotes s for a POSIX shell.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func validMachine(id string) bool { return machineRe.MatchString(id) }

// ruleTypeArg returns the CLI's name for t and the identifier normalised for
// it, or ok=false when the identifier does not have the type's shape.
func ruleTypeArg(t store.RuleType, id string) (string, string, bool) {
	switch t {
	case store.RuleTypeBinary, store.RuleTypeCertificate:
		id = strings.ToLower(id)
		if !sha256Re.MatchString(id) {
			return "", "", false
		}
		if t == store.RuleTypeBinary {
			return "binary", id, true
		}
		return "certificate", id, true
	case store.RuleTypeCDHash:
		id = strings.ToLower(id)
		return "cdhash", id, cdhashRe.MatchString(id)
	case store.RuleTypeTeamID:
		return "teamid", id, teamIDRe.MatchString(id)
	case store.RuleTypeSigningID:
		return "signingid", id, signingIDRe.MatchString(id)
	}
	return "", "", false
}

// targetArgs renders the target flags; withExpiry adds a machine target's
// --expires.
func targetArgs(tg Target, withExpiry bool) (string, bool) {
	switch tg.kind {
	case "global":
		return "--global", true
	case "machine":
		if !validMachine(tg.value) {
			return "", false
		}
		s := "--machine " + quote(tg.value)
		if withExpiry && tg.expires != "" {
			if !durationRe.MatchString(tg.expires) {
				return "", false
			}
			s += " --expires " + tg.expires
		}
		return s, true
	case "group":
		if tg.value == store.UngroupedGroup {
			return "--ungrouped", true
		}
		if tg.value == "" {
			return "", false
		}
		return "--group " + quote(tg.value), true
	}
	return "", false
}

func (b Builder) join(parts ...string) (string, bool) {
	if !b.ok {
		return "", false
	}
	return b.prefix + " " + strings.Join(parts, " "), true
}

func (b Builder) rule(verb string, t store.RuleType, id string, tg Target, withExpiry bool, extra ...string) (string, bool) {
	typ, id, ok := ruleTypeArg(t, id)
	if !ok {
		return "", false
	}
	target, ok := targetArgs(tg, withExpiry)
	if !ok {
		return "", false
	}
	parts := append([]string{"rule", verb, "-t", typ, "-i", quote(id), target}, extra...)
	return b.join(append(parts, "--yes")...)
}

// Rule creates a rule: verb is "allow" or "deny".
func (b Builder) Rule(verb string, t store.RuleType, id string, tg Target) (string, bool) {
	if verb != "allow" && verb != "deny" {
		return "", false
	}
	return b.rule(verb, t, id, tg, true)
}

// CollectAllowedEvents turns collection of allowed executions on or off for an
// existing global or machine rule.
func (b Builder) CollectAllowedEvents(t store.RuleType, id string, tg Target, on bool) (string, bool) {
	if tg.kind == "group" {
		return "", false
	}
	flag := "--collect-allowed-events=false"
	if on {
		flag = "--collect-allowed-events=true"
	}
	return b.rule("update", t, id, tg, false, flag)
}

// Remove removes a global, group or machine rule.
func (b Builder) Remove(t store.RuleType, id string, tg Target) (string, bool) {
	return b.rule("remove", t, id, tg, false)
}

// MachineRules lists one Mac's rules.
func (b Builder) MachineRules(machineID string) (string, bool) {
	if !validMachine(machineID) {
		return "", false
	}
	return b.join("rules", "--machine", quote(machineID))
}

// ConfigGet shows one Mac's configuration.
func (b Builder) ConfigGet(machineID string) (string, bool) {
	if !validMachine(machineID) {
		return "", false
	}
	return b.join("config", "get", "--machine", quote(machineID))
}

// TemporaryMode puts one Mac in mode for a duration such as "2h".
func (b Builder) TemporaryMode(machineID string, mode store.ClientMode, duration string) (string, bool) {
	var m string
	switch mode {
	case store.ClientModeMonitor:
		m = "monitor"
	case store.ClientModeLockdown:
		m = "lockdown"
	case store.ClientModeStandalone:
		m = "standalone"
	default:
		return "", false
	}
	if !validMachine(machineID) || !durationRe.MatchString(duration) {
		return "", false
	}
	return b.join("config", "update", "--machine", quote(machineID), "-c", m, "--for", duration, "--yes")
}

// EndTemporaryMode returns one Mac to its usual mode.
func (b Builder) EndTemporaryMode(machineID string) (string, bool) {
	if !validMachine(machineID) {
		return "", false
	}
	return b.join("config", "update", "--machine", quote(machineID), "--end-temporary-mode", "--yes")
}

// GroupAdd assigns one Mac to a group.
func (b Builder) GroupAdd(machineID, group string) (string, bool) {
	if !validMachine(machineID) || group == "" || group == store.UngroupedGroup {
		return "", false
	}
	return b.join("group", "add", "--machine", quote(machineID), quote(group), "--yes")
}
