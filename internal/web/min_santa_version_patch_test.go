package web_test

// The "update needed" marker and the preview on /admin/config compare releases exactly as Rudolph's fleet job does
// (internal/fleet in Rudolph): both versions go through rudolphtypes.ParseSantaVersion and are compared with Before,
// which looks at the year and month only. So a stored minimum of 2026.10.1 marks the Macs 2026.10 marks: a Mac on
// 2026.10.0 is not below it, whatever the patch levels say, and the pages never disagree with the fleet's Version list.
// A minimum with a suffix (2026.10.1-beta) is read as its release, as the fleet job reads it.
//
// The expectations are worked out with Rudolph's own parser from the pinned module, not spelt out here, so a later
// Rudolph that changes the comparison changes what these tests expect with it.

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	rudolphtypes "github.com/airbnb/rudolph/pkg/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

var patchMacs = []struct{ id, version string }{
	{"m-patch-0", "2026.10.0"},
	{"m-patch-2", "2026.10.2"},
	{"m-older", "2026.9.5"},
}

func patchStore() *fake.Store {
	s := fake.New()
	for _, m := range patchMacs {
		s.AddMachine(store.Machine{MachineID: m.id, SerialNum: "C02" + m.id, Hostname: "host-" + m.id, SantaVersion: m.version, LastSeen: fixedNow})
	}
	return s
}

// fleetJobLists reports whether Rudolph's fleet job lists a Mac reporting version under Version at minimum.
func fleetJobLists(version, minimum string) bool {
	return rudolphtypes.ParseSantaVersion(version).Before(rudolphtypes.ParseSantaVersion(minimum))
}

func fleetJobCount(minimum string) int {
	n := 0
	for _, m := range patchMacs {
		if fleetJobLists(m.version, minimum) {
			n++
		}
	}
	return n
}

// The fixture is only worth anything while Rudolph ignores the patch level: 2026.10.0 not below 2026.10.1.
func TestMinSantaVersionPatch_RudolphComparesYearAndMonth(t *testing.T) {
	for _, minimum := range []string{"2026.10.1", "2026.10.1-beta"} {
		want := map[string]bool{"2026.10.0": false, "2026.10.2": false, "2026.9.5": true}
		for version, below := range want {
			if got := fleetJobLists(version, minimum); got != below {
				t.Errorf("Rudolph: %s below %s is %v, want %v; the fleet job's comparison changed, so review these tests", version, minimum, got, below)
			}
		}
	}
}

func TestMinSantaVersionPatch_TheMarkerAgreesWithTheFleetJob(t *testing.T) {
	for _, minimum := range []string{"2026.10.1", "2026.10.1-beta"} {
		a := newAppWithMinVersion(t, patchStore(), minimum)
		marked := 0
		for _, m := range patchMacs {
			code, body := a.viewerGet(t, "/machines/"+m.id)
			if code != http.StatusOK {
				t.Fatalf("minimum %s, %s: status %d", minimum, m.id, code)
			}
			want := fleetJobLists(m.version, minimum)
			if want {
				marked++
			}
			line := santaLine(visibleText(body))
			if got := strings.Contains(line, "update needed"); got != want {
				t.Errorf("minimum %s: the page of a Mac on %s reads %q; marked %v, but the fleet job lists it: %v", minimum, m.version, line, got, want)
			}
			if want && !strings.Contains(line, "(below "+minimum+")") {
				t.Errorf("minimum %s: the page of a Mac on %s reads %q; want the stored minimum named", minimum, m.version, line)
			}
		}
		_, body := a.viewerGet(t, "/machines")
		if n := strings.Count(body, "update needed"); n != marked {
			t.Errorf("minimum %s: the machine list marks %d Macs, the fleet job lists %d", minimum, n, marked)
		}
	}
}

func TestMinSantaVersionPatch_ThePreviewAgreesWithTheFleetJob(t *testing.T) {
	for _, c := range []struct{ stored, candidate string }{
		{"", "2026.10.1"},
		{"", "2026.10.1-beta"},
		{"2026.10.1", "2026.11"},
		{"2026.10.1-beta", "2026.10"},
	} {
		s := patchStore()
		s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: c.stored})
		a := newEditApp(t, s, true)
		code, body := adminGet(t, a, "/admin/config?"+url.Values{"min_santa_version": {c.candidate}}.Encode())
		if code != http.StatusOK {
			t.Fatalf("stored %q, preview %s: status %d", c.stored, c.candidate, code)
		}
		section := minVersionSection(t, body)
		cand := previewRe("candidate").FindStringSubmatch(section)
		now := previewRe("in-force").FindStringSubmatch(section)
		if cand == nil || now == nil {
			t.Fatalf("stored %q, preview %s: no preview counts in %q", c.stored, c.candidate, visibleText(section))
		}
		inForce, _, _ := store.FleetHealthConfig{MinSantaVersion: c.stored}.MinSantaVersionInForce()
		if want := fmt.Sprintf("%d of %d Macs", fleetJobCount(c.candidate), len(patchMacs)); !strings.Contains(visibleText(cand[1]), want) {
			t.Errorf("stored %q, preview %s: the candidate reads %q, want %q as the fleet job would list", c.stored, c.candidate, visibleText(cand[1]), want)
		}
		if want := fmt.Sprintf("%d of %d Macs", fleetJobCount(inForce), len(patchMacs)); !strings.Contains(visibleText(now[1]), want) {
			t.Errorf("stored %q, preview %s: in force (%s) reads %q, want %q as the fleet job lists", c.stored, c.candidate, inForce, visibleText(now[1]), want)
		}
	}
}

// A Mac reporting a release with a suffix or build metadata is read as its release too, as the fleet job reads it.
func TestMinSantaVersionPatch_AReportedVersionWithASuffix(t *testing.T) {
	s := fake.New()
	versions := map[string]string{"m-beta": "2026.10.0-beta", "m-build": "2026.9.5 (1234)", "m-plus": "2026.10+build.7"}
	for id, v := range versions {
		s.AddMachine(store.Machine{MachineID: id, SerialNum: "C02" + id, Hostname: "host-" + id, SantaVersion: v, LastSeen: fixedNow})
	}
	a := newAppWithMinVersion(t, s, "2026.10.1")
	for id, v := range versions {
		_, body := a.viewerGet(t, "/machines/"+id)
		line := santaLine(visibleText(body))
		if got, want := strings.Contains(line, "update needed"), fleetJobLists(v, "2026.10.1"); got != want {
			t.Errorf("a Mac reporting %q reads %q; marked %v, but the fleet job lists it: %v", v, line, got, want)
		}
	}
}
