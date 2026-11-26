package web_test

// The try form separates the rule from the sample it is tried against (owner request, 2026-11-26): on a first try the
// expression was typed into the args box, as nothing said which box was the rule. The expression has a box of its own,
// "The expression", and every other field sits in "A sample execution". The sample's fields are a grid, not a wrapping
// flex row, which ran past the right edge of a laptop-width window.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestCELTry_ExpressionAndSampleAreSeparateBoxes(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	_, body := a.viewerGet(t, "/rules/cel?expr=true")
	form := regexp.MustCompile(`(?s)<form[^>]*\bclass="cel-try"[^>]*>(.*?)</form>`).FindStringSubmatch(body)
	if form == nil {
		t.Fatal("no try form")
	}
	sets := regexp.MustCompile(`(?s)<fieldset>(.*?)</fieldset>`).FindAllStringSubmatch(form[1], -1)
	if len(sets) != 2 {
		t.Fatalf("%d fieldsets in the try form, want 2 (the expression, then a sample execution)", len(sets))
	}
	if !strings.Contains(sets[0][1], "<legend>The expression") || !strings.Contains(sets[0][1], `name="expr"`) {
		t.Errorf("the first box is not the expression: %.300q", sets[0][1])
	}
	if !strings.Contains(sets[1][1], "<legend>A sample execution") || !strings.Contains(sets[1][1], `class="cel-sample"`) {
		t.Errorf("the second box is not the sample execution: %.300q", sets[1][1])
	}
	for _, name := range []string{"args", "envs", "euid", "path", "cwd", "signing_id", "team_id", "signing_time", "secure_signing_time", "platform"} {
		if !strings.Contains(sets[1][1], `name="`+name+`"`) {
			t.Errorf("the sample execution box has no %s field", name)
		}
		if strings.Contains(sets[0][1], `name="`+name+`"`) {
			t.Errorf("the expression box holds the sample's %s field", name)
		}
	}
	if strings.Contains(sets[1][1], `name="expr"`) {
		t.Error("the expression box is inside the sample execution")
	}
}

func TestCELTry_SampleFieldsAreAGrid(t *testing.T) {
	css, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	rule := regexp.MustCompile(`\.cel-sample\s*\{[^}]*\}`).FindString(string(css))
	if !strings.Contains(rule, "display: grid") || !strings.Contains(rule, "auto-fill") {
		t.Errorf(".cel-sample is %q; want a grid that fills the width it has, so its fields wrap instead of running off the window", rule)
	}
	if regexp.MustCompile(`\.cel-try\s*\{[^}]*display:\s*flex`).MatchString(string(css)) {
		t.Error(".cel-try is a flex row again; its sample fields ran past the window's edge")
	}
}

// A sample with any command answered BLOCKLIST, which read as "this blocks every program with these arguments" (owner,
// 2026-11-26). An answer says it is for a program the rule covers, names the sample's signing ID or team ID when it
// has one, and says Santa runs the expression only for programs matching the rule's identifier.
func TestCELTry_AnswerSaysWhichProgramsTheRuleCovers(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	scope := regexp.MustCompile(`(?s)<p class="hint cel-scope">(.*?)</p>`)
	for _, c := range []struct{ query, names string }{
		{"/rules/cel?expr=true&args=supernormalbinary%0Aremove", ""},
		{"/rules/cel?expr=true&signing_id=platform:com.apple.profiles", "signing ID <code>platform:com.apple.profiles</code>"},
		{"/rules/cel?expr=true&team_id=EQHXZ8M8AV", "team ID <code>EQHXZ8M8AV</code>"},
	} {
		_, body := a.viewerGet(t, c.query)
		m := scope.FindStringSubmatch(body)
		if m == nil {
			t.Errorf("%s: no note on which programs the rule covers", c.query)
			continue
		}
		if !strings.Contains(m[1], "only for programs that match the rule&#39;s identifier") && !strings.Contains(m[1], "only for programs that match the rule's identifier") {
			t.Errorf("%s: the note %q does not say the identifier decides which programs the expression runs for", c.query, m[1])
		}
		if c.names != "" && !strings.Contains(m[1], c.names) {
			t.Errorf("%s: the note %q does not name the sample's %s", c.query, m[1], c.names)
		}
		if strings.Index(body, `class="cel-answer"`) > strings.Index(body, `class="hint cel-scope"`) {
			t.Errorf("%s: the note comes before the answer", c.query)
		}
	}
	// No note where nothing was tried, or the expression did not compile.
	for _, q := range []string{"/rules/cel", "/rules/cel?expr=target.signing_id+=="} {
		if _, body := a.viewerGet(t, q); strings.Contains(body, "cel-scope") {
			t.Errorf("%s: a scope note without an answer", q)
		}
	}
}
