package dynamo

import (
	"context"
	"errors"
	"fmt"

	"github.com/airbnb/rudolph/pkg/model/fleethealth"
	"github.com/airbnb/rudolph/pkg/model/groups"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// Holiday mode and the minimum Santa version, as rudolph fleet holiday and
// rudolph fleet min-santa-version do, with Rudolph's pkg/model/fleethealth
// and the CLI's audit entries; and the cohort action,
// a bounded run of group assignments.

var _ fleethealth.Writer = RudolphClient(nil)

// SetFleetHoliday implements store.Writer.
func (s *Store) SetFleetHoliday(ctx context.Context, actor store.Actor, staleDays int, until string) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	// fleethealth.SetHoliday refuses the same holidays, but with an error
	// that cannot be told from a failed read or write, so it is checked here
	// first, as the CLI checks it before asking for confirmation.
	current, err := fleethealth.Get(c, true)
	if err != nil {
		return fmt.Errorf("dynamo: %w", err)
	}
	if err := rudolphrules.CheckHoliday(current.StaleDays, staleDays, until, s.writeClock.Now()); err != nil {
		return err
	}
	stored, err := fleethealth.SetHoliday(s.writeClock, c, staleDays, until)
	switch {
	case errors.Is(err, fleethealth.ErrChanged):
		// The normal threshold kept changing under the conditional write; nothing was written.
		return store.Invalid("the stale threshold changed while the holiday was being saved; try again")
	case err != nil:
		return fmt.Errorf("dynamo: setting a holiday: %w", err)
	}
	return s.record(ctx, c, who, store.ActionFleetHoliday, store.AuditTargetFleet,
		rudolphrules.SetHolidaySummary(stored.HolidayStaleDays, stored.HolidayUntil, stored.NormalStaleDays()))
}

// EndFleetHoliday implements store.Writer.
func (s *Store) EndFleetHoliday(ctx context.Context, actor store.Actor) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	ended, err := fleethealth.EndHoliday(c)
	switch {
	case errors.Is(err, fleethealth.ErrNoHoliday):
		return store.ErrNothingToChange
	case err != nil:
		return fmt.Errorf("dynamo: ending the holiday: %w", err)
	}
	return s.record(ctx, c, who, store.ActionFleetHoliday, store.AuditTargetFleet,
		rudolphrules.EndHolidaySummary(ended.HolidayStaleDays, ended.HolidayUntil, ended.NormalStaleDays()))
}

// AssignCohort implements store.Writer. Every check runs before the first
// write: the group in the registry (one GetItem), then each Mac's row in the
// snapshot's Ready bucket of the fleet's list (one GetItem each, on the events
// table). The Macs are then assigned one after another with
// groups.AddAssigned, as AssignGroup does.
//
// Unlike AssignGroup it does not look up the spelling a Mac is stored under:
// the readiness row is keyed by the machine ID exactly as the fleet job read
// it from the Mac's sensor data, so an ID with such a row is the stored
// spelling.
func (s *Store) AssignCohort(ctx context.Context, actor store.Actor, snapshot, group string, machineIDs []string) (int, error) {
	cohort, err := rudolphrules.CheckCohort(actor, snapshot, group, machineIDs, s.writeClock.Now())
	if err != nil {
		return 0, err
	}
	if s.eventsTable == "" {
		return 0, fmt.Errorf("%w: no events table configured", store.ErrUnavailable)
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return 0, err
	}
	registry, err := groups.GetRegistry(c, true)
	if err != nil {
		return 0, fmt.Errorf("dynamo: %w", err)
	}
	entry, ok := registry.Groups[cohort.Group]
	if !ok {
		return 0, store.ErrNotFound
	}
	if mode, has := entry.ClientModeOf(); !has || !rudolphrules.IsLockdownMode(enum(int64(mode), clientModes)) {
		return 0, rudolphrules.NotALockdownGroup(cohort.Group)
	}
	for _, id := range cohort.MachineIDs {
		ready, err := s.readyInSnapshot(ctx, snapshot, id)
		if err != nil {
			return 0, err
		}
		if !ready {
			return 0, rudolphrules.NotReady(id)
		}
	}

	for i, id := range cohort.MachineIDs {
		if err := ctx.Err(); err != nil {
			return i, partialCohort(i, len(cohort.MachineIDs), err)
		}
		err := groups.AddAssigned(s.writeClock, c, id, cohort.Group)
		switch {
		case errors.Is(err, groups.ErrGroupNotFound):
			// Deleted since the registry was read above.
			return i, partialCohort(i, len(cohort.MachineIDs), rudolphrules.NotRegistered(cohort.Group))
		case err != nil:
			return i, partialCohort(i, len(cohort.MachineIDs), fmt.Errorf("dynamo: assigning a group: %w", err))
		}
		if err := s.record(ctx, c, cohort.Actor, store.ActionGroupAdd, store.MachineAuditTarget(id), rudolphrules.AssignGroupSummary(cohort.Group)); err != nil {
			// This Mac is assigned, though not audited.
			return i + 1, partialCohort(i+1, len(cohort.MachineIDs), err)
		}
	}
	return len(cohort.MachineIDs), nil
}

