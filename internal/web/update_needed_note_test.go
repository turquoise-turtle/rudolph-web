package web_test

// A shorter "update needed" note on the machine page (owner request, 2026-11-25). A Mac below
// web.Options.MinSantaVersion (RUDOLPH_WEB_MIN_SANTA_VERSION) showed "update needed (below the minimum, Santa 2026.9:
// Santa 2026.9 fixed ways for a local user to pause enforcement)" next to its version. It now reads
// "update needed (below <minimum>)", the configured minimum, such as "update needed (below 2026.9)". The full
// explanation, naming the minimum and why it matters, moves to a title attribute on the element holding the "update
// needed" marker: no script and no inline style, which the page's CSP refuses. The legacy client note is unchanged.
//
// The machine list already shows only the "update needed" badge, with the explanation in its title; that stays, so the
// list and the page explain the marker the same way (decided 2026-11-25: no separate legend).

import (
	"html"
	"regexp"
	"strings"
	"testing"
)

var (
	titledElement = regexp.MustCompile(`(?s)<(\w+)([^>]*)\btitle="([^"]*)"([^>]*)>(.*?)</(\w+)>`)
	anyTag        = regexp.MustCompile(`(?s)<[^>]*>`)
	spaces        = regexp.MustCompile(`\s+`)
)

// visibleText is the page's text without tags (and so without attributes), entities decoded, spaces collapsed.
func visibleText(body string) string {
	body = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>`).ReplaceAllString(body, "")
	return spaces.ReplaceAllString(html.UnescapeString(anyTag.ReplaceAllString(body, " ")), " ")
}

// updateNeededTitle returns the title of the element whose text holds "update needed", and that element's other
// attributes.
func updateNeededTitle(t *testing.T, body string) (title, attrs string) {
	t.Helper()
	for _, m := range titledElement.FindAllStringSubmatch(body, -1) {
		if m[1] == m[6] && strings.Contains(anyTag.ReplaceAllString(m[5], ""), "update needed") {
			return html.UnescapeString(m[3]), m[2] + m[4]
		}
	}
	t.Fatalf("no element with a title holds \"update needed\"; the page reads %q", santaLine(visibleText(body)))
	return "", ""
}

func TestUpdateNeeded_MachinePageNoteIsShort(t *testing.T) {
	for _, minimum := range []string{"2026.9", "2026.10"} {
		a := newAppWithMinVersion(t, versionStore(), minimum)
		_, body := a.viewerGet(t, "/machines/m-old")
		text := visibleText(body)
		if want := "update needed (below " + minimum + ")"; !strings.Contains(text, want) {
			t.Errorf("minimum %s: the machine page does not read %q; its Santa line is %q", minimum, want, santaLine(text))
		}
		for _, long := range []string{"below the minimum", "pause enforcement", "fixed ways"} {
			if strings.Contains(text, long) {
				t.Errorf("minimum %s: the long explanation (%q) is still in the page's text; it belongs in a title", minimum, long)
			}
		}
		title, attrs := updateNeededTitle(t, body)
		if !strings.Contains(title, minimum) || !strings.Contains(title, "pause enforcement") {
			t.Errorf("minimum %s: title %q; want the explanation, naming the minimum and why it matters", minimum, title)
		}
		if strings.Contains(attrs, "style=") || regexp.MustCompile(`\bon[a-z]+=`).MatchString(attrs) {
			t.Errorf("minimum %s: the marker has an inline style or handler (%s); the CSP refuses both", minimum, attrs)
		}
	}
}

func TestUpdateNeeded_LegacyNoteUnchanged(t *testing.T) {
	a := newAppWithMinVersion(t, versionStore(), "2026.9")
	_, body := a.viewerGet(t, "/machines/m-legacy")
	text := visibleText(body)
	for _, want := range []string{"legacy client", "(below 2026.8: supported only on macOS 13, with limits)", "update needed (below 2026.9)"} {
		if !strings.Contains(text, want) {
			t.Errorf("the legacy Mac's page does not read %q; its Santa line is %q", want, santaLine(text))
		}
	}
}

func TestUpdateNeeded_MachineListKeepsTheExplanationInTheTitle(t *testing.T) {
	a := newAppWithMinVersion(t, versionStore(), "2026.9")
	_, body := a.viewerGet(t, "/machines")
	title, _ := updateNeededTitle(t, body)
	if !strings.Contains(title, "2026.9") || !strings.Contains(title, "pause enforcement") {
		t.Errorf("the machine list's update needed title is %q; want the explanation with the minimum", title)
	}
	if strings.Contains(visibleText(body), "pause enforcement") {
		t.Error("the machine list spells the explanation out in its text; it stays in the badge's title")
	}
}

// santaLine is the part of the page's text from "Santa" onwards, for messages.
func santaLine(text string) string {
	i := strings.Index(text, "Santa ")
	if i < 0 {
		return ""
	}
	return text[i:min(len(text), i+200)]
}
