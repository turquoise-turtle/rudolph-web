package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

// Review of the stored-XSS hardening (2026-11-19). The display rewrite is checked against its own idea of where a
// value is printed; this guard does not reuse any of that logic. It fills every string the store can hold with a
// sentinel of invisible characters, renders every page as an admin with editing on, and looks only at the bytes that
// reach the browser. Percent-encoded forms (%E2%80%AE) are fine; a raw character is not, whatever printed it.

// defaultIgnorable is Unicode's derived Default_Ignorable_Code_Point property (DerivedCoreProperties.txt), which Go's
// unicode package does not provide as one table.
func defaultIgnorable(r rune) bool {
	switch {
	case unicode.Is(unicode.White_Space, r), r >= 0xfff9 && r <= 0xfffb, r >= 0x13430 && r <= 0x1343f,
		unicode.Is(unicode.Prepended_Concatenation_Mark, r):
		return false
	}
	return unicode.In(r, unicode.Other_Default_Ignorable_Code_Point, unicode.Cf, unicode.Variation_Selector)
}

// rawOnPage reports a character no page may write as it is: a control character other than the page's own
// whitespace, or a default-ignorable one other than those textsafe keeps for emoji and joined scripts (the sentinel
// holds none of those).
func rawOnPage(r rune) bool {
	switch r {
	case '\n', '\r', '\t', 0x200c, 0x200d, 0xfe0f:
		return false
	}
	return unicode.IsControl(r) || defaultIgnorable(r)
}

