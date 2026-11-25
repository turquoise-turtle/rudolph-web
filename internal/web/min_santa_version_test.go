package web_test

// Minimum Santa version (owner, 2026-11-22): Santa 2026.9 fixed two high-severity ways a local user could crash
// santad and pause enforcement (northpolesec/santa security advisories, 2026-10-27), so the owner wants to see which
// Macs still run an older release. A Mac whose reported version parses and is below the minimum gets an "update
// needed" marker, with the minimum named, next to its version on the machine list and its page; the "legacy client"
// marker below 2026.8 stays as it was. An empty or unparseable reported version gets no marker.
//
// The minimum is one stored value (owner decisions for the editable minimum, after Rudolph 05f9516): Rudolph's fleet
// health setting, FleetHealth / Config, attribute MinSantaVersion, read through store.FleetReader.FleetHealthConfig
// (store.FleetHealthConfig.MinSantaVersion, in force by MinSantaVersionInForce). The web view's own setting is gone:
// no web.Options.MinSantaVersion, no RUDOLPH_WEB_MIN_SANTA_VERSION.
//
//   - In force: the stored release; Rudolph's fleethealth.DefaultMinSantaVersion (2026.9) when none is stored; "off"
//     turns the marker off everywhere. Any release Rudolph accepts counts, including one with a suffix
//     (2026.10-beta compares as 2026.10).
//   - The machine list and the machine page read it with one FleetHealthConfig (one GetItem), cached for 60 seconds
//     per handler (web.New) by Options.Now: a read at most 60 seconds old is reused, an older one read again. A
//     request refused before the page reads its own data (a bad filter) does not read it either.
//   - A failed read is logged with its error, and the page still renders, marking Macs below the default. A failure
//     is not cached: the next page view reads again.
//   - /fleet already reads the setting for the stale threshold on every view, and compares the snapshot's
//     MinSantaVersion with the minimum in force by that same read, so /fleet makes no further FleetHealthConfig call
//     and is never behind a change. It warns ("Warning") when the two differ, saying what each is: the minimum the
//     last fleet run listed Macs below (a release, or off) and the minimum in force now, and that the fleet's lists
//     follow at the job's next run. Releases that name the same release (2026.9 and 2026.9.0) agree, as do off and
//     off. A snapshot that does not say which minimum it used (older pointers) gets no warning. The warning no longer
//     tells anyone to set FLEET_MIN_SANTA_VERSION or min_santa_version, which are both gone.
//
// Writes from the web view (the form on /admin/config) update the marker at once: min_santa_version_edit_test.go.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

// newAppWithMinVersion is the app over s with stored as the stored minimum Santa version: a release, "off", or "" for
// none stored.
func newAppWithMinVersion(t *testing.T, s *fake.Store, stored string) *app {
	t.Helper()
	s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: stored})
	return newApp(t, s, 10)
}

func versionStore() *fake.Store {
	s := fake.New()
	for id, v := range map[string]string{
		"m-legacy": "2025.12.0", "m-old": "2026.8.1", "m-min": "2026.9", "m-new": "2026.10.2", "m-empty": "", "m-odd": "nightly",
	} {
		s.AddMachine(store.Machine{MachineID: id, SerialNum: "C02" + id, Hostname: "host-" + id, SantaVersion: v, LastSeen: fixedNow})
	}
	return s
}

// marked returns which of versionStore's Macs their pages mark "update needed", failing on a page that does not load.
func marked(t *testing.T, a *app) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, id := range []string{"m-legacy", "m-old", "m-min", "m-new", "m-empty", "m-odd"} {
		code, body := a.viewerGet(t, "/machines/"+id)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", id, code)
		}
		out[id] = strings.Contains(body, "update needed")
	}
	return out
}

func minVersionReads(s *fake.Store) int {
	n := 0
	for _, c := range s.Calls() {
		if c.Method == "FleetHealthConfig" {
			n++
		}
	}
	return n
}

