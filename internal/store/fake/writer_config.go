package fake

import (
	"context"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// UpdateMachineConfig implements store.Writer.
func (s *Store) UpdateMachineConfig(_ context.Context, actor store.Actor, machineID string, change store.ConfigChange) error {
	u, err := rudolphrules.CheckConfigChange(actor, change, false)
	if err != nil {
		return err
	}
	if err := rudolphrules.MachineID(machineID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if machineID, err = s.storedMachineID(machineID); err != nil {
		return err
	}
	c := applyConfigChange(s.configs[machineID], change, u)
	for _, setting := range u.Request.Inherit {
		c = withoutSetting(c, setting)
	}
	s.configs[machineID] = c
	if change.CleanSync != nil {
		s.cleanSyncs[machineID] = *change.CleanSync
	}
	s.recordAudit(u.Actor, store.ActionConfigUpdate, store.MachineAuditTarget(machineID), rudolphrules.ConfigUpdateSummary(u.Request))
	return nil
}

// UpdateGlobalConfig implements store.Writer.
func (s *Store) UpdateGlobalConfig(_ context.Context, actor store.Actor, change store.ConfigChange) error {
	u, err := rudolphrules.CheckConfigChange(actor, change, true)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.globalConfig = applyConfigChange(s.globalConfig, change, u)
	s.recordAudit(u.Actor, store.ActionConfigUpdate, store.RuleTarget{Kind: store.TargetGlobal}.AuditTarget(), rudolphrules.ConfigUpdateSummary(u.Request))
	return nil
}

// applyConfigChange sets the settings a checked change gives, with the file
// access action as Rudolph stores it. Clearing the clean sync flag of the
// global config, which Rudolph allows, has nothing to clear in the fake.
func applyConfigChange(c store.Config, change store.ConfigChange, u rudolphrules.ConfigUpdate) store.Config {
	c.Exists = true
	if change.ClientMode != nil {
		c.ClientMode = *change.ClientMode
	}
	if u.Request.OverrideFileAccessAction != nil {
		c.OverrideFileAccessAction = string(*u.Request.OverrideFileAccessAction)
	}
	setPtr(&c.AllowedPathRegex, change.AllowedPathRegex)
	setPtr(&c.BlockedPathRegex, change.BlockedPathRegex)
	setPtr(&c.BatchSize, change.BatchSize)
	setPtr(&c.EnableBundles, change.EnableBundles)
	setPtr(&c.EnableTransitiveRules, change.EnableTransitiveRules)
	setPtr(&c.FullSyncInterval, change.FullSyncInterval)
	setPtr(&c.UploadLogsURL, change.UploadLogsURL)
	setPtr(&c.EnableAllEventUpload, change.EnableAllEventUpload)
	setPtr(&c.DisableUnknownEventUpload, change.DisableUnknownEventUpload)
	setPtr(&c.EventDetailURL, change.EventDetailURL)
	setPtr(&c.EventDetailText, change.EventDetailText)
	return c
}

// setPtr stores a copy of v, if given, so the caller's value is not shared.
func setPtr[T any](dst **T, v *T) {
	if v != nil {
		c := *v
		*dst = &c
	}
}

// withoutSetting removes an inherited setting, named by Rudolph's attribute
// name, as Rudolph removes the attribute from the row.
func withoutSetting(c store.Config, setting string) store.Config {
	switch setting {
	case "ClientMode":
		c.ClientMode = ""
	case "AllowedPathRegex":
		c.AllowedPathRegex = nil
	case "BlockedPathRegex":
		c.BlockedPathRegex = nil
	case "BatchSize":
		c.BatchSize = nil
	case "EnableBundles":
		c.EnableBundles = nil
	case "EnableTransitiveRules":
		c.EnableTransitiveRules = nil
	case "FullSyncInterval":
		c.FullSyncInterval = nil
	case "UploadLogsUrl":
		c.UploadLogsURL = nil
	case "EnableAllEventUpload":
		c.EnableAllEventUpload = nil
	case "DisableUnknownEventUpload":
		c.DisableUnknownEventUpload = nil
	case "OverrideFileAccessAction":
		c.OverrideFileAccessAction = ""
	case "EventDetailURL":
		c.EventDetailURL = nil
	case "EventDetailText":
		c.EventDetailText = nil
	}
	return c
}
