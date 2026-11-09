package web_test

// Owner feedback from the production walkthrough (step 10.1): the events page offered commands for binary rules only.
// Each row now offers a group of commands per rule type the event can be matched by, most useful first: signing ID,
// then team ID, then CDHash, then binary (SHA-256). The first group is open; the others are one click away. The same
// goes for the top lists, which have no CDHash. Also: wide tables use the whole window.

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

const (
	typesTeam    = "B5N3YV5P2H"
	typesSigning = "B5N3YV5P2H:pc-printer-updater"
	typesCDHash  = "ffeeddccbbaa99887766554433221100ffeeddcc"
	typesMachine = "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA"
)

var typesSHA = strings.Repeat("0", 63) + "e"

// commandsCell returns the Commands disclosure of the first table row.
func commandsCell(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<details class="cmds">.*?</details>\s*</td>`).FindString(body)
	if m == "" {
		t.Fatal("no Commands disclosure in the table")
	}
	return m
}

// requireInOrder checks that each want appears in s, each after the previous one.
func requireInOrder(t *testing.T, s string, wants ...string) {
	t.Helper()
	at := 0
	for _, w := range wants {
		i := strings.Index(s[at:], w)
		if i < 0 {
			t.Fatalf("%q missing, or out of order, after position %d in:\n%s", w, at, s)
		}
		at += i + len(w)
	}
}

func eventsBody(t *testing.T, e store.ExecutionEvent) string {
	t.Helper()
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: typesMachine, SerialNum: "C02T", Hostname: "t.example.com", LastSeen: fixedNow})
	e.MachineID = typesMachine
	e.ExecutionTime = fixedNow.Add(-time.Hour)
	if e.FileName == "" {
		e.FileName = "tool"
	}
	s.AddExecutionEvent(e)
	a := newAppWithCLIEnv(t, s, "prod")
	code, body := a.viewerGet(t, "/events")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	return body
}

func rulesFor(kind, id string) []string {
	q := "'" + id + "'"
	return []string{
		"ENV=prod rudolph rule allow -t " + kind + " -i " + q + " --machine '" + typesMachine + "' --expires 24h --yes",
		"ENV=prod rudolph rule allow -t " + kind + " -i " + q + " --global --yes",
		"ENV=prod rudolph rule deny -t " + kind + " -i " + q + " --global --yes",
	}
}

func TestRuleTypeCommands_EventOffersEveryTypeMostUsefulFirst(t *testing.T) {
	body := eventsBody(t, store.ExecutionEvent{FileName: "pc-printer-updater", FilePath: "/Library/Printers",
		Decision: store.DecisionAllowUnknown, FileSHA256: typesSHA, TeamID: typesTeam, SigningID: typesSigning, CDHash: typesCDHash})
	cell := commandsCell(t, body)

	var order []string
	order = append(order, `<details class="cmd-type" open><summary>Signing ID`)
	order = append(order, rulesFor("signingid", typesSigning)...)
	order = append(order, `<details class="cmd-type"><summary>Team ID`)
	order = append(order, rulesFor("teamid", typesTeam)...)
	order = append(order, `<details class="cmd-type"><summary>CDHash`)
	order = append(order, rulesFor("cdhash", typesCDHash)...)
	order = append(order, `<details class="cmd-type"><summary>Binary`)
	order = append(order, rulesFor("binary", typesSHA)...)
	requireInOrder(t, cell, order...)
	if n := strings.Count(cell, " open>"); n != 1 {
		t.Errorf("want only the first rule type open, got %d open groups", n)
	}
}

func TestRuleTypeCommands_UnsignedEventFallsBackToBinary(t *testing.T) {
	body := eventsBody(t, store.ExecutionEvent{Decision: store.DecisionBlockUnknown, FileSHA256: typesSHA})
	cell := commandsCell(t, body)
	requireInOrder(t, cell, append([]string{`<details class="cmd-type" open><summary>Binary`}, rulesFor("binary", typesSHA)...)...)
	for _, kind := range []string{"-t signingid", "-t teamid", "-t cdhash"} {
		if strings.Contains(cell, kind) {
			t.Errorf("an unsigned event offers %s", kind)
		}
	}
}

func TestRuleTypeCommands_PlatformBinaryHasNoTeamID(t *testing.T) {
	body := eventsBody(t, store.ExecutionEvent{Decision: store.DecisionAllowBinary, FileSHA256: typesSHA,
		SigningID: "platform:com.apple.ls", CDHash: typesCDHash})
	cell := commandsCell(t, body)
	requireInOrder(t, cell,
		`<details class="cmd-type" open><summary>Signing ID`, "-t signingid -i 'platform:com.apple.ls'",
		`<details class="cmd-type"><summary>CDHash`, `<details class="cmd-type"><summary>Binary`)
	if strings.Contains(cell, "-t teamid") {
		t.Error("a platform binary offers a team ID rule")
	}
}

// Values the CLI would refuse are left out rather than shown as commands that fail.
func TestRuleTypeCommands_InvalidValuesAreLeftOut(t *testing.T) {
	body := eventsBody(t, store.ExecutionEvent{Decision: store.DecisionAllowUnknown, FileSHA256: typesSHA,
		TeamID: "not a team", SigningID: "bad id", CDHash: "xyz"})
	cell := commandsCell(t, body)
	requireInOrder(t, cell, `<details class="cmd-type" open><summary>Binary`)
	for _, kind := range []string{"-t signingid", "-t teamid", "-t cdhash", "Signing ID", "Team ID", "CDHash"} {
		if strings.Contains(cell, kind) {
			t.Errorf("invalid values still offer %s", kind)
		}
	}
}

func TestRuleTypeCommands_TopListOffersEachType(t *testing.T) {
	s := fake.New()
	s.SetTopList(store.TopKindBlocked, 7, fixedNow, []store.TopBinary{{FileSHA256: typesSHA, FileName: "pc-printer-updater",
		TeamID: typesTeam, SigningID: typesSigning, Count: 3}})
	a := newAppWithCLIEnv(t, s, "prod")
	code, body := a.viewerGet(t, "/events/top-blocked")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	cell := commandsCell(t, body)
	requireInOrder(t, cell,
		`<details class="cmd-type" open><summary>Signing ID`,
		"ENV=prod rudolph rule deny -t signingid -i '"+typesSigning+"' --global --yes",
		"ENV=prod rudolph rule allow -t signingid -i '"+typesSigning+"' --global --yes",
		`<details class="cmd-type"><summary>Team ID`,
		"ENV=prod rudolph rule deny -t teamid -i '"+typesTeam+"' --global --yes",
		`<details class="cmd-type"><summary>Binary`,
		"ENV=prod rudolph rule deny -t binary -i '"+typesSHA+"' --global --yes",
	)
	if strings.Contains(cell, "--machine") {
		t.Error("a top list, which spans machines, offers a machine rule")
	}
}

// The owner's 1660-pixel-wide window left about 280 pixels empty beside a table that scrolled sideways.
func TestWideTablesUseTheWholeWindow(t *testing.T) {
	css, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?m)^\s*([^{}]+?)\s*\{([^}]*)\}`).FindAllStringSubmatch(string(css), -1) {
		for _, sel := range strings.Split(m[1], ",") {
			sel = strings.TrimSpace(sel)
			if (sel == "main" || sel == ".scroll" || sel == "table" || sel == "body") && strings.Contains(m[2], "max-width") {
				t.Errorf("%s has a max-width (%s), so wide tables cannot use the whole window", sel, strings.TrimSpace(m[2]))
			}
		}
	}
}
