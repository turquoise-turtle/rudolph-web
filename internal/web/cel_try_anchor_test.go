package web_test

// The CEL try form keeps its inputs and its result in view together (owner request, 2026-11-25). Landing on the
// answer itself (#try-result, rudolph-web 71e7205) put "Santa would apply ..." at the top of the window and scrolled
// the inputs out of view, so every try meant scrolling back up. The fragment now names the section's heading,
// <h2 id="try">, once on the page: the browser shows the heading, the inputs and, straight after the form, the
// answer. A GET form keeps the action's fragment when it builds the address, so no script is needed. The examples'
// "Try it" links land on the same heading (/rules/cel?expr=...#try). The help page is the only try form: /rules and the
// add-rule form have none (the add-rule form checks a CEL expression when it is saved).

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/santacel"
)

var tryHeading = regexp.MustCompile(`<h2 id="try">Try an expression</h2>`)

func TestCELTry_FormActionPointsAtTheTrySection(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	for _, path := range []string{"/rules/cel", "/rules/cel?expr=true"} {
		code, body := a.viewerGet(t, path)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", path, code)
		}
		forms := regexp.MustCompile(`<form[^>]*\bclass="cel-try"[^>]*>`).FindAllString(body, -1)
		if len(forms) != 1 {
			t.Fatalf("%s: %d try forms, want 1", path, len(forms))
		}
		if !strings.Contains(forms[0], `action="/rules/cel#try"`) || !strings.Contains(forms[0], `method="get"`) {
			t.Errorf("%s: the try form is %s; want method get and action /rules/cel#try", path, forms[0])
		}
	}
}

func TestCELTry_HeadingCarriesTheIDAndTheAnswerFollowsTheForm(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	for q, want := range map[string]string{
		"true":                    "Santa would apply",
		"target.signing_id ==":    "Santa would not apply this",
		"target.team_id == 'ABC'": "Santa would apply",
	} {
		_, body := a.viewerGet(t, "/rules/cel?"+url.Values{"expr": {q}}.Encode())
		if n := strings.Count(body, `id="try"`); n != 1 {
			t.Errorf("%q: id=\"try\" appears %d times, want exactly once", q, n)
			continue
		}
		if strings.Contains(body, `id="try-result"`) {
			t.Errorf("%q: the answer still carries id=\"try-result\", which scrolls the inputs out of view", q)
		}
		h := tryHeading.FindStringIndex(body)
		if h == nil {
			t.Errorf("%q: no <h2 id=\"try\"> heading on the try section", q)
			continue
		}
		form := strings.Index(body, `class="cel-try"`)
		end := strings.Index(body[max(form, 0):], `</form>`)
		answer := strings.Index(body, want)
		if form < h[0] || end < 0 || answer < form+end {
			t.Errorf("%q: want the heading, then the form, then the answer (%q); got heading at %d, form at %d, answer at %d", q, want, h[0], form, answer)
		}
		// Nothing between the form and the answer, so both are on screen together.
		if gap := body[form+end+len(`</form>`) : answer]; strings.Contains(gap, "<h2") || strings.Contains(gap, "<section") || strings.Contains(gap, "<table") {
			t.Errorf("%q: something stands between the form and the answer: %.200q", q, gap)
		}
	}

	_, body := a.viewerGet(t, "/rules/cel")
	if n := strings.Count(body, `id="try"`); n != 1 {
		t.Errorf("before a try, id=\"try\" appears %d times, want exactly once", n)
	}
}

func TestCELTry_ExampleLinksLandOnTheTrySection(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	_, body := a.viewerGet(t, "/rules/cel")
	links := regexp.MustCompile(`<a href="(/rules/cel\?expr=[^"]*)">Try it</a>`).FindAllStringSubmatch(body, -1)
	if len(links) != len(santacel.Examples) {
		t.Fatalf("%d Try it links, want one per example (%d)", len(links), len(santacel.Examples))
	}
	for _, l := range links {
		if !strings.HasSuffix(l[1], "#try") {
			t.Errorf("Try it link %s does not end in #try", l[1])
		}
	}
}

func TestCELTry_NoOtherTryFormsNeedTheFragment(t *testing.T) {
	a := newEditApp(t, celStore(), true)
	for _, path := range []string{"/rules", "/admin/rules/new"} {
		rec := a.get(t, path, a.cookie(t, auth.RoleAdmin))
		if rec.Code != http.StatusOK {
			continue
		}
		if strings.Contains(rec.Body.String(), `class="cel-try"`) {
			t.Errorf("%s has a CEL try form; give its action the #try fragment", path)
		}
	}
}
