package web_test

// Owner request after the first production deployment: show times in each viewer's own time zone, converted in the
// browser rather than on the server. No request header carries the browser's time zone, so one small inline script,
// allowed by its hash in the CSP and by nothing else, rewrites each <time datetime> element. The server renders UTC
// only, which is what a viewer sees if the script does not run.

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

var (
	timeElement   = regexp.MustCompile(`(?s)<time datetime="[^"]*">.*?</time>`)
	renderedTime  = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}(:\d{2})? UTC`)
	scriptElement = regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)
)

func machineBody(t *testing.T) string {
	t.Helper()
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "m-z", SerialNum: "C02Z", Hostname: "z.example.com", ClientMode: store.ClientModeMonitor, LastSeen: fixedNow.Add(-5 * time.Minute)})
	s.SetSyncState("m-z", store.SyncState{PreflightAt: fixedNow.Add(-5 * time.Minute), PostflightAt: fixedNow.Add(-5*time.Minute + 5*time.Second), LastSyncType: "NORMAL"})
	s.SetTemporaryClientMode("m-z", store.ClientModeLockdown, fixedNow.Add(90*time.Minute))
	a := newApp(t, s, 10)
	code, body := a.viewerGet(t, "/machines/m-z")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	return body
}

func TestBrowserTime_MachinePageTimesAreTimeElements(t *testing.T) {
	body := machineBody(t)
	// fixedNow is 2026-09-17 12:00 UTC.
	mustContain(t, body,
		`<time datetime="2026-09-17T11:55:00Z">2026-09-17 11:55:00 UTC</time>`, // last seen and last preflight
		`<time datetime="2026-09-17T11:55:05Z">2026-09-17 11:55:05 UTC</time>`, // last postflight
		`<time datetime="2026-09-17T13:30:00Z">2026-09-17 13:30:00 UTC</time>`, // temporary mode end
	)
	// Every time on the page, including those in the sync health sentences, is inside a <time> element, so the
	// script can show all of them in the browser's zone.
	if rest := timeElement.ReplaceAllString(body, ""); renderedTime.MatchString(rest) {
		t.Errorf("a time outside a <time datetime> element: %q", renderedTime.FindAllString(rest, -1))
	}
}

func TestBrowserTime_EventsPageTimesAreTimeElements(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "m-e", SerialNum: "C02E", Hostname: "e.example.com", LastSeen: fixedNow})
	s.AddExecutionEvent(store.ExecutionEvent{MachineID: "m-e", FileName: "tool", FilePath: "/usr/local/bin", ExecutionTime: fixedNow.Add(-time.Hour)})
	a := newApp(t, s, 10)
	code, body := a.viewerGet(t, "/events")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, `<time datetime="2026-09-17T11:00:00Z">2026-09-17 11:00:00 UTC</time>`)
	if rest := timeElement.ReplaceAllString(body, ""); renderedTime.MatchString(rest) {
		t.Errorf("a time outside a <time datetime> element: %q", renderedTime.FindAllString(rest, -1))
	}
}

// pageScript returns the one inline script of a page, checking that there is exactly one and that it has no
// attributes (no src: nothing is loaded from elsewhere).
func pageScript(t *testing.T, body string) string {
	t.Helper()
	if n := strings.Count(strings.ToLower(body), "<script"); n != 1 {
		t.Fatalf("want exactly one <script> on the page, got %d", n)
	}
	m := scriptElement.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no <script>…</script> element")
	}
	if strings.TrimSpace(m[1]) != "" {
		t.Errorf("the script element has attributes %q; want a plain inline <script>", m[1])
	}
	return m[2]
}

func TestBrowserTime_OnlyTheHashedInlineScriptMayRun(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "m-z", SerialNum: "C02Z", Hostname: "z.example.com", LastSeen: fixedNow})
	a := newApp(t, s, 10)
	for _, page := range []string{"/machines", "/machines/m-z", "/events", "/rules"} {
		t.Run(page, func(t *testing.T) {
			rec := a.get(t, page, a.cookie(t, auth.RoleViewer))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			script := pageScript(t, rec.Body.String())
			sum := sha256.Sum256([]byte(script))
			want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
			d := parseCSP(t, rec.Header().Get("Content-Security-Policy"))
			if got := d["script-src"]; len(got) != 1 || got[0] != want {
				t.Errorf("CSP script-src = %q, want only the inline script's hash %s", got, want)
			}
			for _, name := range []string{"default-src", "frame-ancestors", "base-uri"} {
				if got := d[name]; len(got) != 1 || got[0] != "'none'" {
					t.Errorf("CSP %s = %q, want 'none'", name, got)
				}
			}
		})
	}
}

// The script only reads datetime attributes and writes text: it never builds markup, loads anything or stores anything.
func TestBrowserTime_ScriptOnlyWritesText(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	_, body := a.viewerGet(t, "/machines")
	script := pageScript(t, body)
	mustContain(t, script, "time[datetime]", "Intl.DateTimeFormat", "textContent")
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "Function(",
		"fetch(", "XMLHttpRequest", "cookie", "localStorage", "sessionStorage", "import(", "setTimeout(\"", "src"} {
		if strings.Contains(script, bad) {
			t.Errorf("the script uses %q", bad)
		}
	}
}

// runScript runs the page's script under Node with a minimal DOM holding one <time> element per datetime, and returns
// each element's text afterwards.
func runScript(t *testing.T, script, tz string, datetimes ...string) []string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the script's behaviour is not checked (install Node to check it)")
	}
	harness := `
