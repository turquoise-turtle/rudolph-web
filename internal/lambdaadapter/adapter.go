// Package lambdaadapter runs an http.Handler behind a Lambda Function URL or
// an API Gateway HTTP API, both of which use event payload format 2.0.
package lambdaadapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

// Handler is the Lambda handler signature for payload format 2.0.
type Handler func(context.Context, events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error)

// New adapts h. Function URL events decode into the same struct.
func New(h http.Handler) Handler {
	return func(ctx context.Context, ev events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
		req, err := toRequest(ctx, ev)
		if err != nil {
			return badRequest(), nil
		}
		w := &responseWriter{header: http.Header{}}
		h.ServeHTTP(w, req)
		return w.response(), nil
	}
}

func toRequest(ctx context.Context, ev events.APIGatewayV2HTTPRequest) (*http.Request, error) {
	body := []byte(ev.Body)
	if ev.IsBase64Encoded {
		b, err := base64.StdEncoding.DecodeString(ev.Body)
		if err != nil {
			return nil, fmt.Errorf("decode body: %w", err)
		}
		body = b
	}
	method := ev.RequestContext.HTTP.Method
	if method == "" {
		method = http.MethodGet
	}
	rawPath := ev.RawPath
	if rawPath == "" {
		rawPath = "/"
	}
	if !strings.HasPrefix(rawPath, "/") {
		return nil, fmt.Errorf("path %q is not absolute", rawPath)
	}
	// The path is never parsed as a URL reference: "//evil.example/x" is a
	// path, not a host. Parsing it under a placeholder authority keeps any
	// percent-encoding intact, and the authority is then dropped.
	u, err := url.Parse("http://lambda" + rawPath)
	if err != nil {
		return nil, fmt.Errorf("parse path: %w", err)
	}
	u.Scheme, u.Host, u.User, u.Fragment, u.OmitHost = "", "", nil, "", false
	u.RawQuery = ev.RawQueryString

	req, err := http.NewRequestWithContext(ctx, method, "/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.URL = u
	req.RequestURI = u.RequestURI()
	for k, v := range ev.Headers {
		req.Header.Set(k, v)
	}
	if len(ev.Cookies) > 0 {
		req.Header.Set("Cookie", strings.Join(ev.Cookies, "; "))
	}
	req.Host = req.Header.Get("Host")
	if req.Host == "" {
		req.Host = ev.RequestContext.DomainName
	}
	if ip := ev.RequestContext.HTTP.SourceIP; ip != "" {
		req.RemoteAddr = ip + ":0"
	}
	req.ContentLength = int64(len(body))
	return req, nil
}

// responseWriter buffers a response for conversion into a Lambda response.
type responseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *responseWriter) Header() http.Header { return w.header }

func (w *responseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.header.Get("Content-Type") == "" && w.body.Len() == 0 && len(b) > 0 {
		w.header.Set("Content-Type", http.DetectContentType(b))
	}
	return w.body.Write(b)
}

func (w *responseWriter) response() events.APIGatewayV2HTTPResponse {
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	resp := events.APIGatewayV2HTTPResponse{StatusCode: status, Headers: map[string]string{}}
	for k, vs := range w.header {
		if strings.EqualFold(k, "Set-Cookie") {
			resp.Cookies = append(resp.Cookies, vs...)
			continue
		}
		resp.Headers[k] = strings.Join(vs, ", ")
	}
	if isText(w.header.Get("Content-Type")) {
		resp.Body = w.body.String()
	} else {
		resp.Body = base64.StdEncoding.EncodeToString(w.body.Bytes())
		resp.IsBase64Encoded = true
	}
	return resp
}

func isText(contentType string) bool {
	ct := strings.ToLower(contentType)
	return ct == "" || strings.HasPrefix(ct, "text/") ||
		strings.Contains(ct, "json") || strings.Contains(ct, "xml") || strings.Contains(ct, "javascript")
}

// badRequest is returned when an event cannot be turned into a request. It
// carries the same baseline security headers as the application's responses,
// since it is produced before the handler runs.
func badRequest() events.APIGatewayV2HTTPResponse {
	return events.APIGatewayV2HTTPResponse{
		StatusCode: http.StatusBadRequest,
		Headers: map[string]string{
			"Content-Type":              "text/plain; charset=utf-8",
			"Content-Security-Policy":   "default-src 'none'; frame-ancestors 'none'; base-uri 'none'",
			"X-Content-Type-Options":    "nosniff",
			"X-Frame-Options":           "DENY",
			"Referrer-Policy":           "same-origin",
			"Strict-Transport-Security": "max-age=31536000",
			"Cache-Control":             "no-store",
		},
		Body: "bad request\n",
	}
}
