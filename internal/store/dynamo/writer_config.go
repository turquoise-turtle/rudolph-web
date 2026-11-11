package dynamo

import (
	"context"
	"fmt"

	"github.com/airbnb/rudolph/pkg/model/machineconfiguration"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// configService is Rudolph's machine configuration service, as the CLI uses
// it, but uncached: the Lambda is long-lived, and an update always reads the
// row afresh anyway.
func (s *Store) configService(c RudolphClient) machineconfiguration.MachineConfigurationService {
	return machineconfiguration.GetUncachedMachineConfigurationService(c, s.writeClock)
}

// UpdateMachineConfig implements store.Writer, as rudolph config update
// --machine does.
func (s *Store) UpdateMachineConfig(ctx context.Context, actor store.Actor, machineID string, change store.ConfigChange) error {
	u, err := rudolphrules.CheckConfigChange(actor, change, false)
	if err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	// Rudolph would create a config row for any ID; a row no Mac syncs under
	// never reaches one.
	if machineID, err = s.storedMachineID(c, machineID); err != nil {
		return err
	}
	if _, err := s.configService(c).UpdateMachineConfig(machineID, u.Request); err != nil {
		return fmt.Errorf("dynamo: updating the machine configuration: %w", err)
	}
	return s.record(ctx, c, u.Actor, store.ActionConfigUpdate, store.MachineAuditTarget(machineID), rudolphrules.ConfigUpdateSummary(u.Request))
}

// UpdateGlobalConfig implements store.Writer, as rudolph config update
// --global --confirm-global-lockdown does: the web handler has already had a
// mode other than MONITOR confirmed by name.
func (s *Store) UpdateGlobalConfig(ctx context.Context, actor store.Actor, change store.ConfigChange) error {
	u, err := rudolphrules.CheckConfigChange(actor, change, true)
	if err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	if _, err := s.configService(c).UpdateGlobalConfig(u.Request, machineconfiguration.ConfirmGlobalLockdown()); err != nil {
		return fmt.Errorf("dynamo: updating the global configuration: %w", err)
	}
	return s.record(ctx, c, u.Actor, store.ActionConfigUpdate, store.RuleTarget{Kind: store.TargetGlobal}.AuditTarget(), rudolphrules.ConfigUpdateSummary(u.Request))
}
