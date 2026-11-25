package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// The minimum Santa version is Rudolph's stored fleet health setting, which
// its fleet job reads at every run. The pages read the same value, so the
// "update needed" marker and the fleet's Version list go by one minimum.

const (
	// minVersionCacheFor is how long the machine pages reuse one read of the
	// setting: the marker is on every machine page view, and a change made
	// with the CLI may take this long to show.
	minVersionCacheFor = 60 * time.Second
	// minVersionPreviewPages bounds the preview's walk of the machine list:
	// 50 pages of store.MaxPageSize, 5,000 Macs, read inside one request.
	minVersionPreviewPages = 50

	minVersionField = "min_santa_version"
	// minVersionResultParam says on /admin/config what a form just did.
	minVersionResultParam = "min_santa_version_result"
	minVersionSaved       = "saved"
	minVersionUnchanged   = "unchanged"
)

// minVersion is the minimum Santa version in force, as the templates use it.
type minVersion struct {
	// Version is a release such as 2026.10, or "off".
	Version string
	Off     bool
	// Stored is false when Version is Rudolph's default.
	Stored bool
}

func minVersionOf(cfg store.FleetHealthConfig) minVersion {
	version, off, stored := cfg.MinSantaVersionInForce()
	return minVersion{Version: version, Off: off, Stored: stored}
}

// Below reports whether a Mac reporting the Santa version v gets the "update
// needed" marker.
func (m minVersion) Below(v string) bool {
	return !m.Off && store.SantaVersionBelow(v, m.Version)
}

// minVersionCache is the last successful read of the minimum in force.
type minVersionCache struct {
	mu    sync.Mutex
	value minVersion
	at    time.Time
	ok    bool
}

func (c *minVersionCache) get(now time.Time) (minVersion, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value, c.ok && now.Sub(c.at) <= minVersionCacheFor
}

func (c *minVersionCache) set(v minVersion, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value, c.at, c.ok = v, now, true
}

func (c *minVersionCache) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ok = false
}

// minVersionInForce is the minimum the machine pages mark Macs below, from
// the cache or one read of the setting. When the setting cannot be read the
// pages still render, marking Macs below Rudolph's default; the failure is
// not cached, so the next page view reads again.
func (s *server) minVersionInForce(ctx context.Context) minVersion {
	if v, ok := s.minVersionCache.get(s.now()); ok {
		return v
	}
	cfg, err := s.store.FleetHealthConfig(ctx)
	if err != nil {
		s.log.Warn("reading the minimum Santa version failed; marking Macs below Rudolph's default", "err", err)
		return minVersionOf(store.FleetHealthConfig{})
	}
	v := minVersionOf(cfg)
	s.minVersionCache.set(v, s.now())
	return v
}

// minVersionPreview is what changing the minimum would do: how many of the
// Macs walked the marker would call "update needed" at the candidate, and how
// many it does at the minimum in force.
type minVersionPreview struct {
	// Candidate is the value as Rudolph would store it.
	Candidate string
	Macs      string
	Below     string
	BelowNow  string
	// Capped is set when the fleet has more Macs than the walk reads.
	Capped bool
}

// minVersionSection fills the minimum Santa version section of /admin/config.
type minVersionSection struct {
	// Unread is set when the setting could not be read; the section then
	// shows no forms, which would be filled from it.
	Unread  bool
	InForce minVersion
	Edit    bool
	// CanRun is set for admins who can start the fleet job.
	CanRun bool
	// Result is what a form just did: minVersionSaved or minVersionUnchanged.
	Result  string
	Preview *minVersionPreview
	// Typed is kept in the preview form's input.
	Typed string
}

