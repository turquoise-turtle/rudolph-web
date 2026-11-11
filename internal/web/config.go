package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/airbnb/rudolph/pkg/model/machineconfiguration"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// configRow is one setting in a configuration table. Setting is Rudolph's
// attribute name. Origin is "" on the global config page; on a Mac's page it
// says where the value the Mac gets comes from. Field is set only on an edit
// form.
type configRow struct {
	Setting string
	Label   string
	Set     bool
	Value   string
	Code    bool // shown as code: a regex or URL, where every character matters
	Origin  string
	// Sent is what Rudolph sends when nothing stores the setting, if that is
	// known.
	Sent  string
	Field *configField
}

const (
	originMachine = "this Mac"
	originGlobal  = "global config"
	originDefault = "Rudolph's default"
)

// The kinds of setting, which decide the form input and how a posted value
// is read.
type settingKind int

const (
	kindText settingKind = iota
	kindURL              // text, shown redacted
	kindInt
	kindBool
	kindMode
	kindFileAccess
)

// configSetting reads one setting from a config, in the order the tables
// show them.
type configSetting struct {
	attribute string
	label     string
	code      bool
	kind      settingKind
	value     func(store.Config) (string, bool)
	// set puts a posted value, already read for its kind, into a change.
	set func(*store.ConfigChange, string)
}

var configSettings = []configSetting{
	{"ClientMode", "Client mode", false, kindMode,
		func(c store.Config) (string, bool) { return string(c.ClientMode), c.ClientMode != "" },
		func(ch *store.ConfigChange, v string) { m := store.ClientMode(v); ch.ClientMode = &m }},
	{"AllowedPathRegex", "Allowed path regex", true, kindText,
		func(c store.Config) (string, bool) { return strValue(c.AllowedPathRegex) },
		func(ch *store.ConfigChange, v string) { ch.AllowedPathRegex = &v }},
	{"BlockedPathRegex", "Blocked path regex", true, kindText,
		func(c store.Config) (string, bool) { return strValue(c.BlockedPathRegex) },
		func(ch *store.ConfigChange, v string) { ch.BlockedPathRegex = &v }},
	{"BatchSize", "Batch size", false, kindInt,
		func(c store.Config) (string, bool) { return intValue(c.BatchSize) },
		func(ch *store.ConfigChange, v string) { ch.BatchSize = atoiPtr(v) }},
	{"EnableBundles", "Enable bundles", false, kindBool,
		func(c store.Config) (string, bool) { return boolValue(c.EnableBundles) },
		func(ch *store.ConfigChange, v string) { ch.EnableBundles = yesPtr(v) }},
	{"EnableTransitiveRules", "Enable transitive rules", false, kindBool,
		func(c store.Config) (string, bool) { return boolValue(c.EnableTransitiveRules) },
		func(ch *store.ConfigChange, v string) { ch.EnableTransitiveRules = yesPtr(v) }},
	{"FullSyncInterval", "Full sync interval (seconds)", false, kindInt,
		func(c store.Config) (string, bool) { return intValue(c.FullSyncInterval) },
		func(ch *store.ConfigChange, v string) { ch.FullSyncInterval = atoiPtr(v) }},
	{"UploadLogsUrl", "Upload logs URL", true, kindURL,
		func(c store.Config) (string, bool) { return urlValue(c.UploadLogsURL) },
		func(ch *store.ConfigChange, v string) { ch.UploadLogsURL = &v }},
	{"EnableAllEventUpload", "Enable all event upload", false, kindBool,
		func(c store.Config) (string, bool) { return boolValue(c.EnableAllEventUpload) },
		func(ch *store.ConfigChange, v string) { ch.EnableAllEventUpload = yesPtr(v) }},
	{"DisableUnknownEventUpload", "Disable unknown event upload", false, kindBool,
		func(c store.Config) (string, bool) { return boolValue(c.DisableUnknownEventUpload) },
		func(ch *store.ConfigChange, v string) { ch.DisableUnknownEventUpload = yesPtr(v) }},
	{"OverrideFileAccessAction", "Override file access action", false, kindFileAccess,
		func(c store.Config) (string, bool) {
			return c.OverrideFileAccessAction, c.OverrideFileAccessAction != ""
		},
		func(ch *store.ConfigChange, v string) { ch.OverrideFileAccessAction = &v }},
	{"EventDetailURL", "Event detail URL", true, kindURL,
		func(c store.Config) (string, bool) { return urlValue(c.EventDetailURL) },
		func(ch *store.ConfigChange, v string) { ch.EventDetailURL = &v }},
	{"EventDetailText", "Event detail text", false, kindText,
		func(c store.Config) (string, bool) { return strValue(c.EventDetailText) },
		func(ch *store.ConfigChange, v string) { ch.EventDetailText = &v }},
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
	return rudolphrules.RedactURL(*v), true
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

// atoiPtr and yesPtr convert values parseConfigForm has already checked.
func atoiPtr(v string) *int {
	n, _ := strconv.Atoi(v)
	return &n
}

func yesPtr(v string) *bool {
	b := v == "yes"
	return &b
}

// sentWhenUnset is what Rudolph sends for a setting that neither the Mac's
// config nor the global one stores, where Rudolph decides it. Every preflight
// goes through machineconfiguration.SanitizeForSanta, which sends
// DefaultBatchSize for a batch size that is not set. Without a global config
// row Rudolph uses its universal default config, whose full sync interval is
// DefaultFullSyncInterval; with one, an unset interval is left out. Other
// unset settings are left out too, so Santa keeps its own.
func sentWhenUnset(attribute string, globalExists bool) string {
	switch {
	case attribute == "BatchSize":
		return strconv.Itoa(machineconfiguration.DefaultBatchSize)
	case attribute == "FullSyncInterval" && !globalExists:
		return strconv.Itoa(machineconfiguration.DefaultFullSyncInterval)
	}
	return ""
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
		if !set {
			rows[i].Sent = sentWhenUnset(cs.attribute, global.Exists)
		}
	}
	return rows
}

