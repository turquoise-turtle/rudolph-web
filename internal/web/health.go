package web

import (
	"fmt"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

const (
	// Santa syncs every FullSyncInterval (600 seconds by default), so a Mac
	// that has not been seen for a day is off, offline or failing to sync.
	notSeenAfter = 24 * time.Hour
	// Santa sends postflight seconds after preflight; half an hour without
	// one means the sync failed or its postflight was lost.
	postflightGrace = 30 * time.Minute
	// Rudolph ignores a temporary mode ending further ahead than this (30
	// days plus 10 minutes of clock slack), so it cannot become permanent.
	temporaryModeMaxAhead = 30*24*time.Hour + 10*time.Minute
)

// healthItem is one entry of the machine page's "Sync health" section.
// Warning marks a problem; other entries are information worth knowing. An
// entry that names a time reads Text, At, After; the template renders At as a
// <time> element so the browser can show it in the viewer's zone.
type healthItem struct {
	Warning bool
	Text    string
	At      time.Time // zero: the entry names no time
	After   string
}

// syncHealth is what the machine page shows about how a Mac syncs, worked
// out against now.
type syncHealth struct {
	Items []healthItem
	// TemporaryActive is set while a temporary client mode applies, so the
	// configured mode line shows it rather than the usual mode.
	TemporaryActive bool
	// Problems is set when at least one entry is a warning.
	Problems bool
}

// machineHealth works out the sync health of m. inherited is the client mode
// the Mac gets when it sets none: its groups', or the global config's (Group
// ""); Mode is "" when that could not be read.
func machineHealth(m store.MachineDetail, now time.Time, inherited groupMode) syncHealth {
	var h syncHealth
	warn := func(it healthItem) {
		h.Problems = true
		it.Warning = true
		h.Items = append(h.Items, it)
	}
	note := func(it healthItem) {
		h.Items = append(h.Items, it)
	}

	if !m.LastSeen.IsZero() {
		if age := now.Sub(m.LastSeen); age > notSeenAfter {
			days := int(age / (24 * time.Hour))
			unit := "days"
			if days == 1 {
				unit = "day"
			}
			warn(healthItem{Text: fmt.Sprintf("Not seen for %d %s (last seen ", days, unit), At: m.LastSeen, After: ")."})
		}
	}

	if s := m.Sync; s != nil && !s.PreflightAt.IsZero() && s.PostflightAt.Before(s.PreflightAt) &&
		now.Sub(s.PreflightAt) >= postflightGrace {
		warn(healthItem{Text: "The last sync did not report completion: no postflight after the preflight at ", At: s.PreflightAt, After: "."})
	}

	if m.CleanSyncRequested {
		warn(healthItem{Text: "Clean sync requested: the Mac will clear and download all its rules at its next sync."})
	}

	if mode, until := m.TemporaryClientMode, m.TemporaryClientModeUntil; mode != "" {
		switch {
		case !now.Before(until):
			note(healthItem{Text: fmt.Sprintf("Temporary %s ended ", mode), At: until, After: " and is ignored."})
		case until.Sub(now) > temporaryModeMaxAhead:
			warn(healthItem{Text: fmt.Sprintf("Temporary %s is ignored: its end, ", mode), At: until, After: ", is more than 30 days ahead."})
		default:
			h.TemporaryActive = true
			returnsTo := "the global config's mode"
			switch {
			case m.ConfiguredClientMode != "":
				returnsTo = string(m.ConfiguredClientMode)
			case inherited.Mode != "" && inherited.Group != "":
				returnsTo = fmt.Sprintf("%s (group %s's mode)", inherited.Mode, inherited.Group)
			case inherited.Mode != "":
				returnsTo = string(inherited.Mode) + " (the global config's mode)"
			}
			note(healthItem{Text: fmt.Sprintf("Temporary %s until ", mode), At: until, After: fmt.Sprintf(", then the Mac returns to %s at its next sync.", returnsTo)})
		}
	}
	return h
}
