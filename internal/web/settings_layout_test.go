package web_test

// Settings shown first, editing folded away (owner request, 2026-11-11). With editing on, an admin sees the same
// read-only configuration table as everyone else (<tr data-setting="…">, with "(this Mac)" and "(global config)"
// marks on a Mac's page), and the form to change it below, inside a closed
// <details class="edit config-edit"><summary>Change settings</summary>. The form's rows are marked
// data-edit-setting="<attribute>" instead, so each setting has exactly one data-setting row on the page. The save
// button sits at the end of the form in <div class="form-actions">, after the confirm_mode input, and the stylesheet
// right-aligns that row, so the button reads as the form's action rather than another field.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
)

var configEditDetails = regexp.MustCompile(`(?s)<details class="edit config-edit"><summary>Change settings</summary>(.*?)</details>`)

func TestSettingsLayout_TableFirstFormFolded(t *testing.T) {
	s := configEditStore()
	a := newEditApp(t, s, true)
	for _, path := range []string{"/admin/config", "/machines/" + cfgMac} {
		_, body := adminGet(t, a, path)
		for _, name := range configSettings {
			settingRow(t, body, name) // exactly one read-only row each
		}
		m := configEditDetails.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("%s: no closed <details class=\"edit config-edit\"> with summary Change settings", path)
		}
		inner := m[1]
		mustContain(t, inner, `<form class="config-form"`, `name="confirm_mode"`)
		for _, name := range configSettings {
			mustContain(t, inner, `data-edit-setting="`+name+`"`)
		}
		if strings.Index(body, `<tr data-setting="ClientMode">`) > strings.Index(body, `<details class="edit config-edit">`) {
			t.Errorf("%s: the read-only table should come before the folded form", path)
		}

		actions := regexp.MustCompile(`(?s)<div class="form-actions">(.*?)</div>`).FindStringSubmatch(inner)
		if actions == nil {
			t.Fatalf("%s: no <div class=\"form-actions\"> in the form", path)
		}
		input, button := strings.Index(actions[1], `name="confirm_mode"`), strings.Index(actions[1], `<button type="submit"`)
		if input < 0 || button < 0 || button < input {
			t.Errorf("%s: form-actions should hold the confirm_mode input and then the save button:\n%s", path, actions[1])
		}
	}

	_, body := adminGet(t, a, "/machines/"+cfgMac)
	mustContain(t, settingRow(t, body, "BatchSize"), "25", "(this Mac)")
	mustContain(t, settingRow(t, body, "FullSyncInterval"), "600", "(global config)")
}

func TestSettingsLayout_FormActionsAreRightAligned(t *testing.T) {
	a := newEditApp(t, configEditStore(), true)
	rec := a.get(t, "/static/style.css", nil)
	css := rec.Body.String()
	rule := regexp.MustCompile(`(?s)\.form-actions\s*\{([^}]*)\}`).FindStringSubmatch(css)
	if rule == nil {
		t.Fatal("style.css has no .form-actions rule")
	}
	if !strings.Contains(rule[1], "justify-content: flex-end") && !strings.Contains(rule[1], "text-align: right") {
		t.Errorf(".form-actions does not right-align its contents: %s", rule[1])
	}
}

func TestSettingsLayout_NoFormWithoutEditing(t *testing.T) {
	s := configEditStore()
	readOnly := newEditApp(t, s, false)
	_, body := adminGet(t, readOnly, "/machines/"+cfgMac)
	mustNotContain(t, body, `config-edit`, `data-edit-setting`)
	settingRow(t, body, "BatchSize")

	editing := newEditApp(t, s, true)
	_, viewerBody := editing.viewerGet(t, "/machines/"+cfgMac)
	mustNotContain(t, viewerBody, `config-edit`, `data-edit-setting`)
	settingRow(t, viewerBody, "BatchSize")
	_ = auth.RoleViewer
}
