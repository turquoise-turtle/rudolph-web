package web_test

// File access events (Santa's File Access Authorisation), stored by Rudolph since 85eee68 (owner, 2026-11-11: "file
// access events sounds alright"; the aim is noticing anyone tampering with Jamf). /events/file-access, for viewers,
// lists them newest first, one bounded ListFileAccessEvents call a page, with the time, the Mac (linked to its page,
// with its hostname on a Mac's own list, which looks the Mac up anyway), the rule name, the path accessed, the decision (DENIED, DENIED_INVALID_SIGNATURE,
// AUDIT_ONLY), and the process: its path, signing ID and team ID, and its parents' paths. Filters, one at a time like
// executions: machine (the stored spelling, looked up on the first page) or decision; an unknown decision or both at
// once is a 400 without reading events. The events page and a Mac's page link to it.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

const faaMac = "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA"

func faaStore() *fake.Store {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: faaMac, Hostname: "lab-mac-01", SerialNum: "C02ABC123", LastSeen: fixedNow})
	ev := func(machine, rule, target string, d store.FileAccessDecision, minutesAgo int, chain ...store.Process) {
		s.AddFileAccessEvent(store.FileAccessEvent{MachineID: machine, RuleName: rule, RuleVersion: "v7", Target: target, Decision: d,
			AccessTime: fixedNow.Add(-time.Duration(minutesAgo) * time.Minute), ProcessChain: chain})
	}
	ev(faaMac, "Protect Jamf", "/usr/local/jamf/bin/jamf", store.FileAccessDecisionDenied, 1,
		store.Process{FilePath: "/bin/rm", SigningID: "platform:com.apple.rm", PID: 4242}, store.Process{FilePath: "/bin/zsh"}, store.Process{FilePath: "/usr/bin/sudo"})
	ev(faaMac, "Protect Jamf", "/Library/Application Support/JAMF/Receipts/x.pkg", store.FileAccessDecisionAuditOnly, 2,
		store.Process{FilePath: "/Applications/Tool.app/Contents/MacOS/Tool", SigningID: "EQHXZ8M8AV:com.example.tool", TeamID: "EQHXZ8M8AV"})
	ev("BBBBBBBB-2222-4222-8222-BBBBBBBBBBBB", "Browser cookies", "/Users/alice/Library/Cookies/Cookies.binarycookies", store.FileAccessDecisionDeniedInvalidSignature, 3)
	ev(faaMac, "Protect Jamf", "/usr/local/jamf/bin/jamfAgent", store.FileAccessDecisionDenied, 4, store.Process{FilePath: "/bin/mv"})
	return s
}

func TestFileAccessEvents_ListNewestFirst(t *testing.T) {
	a := newApp(t, faaStore(), 10)
	code, body := a.viewerGet(t, "/events/file-access")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "Protect Jamf", "/usr/local/jamf/bin/jamf", "DENIED", "AUDIT_ONLY", "DENIED_INVALID_SIGNATURE",
		"/bin/rm", "platform:com.apple.rm", "/bin/zsh", "/usr/bin/sudo", "EQHXZ8M8AV:com.example.tool", "Browser cookies",
		`href="/machines/`+faaMac+`"`, fixedNow.Add(-time.Minute).Format("2006-01-02 15:04:05")+" UTC")
	order := []string{"/usr/local/jamf/bin/jamf<", "Receipts/x.pkg", "Cookies.binarycookies", "jamfAgent"}
	last := -1
	for _, s := range order {
		at := strings.Index(body, s)
		if at < 0 || at < last {
			t.Errorf("%q missing or out of order (newest first)", s)
		}
		last = at
	}
}

func TestFileAccessEvents_FiltersAndPagination(t *testing.T) {
	s := faaStore()
	a := newApp(t, s, 2)

	code, body := a.viewerGet(t, "/events/file-access?machine="+strings.ToLower(faaMac))
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "/usr/local/jamf/bin/jamf<", "Receipts/x.pkg", "lab-mac-01")
	mustNotContain(t, body, "Cookies.binarycookies", "jamfAgent")
	if calls := s.Calls(); calls[len(calls)-1].Filter != (store.FileAccessEventFilter{MachineID: faaMac}) {
		t.Errorf("the machine filter was not the stored spelling: %+v", calls[len(calls)-1].Filter)
	}
	code, body = a.viewerGet(t, nextLink(t, body))
	if code != http.StatusOK {
		t.Fatalf("page 2 status %d", code)
	}
	mustContain(t, body, "jamfAgent")
	mustNotContain(t, body, "Cookies.binarycookies", "Receipts/x.pkg")

	code, body = a.viewerGet(t, "/events/file-access?decision=AUDIT_ONLY")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "Receipts/x.pkg")
	mustNotContain(t, body, "jamfAgent", "Cookies.binarycookies")

	s.ResetCalls()
	for _, q := range []string{"decision=ALLOWED", "decision=FILE_ACCESS_DECISION_DENIED", "decision=DENIED&machine=" + faaMac} {
		code, body := a.viewerGet(t, "/events/file-access?"+q)
		if code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, code)
		}
		if strings.Contains(q, "&") {
			mustContain(t, body, "one")
		}
	}
	for _, c := range s.Calls() {
		if c.Method == "ListFileAccessEvents" {
			t.Errorf("an invalid filter still listed events: %+v", c)
		}
	}
}

func TestFileAccessEvents_Links(t *testing.T) {
	a := newApp(t, faaStore(), 10)
	_, body := a.viewerGet(t, "/events")
	mustContain(t, body, `href="/events/file-access"`)
	_, body = a.viewerGet(t, "/machines/"+faaMac)
	mustContain(t, body, `href="/events/file-access?`+url.Values{"machine": {faaMac}}.Encode()+`"`)
}

func TestFileAccessEvents_HostileDataIsEscaped(t *testing.T) {
	s := fake.New()
	s.AddFileAccessEvent(store.FileAccessEvent{MachineID: "m<1>", RuleName: "<script>rule</script>", Target: "/tmp/<img src=x>",
		Decision: store.FileAccessDecisionDenied, AccessTime: fixedNow, ProcessChain: []store.Process{{FilePath: "/tmp/<b>evil</b>", SigningID: `"><svg>`}}})
	a := newApp(t, s, 10)
	code, body := a.viewerGet(t, "/events/file-access")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustNotContain(t, body, "<script>rule", "<img src=x>", "<b>evil</b>", `"><svg>`)
}

// Without an events table the list answers as the executions list does, without leaking the store's error text.
func TestFileAccessEvents_UnavailableIsExplained(t *testing.T) {
	s := faaStore()
	s.SetError(store.ErrUnavailable)
	a := newApp(t, s, 10)
	execCode, _ := a.viewerGet(t, "/events")
	code, body := a.viewerGet(t, "/events/file-access")
	if code != execCode || code == http.StatusOK {
		t.Errorf("status %d; want %d, as for the executions list", code, execCode)
	}
	mustNotContain(t, body, "store:")
}