func TestMinSantaVersion_MarkedBelowTheMinimum(t *testing.T) {
	a := newAppWithMinVersion(t, versionStore(), "2026.9")
	want := map[string]bool{"m-legacy": true, "m-old": true, "m-min": false, "m-new": false, "m-empty": false, "m-odd": false}
	if got := marked(t, a); !reflect.DeepEqual(got, want) {
		t.Errorf("marked %v, want %v", got, want)
	}
	_, body := a.viewerGet(t, "/machines/m-old")
	mustContain(t, body, "update needed", "2026.9")
	_, body = a.viewerGet(t, "/machines/m-legacy")
	mustContain(t, body, "legacy client")

	_, body = a.viewerGet(t, "/machines")
	if n := strings.Count(body, "update needed"); n != 2 {
		t.Errorf("the machine list marks %d Macs, want the 2 below 2026.9", n)
	}
}

// Nothing stored: Rudolph's default, 2026.9, applies, as it does for the fleet job.
func TestMinSantaVersion_DefaultWhenNoneIsStored(t *testing.T) {
	a := newAppWithMinVersion(t, versionStore(), "")
	want := map[string]bool{"m-legacy": true, "m-old": true, "m-min": false, "m-new": false, "m-empty": false, "m-odd": false}
	if got := marked(t, a); !reflect.DeepEqual(got, want) {
		t.Errorf("marked %v, want %v (below the default 2026.9)", got, want)
	}
	_, body := a.viewerGet(t, "/machines/m-old")
	if text := visibleText(body); !strings.Contains(text, "update needed (below 2026.9)") {
		t.Errorf("the page does not name the default; its Santa line is %q", santaLine(text))
	}
}

func TestMinSantaVersion_TheStoredValueIsUsed(t *testing.T) {
	a := newAppWithMinVersion(t, versionStore(), "2026.10")
	want := map[string]bool{"m-legacy": true, "m-old": true, "m-min": true, "m-new": false, "m-empty": false, "m-odd": false}
	if got := marked(t, a); !reflect.DeepEqual(got, want) {
		t.Errorf("marked %v, want %v (below the stored 2026.10)", got, want)
	}
	_, body := a.viewerGet(t, "/machines/m-min")
	if text := visibleText(body); !strings.Contains(text, "update needed (below 2026.10)") {
		t.Errorf("the page does not name the stored minimum; its Santa line is %q", santaLine(text))
	}
	_, body = a.viewerGet(t, "/machines")
	if n := strings.Count(body, "update needed"); n != 3 {
		t.Errorf("the machine list marks %d Macs, want the 3 below 2026.10", n)
	}
}

// Rudolph accepts a release with a suffix and compares it as the release; the marker must not silently vanish.
func TestMinSantaVersion_AReleaseWithASuffix(t *testing.T) {
	a := newAppWithMinVersion(t, versionStore(), "2026.10-beta")
	want := map[string]bool{"m-legacy": true, "m-old": true, "m-min": true, "m-new": false, "m-empty": false, "m-odd": false}
	if got := marked(t, a); !reflect.DeepEqual(got, want) {
		t.Errorf("marked %v, want %v (below 2026.10)", got, want)
	}
}

func TestMinSantaVersion_OffTurnsItOff(t *testing.T) {
	a := newAppWithMinVersion(t, versionStore(), "off")
	for _, path := range []string{"/machines", "/machines/m-old", "/machines/m-legacy"} {
		_, body := a.viewerGet(t, path)
		mustNotContain(t, body, "update needed")
	}
	_, body := a.viewerGet(t, "/machines/m-legacy")
	mustContain(t, body, "legacy client")
}

func TestWebOptions_HaveNoMinSantaVersion(t *testing.T) {
	if _, ok := reflect.TypeOf(web.Options{}).FieldByName("MinSantaVersion"); ok {
		t.Error("web.Options still has MinSantaVersion; the minimum is Rudolph's stored setting, read at run time")
	}
}

