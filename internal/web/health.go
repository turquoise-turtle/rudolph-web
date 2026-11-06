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
// Warning marks a problem; other entries are information worth knowing.
type healthItem struct {
	Warning bool
	Text    string
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

func timeText(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05") + " UTC"
}

func machineHealth(m store.MachineDetail, now time.Time) syncHealth {
	var h syncHealth
	warn := func(format string, a ...any) {
		h.Problems = true
		h.Items = append(h.Items, healthItem{Warning: true, Text: fmt.Sprintf(format, a...)})
	}
	note := func(format string, a ...any) {
		h.Items = append(h.Items, healthItem{Text: fmt.Sprintf(format, a...)})
	}

	if !m.LastSeen.IsZero() {
		if age := now.Sub(m.LastSeen); age > notSeenAfter {
			days := int(age / (24 * time.Hour))
			unit := "days"
			if days == 1 {
				unit = "day"
			}
			warn("Not seen for %d %s (last seen %s).", days, unit, timeText(m.LastSeen))
		}
	}

	if s := m.Sync; s != nil && !s.PreflightAt.IsZero() && s.PostflightAt.Before(s.PreflightAt) &&
		now.Sub(s.PreflightAt) >= postflightGrace {
		warn("The last sync did not report completion: no postflight after the preflight at %s.", timeText(s.PreflightAt))
	}

	if m.CleanSyncRequested {
		warn("Clean sync requested: the Mac will clear and download all its rules at its next sync.")
	}

	if mode, until := m.TemporaryClientMode, m.TemporaryClientModeUntil; mode != "" {
		switch {
		case !now.Before(until):
			note("Temporary %s ended %s and is ignored.", mode, timeText(until))
		case until.Sub(now) > temporaryModeMaxAhead:
			warn("Temporary %s is ignored: its end, %s, is more than 30 days ahead.", mode, timeText(until))
		default:
			h.TemporaryActive = true
			returnsTo := "the global config's mode"
			if m.ConfiguredClientMode != "" {
				returnsTo = string(m.ConfiguredClientMode)
			}
			note("Temporary %s until %s, then the Mac returns to %s at its next sync.", mode, timeText(until), returnsTo)
		}
	}
	return h
}
