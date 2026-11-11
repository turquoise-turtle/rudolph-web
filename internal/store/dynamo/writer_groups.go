package dynamo

import (
	"context"
	"errors"
	"fmt"

	"github.com/airbnb/rudolph/pkg/model/groups"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// Group management, as rudolph group create|update|delete|add|remove do, with
// Rudolph's pkg/model/groups and the CLI's audit entries.

// CreateGroup implements store.Writer.
func (s *Store) CreateGroup(ctx context.Context, actor store.Actor, name, description string, acceptReported bool) (string, error) {
	who, err := actor.AuditName()
	if err != nil {
		return "", err
	}
	if name, err = rudolphrules.GroupName(name); err != nil {
		return "", err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return "", err
	}
	err = groups.CreateGroup(s.writeClock, c, name, description, acceptReported)
	switch {
	case errors.Is(err, groups.ErrGroupExists):
		return "", rudolphrules.GroupExists(name)
	case errors.Is(err, groups.ErrTooManyGroups):
		return "", rudolphrules.TooManyGroups()
	case errors.Is(err, groups.ErrRegistryChanged):
		// Another change to the registry won the conditional write; nothing was written.
		return "", store.Invalid("the groups changed while this one was being created; try again")
	case err != nil:
		return "", fmt.Errorf("dynamo: creating a group: %w", err)
	}
	return name, s.record(ctx, c, who, store.ActionGroupCreate, groupAuditTarget(name), rudolphrules.CreateGroupSummary(name, description, acceptReported))
}

// UpdateGroup implements store.Writer.
func (s *Store) UpdateGroup(ctx context.Context, actor store.Actor, name string, description *string, acceptReported *bool) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	if name, err = rudolphrules.ExistingGroupName(name, "changed"); err != nil {
		return err
	}
	if description == nil && acceptReported == nil {
		return store.ErrNothingToChange
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	registry, err := groups.GetRegistry(c, true)
	if err != nil {
		return fmt.Errorf("dynamo: %w", err)
	}
	entry, ok := registry.Groups[name]
	if !ok {
		return store.ErrNotFound
	}
	description, acceptReported = rudolphrules.GroupChanges(store.Group{Name: name, Description: entry.Description, AcceptReported: entry.AcceptReported}, description, acceptReported)
	if description == nil && acceptReported == nil {
		return store.ErrNothingToChange
	}
	err = groups.UpdateGroup(c, name, description, acceptReported)
	if errors.Is(err, groups.ErrGroupNotFound) {
		return store.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("dynamo: updating a group: %w", err)
	}
	return s.record(ctx, c, who, store.ActionGroupUpdate, groupAuditTarget(name), rudolphrules.UpdateGroupSummary(name, description, acceptReported))
}

// DeleteGroup implements store.Writer. Assigned membership stays on the Macs'
// rows, as with the CLI, where it has no effect while no group of that name
// is registered.
func (s *Store) DeleteGroup(ctx context.Context, actor store.Actor, name string, force bool) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	if name, err = rudolphrules.ExistingGroupName(name, "deleted"); err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	registry, err := groups.GetRegistry(c, true)
	if err != nil {
		return fmt.Errorf("dynamo: %w", err)
	}
	if _, ok := registry.Groups[name]; !ok {
		return store.ErrNotFound
	}
	if !force {
		// groups.DeleteGroup refuses this too, but with an error that cannot be told from a failed read.
		rows, err := groups.GetRules(c, name)
		if err != nil {
			return fmt.Errorf("dynamo: %w", err)
		}
		if len(rows) > 0 {
			return rudolphrules.GroupHasRules(name, len(rows))
		}
	}
	err = groups.DeleteGroup(c, name, force)
	switch {
	case errors.Is(err, groups.ErrDeletedPartly):
		// Some rules are gone: record it, as the CLI's RecordPartial does.
		err = fmt.Errorf("dynamo: deleting a group: %w", err)
		if rerr := s.record(ctx, c, who, store.ActionGroupDelete, groupAuditTarget(name), rudolphrules.PartialDeleteGroupSummary(name, shortError(err))); rerr != nil {
			return errors.Join(err, rerr)
		}
		return err
	case errors.Is(err, groups.ErrGroupNotFound):
		return store.ErrNotFound
	case err != nil:
		return fmt.Errorf("dynamo: deleting a group: %w", err)
	}
	return s.record(ctx, c, who, store.ActionGroupDelete, groupAuditTarget(name), rudolphrules.DeleteGroupSummary(name, force))
}