// previewMinVersion walks the machine list, the sensor data the machine list
// already reads, and counts the Macs below candidate and below inForce. A Mac
// whose version cannot be read is walked but never below.
func (s *server) previewMinVersion(ctx context.Context, candidate string, inForce minVersion) (*minVersionPreview, error) {
	at := minVersionOf(store.FleetHealthConfig{MinSantaVersion: candidate})
	p := &minVersionPreview{Candidate: candidate}
	var macs, below, belowNow int
	pr := store.PageRequest{Limit: store.MaxPageSize}
	for range minVersionPreviewPages {
		page, err := s.store.ListMachines(ctx, store.MachineFilter{}, pr)
		if err != nil {
			return nil, err
		}
		for _, m := range page.Items {
			macs++
			if at.Below(m.SantaVersion) {
				below++
			}
			if inForce.Below(m.SantaVersion) {
				belowNow++
			}
		}
		pr.Cursor = page.NextCursor
		if pr.Cursor == "" {
			break
		}
	}
	p.Capped = pr.Cursor != ""
	p.Macs, p.Below, p.BelowNow = thousands(macs), thousands(below), thousands(belowNow)
	return p, nil
}

// minVersionSectionOf reads the setting for /admin/config, afresh rather than
// from the cache since this is where it is changed, and runs the preview an
// admin with editing asked for. ok is false when it has answered the request
// itself.
func (s *server) minVersionSectionOf(w http.ResponseWriter, r *http.Request) (section minVersionSection, ok bool) {
	section.Edit = s.canEdit(r)
	section.CanRun = section.Edit && s.fleetRunner != nil
	q := r.URL.Query()
	// The candidate is checked before anything is read: a refused value
	// costs no walk of the machine list.
	candidate, preview := "", section.Edit && q.Has(minVersionField)
	if preview {
		section.Typed = strings.TrimSpace(q.Get(minVersionField))
		var err error
		if candidate, err = rudolphrules.CheckMinSantaVersion(q.Get(minVersionField)); err != nil {
			s.renderError(w, r, http.StatusBadRequest, "Not previewed: "+strings.TrimPrefix(err.Error(), store.ErrInvalidArgument.Error()+": "))
			return section, false
		}
	}
	cfg, err := s.store.FleetHealthConfig(r.Context())
	if err != nil {
		s.log.Warn("reading the fleet health setting failed", "err", err)
		section.Unread = true
		section.InForce = minVersionOf(store.FleetHealthConfig{})
		return section, true
	}
	section.InForce = minVersionOf(cfg)
	s.minVersionCache.set(section.InForce, s.now())
	if section.Edit {
		switch result := q.Get(minVersionResultParam); result {
		case minVersionSaved, minVersionUnchanged:
			section.Result = result
		}
	}
	if preview {
		p, err := s.previewMinVersion(r.Context(), candidate, section.InForce)
		if err != nil {
			s.storeError(w, r, err)
			return section, false
		}
		section.Preview = p
	}
	return section, true
}

// minVersionRedirect sends the admin back to /admin/config after a change.
// The cached minimum is dropped first, so the marker follows at once.
func (s *server) minVersionRedirect(w http.ResponseWriter, r *http.Request, result string) {
	s.minVersionCache.forget()
	http.Redirect(w, r, "/admin/config?"+url.Values{minVersionResultParam: {result}}.Encode(), http.StatusSeeOther)
}

func (s *server) setMinSantaVersion(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	if err := s.writer.SetMinSantaVersion(r.Context(), actorOf(r), r.PostForm.Get(minVersionField)); err != nil {
		s.writeError(w, r, err, "")
		return
	}
	s.minVersionRedirect(w, r, minVersionSaved)
}

func (s *server) clearMinSantaVersion(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	err := s.writer.ClearMinSantaVersion(r.Context(), actorOf(r))
	switch {
	case errors.Is(err, store.ErrNothingToChange):
		// A second click, or the CLI clearing it meanwhile, leaves it as asked.
		s.minVersionRedirect(w, r, minVersionUnchanged)
	case err != nil:
		s.writeError(w, r, err, "")
	default:
		s.minVersionRedirect(w, r, minVersionSaved)
	}
}
