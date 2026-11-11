package web_test

// Configuration pages (owner approved, 2026-11-11, step S1 of the admin plan; read only, editing comes later):
//   - GET /admin/config, for admins whether or not editing is on (like /admin/audit), shows every global setting
//     Rudolph sends, one table row each: <tr data-setting="<Rudolph attribute name>">, with the value, or "not set"
//     when the global config doesn't store it. Without a global config row at all, the page says Rudolph's defaults
//     apply. One store call: GlobalConfig.
//   - A Mac's page gains a "Configuration" table with the same rows, each showing what the Mac gets: its own value,
//     marked "(this Mac)", or the global one, marked "(global config)"; "not set" when neither stores it. The page
//     reads the Mac's config from the GetMachine query it already makes, and the global config with one GlobalConfig
//     call, which replaces the GlobalClientMode call: no more store calls than before.
//   - Settings are those `rudolph config update` sets, by attribute: ClientMode, AllowedPathRegex, BlockedPathRegex,
//     BatchSize, EnableBundles, EnableTransitiveRules, FullSyncInterval, UploadLogsUrl, EnableAllEventUpload,
//     DisableUnknownEventUpload, OverrideFileAccessAction, EventDetailURL, EventDetailText. USB settings are left out
//     for now (owner): Santa 2026.8 replaces them with a removable media policy Rudolph doesn't model yet.
//   - URLs are shown as the CLI's audit shows them: scheme, host and path only, as a query, fragment or user
//     information may hold a token, and Mac pages are open to viewers.
//   - Nav: admins get a "settings" link to /admin/config.

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

var configSettings = []string{"ClientMode", "AllowedPathRegex", "BlockedPathRegex", "BatchSize", "EnableBundles",
	"EnableTransitiveRules", "FullSyncInterval", "UploadLogsUrl", "EnableAllEventUpload", "DisableUnknownEventUpload",
	"OverrideFileAccessAction", "EventDetailURL", "EventDetailText"}

