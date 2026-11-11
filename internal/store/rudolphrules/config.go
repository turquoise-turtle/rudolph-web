package rudolphrules

import (
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/airbnb/rudolph/pkg/model/machineconfiguration"
	"github.com/airbnb/rudolph/pkg/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// ConfigUpdate is a checked configuration change, as Rudolph's machine
// configuration service takes it.
type ConfigUpdate struct {
	Actor   string
	Request machineconfiguration.MachineConfigurationUpdateRequest
}

// CheckConfigChange checks a change to the global (global true) or a Mac's
// configuration with Rudolph's own validation (ValidateUpdateRequest, which
// the service repeats), and refuses inheriting the USB settings, which the web
// view leaves out. An empty change is store.ErrNothingToChange.
func CheckConfigChange(actor store.Actor, change store.ConfigChange, global bool) (ConfigUpdate, error) {
	name, err := actor.AuditName()
	if err != nil {
		return ConfigUpdate{}, err
	}
	if change.IsEmpty() {
		return ConfigUpdate{}, fmt.Errorf("%w: no setting to change or inherit was given", store.ErrNothingToChange)
	}
	req := machineconfiguration.MachineConfigurationUpdateRequest{
		AllowedPathRegex:          change.AllowedPathRegex,
		BlockedPathRegex:          change.BlockedPathRegex,
		BatchSize:                 change.BatchSize,
		EnableBundles:             change.EnableBundles,
		EnableTransitiveRules:     change.EnableTransitiveRules,
		CleanSync:                 change.CleanSync,
		FullSyncInterval:          change.FullSyncInterval,
		UploadLogsURL:             change.UploadLogsURL,
		EnableAllEventUpload:      change.EnableAllEventUpload,
		DisableUnknownEventUpload: change.DisableUnknownEventUpload,
		EventDetailURL:            change.EventDetailURL,
		EventDetailText:           change.EventDetailText,
		Inherit:                   slices.Clone(change.Inherit),
	}
	if change.ClientMode != nil {
		mode, err := ClientMode(*change.ClientMode)
		if err != nil {
			return ConfigUpdate{}, err
		}
		req.ClientMode = &mode
	}
	if change.OverrideFileAccessAction != nil {
		// As the CLI's --override-file-access-action, so the audit summary has
		// the name Rudolph stores.
		action, err := types.ParseFileAccessAction(*change.OverrideFileAccessAction)
		if err != nil {
			return ConfigUpdate{}, store.Invalid("%v", err)
		}
		req.OverrideFileAccessAction = &action
	}
	if err := machineconfiguration.ValidateUpdateRequest(req, global); err != nil {
		return ConfigUpdate{}, store.Invalid("%v", err)
	}
	for _, setting := range req.Inherit {
		if !slices.Contains(store.InheritableSettings, setting) {
			return ConfigUpdate{}, store.Invalid("%s cannot be inherited from the web view; use rudolph config update --machine", setting)
		}
	}
	return ConfigUpdate{Actor: name, Request: req}, nil
}

// ConfigUpdateSummary lists the settings a config update changes, as the
// CLI's updateSummary (internal/cli/config/config-audit.go) does, e.g.
// "ClientMode=LOCKDOWN, BatchSize=50" or "BatchSize inherited". That function
// is internal to Rudolph's CLI, so it is repeated here.
func ConfigUpdateSummary(request machineconfiguration.MachineConfigurationUpdateRequest) string {
	var changes []string
	value := reflect.ValueOf(request)
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() != reflect.Pointer || field.IsNil() {
			continue
		}
		name := value.Type().Field(i).Name
		var text string
		switch v := field.Elem().Interface().(type) {
		case types.ClientMode:
			text = clientModeName(v)
		case types.FileAccessAction:
			text = string(v)
		case []string:
			text = strings.Join(v, ",")
		case string:
			if urlSettings[name] {
				v = RedactURL(v)
			}
			text = fmt.Sprintf("%q", v)
		default:
			text = fmt.Sprint(v)
		}
		changes = append(changes, name+"="+text)
	}
	for _, setting := range request.Inherit {
		changes = append(changes, setting+" inherited")
	}
	return strings.Join(changes, ", ")
}

func clientModeName(mode types.ClientMode) string {
	text, err := mode.MarshalText()
	if err != nil {
		return fmt.Sprintf("%d (INVALID)", mode)
	}
	return string(text)
}

// urlSettings are the request fields holding URLs, which RedactURL shortens
// in audit entries.
var urlSettings = map[string]bool{"UploadLogsURL": true, "EventDetailURL": true}

// RedactURL keeps the scheme, host and path of a URL, as the CLI's redactURL
// does. Audit entries never expire, and Mac pages are open to viewers, so a
// token in a query string, fragment or user information must not end up in
// either. EventDetailURL may hold Santa's %placeholders%, which are not valid
// URL escapes, so the query and fragment are cut off before parsing, and the
// user information is cut by hand when the rest does not parse.
func RedactURL(raw string) string {
	cut := raw
	if i := strings.IndexAny(cut, "?#"); i >= 0 {
		cut = cut[:i]
	}
	if u, err := url.Parse(cut); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	if scheme, rest, ok := strings.Cut(cut, "://"); ok {
		if at := strings.LastIndex(strings.SplitN(rest, "/", 2)[0], "@"); at >= 0 {
			return scheme + "://" + rest[at+1:]
		}
	}
	return cut
}
