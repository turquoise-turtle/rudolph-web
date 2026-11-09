package web_test

// Owner feedback from the production walkthrough (step 10.1): the events table's commands were in its last column,
// after the hashes, off the right of a laptop screen, so the owner never found them. They now come right after the
// file, before the wide hash columns.

import (
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func TestEvents_CommandsComeRightAfterTheFile(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "m-e", SerialNum: "C02E", Hostname: "e.example.com", LastSeen: fixedNow})
	s.AddExecutionEvent(store.ExecutionEvent{MachineID: "m-e", FileName: "tool", FilePath: "/usr/local/bin",
		FileSHA256: editSHA, Decision: store.Decision("BLOCK_BINARY"), ExecutionTime: fixedNow.Add(-time.Hour)})
	a := newApp(t, s, 10)
	code, body := a.viewerGet(t, "/events")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	var headers []string
	for _, m := range regexp.MustCompile(`<th>([^<]*)</th>`).FindAllStringSubmatch(body, -1) {
		headers = append(headers, m[1])
	}
	file, cmds, sha := -1, -1, -1
	for i, h := range headers {
		switch h {
		case "File":
			file = i
		case "Rudolph CLI":
			cmds = i
		case "SHA-256":
			sha = i
		}
	}
	if file < 0 || cmds < 0 || sha < 0 {
		t.Fatalf("headers %q: want File, Rudolph CLI and SHA-256", headers)
	}
	if cmds != file+1 || sha < cmds {
		t.Errorf("headers %q: want Rudolph CLI right after File and before SHA-256", headers)
	}
	mustContain(t, body, "<summary>Commands</summary>")
}
