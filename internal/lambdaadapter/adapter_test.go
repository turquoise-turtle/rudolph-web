package lambdaadapter_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/turquoise-turtle/rudolph-web/internal/lambdaadapter"
)

// A trimmed Lambda Function URL event (payload format 2.0, shared with
// API Gateway HTTP APIs).
const functionURLEvent = `{
  "version": "2.0",
  "routeKey": "$default",
  "rawPath": "/machines",
  "rawQueryString": "hostname=eng-&cursor=abc%3D%3D",
  "cookies": ["__Host-rudolph_session=sessval", "other=1"],
  "headers": {
    "host": "abc123.lambda-url.eu-west-1.on.aws",
    "user-agent": "curl/8",
    "x-forwarded-proto": "https",
    "sec-fetch-site": "same-origin"
  },
  "requestContext": {
    "domainName": "abc123.lambda-url.eu-west-1.on.aws",
    "http": {"method": "GET", "path": "/machines", "protocol": "HTTP/1.1", "sourceIp": "203.0.113.9", "userAgent": "curl/8"}
  },
  "isBase64Encoded": false
}`

func TestAdapter_TranslatesRequestAndResponse(t *testing.T) {
	var got *http.Request
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		http.SetCookie(w, &http.Cookie{Name: "a", Value: "1", Path: "/", Secure: true, HttpOnly: true})
		http.SetCookie(w, &http.Cookie{Name: "b", Value: "2", Path: "/"})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Add("Vary", "Cookie")
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "<p>hello &amp; bye</p>")
	})

	var ev events.APIGatewayV2HTTPRequest
	if err := json.Unmarshal([]byte(functionURLEvent), &ev); err != nil {
		t.Fatal(err)
	}
	resp, err := lambdaadapter.New(h)(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}

	// Request side.
	if got == nil {
		t.Fatal("handler not called")
	}
	if got.Method != http.MethodGet || got.URL.Path != "/machines" {
		t.Errorf("method/path = %s %s", got.Method, got.URL.Path)
	}
	if got.URL.Query().Get("hostname") != "eng-" || got.URL.Query().Get("cursor") != "abc==" {
		t.Errorf("query = %v", got.URL.Query())
	}
	if got.Host != "abc123.lambda-url.eu-west-1.on.aws" {
		t.Errorf("Host = %q", got.Host)
	}
	if c, err := got.Cookie("__Host-rudolph_session"); err != nil || c.Value != "sessval" {
		t.Errorf("session cookie not forwarded: %v %v", c, err)
	}
	if c, err := got.Cookie("other"); err != nil || c.Value != "1" {
		t.Errorf("second cookie not forwarded: %v %v", c, err)
	}
	if got.Header.Get("Sec-Fetch-Site") != "same-origin" {
		t.Error("headers not forwarded")
	}
	if !strings.HasPrefix(got.RemoteAddr, "203.0.113.9") {
		t.Errorf("RemoteAddr = %q", got.RemoteAddr)
	}

	// Response side.
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status %d", resp.StatusCode)
	}
	if resp.Body != "<p>hello &amp; bye</p>" || resp.IsBase64Encoded {
		t.Errorf("body %q base64=%v", resp.Body, resp.IsBase64Encoded)
	}
	if resp.Headers["Content-Type"] != "text/html; charset=utf-8" {
		t.Errorf("headers %v", resp.Headers)
	}
	// Multiple Set-Cookie headers must go in the cookies list; a comma-joined
	// Set-Cookie header would corrupt them.
	cookies := append([]string(nil), resp.Cookies...)
	sort.Strings(cookies)
	if len(cookies) != 2 || !strings.HasPrefix(cookies[0], "a=1") || !strings.HasPrefix(cookies[1], "b=2") ||
		!strings.Contains(cookies[0], "Secure") || !strings.Contains(cookies[0], "HttpOnly") {
		t.Errorf("cookies %q", resp.Cookies)
	}
	for k := range resp.Headers {
		if strings.EqualFold(k, "Set-Cookie") {
			t.Error("Set-Cookie must not be returned in Headers")
		}
	}
}

func TestAdapter_Base64Bodies(t *testing.T) {
	var gotBody string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', 0x00, 0xff})
	})
	ev := events.APIGatewayV2HTTPRequest{
		RawPath:         "/upload",
		Body:            base64.StdEncoding.EncodeToString([]byte("a=1&b=2")),
		IsBase64Encoded: true,
		Headers:         map[string]string{"content-type": "application/x-www-form-urlencoded"},
	}
	ev.RequestContext.HTTP.Method = http.MethodPost
	resp, err := lambdaadapter.New(h)(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if gotBody != "a=1&b=2" {
		t.Errorf("request body %q", gotBody)
	}
	if !resp.IsBase64Encoded {
		t.Fatal("binary response must be base64 encoded")
	}
	raw, err := base64.StdEncoding.DecodeString(resp.Body)
	if err != nil || string(raw) != "\x89PNG\x00\xff" {
		t.Errorf("decoded body %q err %v", raw, err)
	}
}

func TestAdapter_DefaultStatusIs200(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	ev := events.APIGatewayV2HTTPRequest{RawPath: "/healthz"}
	ev.RequestContext.HTTP.Method = http.MethodGet
	resp, err := lambdaadapter.New(h)(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Body != "ok" {
		t.Errorf("resp %+v", resp)
	}
}
