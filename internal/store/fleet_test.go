package store_test

// Fleet health and Lockdown readiness (Rudolph's docs/fleet-health.md; design "Fleet health (Fix 2) and Lockdown
// readiness (Fix 3)", owner decisions of 2026-11-22). Rudolph's daily fleet job writes a snapshot to the events table:
// a pointer (FleetPointer / Current) with every count, and list rows under FleetSnapshot#<snapshot>#Health#<category>
// and FleetSnapshot#<snapshot>#Ready#All or #Ready#Group#<group> (testdata/webcontract/fleet-rows.json). The web
// view reads them as the top lists: the pointer with one GetItem, a page with one Query.
//
// The read models and their names:
//   - FleetCategory, one of FleetCategories in Rudolph's order: Stale, Incomplete, Mode, Rules, SIP, Version, the
//     values being Rudolph's own names (the last segment of a health list's partition key).
//   - ReadinessBucket, one of ReadinessBuckets in sort key order: ready (0), few (1), many (2), unmeasured (3),
//     locked (4); ReadinessAny ("") is no bucket filter. Rudolph's sort key starts with the bucket's digit.
//   - FleetSummary (the pointer), FleetHealthRow, ReadinessRow, ReadinessCounts, FleetList[T], ReadinessFilter,
//     UnknownCounts, UnknownBinary and FleetHealthConfig: see the tests of the fake and the DynamoDB store.
//   - MaxCohortMacs = 50: the most Macs one cohort assignment takes, as MaxMovedRules bounds a move.
//   - ErrSnapshotTooOld: AssignCohort's refusal of a snapshot generated more than 26 hours ago (the job runs daily).
//   - PartialCohortError{Assigned, Total int; Err error}: AssignCohort failed after assigning some Macs.

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

func TestFleetCategories_AreRudolphsInItsOrder(t *testing.T) {
	want := []store.FleetCategory{"Stale", "Incomplete", "Mode", "Rules", "SIP", "Version"}
	if !slices.Equal(store.FleetCategories, want) {
		t.Errorf("FleetCategories %v, want %v", store.FleetCategories, want)
	}
	named := []store.FleetCategory{store.FleetCategoryStale, store.FleetCategoryIncomplete, store.FleetCategoryMode,
		store.FleetCategoryRules, store.FleetCategorySIP, store.FleetCategoryVersion}
	if !slices.Equal(named, want) {
		t.Errorf("the category constants are %v, want %v", named, want)
	}
}

func TestReadinessBuckets_InSortKeyOrder(t *testing.T) {
	want := []store.ReadinessBucket{store.ReadinessReady, store.ReadinessFew, store.ReadinessMany, store.ReadinessUnmeasured, store.ReadinessLocked}
	if !slices.Equal(store.ReadinessBuckets, want) {
		t.Errorf("ReadinessBuckets %v, want ready, few, many, unmeasured, locked", store.ReadinessBuckets)
	}
	for b, name := range map[store.ReadinessBucket]string{
		store.ReadinessReady: "ready", store.ReadinessFew: "few", store.ReadinessMany: "many",
		store.ReadinessUnmeasured: "unmeasured", store.ReadinessLocked: "locked",
	} {
		if string(b) != name {
			t.Errorf("bucket %q, want %q (the value the readiness pages put in ?bucket=)", b, name)
		}
	}
	if store.ReadinessAny != "" {
		t.Errorf("ReadinessAny is %q, want the empty bucket", store.ReadinessAny)
	}
}

func TestMaxCohortMacs(t *testing.T) {
	if store.MaxCohortMacs != 50 {
		t.Errorf("MaxCohortMacs = %d, want 50", store.MaxCohortMacs)
	}
}

func TestPartialCohortError_SaysHowManyAndUnwraps(t *testing.T) {
	cause := errors.New("throttled")
	var err error = &store.PartialCohortError{Assigned: 2, Total: 3, Err: cause}
	if !errors.Is(err, cause) {
		t.Error("PartialCohortError does not unwrap to its cause")
	}
	if !strings.Contains(err.Error(), "2 of 3") {
		t.Errorf("message %q does not say 2 of 3 Macs were assigned", err.Error())
	}
	var partial *store.PartialCohortError
	if !errors.As(err, &partial) || partial.Assigned != 2 {
		t.Errorf("errors.As: %+v", partial)
	}
	if errors.Is(store.ErrSnapshotTooOld, store.ErrInvalidArgument) {
		t.Error("ErrSnapshotTooOld wraps ErrInvalidArgument; it is a conflict with a newer snapshot, not bad input")
	}
}
