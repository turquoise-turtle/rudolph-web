package fake

import (
	"fmt"
	"sort"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// NewDemo returns a store seeded with plausible demo data for local development.
func NewDemo(now time.Time) *Store {
	s := New()
	models := []string{"Mac15,3", "Mac14,2", "MacBookPro18,1"}
	for i := 1; i <= 120; i++ {
		id := fmt.Sprintf("%08X-0000-4000-8000-%012d", i*7919, i)
		team := "eng"
		if i%3 == 0 {
			team = "ops"
		}
		mode := store.ClientModeLockdown
		if i%5 == 0 {
			mode = store.ClientModeMonitor
		}
		s.AddMachine(store.Machine{
			MachineID: id, SerialNum: fmt.Sprintf("C02X%05d", i), Hostname: fmt.Sprintf("%s-mbp-%03d", team, i),
			ModelIdentifier: models[i%len(models)], OSVersion: "15.6.1", OSBuild: "24G90", SantaVersion: "2026.8",
			ClientMode: mode, PrimaryUser: fmt.Sprintf("user%03d", i), SIPStatus: "enabled",
			RuleCounts: store.RuleCounts{Binary: 40 + i%7, Certificate: 3, TeamID: 12, SigningID: 5, CDHash: 1},
			LastSeen:   now.Add(-time.Duration(i) * 11 * time.Minute),
		})
		if i%20 == 0 {
			s.SetConfiguredClientMode(id, store.ClientModeLockdown)
		}
		if i%9 != 0 {
			s.SetSyncState(id, store.SyncState{
				PreflightAt: now.Add(-time.Duration(i) * 11 * time.Minute), PostflightAt: now.Add(-time.Duration(i)*11*time.Minute + 20*time.Second),
				LastSyncType: "NORMAL", RulesProcessed: i % 13,
			})
		}
		if i%10 == 1 {
			s.AddMachineRule(id, store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist,
				Identifier: fmt.Sprintf("%064x", i), Description: "Break-glass exception", ExpiresAt: now.Add(24 * time.Hour), State: store.RuleStateInForce})
		}
		if i%10 == 2 {
			s.AddMachineRule(id, store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist,
				Identifier: fmt.Sprintf("%064x", i), Description: "Expired exception", ExpiresAt: now.Add(-time.Hour), State: store.RuleStateExpired})
			s.AddMachineRule(id, store.Rule{RuleType: store.RuleTypeTeamID, Policy: store.PolicyBlocklist,
				Identifier: "UBF8T346G9", Description: "Withdrawn block", State: store.RuleStateRemoving})
		}
		for j := 0; j < 6; j++ {
			d := store.DecisionBlockUnknown
			name := fmt.Sprintf("tool-%d", (i+j)%9)
			if j%3 == 0 {
				d = store.DecisionAllowBinary
			}
			// The demo's global Team ID rule covers EQHXZ8M8AV, so the other
			// team's tools stay uncovered in the top unknown lists.
			devTeam := "EQHXZ8M8AV"
			if (i+j)%9 >= 6 {
				devTeam = "Q4W7E9R2T5"
			}
			s.AddExecutionEvent(store.ExecutionEvent{
				MachineID: id, ExecutionTime: now.Add(-time.Duration(i*j) * 7 * time.Minute), Decision: d,
				FileSHA256: fmt.Sprintf("%064x", 1000+(i+j)%9), FileName: name, FilePath: "/Users/Shared/" + name,
				ExecutingUser: fmt.Sprintf("user%03d", i), TeamID: devTeam, SigningID: devTeam + ":com.example." + name,
			})
		}
	}
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeTeamID, Policy: store.PolicyAllowlist, Identifier: "EQHXZ8M8AV", Description: "Google", State: store.RuleStateInForce})
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeTeamID, Policy: store.PolicyAllowlist, Identifier: "UBF8T346G9", Description: "Microsoft", State: store.RuleStateInForce})
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeSigningID, Policy: store.PolicyAllowlist, Identifier: "platform:com.apple.curl", State: store.RuleStateInForce})
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeCDHash, Policy: store.PolicyCEL, Identifier: "a9fdcbc0427a0a585f91bbc7342c261c8ead1942", CELExpr: "target.signing_time >= timestamp('2025-01-01T00:00:00Z')", State: store.RuleStateInForce})
	for i := 0; i < 80; i++ {
		s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeBinary, Policy: []store.Policy{store.PolicyBlocklist, store.PolicyAllowlist, store.PolicySilentBlocklist}[i%3],
			Identifier: fmt.Sprintf("%064x", 5000+i), CustomMessage: "Contact IT", State: store.RuleStateInForce})
	}
	seedDemoTopLists(s, now)
	seedDemoFleet(s, now)
	return s
}

