package web_test

// Client versions (Rudolph's docs/sync.md): Santa 2026.8 or newer is supported; 2025.12 only for macOS 13 Macs, with
// limits. The machine list and page show a "legacy client" marker next to the reported Santa version below 2026.8, so
// the owner can see which Macs are on the floor and when the last one is gone. Events Santa marked identity_unverified
// carry a warning that their hash and file details are not verified.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func TestClientVersions_LegacyMarker(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "m-old", SerialNum: "C02OLD", Hostname: "old-imac", SantaVersion: "2025.12.0", LastSeen: fixedNow})
	s.AddMachine(store.Machine{MachineID: "m-new", SerialNum: "C02NEW", Hostname: "new-mac", SantaVersion: "2026.9.0", LastSeen: fixedNow})
	s.AddMachine(store.Machine{MachineID: "m-odd", SerialNum: "C02ODD", Hostname: "odd-mac", SantaVersion: "", LastSeen: fixedNow})
	a := newApp(t, s, 10)

	code, body := a.viewerGet(t, "/machines/m-old")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "2025.12.0", "legacy client")
	for _, id := range []string{"m-new", "m-odd"} {
		_, body = a.viewerGet(t, "/machines/"+id)
		mustNotContain(t, body, "legacy client")
	}

	_, body = a.viewerGet(t, "/machines")
	mustContain(t, body, "2025.12.0", "legacy client", "2026.9.0")
}

func TestClientVersions_IdentityUnverifiedEventsAreFlagged(t *testing.T) {
	s := fake.New()
	s.AddExecutionEvent(store.ExecutionEvent{MachineID: "m1", FileSHA256: "sha-unverified", FileName: "suspect", Decision: store.DecisionBlockBinaryMismatch, ExecutionTime: fixedNow, IdentityUnverified: true})
	s.AddExecutionEvent(store.ExecutionEvent{MachineID: "m1", FileSHA256: "sha-ok", FileName: "fine", Decision: store.DecisionBlockUnknown, ExecutionTime: fixedNow.Add(-1)})
	a := newApp(t, s, 10)
	code, body := a.viewerGet(t, "/events")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "suspect", "identity not verified")
	if n := strings.Count(body, "identity not verified"); n != 1 {
		t.Errorf("%d events flagged, want only the unverified one", n)
	}
}