const fs = require("fs");
const input = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const elements = input.datetimes.map(d => ({
  textContent: "server text",
  getAttribute: name => (name === "datetime" ? d : null),
  setAttribute: () => {},
}));
const listeners = [];
globalThis.document = {
  readyState: "loading",
  querySelectorAll: sel => {
    if (sel !== "time[datetime]") throw new Error("unexpected selector " + sel);
    return elements;
  },
  addEventListener: (type, fn) => listeners.push([type, fn]),
};
globalThis.window = globalThis;
globalThis.addEventListener = (type, fn) => listeners.push([type, fn]);
(0, eval)(input.script);
document.readyState = "interactive";
for (const [type, fn] of listeners) if (type === "DOMContentLoaded" || type === "load") fn();
process.stdout.write(JSON.stringify(elements.map(e => e.textContent)));
`
	dir := t.TempDir()
	harnessPath := filepath.Join(dir, "harness.js")
	inputPath := filepath.Join(dir, "input.json")
	if err := os.WriteFile(harnessPath, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	input := `{"script":` + jsonString(script) + `,"datetimes":[`
	for i, d := range datetimes {
		if i > 0 {
			input += ","
		}
		input += jsonString(d)
	}
	input += "]}"
	if err := os.WriteFile(inputPath, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, harnessPath, inputPath)
	cmd.Env = append(os.Environ(), "TZ="+tz, "LANG=en_AU.UTF-8", "LC_ALL=en_AU.UTF-8")
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("node: %v\n%s", err, stderr)
	}
	var texts []string
	s := strings.Trim(string(out), "[]")
	for _, part := range regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`).FindAllStringSubmatch(s, -1) {
		texts = append(texts, part[1])
	}
	if len(texts) != len(datetimes) {
		t.Fatalf("node output %s: want %d texts", out, len(datetimes))
	}
	return texts
}

func jsonString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`, " ", ` `, " ", ` `)
	return `"` + r.Replace(s) + `"`
}

func TestBrowserTime_ScriptShowsTheBrowsersZone(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	_, body := a.viewerGet(t, "/machines")
	script := pageScript(t, body)

	// The zone's short name depends on the browser's locale (JST or GMT+9), so only its shape is fixed.
	tokyo := runScript(t, script, "Asia/Tokyo", "2026-09-17T11:55:00Z", "2026-12-31T23:30:00Z")
	for i, want := range []*regexp.Regexp{
		regexp.MustCompile(`^2026-09-17 11:55:00 UTC \(2026-09-17 20:55 (JST|GMT\+9|UTC\+9)\)$`),
		regexp.MustCompile(`^2026-12-31 23:30:00 UTC \(2027-01-01 08:30 (JST|GMT\+9|UTC\+9)\)$`),
	} {
		if !want.MatchString(tokyo[i]) {
			t.Errorf("Asia/Tokyo: got %q, want %s", tokyo[i], want)
		}
	}

	if got := runScript(t, script, "UTC", "2026-09-17T11:55:00Z"); got[0] != "2026-09-17 11:55:00 UTC" {
		t.Errorf("UTC browser: got %q, want UTC only", got[0])
	}

	// Daylight saving: New York is UTC-4 in September and UTC-5 in December.
	ny := runScript(t, script, "America/New_York", "2026-09-17T11:55:00Z", "2026-12-17T11:55:00Z")
	if !strings.HasPrefix(ny[0], "2026-09-17 11:55:00 UTC (2026-09-17 07:55 ") {
		t.Errorf("America/New_York in September: got %q", ny[0])
	}
	if !strings.HasPrefix(ny[1], "2026-12-17 11:55:00 UTC (2026-12-17 06:55 ") {
		t.Errorf("America/New_York in December: got %q", ny[1])
	}

	// A datetime the script cannot parse keeps the server's text.
	if got := runScript(t, script, "Asia/Tokyo", "not a time", ""); got[0] != "server text" || got[1] != "server text" {
		t.Errorf("unparseable datetime: got %q, want the server text kept", got)
	}
}
