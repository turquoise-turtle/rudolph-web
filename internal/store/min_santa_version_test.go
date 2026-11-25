package store_test

// The minimum Santa version is one stored value, Rudolph's (PK FleetHealth, SK Config, attribute MinSantaVersion,
// pkg/model/fleethealth). store.FleetHealthConfig.MinSantaVersion holds it as fleethealth.FromItem reads it: a release,
// "off", or "" when nothing usable is stored. MinSantaVersionInForce answers as Rudolph's
// fleethealth.Config.MinSantaVersionInForce does, so the default is Rudolph's fleethealth.DefaultMinSantaVersion and
// never a copy of the web view's own.

import (
	"testing"

	"github.com/airbnb/rudolph/pkg/model/fleethealth"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

func TestFleetHealthConfig_MinSantaVersionInForce(t *testing.T) {
	for _, c := range []struct {
		raw           string
		version       string
		off, inStored bool
	}{
		{"", fleethealth.DefaultMinSantaVersion, false, false},
		{"2026.10", "2026.10", false, true},
		{"2026.10.1", "2026.10.1", false, true},
		{"off", "off", true, true},
	} {
		cfg := store.FleetHealthConfig{StaleDays: 10, MinSantaVersion: c.raw}
		version, off, stored := cfg.MinSantaVersionInForce()
		if version != c.version || off != c.off || stored != c.inStored {
			t.Errorf("stored %q: in force (%q, off %v, stored %v), want (%q, %v, %v)", c.raw, version, off, stored, c.version, c.off, c.inStored)
		}
	}
}
