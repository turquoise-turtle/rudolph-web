package web_test

// The CEL try form keeps its result in view (owner request, 2026-11-25). After "Try" on /rules/cel the browser landed
// at the top of the page, far above the answer. The form's action now carries a fragment naming the answer,
// action="/rules/cel#try-result", and the answer (the paragraph with class cel-answer, holding "Santa would apply ..."
// or "Santa would not apply this: ...") carries id="try-result", once on the page. A GET form keeps the action's
// fragment when it builds the address, so no script is needed. The examples' "Try it" links land on the answer the
// same way (/rules/cel?expr=...#try-result). The help page is the only try form: /rules and the add-rule form have
// none (the add-rule form checks a CEL expression when it is saved).

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/santacel"
)

var tryResultElement = regexp.MustCompile(`(?s)<(\w+)[^>]*\bid="try-result"[^>]*>(.*?)</(\w+)>`)

func TestCELTry_FormActionPointsAtTheResult(t *testing.T) {
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
		if !strings.Contains(forms[0], `action="/rules/cel#try-result"`) || !strings.Contains(forms[0], `method="get"`) {
			t.Errorf("%s: the try form is %s; want method get and action /rules/cel#try-result", path, forms[0])
		}
	}
}

func TestCELTry_ResultCarriesTheID(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	for q, want := range map[string]string{
		"true":                    "Santa would apply",
		"target.signing_id ==":    "Santa would not apply this",
		"target.team_id == 'ABC'": "Santa would apply",
	} {
		_, body := a.viewerGet(t, "/rules/cel?"+url.Values{"expr": {q}}.Encode())
		if n := strings.Count(body, `id="try-result"`); n != 1 {
			t.Errorf("%q: id=\"try-result\" appears %d times, want exactly once", q, n)
			continue
		}
		m := tryResultElement.FindStringSubmatch(body)
		if m == nil {
			t.Errorf("%q: no element with id=\"try-result\"", q)
			continue
		}
		if !strings.Contains(m[2], want) {
			t.Errorf("%q: the element with id=\"try-result\" holds %.200q; want the answer (%q)", q, m[2], want)
		}
	}

	// Before a try there is no answer; an anchor with that id may be there, but never twice.
	_, body := a.viewerGet(t, "/rules/cel")
	if n := strings.Count(body, `id="try-result"`); n > 1 {
		t.Errorf("before a try, id=\"try-result\" appears %d times", n)
	}
}

func TestCELTry_ExampleLinksLandOnTheResult(t *testing.T) {
	a := newEditApp(t, celStore(), false)
	_, body := a.viewerGet(t, "/rules/cel")
	links := regexp.MustCompile(`<a href="(/rules/cel\?expr=[^"]*)">Try it</a>`).FindAllStringSubmatch(body, -1)
	if len(links) != len(santacel.Examples) {
		t.Fatalf("%d Try it links, want one per example (%d)", len(links), len(santacel.Examples))
	}
	for _, l := range links {
		if !strings.HasSuffix(l[1], "#try-result") {
			t.Errorf("Try it link %s does not end in #try-result", l[1])
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
			t.Errorf("%s has a CEL try form; give its action the #try-result fragment and its answer the id", path)
		}
	}
}