// demoCohortGroup is the demo's Lockdown cohort: a group with a LOCKDOWN
// client mode that counts assigned Macs only.
const demoCohortGroup = "lockdown-1"

// seedDemoFleet gives the demo what Rudolph's fleet job and eventupload
// would have written for the demo's Macs: groups, a snapshot of this
// morning with every health list and the readiness lists, and each Mac's
// unknown markers, made from its unknown executions.
func seedDemoFleet(s *Store, now time.Time) {
	s.AddGroup(store.Group{Name: "eng", Description: "Engineering", AcceptReported: true})
	s.AddGroup(store.Group{Name: "ops", Description: "Operations", AcceptReported: true})
	s.AddGroup(store.Group{Name: demoCohortGroup, Description: "First Lockdown cohort", ClientMode: store.ClientModeLockdown})

	generated := now.UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	today := now.UTC().Truncate(24 * time.Hour)
	markers := map[string]map[string]store.UnknownBinary{}
	for _, e := range s.execEvents {
		if e.Decision != store.DecisionBlockUnknown && e.Decision != store.DecisionAllowUnknown {
			continue
		}
		if markers[e.MachineID] == nil {
			markers[e.MachineID] = map[string]store.UnknownBinary{}
		}
		day := e.ExecutionTime.UTC().Truncate(24 * time.Hour)
		if m, ok := markers[e.MachineID][e.FileSHA256]; !ok || day.After(m.LastDay) {
			markers[e.MachineID][e.FileSHA256] = store.UnknownBinary{FileSHA256: e.FileSHA256, FileName: e.FileName, SigningID: e.SigningID, TeamID: e.TeamID, LastDay: day}
		}
	}

	snap := FleetSnapshot{
		Summary: store.FleetSummary{
			Snapshot: generated.Format("20060102T150405.000000000Z") + "-0a1b2c3d", GeneratedAt: generated, MinSantaVersion: "2026.9",
			StaleDays: rudolphrules.DefaultStaleDays, Health: map[store.FleetCategory]int{}, ReadinessGroups: map[string]store.ReadinessCounts{},
		},
		Health:    map[store.FleetCategory][]store.FleetHealthRow{},
		Readiness: map[string][]store.ReadinessRow{},
	}
	ids := make([]string, 0, len(s.machines))
	for id := range s.machines {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for n, id := range ids {
		i := n + 1
		m := s.machines[id]
		team := "eng"
		if i%3 == 0 {
			team = "ops"
		}
		var assigned []string
		if i%8 == 0 {
			assigned = []string{demoCohortGroup}
		}
		s.SetMachineGroups(id, assigned, []string{team}, []string{team})

		var counts store.UnknownCounts
		for _, b := range markers[id] {
			s.AddUnknownBinary(id, b)
			if !b.LastDay.Before(today.AddDate(0, 0, -29)) {
				counts.Last30++
			}
			if !b.LastDay.Before(today.AddDate(0, 0, -6)) {
				counts.Last7++
			}
		}
		s.SetMachineUnknown(id, counts)

		base := store.FleetHealthRow{MachineID: id, Hostname: m.Hostname, SerialNum: m.SerialNum, LastSeen: m.LastSeen, LastCompleted: m.LastSeen.Add(20 * time.Second)}
		attention := false
		list := func(c store.FleetCategory, row store.FleetHealthRow) {
			snap.Health[c] = append(snap.Health[c], row)
			snap.Summary.Health[c]++
			attention = true
		}
		stale, incomplete := i%17 == 0, i%9 == 0 && i%17 != 0
		locked := len(assigned) > 0 || i%20 == 0
		if stale {
			row := base
			row.LastSeen = now.Add(-time.Duration(11+i%20) * 24 * time.Hour)
			row.LastCompleted, row.Never = row.LastSeen, i%34 == 0
			if row.Never {
				row.LastCompleted = time.Time{}
			}
			list(store.FleetCategoryStale, row)
		}
		if incomplete {
			row := base
			row.LastCompleted, row.SyncsWithoutPostflight = m.LastSeen.Add(-6*time.Hour), 2+i%4
			list(store.FleetCategoryIncomplete, row)
		}
		if locked && m.ClientMode != store.ClientModeLockdown && !stale {
			row := base
			row.Reported, row.Intended, row.Source = m.ClientMode, store.ClientModeLockdown, "own"
			if len(assigned) > 0 {
				row.Source = "group:" + demoCohortGroup
			}
			list(store.FleetCategoryMode, row)
		}
		if i%40 == 7 {
			row := base
			row.Reason, row.RulesReceived, row.RulesProcessed = "NotApplied", 12, 9
			list(store.FleetCategoryRules, row)
		}
		if i%50 == 0 {
			row := base
			row.Flags = "0x7f"
			list(store.FleetCategorySIP, row)
		}
		if store.SantaVersionBelow(m.SantaVersion, snap.Summary.MinSantaVersion) {
			row := base
			row.SantaVersion = m.SantaVersion
			list(store.FleetCategoryVersion, row)
		}
		snap.Summary.Macs++
		if attention {
			snap.Summary.Attention++
		}

		intended := store.ClientModeMonitor
		if locked {
			intended = store.ClientModeLockdown
		}
		ready := store.ReadinessRow{MachineID: id, Hostname: m.Hostname, SerialNum: m.SerialNum, ReportedMode: m.ClientMode, IntendedMode: intended,
			ObservedDays: 30 + i%60, Counted: !locked, Unknown30: counts.Last30, Unknown7: counts.Last7}
		switch {
		case locked:
			ready.Bucket, ready.Unknown30, ready.Unknown7 = store.ReadinessLocked, 0, 0
		case stale:
			ready.Bucket, ready.Why = store.ReadinessUnmeasured, "stale"
		case incomplete:
			ready.Bucket, ready.Why = store.ReadinessUnmeasured, "incomplete"
		case i%11 == 0:
			ready.Bucket, ready.Why, ready.ObservedDays = store.ReadinessUnmeasured, "observed", 5+i%20
		case i%7 == 0:
			ready.Bucket, ready.Pending30, ready.Pending7 = store.ReadinessMany, 10+i%30, 3+i%5
			ready.Unknown30, ready.Unknown7 = ready.Unknown30+ready.Pending30, ready.Unknown7+ready.Pending7
		case i%4 == 1:
			ready.Bucket, ready.Pending30, ready.Pending7 = store.ReadinessFew, 1+i%9, i%2
			ready.Unknown30, ready.Unknown7 = ready.Unknown30+ready.Pending30, ready.Unknown7+ready.Pending7
		default:
			ready.Bucket = store.ReadinessReady
		}
		for _, scope := range append([]string{"", team}, assigned...) {
			snap.Readiness[scope] = append(snap.Readiness[scope], ready)
			c := snap.Summary.ReadinessAll
			if scope != "" {
				c = snap.Summary.ReadinessGroups[scope]
			}
			c.Macs++
			switch ready.Bucket {
			case store.ReadinessReady:
				c.Ready++
			case store.ReadinessFew:
				c.Few++
			case store.ReadinessMany:
				c.Many++
			case store.ReadinessUnmeasured:
				c.Unmeasured++
			case store.ReadinessLocked:
				c.Locked++
			}
			if scope == "" {
				snap.Summary.ReadinessAll = c
			} else {
				snap.Summary.ReadinessGroups[scope] = c
			}
		}
	}
	// The job counts every registered group and the Macs in none, also when
	// they have no Macs.
	if _, ok := snap.Summary.ReadinessGroups[store.UngroupedGroup]; !ok {
		snap.Summary.ReadinessGroups[store.UngroupedGroup] = store.ReadinessCounts{}
	}
	s.SetFleetSnapshot(snap)
}

// demoTopKindMatches says which decisions count towards each list, as the
// ranker's day counters do.
var demoTopKindMatches = map[store.TopKind]func(store.Decision) bool{
	store.TopKindBlocked:        store.Decision.IsBlock,
	store.TopKindAllowed:        func(d store.Decision) bool { return !d.IsBlock() },
	store.TopKindUnknown:        func(d store.Decision) bool { return d == store.DecisionBlockUnknown || d == store.DecisionAllowUnknown },
	store.TopKindUnknownBlocked: func(d store.Decision) bool { return d == store.DecisionBlockUnknown },
	store.TopKindUnknownAllowed: func(d store.Decision) bool { return d == store.DecisionAllowUnknown },
}

// demoTopMachineRows is how many rows the ranker counts Macs for, and
// reorders and marks with GlobalRule in the unknown lists.
const demoTopMachineRows = 100

// seedDemoTopLists ranks the demo events the way Rudolph's ranker does: by
// count, highest first, then by SHA-256, with windows of whole UTC days
// ending today. The first rows of the unknown lists then put binaries
// without a global rule first, then order by Macs.
func seedDemoTopLists(s *Store, now time.Time) {
	today := now.UTC().Truncate(24 * time.Hour)
	generated := now.UTC().Truncate(time.Hour)
	for _, kind := range store.TopKinds {
		matches := demoTopKindMatches[kind]
		for _, days := range store.TopWindowDays {
			from := today.AddDate(0, 0, 1-days)
			byHash := map[string]*store.TopBinary{}
			macs := map[string]map[string]bool{}
			for _, e := range s.execEvents {
				if e.ExecutionTime.Before(from) || !matches(e.Decision) {
					continue
				}
				b := byHash[e.FileSHA256]
				if b == nil {
					b = &store.TopBinary{FileSHA256: e.FileSHA256, FileName: e.FileName, TeamID: e.TeamID, SigningID: e.SigningID, MachinesKnown: true}
					byHash[e.FileSHA256] = b
					macs[e.FileSHA256] = map[string]bool{}
				}
				b.Count++
				macs[e.FileSHA256][e.MachineID] = true
				b.Machines = len(macs[e.FileSHA256])
			}
			list := make([]store.TopBinary, 0, len(byHash))
			for _, b := range byHash {
				list = append(list, *b)
			}
			sort.Slice(list, func(i, j int) bool {
				if list[i].Count != list[j].Count {
					return list[i].Count > list[j].Count
				}
				return list[i].FileSHA256 < list[j].FileSHA256
			})
			if kind.IsUnknown() {
				head := list[:min(len(list), demoTopMachineRows)]
				for i := range head {
					head[i].GlobalRule = s.demoGlobalRule(head[i])
				}
				sort.SliceStable(head, func(i, j int) bool {
					if (head[i].GlobalRule == "") != (head[j].GlobalRule == "") {
						return head[i].GlobalRule == ""
					}
					return head[i].Machines > head[j].Machines
				})
			}
			s.SetTopList(kind, days, generated, list)
		}
	}
}

// demoGlobalRule returns the sort key of the first global rule covering the
// binary, checking binary, signing ID and team ID rules in that order as the
// ranker does.
func (s *Store) demoGlobalRule(b store.TopBinary) string {
	for _, c := range []struct {
		t      store.RuleType
		prefix string
		id     string
	}{
		{store.RuleTypeBinary, "Binary#", b.FileSHA256},
		{store.RuleTypeSigningID, "SigningID#", b.SigningID},
		{store.RuleTypeTeamID, "TeamID#", b.TeamID},
	} {
		if c.id == "" {
			continue
		}
		if _, ok := s.globalRules[ruleKey(store.Rule{RuleType: c.t, Identifier: c.id})]; ok {
			return c.prefix + c.id
		}
	}
	return ""
}