// machineConfigRows lists what a Mac gets for each setting, as `rudolph config
// get --machine` works it out: a setting its own config stores with a
// readable value is the Mac's, and every other one follows the global config.
// Without globalRead, what Rudolph sends for an unset setting is not known.
func machineConfigRows(own, global store.Config, globalRead bool) []configRow {
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
		} else if globalRead {
			row.Sent = sentWhenUnset(cs.attribute, global.Exists)
		}
		rows[i] = row
	}
	return rows
}

// configForm fills the "configform" template: the configuration table with
// an input per setting for the config being edited.
type configForm struct {
	Action  string
	Machine bool // a Mac's own config, whose settings can be inherited
	Rows    []configRow
}

// configField is a setting's input. Input is the edited config's own value
// for a text input; a select has Options instead. Own says the edited config
// stores the setting, so a Mac can inherit it instead.
type configField struct {
	Name    string
	Select  bool
	Input   string
	Options []configOption
	Own     bool
}

type configOption struct {
	Value    string
	Label    string
	Selected bool
}

// withFields adds the inputs for editing own, the config the rows' values
// come from (a Mac's own config, or the global one).
func withFields(rows []configRow, own store.Config, machine bool) []configRow {
	for i, cs := range configSettings {
		v, stored := cs.value(own)
		f := &configField{Name: cs.attribute, Input: v, Own: stored}
		var choices []string
		switch cs.kind {
		case kindMode:
			for _, m := range store.ClientModes {
				choices = append(choices, string(m))
			}
		case kindFileAccess:
			choices = store.FileAccessActions
		case kindBool:
			choices = []string{"yes", "no"}
		}
		if choices != nil {
			f.Select = true
			// Blank is offered only while nothing is stored: it leaves the
			// setting unset, and there is no blank value to store.
			if !stored {
				label := "not set"
				if machine {
					label = "global config"
				}
				f.Options = append(f.Options, configOption{Value: "", Label: label, Selected: true})
			}
			// A stored value the form does not offer (a legacy spelling, or a
			// mode Rudolph does not know) is kept as an option, so saving the
			// form unchanged leaves it alone.
			if stored && !slices.Contains(choices, v) {
				f.Options = append(f.Options, configOption{Value: v, Label: v + " (as stored)", Selected: true})
			}
			for _, c := range choices {
				f.Options = append(f.Options, configOption{Value: c, Label: c, Selected: stored && c == v})
			}
		}
		rows[i].Field = f
	}
	return rows
}

// pathRegexError checks a path regex as the CLI does by default, with Go's
// RE2 syntax: Santa silently ignores a regex it cannot compile, so a typo
// would block or allow nothing without anyone noticing.
func pathRegexError(label, v string) string {
	if v == "" {
		return ""
	}
	if _, err := regexp.Compile(v); err != nil {
		return fmt.Sprintf("the %s is not a valid regex (checked as Go RE2 syntax, as the CLI does): %v. Use rudolph config update --skip-regex-validation for ICU-only syntax.", strings.ToLower(label), err)
	}
	return ""
}

// origPrefix starts the hidden input that carries what a setting's input
// showed when the form was drawn.
const origPrefix = "orig_"

// formRefusal is why a configuration form is not saved: the status to answer
// with and what to tell the person.
type formRefusal struct {
	status int
	msg    string
}

func badForm(msg string) *formRefusal {
	return &formRefusal{http.StatusBadRequest, msg}
}

