package web_test

// Text a Mac reports can carry characters html/template passes through unchanged: bidi overrides and isolates,
// zero-width characters and control characters. A file name such as "invoice\u202efdp.exe" would read as
// "invoiceexe.pdf", and a host name or path could hide or reorder what it says (stored-XSS review, 2026-11-18). The
// view shows each such character as a visible Go-style escape (\x1b, \u202e, \n), as Rudolph's CLI does, and leaves
// other text, emoji sequences joined with U+200D included, as it is. A reported group name with one of them is never
// offered as a new group, and creating such a group is refused.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

// invisible reports whether r is a character the view must not write as it is.
func invisible(r rune) bool {
	switch {
	case r == '\n' || r == '\t' || r == '\r':
		return false // HTML whitespace of the page itself
	case r < 0x20 || (r >= 0x7f && r <= 0x9f):
		return true
	case r == 0x200c || r == 0x200d:
		return false
	case (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2060 && r <= 0x2069) || r == 0xfeff || r == 0x061c || r == 0x00ad:
		return true
	}
	return false
}

func TestInvisibleCharactersInReportedTextAreShownAsEscapes(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: editMachine, Hostname: "lab\u202emac\nFAKE", SerialNum: "C02\x1b[2J", PrimaryUser: "j\u200bsmith",
		ModelIdentifier: "Mac15,3\u2066", OSVersion: "15.1\u00ad", SantaVersion: "2026.9\u0085", SIPStatus: "enabled\ufeff", LastSeen: fixedNow})
	s.SetMachineGroups(editMachine, nil, nil, []string{"ops\u202e"})
	s.AddExecutionEvent(store.ExecutionEvent{MachineID: editMachine, ExecutionTime: fixedNow, Decision: store.DecisionBlockUnknown,
		FileSHA256: strings.Repeat("a", 64), FileName: "invoice\u202efdp.exe", FilePath: "/Users/j/Downloads/invoice\u202efdp.exe",
		ExecutingUser: "j\u2067smith", SigningID: "ABCDE12345:com.x\u200f", TeamID: "ABCDE12345"})
	s.AddFileAccessEvent(store.FileAccessEvent{MachineID: editMachine, AccessTime: fixedNow, RuleName: "keys\u2068", RuleVersion: "v1",
		Target: "/Users/j/.ssh/id\u202aed25519", Decision: store.FileAccessDecisionDenied,
		ProcessChain: []store.Process{{FilePath: "/usr/bin/cu\u200brl", PID: 1}}})
	for _, k := range store.TopKinds {
		for _, w := range []int{1, 7, 30} {
			s.SetTopList(k, w, fixedNow.Add(-time.Hour), []store.TopBinary{{FileSHA256: strings.Repeat("b", 64), FileName: "top\u202egnp.app", Count: 2}})
		}
	}
	s.AddAuditEntry(store.AuditEntry{Time: fixedNow, Actor: "cli:admin@evil\x1b]0;x\x07", Action: "rule.add", Target: "global", Summary: "added \u202etxt.exe"})
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleViewer, auth.RoleAdmin)

	pages := map[string][]string{
		"/machines":                {`lab\u202emac\nFAKE`, `C02\x1b[2J`, `2026.9\u0085`},
		"/machines/" + editMachine: {`lab\u202emac\nFAKE`, `C02\x1b[2J`, `j\u200bsmith`, `Mac15,3\u2066`, `15.1\u00ad`, `2026.9\u0085`, `enabled\ufeff`},
		"/events":                  {`invoice\u202efdp.exe`, `j\u2067smith`},
		"/events/file-access":      {`keys\u2068`, `id\u202aed25519`, `cu\u200brl`},
		"/events/top-blocked":      {`top\u202egnp.app`},
		"/events/top-allowed":      {`top\u202egnp.app`},
		"/events/top-unknown":      {`top\u202egnp.app`},
		"/admin/audit":             {`evil\x1b]0;x\x07`, `\u202etxt.exe`},
	}
	for p, want := range pages {
		t.Run(p, func(t *testing.T) {
			rec := a.get(t, p, admin)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			body := rec.Body.String()
			for i, r := range body {
				if invisible(r) {
					t.Errorf("page writes %U as it is (byte %d): %q", r, i, body[max(0, i-40):min(len(body), i+40)])
					break
				}
			}
			mustContain(t, body, want...)
		})
	}

	body := a.get(t, "/machines/"+editMachine, admin).Body.String()
	if strings.Contains(body, "Make this a group") {
		t.Error(`"ops\u202e" is offered as a new group; a name with a bidi override can never be one`)
	}
	if rec := a.post(t, "/admin/groups", url.Values{"name": {"ops\u202e"}, "accept_reported": {"on"}}, admin, true); rec.Code != http.StatusBadRequest {
		t.Errorf("creating group %q: status %d, want 400", "ops\u202e", rec.Code)
	}
	if rec := a.post(t, "/admin/groups", url.Values{"name": {"o\u200bps"}}, admin, true); rec.Code != http.StatusBadRequest {
		t.Errorf("creating group %q: status %d, want 400", "o\u200bps", rec.Code)
	}
}

func TestOrdinaryUnicodeIsShownAsItIs(t *testing.T) {
	host := "Jürgen’s Mac 👨\u200d👩\u200d👧 工程"
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: editMachine, Hostname: host, SerialNum: "C02ABC", LastSeen: fixedNow})
	s.AddExecutionEvent(store.ExecutionEvent{MachineID: editMachine, ExecutionTime: fixedNow, Decision: store.DecisionAllowBinary,
		FileSHA256: strings.Repeat("a", 64), FileName: "Café.app", FilePath: "/Applications/Café.app/Contents/MacOS/Café"})
	a := newApp(t, s, 10)
	for p, want := range map[string]string{"/machines": host, "/machines/" + editMachine: host, "/events": "Café.app"} {
		_, body := a.viewerGet(t, p)
		mustContain(t, body, want)
	}
}
