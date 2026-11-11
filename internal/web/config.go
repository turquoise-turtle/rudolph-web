package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// configRow is one setting in a configuration table. Setting is Rudolph's
// attribute name. Origin is "" on the global config page; on a Mac's page it
// says where the value the Mac gets comes from.
type configRow struct {
	Setting string
	Label   string
	Set     bool
	Value   string
	Code    bool // shown as code: a regex or URL, where every character matters
	Origin  string
}

const (
	originMachine = "this Mac"
	originGlobal  = "global config"
	originDefault = "Rudolph's default"
)

// configSetting reads one setting from a config, in the order the tables
// show them.
type configSetting struct {
	attribute string
	label     string
	code      bool
	value     func(store.Config) (string, bool)
}

var configSettings = []configSetting{
	{"ClientMode", "Client mode", false, func(c store.Config) (string, bool) { return string(c.ClientMode), c.ClientMode != "" }},
	{"AllowedPathRegex", "Allowed path regex", true, func(c store.Config) (string, bool) { return strValue(c.AllowedPathRegex) }},
	{"BlockedPathRegex", "Blocked path regex", true, func(c store.Config) (string, bool) { return strValue(c.BlockedPathRegex) }},
	{"BatchSize", "Batch size", false, func(c store.Config) (string, bool) { return intValue(c.BatchSize) }},
	{"EnableBundles", "Enable bundles", false, func(c store.Config) (string, bool) { return boolValue(c.EnableBundles) }},
	{"EnableTransitiveRules", "Enable transitive rules", false, func(c store.Config) (string, bool) { return boolValue(c.EnableTransitiveRules) }},
	{"FullSyncInterval", "Full sync interval (seconds)", false, func(c store.Config) (string, bool) { return intValue(c.FullSyncInterval) }},
	{"UploadLogsUrl", "Upload logs URL", true, func(c store.Config) (string, bool) { return urlValue(c.UploadLogsURL) }},
	{"EnableAllEventUpload", "Enable all event upload", false, func(c store.Config) (string, bool) { return boolValue(c.EnableAllEventUpload) }},
	{"DisableUnknownEventUpload", "Disable unknown event upload", false, func(c store.Config) (string, bool) { return boolValue(c.DisableUnknownEventUpload) }},
	{"OverrideFileAccessAction", "Override file access action", false, func(c store.Config) (string, bool) {
		return c.OverrideFileAccessAction, c.OverrideFileAccessAction != ""
	}},
	{"EventDetailURL", "Event detail URL", true, func(c store.Config) (string, bool) { return urlValue(c.EventDetailURL) }},
	{"EventDetailText", "Event detail text", false, func(c store.Config) (string, bool) { return strValue(c.EventDetailText) }},
}

func strValue(v *string) (string, bool) {
	if v == nil {
		return "", false
	}
	return *v, true
}

func urlValue(v *string) (string, bool) {
	if v == nil {
		return "", false
	}
	return redactURL(*v), true
}

func intValue(v *int) (string, bool) {
	if v == nil {
		return "", false
	}
	return strconv.Itoa(*v), true
}

func boolValue(v *bool) (string, bool) {
	if v == nil {
		return "", false
	}
	if *v {
		return "yes", true
	}
	return "no", true
}

// redactURL keeps a URL's scheme, host and path, as Rudolph's CLI audit shows
// one (internal/cli/config/config-audit.go): a query, fragment or user
// information may hold a token, and Mac pages are open to viewers.
func redactURL(raw string) string {
	cut := raw
	if i := strings.IndexAny(cut, "?#"); i >= 0 {
		cut = cut[:i]
	}
	if u, err := url.Parse(cut); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	// A path with a placeholder such as %file_sha% does not parse, so the user
	// information is cut by hand.
	if scheme, rest, ok := strings.Cut(cut, "://"); ok {
		if at := strings.LastIndex(strings.SplitN(rest, "/", 2)[0], "@"); at >= 0 {
			return scheme + "://" + rest[at+1:]
		}
	}
	return cut
}

// globalConfigRows lists the global config's settings. Without a global
// config Rudolph sends its defaults, of which the store knows the client mode.
func globalConfigRows(global store.Config) []configRow {
	rows := make([]configRow, len(configSettings))
	for i, cs := range configSettings {
		v, set := cs.value(global)
		rows[i] = configRow{Setting: cs.attribute, Label: cs.label, Set: set, Value: v, Code: cs.code}
		if set && !global.Exists {
			rows[i].Origin = originDefault
		}
	}
	return rows
}

// machineConfigRows lists what a Mac gets for each setting, as `rudolph config
// get --machine` works it out: a setting its own config stores with a
// readable value is the Mac's, and every other one follows the global config.
func machineConfigRows(own, global store.Config) []configRow {
	rows := make([]configRow, len(configSettings))
	for i, cs := range configSettings {
		row := configRow{Setting: cs.attribute, Label: cs.label, Code: cs.code}
		if v, set := cs.value(own); set {
			row.Set, row.Value, row.Origin = true, v, originMachine
		} else if v, set := cs.value(global); set {
			row.Set, row.Value, row.Origin = true, v, originGlobal
			if !global.Exists {
				row.Origin = originDefault
			}
		}
		rows[i] = row
	}
	return rows
}

func (s *server) globalConfig(w http.ResponseWriter, r *http.Request) {
	global, err := s.store.GlobalConfig(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "config", "Global configuration", "config", struct {
		Exists bool
		Rows   []configRow
	}{global.Exists, globalConfigRows(global)})
}