// AssignGroup implements store.Writer.
func (s *Store) AssignGroup(ctx context.Context, actor store.Actor, machineID, group string) error {
	return s.changeAssigned(ctx, actor, machineID, group, true)
}

// UnassignGroup implements store.Writer. Like the CLI it does not require the
// group to be registered, so an assignment left by a deleted group can go.
func (s *Store) UnassignGroup(ctx context.Context, actor store.Actor, machineID, group string) error {
	return s.changeAssigned(ctx, actor, machineID, group, false)
}

func (s *Store) changeAssigned(ctx context.Context, actor store.Actor, machineID, group string, assign bool) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	if err := rudolphrules.MachineID(machineID); err != nil {
		return err
	}
	name, err := rudolphrules.GroupName(group)
	if err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	if machineID, err = s.storedMachineID(c, machineID); err != nil {
		return err
	}
	if !assign {
		if err := groups.RemoveAssigned(s.writeClock, c, machineID, name); err != nil {
			return fmt.Errorf("dynamo: removing an assigned group: %w", err)
		}
		return s.record(ctx, c, who, store.ActionGroupRemove, store.MachineAuditTarget(machineID), rudolphrules.UnassignGroupSummary(name))
	}
	err = groups.AddAssigned(s.writeClock, c, machineID, name)
	if errors.Is(err, groups.ErrGroupNotFound) {
		return rudolphrules.NotRegistered(name)
	}
	if err != nil {
		return fmt.Errorf("dynamo: assigning a group: %w", err)
	}
	return s.record(ctx, c, who, store.ActionGroupAdd, store.MachineAuditTarget(machineID), rudolphrules.AssignGroupSummary(name))
}

// groupAuditTarget is a registered group's audit target, "group:<name>".
func groupAuditTarget(name string) string {
	return store.RuleTarget{Kind: store.TargetGroup, Group: name}.AuditTarget()
}

// SetGroupClientMode implements store.Writer, as rudolph config update
// --group (or --ungrouped) does after --confirm-group-lockdown.
func (s *Store) SetGroupClientMode(ctx context.Context, actor store.Actor, group string, mode store.ClientMode) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	name, err := rudolphrules.GroupConfigName(group)
	if err != nil {
		return err
	}
	want, err := rudolphrules.GroupClientMode(mode)
	if err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	registry, err := groups.GetRegistry(c, true)
	if err != nil {
		return fmt.Errorf("dynamo: %w", err)
	}
	entry, registered := registry.Groups[name]
	if !registered && name != groups.Ungrouped {
		return store.ErrNotFound
	}
	// A stored mode Rudolph cannot decode is still worth removing or replacing.
	current, has := entry.ClientModeOf()
	if (want == nil && !entry.ClientMode.Stored()) || (want != nil && has && current == *want) {
		return store.ErrNothingToChange
	}
	if !registered {
		err := groups.CreateGroup(s.writeClock, c, groups.Ungrouped, "Macs in no group", false)
		switch {
		case errors.Is(err, groups.ErrRegistryChanged):
			return store.Invalid("the groups changed while this one was being changed; try again")
		case err != nil && !errors.Is(err, groups.ErrGroupExists):
			return fmt.Errorf("dynamo: registering the group of Macs in no group: %w", err)
		}
	}
	err = groups.SetClientMode(c, name, want)
	if errors.Is(err, groups.ErrGroupNotFound) {
		return store.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("dynamo: changing a group's client mode: %w", err)
	}
	return s.record(ctx, c, who, store.ActionConfigUpdate, groupAuditTarget(name), rudolphrules.GroupClientModeSummary(want))
}
