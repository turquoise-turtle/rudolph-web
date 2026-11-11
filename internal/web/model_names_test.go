package web_test

// A Mac's page shows Apple's name for its model after the identifier (owner request, 2026-11-11):
// "Mac16,12 - MacBook Air (13-inch, M4, 2025)"; an identifier the table doesn't know is shown alone. The machine list
// keeps the bare identifier.

import (
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func TestMachinePage_ShowsTheModelName(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "m-air", SerialNum: "C02AIR", Hostname: "air.example.com", ModelIdentifier: "Mac16,12", ClientMode: store.ClientModeMonitor, LastSeen: fixedNow})
	s.AddMachine(store.Machine{MachineID: "m-new", SerialNum: "C02NEW", Hostname: "new.example.com", ModelIdentifier: "Mac99,1", ClientMode: store.ClientModeMonitor, LastSeen: fixedNow})
	a := newApp(t, s, 10)

	_, body := a.viewerGet(t, "/machines/m-air")
	mustContain(t, body, "Mac16,12 - MacBook Air (13-inch, M4, 2025)")

	_, body = a.viewerGet(t, "/machines/m-new")
	mustContain(t, body, "Mac99,1")
	mustNotContain(t, body, "Mac99,1 -")

	_, list := a.viewerGet(t, "/machines")
	mustContain(t, list, "<td>Mac16,12</td>")
}