// everyPage fills every string the store can hold with the sentinel and returns the app with editing on, an admin's
// cookie and every page to render, each with whether it shows stored text as such.
func everyPage(t *testing.T) (*app, *http.Cookie, map[string]bool) {
	t.Helper()
	// A bidi override, a zero-width space, a text-presentation selector and a stray tag letter.
	inv := string(rune(0x202e)) + string(rune(0x200b)) + string(rune(0xfe0e)) + string(rune(0xe0041))
	x := func(s string) string { return s + inv + "x" }
	ptr := func(s string) *string { return &s }
	sha := strings.Repeat("c", 64)
	group := x("ops")
	otherMachine := x("machine")

	s := fake.New()
	for _, id := range []string{editMachine, otherMachine} {
		s.AddMachine(store.Machine{MachineID: id, SerialNum: x("C02"), Hostname: x("lab"), ModelIdentifier: x("Mac15,3"),
			OSVersion: x("15.1"), OSBuild: x("24B83"), SantaVersion: x("2026.9"), ClientMode: store.ClientMode(x("MONITOR")),
			PrimaryUser: x("jsmith"), SIPStatus: x("enabled"), LastSeen: fixedNow})
	}
	s.SetSyncState(editMachine, store.SyncState{PreflightAt: fixedNow, PostflightAt: fixedNow, LastSyncType: x("NORMAL")})
	cfg := store.Config{Exists: true, ClientMode: store.ClientModeMonitor, AllowedPathRegex: ptr(x("^/Applications")),
		BlockedPathRegex: ptr(x("^/tmp")), UploadLogsURL: ptr(x("https://logs.example.com/")), OverrideFileAccessAction: x("NONE"),
		EventDetailURL: ptr(x("https://detail.example.com/%file_sha%")), EventDetailText: ptr(x("Details"))}
	s.SetMachineConfig(editMachine, cfg)
	s.SetGlobalConfig(cfg)
	s.SetTemporaryClientMode(editMachine, store.ClientModeLockdown, fixedNow.Add(time.Hour))

	rule := func(t store.RuleType, p store.Policy, id string) store.Rule {
		r := store.Rule{RuleType: t, Policy: p, Identifier: id, CustomMessage: x("Blocked"), CustomURL: x("https://help.example.com/"),
			Description: x("why"), State: store.RuleStateInForce}
		if p == store.PolicyCEL {
			r.CELExpr = x("target.signing_time > timestamp('2025-01-01T00:00:00Z')")
		}
		return r
	}
	for _, r := range []store.Rule{
		rule(store.RuleTypeBinary, store.PolicyBlocklist, x("binary")),
		rule(store.RuleTypeSigningID, store.PolicyCEL, x("EQHXZ8M8AV:com.x")),
		rule(store.RuleTypeTeamID, store.PolicyAllowlist, "EQHXZ8M8AV"),
	} {
		s.AddGlobalRule(r)
		s.AddMachineRule(editMachine, r)
		s.AddGroupRule(group, r)
	}
	s.AddGroup(store.Group{Name: group, Description: x("lab Macs"), AcceptReported: true})
	s.SetMachineGroups(editMachine, []string{group}, []string{group}, []string{x("reported")})

	for _, id := range []string{editMachine, otherMachine} {
		s.AddExecutionEvent(store.ExecutionEvent{MachineID: id, ExecutionTime: fixedNow, Decision: store.DecisionBlockUnknown,
			FileSHA256: sha, FilePath: x("/Users/j/Downloads/invoice"), FileName: x("invoice"), ExecutingUser: x("jsmith"),
			TeamID: x("ABCDE12345"), SigningID: x("ABCDE12345:com.x"), CDHash: x("cdhash")})
		s.AddFileAccessEvent(store.FileAccessEvent{MachineID: id, AccessTime: fixedNow, RuleVersion: x("v1"), RuleName: x("keys"),
			Target: x("/Users/j/.ssh/id_ed25519"), Decision: store.FileAccessDecisionDenied,
			ProcessChain: []store.Process{{FilePath: x("/usr/bin/curl"), FileSHA256: x("sha"), CDHash: x("cd"), SigningID: x("com.apple.curl"),
				TeamID: x("team"), PID: 1}}})
	}
	for _, k := range store.TopKinds {
		for _, w := range []int{1, 7, 30} {
			s.SetTopList(k, w, fixedNow.Add(-time.Hour), []store.TopBinary{{FileSHA256: sha, FileName: x("top"), TeamID: x("ABCDE12345"),
				SigningID: x("ABCDE12345:com.top"), Count: 2, Machines: 1, MachinesKnown: true, GlobalRule: x("BLOCKLIST")}})
		}
	}
	s.AddAuditEntry(store.AuditEntry{Time: fixedNow, Actor: x("cli:admin@example.com"), Action: x("rule.add"), Target: x("global"),
		Summary: x("added")})
	s.AddAuditEntry(store.AuditEntry{Time: fixedNow, Actor: x("web:admin@example.com"), Action: "group.update", Target: "group:" + group,
		Summary: x("changed")})

	fleetRow := store.FleetHealthRow{MachineID: editMachine, Hostname: x("lab"), SerialNum: x("C02"), LastSeen: fixedNow,
		LastCompleted: fixedNow, Reported: store.ClientMode(x("MONITOR")), Intended: store.ClientMode(x("LOCKDOWN")), Source: x("group:ops"),
		Reason: x("Outlier"), Hash: x("hash"), ExpectedHash: x("expected"), Flags: x("0x7f"), SantaVersion: x("2026.8")}
	readyRow := store.ReadinessRow{MachineID: editMachine, Hostname: x("lab"), SerialNum: x("C02"), Bucket: store.ReadinessReady, Counted: true,
		ReportedMode: store.ClientMode(x("MONITOR")), IntendedMode: store.ClientMode(x("MONITOR")), Why: x("observed")}
	health := map[store.FleetCategory][]store.FleetHealthRow{}
	for _, c := range store.FleetCategories {
		health[c] = []store.FleetHealthRow{fleetRow}
	}
	s.SetFleetSnapshot(fake.FleetSnapshot{
		Summary: store.FleetSummary{Snapshot: x("20260917T110000.000000000Z-0a1b2c3d"), GeneratedAt: fixedNow.Add(-time.Hour), Macs: 2,
			MinSantaVersion: x("2026.9"), StaleDays: 60, Holiday: true, HolidayUntil: x("2027-02-01"),
			ReadinessAll: store.ReadinessCounts{Macs: 2, Ready: 2}, ReadinessGroups: map[string]store.ReadinessCounts{group: {Macs: 1, Ready: 1}}},
		Health:    health,
		Readiness: map[string][]store.ReadinessRow{"": {readyRow}, group: {readyRow}},
	})
	s.SetFleetHealthConfig(store.FleetHealthConfig{StaleDays: 10, HolidayStaleDays: 60, HolidayUntil: x("2027-02-01")})
	s.SetMachineUnknown(editMachine, store.UnknownCounts{Last30: 2, Last7: 1})
	s.AddUnknownBinary(editMachine, store.UnknownBinary{FileSHA256: sha, FileName: x("tool"), SigningID: x("ABCDE12345:com.tool"),
		TeamID: x("ABCDE12345"), LastDay: fixedNow})

	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleViewer, auth.RoleAdmin)
	esc := url.PathEscape(group)
	tried := url.Values{"expr": {`path == "` + x("/tmp/a") + `"`}, "path": {x("/tmp/a")}, "args": {x("-v")}, "envs": {x("HOME=/")},
		"cwd": {x("/")}}
	// false: the page shows no stored text as such (the CEL page posts its query back only in form fields), so the
	// sentinel need not appear escaped; its raw characters are still looked for.
	return a, admin, map[string]bool{
		"/machines":                      true,
		"/machines/" + editMachine:       true,
		"/events":                        true,
		"/events?machine=" + editMachine: true,
		"/events/file-access":            true,
		"/events/top-blocked":            true,
		"/events/top-allowed":            true,
		"/events/top-unknown":            true,
		"/groups":                        true,
		"/groups/" + esc:                 true,
		"/groups/" + esc + "/members":    true,
		"/rules":                         true,
		"/rules/cel":                     false,
		"/rules/cel?" + tried.Encode():   false,
		"/admin":                         false,
		"/admin/config":                  true,
		"/admin/audit":                   true,
		"/admin/rules/new?team_id=EQHXZ8M8AV&signing_id=EQHXZ8M8AV:com.x": false,
		"/fleet":                             true,
		"/fleet/health/stale":                true,
		"/fleet/health/mode":                 true,
		"/fleet/health/rules":                true,
		"/fleet/health/sip":                  true,
		"/fleet/health/version":              true,
		"/fleet/readiness":                   true,
		"/fleet/readiness/macs?bucket=ready": true,
		"/fleet/readiness/macs?" + url.Values{"group": {group}, "bucket": {"ready"}}.Encode(): true,
		"/machines/" + editMachine + "/unknown":                                               true,
	}
}

