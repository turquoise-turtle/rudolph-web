package rudolphrules_test

import (
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// A group registered before format characters were refused is still named by its stored name when it is changed,
// deleted, assigned or targeted; the rest of the name is checked as before.
func TestRegisteredGroupName_AcceptsFormatCharacters(t *testing.T) {
	for in, want := range map[string]string{" Ops\u202e ": "ops\u202e", "en\u200bg": "en\u200bg"} {
		for name, f := range map[string]func(string) (string, error){
			"RegisteredGroupName": rudolphrules.RegisteredGroupName,
			"GroupTargetName":     rudolphrules.GroupTargetName,
			"ExistingGroupName":   func(n string) (string, error) { return rudolphrules.ExistingGroupName(n, "changed") },
		} {
			if got, err := f(in); err != nil || got != want {
				t.Errorf("%s(%q) = %q, %v; want %q", name, in, got, err, want)
			}
		}
	}
	for _, bad := range []string{"\u202e", "rudolph:x\u202e", "a\u202e\x01", "rudolph:ungrouped"} {
		if got, err := rudolphrules.RegisteredGroupName(bad); err == nil {
			t.Errorf("RegisteredGroupName(%q) = %q, want an error", bad, got)
		}
	}
	if got, err := rudolphrules.GroupTargetName("rudolph:ungrouped"); err != nil || got != "rudolph:ungrouped" {
		t.Errorf("GroupTargetName(ungrouped) = %q, %v", got, err)
	}
}

func TestGroupName_KeepsJoiners(t *testing.T) {
	for _, name := range []string{"a\u200cb", "a\u200db"} {
		if _, err := rudolphrules.GroupName(name); err != nil {
			t.Errorf("GroupName(%q): %v", name, err)
		}
	}
}
