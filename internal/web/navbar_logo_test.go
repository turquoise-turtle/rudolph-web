package web_test

// Owner: "can you add that logo to the left of the Rudolph title in the navbar?". The favicon's SVG, from the same
// hash-named URL, decorates the site name: empty alt text, since the name is right beside it.

import (
	"regexp"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func TestNavbarLogo_BeforeTheSiteName(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	_, body := a.viewerGet(t, "/machines")
	brand := regexp.MustCompile(`<a class="brand" href="/machines"><img class="logo" src="(/static/favicon\.[0-9a-f]+\.svg)" alt="" width="\d+" height="\d+">\s*Rudolph</a>`).FindStringSubmatch(body)
	if brand == nil {
		t.Fatal("the brand link does not start with the logo image (empty alt, width and height) before Rudolph")
	}
	icon := regexp.MustCompile(`<link rel="icon" href="([^"]+)" type="image/svg\+xml">`).FindStringSubmatch(body)
	if icon == nil || icon[1] != brand[1] {
		t.Errorf("the logo %s is not the favicon's hashed SVG %v", brand[1], icon)
	}
}