// parseConfigForm reads a configuration form into a change of own, the
// config the form edits. Only what changed is kept. A setting posted with its
// orig_<attribute> changed only when the posted value differs from it, so a
// form drawn before someone else changed that setting does not put it back;
// one changed on the form whose stored value is no longer what the form
// showed is refused with 409, since saving it would overwrite a change the
// person has not seen. Without orig_<attribute> a setting changed when it
// differs from the stored value. Either way a setting posted with its stored
// value (for a URL, the redacted value the form shows) or posted blank while
// not stored is left alone.
func parseConfigForm(f url.Values, own store.Config, machine bool) (store.ConfigChange, *formRefusal) {
	var change store.ConfigChange
	known := map[string]configSetting{}
	for _, cs := range configSettings {
		known[cs.attribute] = cs
	}
	for name, values := range f {
		_, isSetting := known[name]
		_, isOrig := known[strings.TrimPrefix(name, origPrefix)]
		isOrig = isOrig && strings.HasPrefix(name, origPrefix)
		switch {
		case isSetting, isOrig, name == "confirm_mode":
		case name == "inherit" && machine:
		case name == "inherit":
			return change, badForm("the global configuration has nothing to inherit from.")
		default:
			return change, badForm(fmt.Sprintf("%q is not a setting the web view changes.", name))
		}
		if name != "inherit" && len(values) != 1 {
			return change, badForm(fmt.Sprintf("%s was sent more than once.", name))
		}
	}

	changed := map[string]bool{}
	for _, cs := range configSettings {
		values, posted := f[cs.attribute]
		if !posted {
			continue
		}
		v := cs.posted(values[0])
		orig, hasOrig := f[origPrefix+cs.attribute]
		if hasOrig && v == cs.posted(orig[0]) {
			continue
		}
		current, stored := cs.value(own)
		if v == current && stored || v == "" && !stored {
			continue
		}
		// The input shows the stored value, or blank when nothing is stored,
		// so that is what orig_ holds when nobody has changed it since.
		if hasOrig && cs.posted(orig[0]) != current {
			return change, &formRefusal{http.StatusConflict, fmt.Sprintf("%s was changed since this page was loaded; reload it to see the configuration as it is now, and make your change again.", cs.label)}
		}
		switch cs.kind {
		case kindText:
			if cs.code {
				if msg := pathRegexError(cs.label, v); msg != "" {
					return change, badForm(msg)
				}
			}
		case kindURL:
		case kindInt:
			if v == "" {
				return change, badForm(blankMessage(cs.label, machine))
			}
			if _, err := strconv.Atoi(v); err != nil {
				return change, badForm(fmt.Sprintf("%s must be a whole number.", cs.label))
			}
		case kindBool:
			if v != "yes" && v != "no" {
				if v == "" {
					return change, badForm(blankMessage(cs.label, machine))
				}
				return change, badForm(fmt.Sprintf("%s must be yes or no.", cs.label))
			}
		case kindMode:
			if !slices.Contains(store.ClientModes, store.ClientMode(v)) {
				if v == "" {
					return change, badForm(blankMessage(cs.label, machine))
				}
				return change, badForm("unknown client mode; choose MONITOR, LOCKDOWN or STANDALONE.")
			}
		case kindFileAccess:
			if !slices.Contains(store.FileAccessActions, v) {
				if v == "" {
					return change, badForm(blankMessage(cs.label, machine))
				}
				return change, badForm("unknown file access action; choose NONE, AUDIT_ONLY or DISABLE.")
			}
		}
		cs.set(&change, v)
		changed[cs.attribute] = true
	}

	for _, name := range f["inherit"] {
		cs, ok := known[name]
		if !ok {
			return change, badForm(fmt.Sprintf("%q is not a setting this Mac can inherit from here.", name))
		}
		if changed[name] {
			return change, badForm(fmt.Sprintf("%s is both changed and inherited; do one or the other.", cs.label))
		}
		// Inheriting what the Mac already inherits changes nothing.
		if _, stored := cs.value(own); stored && !slices.Contains(change.Inherit, name) {
			change.Inherit = append(change.Inherit, name)
		}
	}

	if change.ClientMode != nil && *change.ClientMode != store.ClientModeMonitor {
		mode := string(*change.ClientMode)
		if !modeConfirmed(f, mode) {
			what := "this Mac"
			if !machine {
				what = "every Mac without its own client mode"
			}
			return change, badForm(fmt.Sprintf("%s blocks unknown binaries on %s. To confirm, type %s in the confirmation box.", mode, what, mode))
		}
	}
	return change, nil
}

// modeConfirmed says the confirmation box holds the mode's name.
func modeConfirmed(f url.Values, mode string) bool {
	return strings.EqualFold(strings.TrimSpace(f.Get("confirm_mode")), mode)
}