func TestEveryPageWritesNoRawInvisibleCharacter(t *testing.T) {
	a, admin, pages := everyPage(t)
	for p, showsData := range pages {
		t.Run(p, func(t *testing.T) {
			rec := a.get(t, p, admin)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			body := rec.Body.String()
			if showsData && !strings.Contains(body, `\u202e`) {
				t.Errorf("no sentinel shown on the page; the test data does not reach it")
			}
			found := map[rune]int{}
			for i, r := range body {
				if rawOnPage(r) {
					if found[r] == 0 {
						t.Errorf("U+%04X written raw (byte %d): %q", r, i, body[max(0, i-60):min(len(body), i+20)])
					}
					found[r]++
				}
			}
		})
	}
}

// render writes invisible characters as character references, which the browser decodes back into the characters
// themselves, so a value printed without show passes the raw guard above as &#x202e;. Outside a posted form value no
// page may carry such a reference (web.InvisibleCharRefsOutsideFormValues).
func TestEveryPageWritesNoInvisibleCharRefOutsideFormValues(t *testing.T) {
	a, admin, pages := everyPage(t)
	for p := range pages {
		t.Run(p, func(t *testing.T) {
			rec := a.get(t, p, admin)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			for _, f := range web.InvisibleCharRefsOutsideFormValues(rec.Body.String()) {
				t.Error(f)
			}
		})
	}
}
