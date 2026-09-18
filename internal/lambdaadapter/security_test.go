package lambdaadapter_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/turquoise-turtle/rudolph-web/internal/lambdaadapter"
)

const lambdaHost = "abc123.lambda-url.us-east-1.on.aws"

func baseEvent(method, rawPath string) events.APIGatewayV2HTTPRequest {
	ev := events.APIGatewayV2HTTPRequest{
		Version:  "2.0",
		RawPath:  rawPath,
		Headers:  map[string]string{"host": lambdaHost},
		RouteKey: "$default",
	}
	ev.RequestContext.DomainName = lambdaHost
	ev.RequestContext.HTTP.Method = method
	ev.RequestContext.HTTP.Path = rawPath
	return ev
}

// A path starting with // must stay a path. Parsing it as a URL reference
// would turn "evil.example" into URL.Host, which handlers and redirects may
// trust.
func TestAdapter_DoubleSlashPathNeverBecomesHost(t *testing.T) {
	for _, rawPath := range []string{"//evil.example/machines", "///evil.example/machines", "//evil.example"} {
		t.Run(rawPath, func(t *testing.T) {
			var got *http.Request
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r
				_, _ = io.WriteString(w, "ok")
			})
			ev := baseEvent(http.MethodGet, rawPath)
			ev.RawQueryString = "a=1"
			resp, err := lambdaadapter.New(h)(context.Background(), ev)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil {
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("handler not called and status %d, want 400", resp.StatusCode)
				}
				return
			}
			if got.URL.Host != "" {
				t.Errorf("URL.Host = %q taken from the path, want empty", got.URL.Host)
			}
			if got.URL.Path != rawPath {
				t.Errorf("URL.Path = %q, want %q", got.URL.Path, rawPath)
			}
			if got.URL.Scheme != "" || got.URL.User != nil {
				t.Errorf("URL has scheme %q / user %v from the path", got.URL.Scheme, got.URL.User)
			}
			if got.Host != lambdaHost {
				t.Errorf("Host = %q, want %q", got.Host, lambdaHost)
			}
			if got.RequestURI != rawPath+"?a=1" {
				t.Errorf("RequestURI = %q, want %q", got.RequestURI, rawPath+"?a=1")
			}
			if got.URL.Query().Get("a") != "1" {
				t.Errorf("query lost: %q", got.URL.RawQuery)
			}
		})
	}
}

// Payload format 2.0 joins repeated request headers with commas; the adapter
// forwards them intact. Repeated response headers are joined the same way.
func TestAdapter_MultiValueHeaders(t *testing.T) {
	var got *http.Request
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Add("Vary", "Cookie")
		w.Header().Add("Vary", "Accept-Encoding")
		_, _ = io.WriteString(w, "ok")
	})
	ev := baseEvent(http.MethodGet, "/machines")
	ev.Headers["accept"] = "text/html, application/xhtml+xml"
	ev.Headers["x-forwarded-for"] = "198.51.100.1, 203.0.113.9"
	resp, err := lambdaadapter.New(h)(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("handler not called")
	}
	if v := got.Header.Get("Accept"); v != "text/html, application/xhtml+xml" {
		t.Errorf("Accept = %q", v)
	}
	if v := got.Header.Values("X-Forwarded-For"); len(v) != 1 || v[0] != "198.51.100.1, 203.0.113.9" {
		t.Errorf("X-Forwarded-For = %q", v)
	}
	if v := resp.Headers["Vary"]; v != "Cookie, Accept-Encoding" {
		t.Errorf("response Vary = %q, want both values joined", v)
	}
}

// The cookies array is authoritative. A Cookie header, if an integration
// also supplies one, must not add or shadow cookies when the array is present,
// and is used when there is no array.
func TestAdapter_CookieHeaderVersusCookiesArray(t *testing.T) {
	const name = "__Host-rudolph_session"
	run := func(t *testing.T, ev events.APIGatewayV2HTTPRequest) *http.Request {
		t.Helper()
		var got *http.Request
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r })
		if _, err := lambdaadapter.New(h)(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatal("handler not called")
		}
		return got
	}

	t.Run("array and header", func(t *testing.T) {
		ev := baseEvent(http.MethodGet, "/machines")
		ev.Headers["cookie"] = name + "=from-header; injected=1"
		ev.Cookies = []string{name + "=from-array", "other=2"}
		r := run(t, ev)
		var sessions []string
		for _, c := range r.Cookies() {
			if c.Name == name {
				sessions = append(sessions, c.Value)
			}
			if c.Name == "injected" {
				t.Error("cookie from the Cookie header was merged with the cookies array")
			}
		}
		if len(sessions) != 1 || sessions[0] != "from-array" {
			t.Errorf("session cookies = %q, want exactly [from-array]", sessions)
		}
		if c, err := r.Cookie("other"); err != nil || c.Value != "2" {
			t.Errorf("other cookie = %v, %v", c, err)
		}
		if n := len(r.Header.Values("Cookie")); n != 1 {
			t.Errorf("%d Cookie headers, want 1", n)
		}
	})

	t.Run("header only", func(t *testing.T) {
		ev := baseEvent(http.MethodGet, "/machines")
		ev.Headers["cookie"] = name + "=from-header"
		r := run(t, ev)
		if c, err := r.Cookie(name); err != nil || c.Value != "from-header" {
			t.Errorf("session cookie = %v, %v; want from-header", c, err)
		}
	})
}

// An undecodable body is rejected before the handler runs, with a 400 that
// still carries baseline security headers.
func TestAdapter_InvalidBase64BodyIsBadRequest(t *testing.T) {
	called := false
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	ev := baseEvent(http.MethodPost, "/auth/logout")
	ev.Body = "!!! not base64 !!!"
	ev.IsBase64Encoded = true
	resp, err := lambdaadapter.New(h)(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("handler ran with an undecodable body")
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
	header := func(k string) string {
		for hk, v := range resp.Headers {
			if strings.EqualFold(hk, k) {
				return v
			}
		}
		return ""
	}
	if v := header("X-Content-Type-Options"); v != "nosniff" {
		t.Errorf("X-Content-Type-Options %q, want nosniff", v)
	}
	if v := header("Cache-Control"); !strings.Contains(v, "no-store") {
		t.Errorf("Cache-Control %q, want no-store", v)
	}
	if v := header("Content-Security-Policy"); !strings.Contains(v, "default-src 'none'") || !strings.Contains(v, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy %q, want default-src 'none' and frame-ancestors 'none'", v)
	}
	if v := header("Content-Type"); !strings.HasPrefix(v, "text/plain") {
		t.Errorf("Content-Type %q, want text/plain", v)
	}
}
