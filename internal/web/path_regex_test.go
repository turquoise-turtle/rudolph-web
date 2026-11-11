package web_test

// Path regex checks (owner approved, 2026-11-11). Santa compiles AllowedPathRegex and BlockedPathRegex with ICU
// (NSRegularExpression) and silently ignores one it cannot compile, so the forms check them before saving:
//   - A pattern Go's RE2 compiles is saved.
//   - A pattern RE2 refuses but a backtracking engine close to ICU (github.com/dlclark/regexp2) compiles is saved too:
//     lookahead, lookbehind, backreferences and atomic groups, which Santa supports.
//   - A pattern both refuse is refused with 400, naming the setting and saying Santa ignores a regex it cannot compile,
//     unless the form also posts skip_regex_validation=on (a checkbox on the form: "Save path regexes unchecked"), for
//     ICU syntax neither engine knows, such as possessive quantifiers. The box skips only the regex check.
// The same on the global form and a Mac's form.

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
)

func TestPathRegex_RE2AndICULikePatternsAreSaved(t *testing.T) {
	for _, pattern := range []string{
		`^/Users/[^/]+/Downloads/`,
		`^/Users/[^/]+/Downloads/(?!Approved/)`, // lookahead
		`^/Applications/(?<!Utilities/)Tools/`,  // lookbehind
		`^/opt/(\w+)/\1/`,                       // backreference
		`^/opt/(?>lab|labs)/`,                   // atomic group
	} {
		for _, machine := range []bool{false, true} {
			s := configEditStore()
			a := newEditApp(t, s, true)
			admin := a.cookie(t, auth.RoleAdmin)
			path := "/admin/config"
			if machine {
				path = "/admin/machines/" + cfgMac + "/config"
			}
			rec := a.post(t, path, url.Values{"BlockedPathRegex": {pattern}}, admin, true)
			if rec.Code != http.StatusSeeOther {
				t.Errorf("%s (machine %v): status %d, want 303\n%s", pattern, machine, rec.Code, rec.Body.String())
				continue
			}
			got := globalConfigOf(t, s).BlockedPathRegex
			if machine {
				got = machineConfigOf(t, s).BlockedPathRegex
			}
			if got == nil || *got != pattern {
				t.Errorf("%s (machine %v): stored %v", pattern, machine, got)
			}
		}
	}
}

func TestPathRegex_InvalidPatternsNeedTheOverride(t *testing.T) {
	for _, pattern := range []string{
		`^/Users/(`,     // unbalanced
		`^/opt/[a-`,     // unterminated class
		`^/opt/lab/a*+`, // possessive: ICU only
	} {
		for _, machine := range []bool{false, true} {
			s := configEditStore()
			a := newEditApp(t, s, true)
			admin := a.cookie(t, auth.RoleAdmin)
			path := "/admin/config"
			if machine {
				path = "/admin/machines/" + cfgMac + "/config"
			}
			rec := a.post(t, path, url.Values{"AllowedPathRegex": {pattern}}, admin, true)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s (machine %v): status %d, want 400", pattern, machine, rec.Code)
				continue
			}
			mustContain(t, rec.Body.String(), "allowed path", "Santa ignores", "Save path regexes unchecked")
			stored := globalConfigOf(t, s).AllowedPathRegex
			if machine {
				stored = machineConfigOf(t, s).AllowedPathRegex
			}
			if stored != nil {
				t.Errorf("%s (machine %v): a refused pattern was stored", pattern, machine)
			}

			rec = a.post(t, path, url.Values{"AllowedPathRegex": {pattern}, "skip_regex_validation": {"on"}}, admin, true)
			if rec.Code != http.StatusSeeOther {
				t.Errorf("%s (machine %v) with the override: status %d, want 303\n%s", pattern, machine, rec.Code, rec.Body.String())
				continue
			}
			stored = globalConfigOf(t, s).AllowedPathRegex
			if machine {
				stored = machineConfigOf(t, s).AllowedPathRegex
			}
			if stored == nil || *stored != pattern {
				t.Errorf("%s (machine %v) with the override: stored %v", pattern, machine, stored)
			}
		}
	}
}

func TestPathRegex_OverrideSkipsOnlyTheRegexCheck(t *testing.T) {
	s := configEditStore()
	a := newEditApp(t, s, true)
	rec := a.post(t, "/admin/config", url.Values{"BatchSize": {"0"}, "skip_regex_validation": {"on"}}, a.cookie(t, auth.RoleAdmin), true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("batch size 0 with the regex override: status %d, want 400", rec.Code)
	}
}

func TestPathRegex_FormsOfferTheOverride(t *testing.T) {
	s := configEditStore()
	a := newEditApp(t, s, true)
	for _, path := range []string{"/admin/config", "/machines/" + cfgMac} {
		_, body := adminGet(t, a, path)
		mustContain(t, body, `name="skip_regex_validation"`, "Save path regexes unchecked")
	}
}
