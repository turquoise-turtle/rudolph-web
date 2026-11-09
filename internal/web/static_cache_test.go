package web_test

// Found after a production deploy: the page linked /static/style.css, which the browser and CloudFront (whose
// CachingOptimized policy also ignores query strings) kept for an hour, so a new layout did not show. The page now links
// the stylesheet under a name that carries a hash of its content: a changed stylesheet gets a new URL, and that URL can
// be cached for good.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

var stylesheetLink = regexp.MustCompile(`<link rel="stylesheet" href="(/static/style\.([0-9a-f]+)\.css)">`)

func TestStaticCache_StylesheetURLCarriesItsContentHash(t *testing.T) {
	css, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(css)
	full := hex.EncodeToString(sum[:])

	a := newApp(t, fake.New(), 10)
	_, body := a.viewerGet(t, "/machines")
	m := stylesheetLink.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no hashed stylesheet link in the page")
	}
	if len(m[2]) < 12 || !strings.HasPrefix(full, m[2]) {
		t.Errorf("stylesheet URL %s: want at least 12 hex digits of the SHA-256 of style.css (%s)", m[1], full)
	}

	rec := a.get(t, m[1], nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("%s: status %d type %q", m[1], rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Body.String() != string(css) {
		t.Errorf("%s does not serve style.css", m[1])
	}
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") || !strings.Contains(cc, "public") {
		t.Errorf("%s: Cache-Control %q, want public, max-age=31536000, immutable", m[1], cc)
	}

	// The error page shares the layout and the link.
	rec = a.get(t, "/admin", a.cookie(t, auth.RoleViewer))
	if got := stylesheetLink.FindStringSubmatch(rec.Body.String()); got == nil || got[1] != m[1] {
		t.Errorf("the 403 page links %v, want %s", got, m[1])
	}
}

// A hash that is not the current stylesheet's is not served: caching it for good would pin whatever it returned.
func TestStaticCache_StaleHashIsNotFound(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	for _, p := range []string{"/static/style.000000000000.css", "/static/style..css", "/static/style.ZZZZZZZZZZZZ.css"} {
		if rec := a.get(t, p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
	}
}

// The plain name still works (old bookmarks, the cached HTML of an open tab) but must be revalidated every time.
func TestStaticCache_PlainNameIsRevalidated(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	rec := a.get(t, "/static/style.css", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") || strings.Contains(cc, "immutable") {
		t.Errorf("/static/style.css: Cache-Control %q, want no-cache", cc)
	}
}