func TestMinSantaVersion_CachedFor60Seconds(t *testing.T) {
	s := versionStore()
	s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: "2026.10"})
	c := &clock{now: fixedNow}
	a := newFleetApp(t, s, nil, func(o *web.Options) { o.Now = c.Now })
	s.ResetCalls()

	note := func(id string) string {
		t.Helper()
		_, body := a.viewerGet(t, "/machines/"+id)
		return santaLine(visibleText(body))
	}
	if line := note("m-min"); !strings.Contains(line, "update needed (below 2026.10)") {
		t.Fatalf("m-min: %q, want it below the stored 2026.10", line)
	}
	a.viewerGet(t, "/machines")
	a.viewerGet(t, "/machines/m-old")
	if n := minVersionReads(s); n != 1 {
		t.Errorf("%d FleetHealthConfig reads for three pages within a minute, want 1", n)
	}

	// Changed meanwhile, as by `rudolph fleet min-santa-version 2026.8`.
	s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: "2026.8"})
	c.advance(59 * time.Second)
	if line := note("m-min"); !strings.Contains(line, "update needed (below 2026.10)") {
		t.Errorf("59 seconds later m-min reads %q; the minimum read a minute ago is still used", line)
	}
	if n := minVersionReads(s); n != 1 {
		t.Errorf("%d reads after 59 seconds, want still 1", n)
	}
	c.advance(2 * time.Second)
	if line := note("m-min"); strings.Contains(line, "update needed") {
		t.Errorf("61 seconds later m-min reads %q; the new minimum, 2026.8, is not used", line)
	}
	if line := note("m-legacy"); !strings.Contains(line, "update needed (below 2026.8)") {
		t.Errorf("61 seconds later m-legacy reads %q, want it below 2026.8", line)
	}
	if n := minVersionReads(s); n != 2 {
		t.Errorf("%d reads after 61 seconds, want 2", n)
	}
}

// failingSetting is a store whose fleet health setting cannot be read while fail is set.
type failingSetting struct {
	*fake.Store
	mu   sync.Mutex
	fail bool
}

func (f *failingSetting) setFail(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = v
}

func (f *failingSetting) FleetHealthConfig(ctx context.Context) (store.FleetHealthConfig, error) {
	f.mu.Lock()
	fail := f.fail
	f.mu.Unlock()
	if fail {
		return store.FleetHealthConfig{}, errors.New("dynamo: reading the fleet health setting: throttled by the test")
	}
	return f.Store.FleetHealthConfig(ctx)
}

// syncBuffer is a log destination that requests may write to concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestMinSantaVersion_AFailedReadFallsBackToTheDefault(t *testing.T) {
	f := &failingSetting{Store: versionStore(), fail: true}
	f.Store.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: "2026.10"})
	var logs syncBuffer
	a := newFleetApp(t, f, nil, func(o *web.Options) { o.Logger = slog.New(slog.NewTextHandler(&logs, nil)) })

	code, body := a.viewerGet(t, "/machines/m-old")
	if code != http.StatusOK {
		t.Fatalf("status %d; the page must render without the setting", code)
	}
	if text := visibleText(body); !strings.Contains(text, "update needed (below 2026.9)") {
		t.Errorf("with the setting unreadable m-old reads %q; want the default, 2026.9", santaLine(text))
	}
	if code, body := a.viewerGet(t, "/machines"); code != http.StatusOK || strings.Count(body, "update needed") != 2 {
		t.Errorf("machine list: status %d, %d marked; want 200 and the 2 below the default", code, strings.Count(body, "update needed"))
	}
	if !strings.Contains(logs.String(), "throttled by the test") {
		t.Errorf("the failed read is not logged with its error; the log holds:\n%s", logs.String())
	}

	f.setFail(false)
	_, body = a.viewerGet(t, "/machines/m-min")
	if text := visibleText(body); !strings.Contains(text, "update needed (below 2026.10)") {
		t.Errorf("once the setting can be read m-min reads %q; a failure must not be cached", santaLine(text))
	}
}

