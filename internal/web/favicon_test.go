package web_test

// Owner: "can we add a favicon?". The icon is served from the site itself (no external requests), cache-busted like
// the stylesheet, and /favicon.ico, which browsers ask for on their own, answers even before sign-in so it does not
// bounce a signed-out visitor to Microsoft.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

func hashedLink(t *testing.T, body, pattern, file string) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no link matching %s in the page", pattern)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if len(m[2]) < 12 || !strings.HasPrefix(hex.EncodeToString(sum[:]), m[2]) {
		t.Errorf("%s: want at least 12 hex digits of the SHA-256 of %s", m[1], file)
	}
	return m[1]
}

func TestFavicon_LinkedAndCacheBusted(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	_, body := a.viewerGet(t, "/machines")

	svg := hashedLink(t, body, `<link rel="icon" href="(/static/favicon\.([0-9a-f]+)\.svg)" type="image/svg\+xml">`, "static/favicon.svg")
	png := hashedLink(t, body, `<link rel="icon" href="(/static/favicon\.([0-9a-f]+)\.png)" type="image/png" sizes="32x32">`, "static/favicon.png")
	hashedLink(t, body, `<link rel="apple-touch-icon" href="(/static/apple-touch-icon\.([0-9a-f]+)\.png)">`, "static/apple-touch-icon.png")

	for target, ctype := range map[string]string{svg: "image/svg+xml", png: "image/png"} {
		rec := a.get(t, target, nil) // static files need no session
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), ctype) {
			t.Errorf("%s: status %d type %q, want 200 %s", target, rec.Code, rec.Header().Get("Content-Type"), ctype)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
			t.Errorf("%s: Cache-Control %q, want immutable", target, cc)
		}
	}
}

// The SVG is an image only: nothing in it runs or loads anything.
func TestFavicon_SVGIsInert(t *testing.T) {
	data, err := os.ReadFile("static/favicon.svg")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ToLower(string(data))
	if !strings.Contains(s, "<svg") || !strings.Contains(s, `viewbox=`) {
		t.Error("favicon.svg is not an SVG with a viewBox")
	}
	for _, bad := range []string{"<script", "href", "<foreignobject", "on", "url(", "@import", "<!entity", "<image"} {
		if bad == "on" {
			if regexp.MustCompile(`\son[a-z]+\s*=`).MatchString(s) {
				t.Error("favicon.svg has an event handler attribute")
			}
			continue
		}
		if strings.Contains(s, bad) {
			t.Errorf("favicon.svg contains %q", bad)
		}
	}
}

func TestFavicon_PNGsAreRealImages(t *testing.T) {
	for file, size := range map[string]uint32{"static/favicon.png": 32, "static/apple-touch-icon.png": 180} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) || len(data) < 24 {
			t.Fatalf("%s is not a PNG", file)
		}
		w := uint32(data[16])<<24 | uint32(data[17])<<16 | uint32(data[18])<<8 | uint32(data[19])
		h := uint32(data[20])<<24 | uint32(data[21])<<16 | uint32(data[22])<<8 | uint32(data[23])
		if w != size || h != size {
			t.Errorf("%s is %dx%d, want %dx%d", file, w, h, size, size)
		}
	}
}

// Browsers request /favicon.ico on their own. It answers with the icon whether or not anyone is signed in, rather than
// bouncing a signed-out visitor to sign-in, and is revalidated since its name never changes.
func TestFavicon_RootICOAnswersWithoutSignIn(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	png, err := os.ReadFile("static/favicon.png")
	if err != nil {
		t.Fatal(err)
	}
	rec := a.get(t, "/favicon.ico", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/favicon.ico signed out: status %d (Location %q), want 200", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/png") || !bytes.Equal(rec.Body.Bytes(), png) {
		t.Errorf("/favicon.ico: type %q, want the PNG icon", rec.Header().Get("Content-Type"))
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("/favicon.ico: Cache-Control %q, want no-cache", cc)
	}
	// Only that one root file is exempt: its neighbours still need sign-in.
	if rec := a.get(t, "/favicon.png", nil); rec.Code != http.StatusFound {
		t.Errorf("/favicon.png signed out: status %d, want the sign-in redirect", rec.Code)
	}
}