func settingRow(t *testing.T, body, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<tr data-setting="`+regexp.QuoteMeta(name)+`">(.*?)</tr>`).FindAllStringSubmatch(body, -1)
	if len(m) != 1 {
		t.Fatalf("found %d rows for setting %s, want 1", len(m), name)
	}
	return m[0][1]
}

func ptr[T any](v T) *T { return &v }

func globalConfigFixture() store.Config {
	return store.Config{
		Exists:               true,
		ClientMode:           store.ClientModeLockdown,
		BatchSize:            ptr(100),
		FullSyncInterval:     ptr(900),
		EnableBundles:        ptr(true),
		BlockedPathRegex:     ptr(`^/Users/[^/]+/Downloads/<script>`),
		UploadLogsURL:        ptr("https://user:secret@logs.example.com/upload?token=abc123#frag"),
		EnableAllEventUpload: ptr(false),
		EventDetailURL:       ptr("https://help.example.com/santa?sha=%file_sha%"),
	}
}

func adminGet(t *testing.T, a *app, path string) (int, string) {
	t.Helper()
	rec := a.get(t, path, a.cookie(t, auth.RoleAdmin))
	return rec.Code, rec.Body.String()
}

func TestConfigPage_ShowsEveryGlobalSetting(t *testing.T) {
	s := fake.New()
	s.SetGlobalConfig(globalConfigFixture())
	for _, writable := range []bool{false, true} {
		a := newEditApp(t, s, writable)
		s.ResetCalls()
		code, body := adminGet(t, a, "/admin/config")
		if code != http.StatusOK {
			t.Fatalf("editing %v: status %d", writable, code)
		}
		for _, name := range configSettings {
			settingRow(t, body, name)
		}
		mustContain(t, settingRow(t, body, "ClientMode"), "LOCKDOWN")
		mustContain(t, settingRow(t, body, "BatchSize"), "100")
		mustContain(t, settingRow(t, body, "FullSyncInterval"), "900")
		mustContain(t, settingRow(t, body, "EnableBundles"), "yes")
		mustContain(t, settingRow(t, body, "EnableAllEventUpload"), "no")
		mustContain(t, settingRow(t, body, "EnableTransitiveRules"), "not set")
		mustContain(t, settingRow(t, body, "OverrideFileAccessAction"), "not set")
		mustContain(t, settingRow(t, body, "BlockedPathRegex"), "&lt;script&gt;")
		mustNotContain(t, settingRow(t, body, "BlockedPathRegex"), "<script>")
		mustNotContain(t, body, "BlockUsbMount", "RemountUsbMode")

		upload := settingRow(t, body, "UploadLogsUrl")
		mustContain(t, upload, "https://logs.example.com/upload")
		mustNotContain(t, body, "secret", "abc123", "#frag")
		mustContain(t, settingRow(t, body, "EventDetailURL"), "https://help.example.com/santa")

		var calls []string
		for _, c := range s.Calls() {
			calls = append(calls, c.Method)
		}
		if strings.Join(calls, ",") != "GlobalConfig" {
			t.Errorf("editing %v: store calls %v, want one GlobalConfig", writable, calls)
		}
	}
}

func TestConfigPage_NoGlobalRowMeansDefaults(t *testing.T) {
	s := fake.New() // no global config row
	a := newEditApp(t, s, false)
	code, body := adminGet(t, a, "/admin/config")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "default")
	mustContain(t, settingRow(t, body, "ClientMode"), "MONITOR")
}

func TestConfigPage_AdminsOnly(t *testing.T) {
	s := fake.New()
	a := newEditApp(t, s, true)
	if rec := a.get(t, "/admin/config", a.cookie(t, auth.RoleViewer)); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status %d, want 403", rec.Code)
	}
	_, body := adminGet(t, a, "/machines")
	mustContain(t, body, `href="/admin/config"`)
	_, viewerBody := a.viewerGet(t, "/machines")
	mustNotContain(t, viewerBody, `href="/admin/config"`)
}

func TestMachinePage_ConfigurationShowsOwnAndInherited(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: "m-c", SerialNum: "C02C", Hostname: "c.example.com", ClientMode: store.ClientModeMonitor, LastSeen: fixedNow})
	s.SetGlobalConfig(globalConfigFixture())
	s.SetMachineConfig("m-c", store.Config{
		Exists:                    true,
		BatchSize:                 ptr(25),
		DisableUnknownEventUpload: ptr(true),
		AllowedPathRegex:          ptr(`^/opt/lab/`),
	})
	a := newApp(t, s, 10)
	s.ResetCalls()
	code, body := a.viewerGet(t, "/machines/m-c")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "Configuration")
	for _, name := range configSettings {
		settingRow(t, body, name)
	}
	for name, want := range map[string][]string{
		"BatchSize":                 {"25", "(this Mac)"},
		"DisableUnknownEventUpload": {"yes", "(this Mac)"},
		"AllowedPathRegex":          {"^/opt/lab/", "(this Mac)"},
		"ClientMode":                {"LOCKDOWN", "(global config)"},
		"FullSyncInterval":          {"900", "(global config)"},
		"EnableBundles":             {"yes", "(global config)"},
		"EnableTransitiveRules":     {"not set"},
	} {
		row := settingRow(t, body, name)
		mustContain(t, row, want...)
	}
	mustNotContain(t, settingRow(t, body, "BatchSize"), "100")
	mustNotContain(t, settingRow(t, body, "EnableTransitiveRules"), "(this Mac)", "(global config)")
	mustNotContain(t, body, "secret", "abc123")

	var global, machine int
	for _, c := range s.Calls() {
		switch c.Method {
		case "GlobalConfig", "GlobalClientMode":
			global++
		case "GetMachine":
			machine++
		}
	}
	if global != 1 || machine != 1 {
		t.Errorf("machine page: %d global config reads and %d GetMachine calls, want 1 and 1: %+v", global, machine, s.Calls())
	}
}

// The existing helper still sets the global client mode, which the configuration table shows.
func TestMachinePage_ConfigurationFollowsSetGlobalClientMode(t *testing.T) {
	body := healthApp(t, fixedNow.Add(-time.Minute), healthy(fixedNow.Add(-time.Minute)), func(s *fake.Store) {
		s.SetGlobalClientMode(store.ClientModeLockdown)
	})
	mustContain(t, settingRow(t, body, "ClientMode"), "LOCKDOWN", "(global config)")
}