// warningOf returns the text of the paragraph holding "Warning", or "" when there is none.
func warningOf(body string) string {
	for _, chunk := range strings.Split(body, "</p>") {
		if i := strings.Index(chunk, "Warning"); i >= 0 {
			if start := strings.LastIndex(chunk[:i], "<p"); start >= 0 {
				return visibleText(chunk[start:])
			}
			return visibleText(chunk)
		}
	}
	return ""
}

func TestFleetPage_WarnsWhenMinVersionDiffers(t *testing.T) {
	for _, c := range []struct {
		name, snapshot, stored string
		names                  []string // nil: no warning
	}{
		{"older run, default in force", "2026.8", "", []string{"2026.8", "2026.9"}},
		{"same as stored", "2026.8", "2026.8", nil},
		{"same as stored, newer", "2026.10", "2026.10", nil},
		{"same release spelt longer", "2026.9", "2026.9.0", nil},
		{"same as the default", "2026.9", "", nil},
		{"both off", "off", "off", nil},
		{"run off, default in force", "off", "", []string{"off", "2026.9"}},
		{"run with the default, off in force", "2026.9", "off", []string{"2026.9", "off"}},
		{"run with a release, another stored", "2026.9", "2026.10", []string{"2026.9", "2026.10"}},
		{"an older pointer that does not say", "", "2026.10", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := fake.New()
			sum := fleetSummary()
			sum.MinSantaVersion = c.snapshot
			s.SetFleetSnapshot(fake.FleetSnapshot{Summary: sum})
			s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: c.stored})
			code, body := newFleetApp(t, s, nil, nil).viewerGet(t, "/fleet")
			if code != http.StatusOK {
				t.Fatalf("status %d", code)
			}
			warning := warningOf(body)
			if c.names == nil {
				if warning != "" {
					t.Errorf("a warning where the minimums agree: %q", warning)
				}
				return
			}
			if warning == "" {
				t.Fatalf("no warning; the run's minimum %q and the one in force differ", c.snapshot)
			}
			for _, v := range c.names {
				if !strings.Contains(warning, v) {
					t.Errorf("the warning %q does not name %s", warning, v)
				}
			}
			if !strings.Contains(warning, "next") {
				t.Errorf("the warning %q does not say the fleet's lists follow at the job's next run", warning)
			}
			for _, gone := range []string{"FLEET_MIN_SANTA_VERSION", "min_santa_version"} {
				if strings.Contains(warning, gone) {
					t.Errorf("the warning %q still points at %s, which is gone", warning, gone)
				}
			}
		})
	}
}

// /fleet compares with the setting as it reads it on that view, not with a cached copy.
func TestFleetPage_ComparesWithTheSettingReadOnThatView(t *testing.T) {
	s := versionStore()
	sum := fleetSummary()
	sum.MinSantaVersion = "2026.8"
	s.SetFleetSnapshot(fake.FleetSnapshot{Summary: sum})
	a := newFleetApp(t, s, nil, nil)
	a.viewerGet(t, "/machines/m-old") // caches the default, 2026.9
	_, body := a.viewerGet(t, "/fleet")
	if warningOf(body) == "" {
		t.Fatal("no warning while the run used 2026.8 and the default 2026.9 is in force")
	}
	s.SetFleetHealthConfig(store.FleetHealthConfig{MinSantaVersion: "2026.8"})
	s.ResetCalls()
	_, body = a.viewerGet(t, "/fleet")
	if w := warningOf(body); w != "" {
		t.Errorf("/fleet warns %q after 2026.8 was stored; it must compare with the setting it reads", w)
	}
	if n := minVersionReads(s); n != 1 {
		t.Errorf("/fleet made %d FleetHealthConfig calls, want its one read", n)
	}
}