// posted is a posted value as parseConfigForm compares it: numbers and URLs
// without the spaces around them.
func (cs configSetting) posted(v string) string {
	if cs.kind == kindInt || cs.kind == kindURL {
		return strings.TrimSpace(v)
	}
	return v
}

func blankMessage(label string, machine bool) string {
	if machine {
		return fmt.Sprintf("%s cannot be blank; tick inherit to follow the global configuration instead.", label)
	}
	return fmt.Sprintf("%s cannot be blank.", label)
}

func (s *server) globalConfig(w http.ResponseWriter, r *http.Request) {
	global, err := s.store.GlobalConfig(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	data := struct {
		Exists bool
		Rows   []configRow
		Edit   bool
		Form   configForm
	}{Exists: global.Exists, Rows: globalConfigRows(global), Edit: s.canEdit(r)}
	if data.Edit {
		data.Form = configForm{Action: "/admin/config", Rows: withFields(globalConfigRows(global), global, false)}
	}
	s.render(w, r, http.StatusOK, "config", "Global configuration", "config", data)
}

func (s *server) updateGlobalConfig(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	global, err := s.store.GlobalConfig(store.Consistent(r.Context()))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	change, refusal := parseConfigForm(r.PostForm, global, false)
	if refusal != nil {
		s.renderError(w, r, refusal.status, "Not saved: "+refusal.msg)
		return
	}
	if !change.IsEmpty() {
		if err := s.writer.UpdateGlobalConfig(r.Context(), actorOf(r), change); err != nil {
			s.writeError(w, r, err, "")
			return
		}
	}
	http.Redirect(w, r, "/admin/config", http.StatusSeeOther)
}

// editedMachine reads the Mac a configuration form posts for, answering 404
// for one the web view does not show.
func (s *server) editedMachine(w http.ResponseWriter, r *http.Request) (store.MachineDetail, bool) {
	// Consistent, so that the check against what the form showed sees a
	// change made a moment ago.
	m, err := s.store.GetMachine(store.Consistent(r.Context()), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.renderError(w, r, http.StatusNotFound, "Machine not found.")
		} else {
			s.storeError(w, r, err)
		}
		return m, false
	}
	return m, true
}

func (s *server) updateMachineConfig(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	m, ok := s.editedMachine(w, r)
	if !ok {
		return
	}
	change, refusal := parseConfigForm(r.PostForm, m.Config, true)
	if refusal != nil {
		s.renderError(w, r, refusal.status, "Not saved: "+refusal.msg)
		return
	}
	if slices.Contains(change.Inherit, "ClientMode") {
		// Inheriting the client mode moves the Mac to the global one, which
		// blocks unknown binaries as surely as choosing it would.
		mode, err := s.store.GlobalClientMode(store.Consistent(r.Context()))
		if err != nil {
			s.storeError(w, r, err)
			return
		}
		if mode != "" && mode != store.ClientModeMonitor && !modeConfirmed(r.PostForm, string(mode)) {
			s.renderError(w, r, http.StatusBadRequest, fmt.Sprintf("Not saved: inheriting the client mode moves this Mac to the global mode, %s, which blocks unknown binaries on this Mac. To confirm, type %s in the confirmation box.", mode, mode))
			return
		}
	}
	id := r.PathValue("id")
	if !change.IsEmpty() {
		if err := s.writer.UpdateMachineConfig(r.Context(), actorOf(r), id, change); err != nil {
			s.writeError(w, r, err, "Machine not found.")
			return
		}
	}
	http.Redirect(w, r, "/machines/"+url.PathEscape(id), http.StatusSeeOther)
}

func (s *server) cleanSync(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	var request bool
	switch r.PostForm.Get("action") {
	case "request":
		request = true
	case "cancel":
	default:
		s.renderError(w, r, http.StatusBadRequest, "Not saved: unknown clean sync action.")
		return
	}
	m, ok := s.editedMachine(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	// A second request while one is pending, or cancelling none, would only
	// add an audit entry.
	if m.CleanSyncRequested != request {
		if err := s.writer.UpdateMachineConfig(r.Context(), actorOf(r), id, store.ConfigChange{CleanSync: &request}); err != nil {
			s.writeError(w, r, err, "Machine not found.")
			return
		}
	}
	http.Redirect(w, r, "/machines/"+url.PathEscape(id), http.StatusSeeOther)
}

// machineConfigForm is the edit form of a Mac's own configuration; empty
// when the page shows no edit forms.
func machineConfigForm(edit editForms, m store.MachineDetail, global store.Config, globalRead bool) configForm {
	if !edit.On {
		return configForm{}
	}
	return configForm{
		Action:  "/admin/machines/" + url.PathEscape(m.MachineID) + "/config",
		Machine: true,
		Rows:    withFields(machineConfigRows(m.Config, global, globalRead), m.Config, true),
	}
}