// partialCohort is the error of a cohort stopped by err after assigned Macs:
// err itself when nothing was written, so a refusal stays a refusal.
func partialCohort(assigned, total int, err error) error {
	if assigned == 0 {
		return err
	}
	return &store.PartialCohortError{Assigned: assigned, Total: total, Err: err}
}

// readyInSnapshot reports whether the Mac has a row in the Ready bucket of
// the snapshot's list of the fleet. A Ready row has nothing pending, so its
// sort key is known without a Query: bucket 0, six zeros, the machine ID.
func (s *Store) readyInSnapshot(ctx context.Context, snapshot, machineID string) (bool, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.eventsTable),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: readinessList(store.ReadinessFilter{}).partition(snapshot)},
			"SK": &types.AttributeValueMemberS{Value: "0#000000#" + machineID},
		},
		ProjectionExpression: aws.String("PK"),
	})
	if err != nil {
		return false, fmt.Errorf("dynamo: reading the readiness of machine %s: %w", machineID, err)
	}
	return len(out.Item) > 0, nil
}

// SetMinSantaVersion implements store.Writer with Rudolph's
// fleethealth.SetMinSantaVersion, which writes that one attribute.
func (s *Store) SetMinSantaVersion(ctx context.Context, actor store.Actor, version string) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	// fleethealth.SetMinSantaVersion refuses the same values, but with an
	// error that cannot be told from a failed read or write, so the value is
	// checked here first, as the CLI checks it before asking for confirmation.
	version, err = rudolphrules.CheckMinSantaVersion(version)
	if err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	// The summary names the value in force before the change, which
	// SetMinSantaVersion does not return.
	before, err := fleethealth.Get(c, true)
	if err != nil {
		return fmt.Errorf("dynamo: %w", err)
	}
	stored, err := fleethealth.SetMinSantaVersion(c, version)
	if err != nil {
		return fmt.Errorf("dynamo: setting the minimum Santa version: %w", err)
	}
	return s.record(ctx, c, who, store.ActionFleetMinSantaVersion, store.AuditTargetFleet,
		rudolphrules.SetMinSantaVersionSummary(stored.MinSantaVersion, before.MinSantaVersion))
}

// ClearMinSantaVersion implements store.Writer with Rudolph's
// fleethealth.ClearMinSantaVersion.
func (s *Store) ClearMinSantaVersion(ctx context.Context, actor store.Actor) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	before, err := fleethealth.ClearMinSantaVersion(c)
	switch {
	case errors.Is(err, fleethealth.ErrNoMinSantaVersion):
		return store.ErrNothingToChange
	case err != nil:
		return fmt.Errorf("dynamo: clearing the minimum Santa version: %w", err)
	}
	return s.record(ctx, c, who, store.ActionFleetMinSantaVersion, store.AuditTargetFleet,
		rudolphrules.ClearMinSantaVersionSummary(before.MinSantaVersion))
}
