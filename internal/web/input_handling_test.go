package web_test

// Input handling review, 2026-11-18. Any Mac of the fleet can put strings of its choosing into the events table
// (Rudolph cuts event strings to 4096 bytes and index keys to 1024, and stores file_sha256 as sent, up to the 1024
// bytes of a sort key), and a link can carry any query string or path to a signed-in person's browser. The tests
// here that read "DynamoDB" run the real reader (internal/store/dynamo) over a stand-in for the table, since the
// in-memory store has cursors and limits of its own.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/lambdaadapter"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/fakeidp"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

const (
	// maxURLBytes is the longest URL CloudFront passes on (its "maximum length of a URL" quota); the deployment
	// may sit behind it, and other front ends allow about as much.
	maxURLBytes = 8192
	// maxLambdaResponseBytes is the largest response a Lambda may return; a longer one reaches the browser as a 502.
	maxLambdaResponseBytes = 6 << 20
	// maxQueryBytes is how much data one DynamoDB Query returns before it stops, whatever its Limit.
	maxQueryBytes = 1 << 20
	// maxSortKeyBytes is DynamoDB's limit for a sort key.
	maxSortKeyBytes = 1024

	eventTS = "00001794000000000000"
)

// newReaderApp is newApp over any reader, such as the DynamoDB one.
func newReaderApp(t *testing.T, r store.Reader, pageSize int) *app {
	t.Helper()
	idp := fakeidp.New(t, tenantID, clientID, "secret")
	a, err := auth.New(auth.Config{
		Issuer: idp.Issuer, TenantID: tenantID, ClientID: clientID, ClientSecret: "secret",
		RedirectURL: origin + "/auth/callback", CookieKey: []byte("0123456789abcdef0123456789abcdef"),
		SessionTTL: time.Hour, Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	h, err := web.New(web.Options{Auth: a, Store: r, PageSize: pageSize, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	return &app{h: h, auth: a, idp: idp}
}

// tableAPI stands in for DynamoDB: Query answers the fleet's execution events, newest first, as the Fleet_TS index
// does, and nothing for any other list.
type tableAPI struct {
	dynamo.ReadAPI
	events []map[string]types.AttributeValue // newest first
}

func attr(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

func attrValue(v types.AttributeValue) string {
	s, _ := v.(*types.AttributeValueMemberS)
	if s == nil {
		return ""
	}
	return s.Value
}

func (a *tableAPI) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	if in.IndexName == nil || *in.IndexName != "Fleet_TS" {
		return &dynamodb.QueryOutput{}, nil
	}
	first := 0
	if in.ExclusiveStartKey != nil {
		first = len(a.events)
		for i, e := range a.events {
			if attrValue(e["PK"]) == attrValue(in.ExclusiveStartKey["PK"]) && attrValue(e["SK"]) == attrValue(in.ExclusiveStartKey["SK"]) {
				first = i + 1
			}
		}
	}
	out := &dynamodb.QueryOutput{}
	last := min(len(a.events), first+int(*in.Limit))
	out.Items = a.events[first:last]
	if last < len(a.events) {
		e := a.events[last-1]
		out.LastEvaluatedKey = map[string]types.AttributeValue{"PK": e["PK"], "SK": e["SK"], "Fleet": e["Fleet"], "TS": e["TS"]}
	}
	return out, nil
}

// longestSHA is a file_sha256 that fills an event's sort key ("<TS>#<SHA-256>#<pid>") to DynamoDB's limit.
func longestSHA(char string) string {
	return strings.Repeat(char, maxSortKeyBytes-len(eventTS)-len("#")-len("#2147483647"))
}

// eventRow is execution event number n of a Mac, as Rudolph stores it, with the given file_sha256.
func eventRow(n int, sha string) map[string]types.AttributeValue {
	machine := fmt.Sprintf("AAAAAAAA-1111-4111-8111-%012d", n)
	return map[string]types.AttributeValue{
		"PK": attr("Exec#" + machine), "SK": attr(eventTS + "#" + sha + "#2147483647"), "Fleet": attr("Exec"), "TS": attr(eventTS),
		"MachineID": attr(machine), "Decision": attr("BLOCK_UNKNOWN"), "FileSHA256": attr(sha),
		"FileName": attr(fmt.Sprintf("event-number-%d", n)), "FilePath": attr("/tmp/x"),
	}
}

func newTableApp(t *testing.T, api *tableAPI, pageSize int) *app {
	t.Helper()
	codec, err := store.NewCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	s, err := dynamo.New(dynamo.Options{Client: api, Table: "rudolph", EventsTable: "events", Cursors: codec})
	if err != nil {
		t.Fatal(err)
	}
	return newReaderApp(t, s, pageSize)
}

// A Mac that uploads events whose file_sha256 fills the sort key with characters JSON escapes makes the cursor past
// each of them about 9.5 KB long. The Next link of a page ending on such a row is then longer than a URL may be, so
// every older event of the list is out of reach until the rows expire, 90 days later: a Mac can hide what the fleet
// ran before by uploading a page's worth of them.
func TestEvents_NextLinkPastTheLongestKeyFitsAURL(t *testing.T) {
	for name, char := range map[string]string{"letters": "a", "angle brackets": "<", "control characters": "\x01", "quotes": `"`} {
		t.Run(name, func(t *testing.T) {
			api := &tableAPI{events: []map[string]types.AttributeValue{eventRow(1, longestSHA(char)), eventRow(2, strings.Repeat("ab", 32))}}
			a := newTableApp(t, api, 1)
			code, body := a.viewerGet(t, "/events")
			if code != http.StatusOK {
				t.Fatalf("status %d", code)
			}
			mustContain(t, body, "event-number-1")
			next := nextLink(t, body)
			if next == "" {
				t.Fatal("no Next link on a page with an older event after it")
			}
			if len(next) > maxURLBytes {
				t.Fatalf("the Next link past a row with a %d-byte sort key is %d bytes long; a URL may be at most %d, so the older events cannot be reached",
					maxSortKeyBytes, len(next), maxURLBytes)
			}
			code, body = a.viewerGet(t, next)
			if code != http.StatusOK {
				t.Fatalf("page 2: status %d", code)
			}
			mustContain(t, body, "event-number-2")
		})
	}
}

// The pager keeps the cursors of up to four earlier pages in each link, and leaves out one longer than it accepts
// (1024 bytes), "store cursors are far shorter". An event cursor whose row has the longest sort key is over 1600
// bytes however it is encoded, so past such rows the pager loses Prev and the numbered links to the pages before.
func TestEvents_EarlierPagesStayLinkedPastTheLongestKey(t *testing.T) {
	sha := longestSHA("a")
	api := &tableAPI{events: []map[string]types.AttributeValue{eventRow(1, sha), eventRow(2, sha), eventRow(3, sha), eventRow(4, sha)}}
	a := newTableApp(t, api, 1)
	_, body := a.viewerGet(t, "/events")
	page2 := nextLink(t, body)
	code, body := a.viewerGet(t, page2)
	if code != http.StatusOK {
		t.Fatalf("page 2: status %d", code)
	}
	code, body = a.viewerGet(t, nextLink(t, body))
	if code != http.StatusOK {
		t.Fatalf("page 3: status %d", code)
	}
	mustContain(t, body, "event-number-3")
	p := readPager(t, body)
	if p.current != "3" {
		t.Fatalf("on page %q, want 3", p.current)
	}
	if p.prev == "" || p.numbers["2"] == "" {
		t.Fatalf("page 3 has a Prev link of %d bytes and a link to page 2 of %d bytes (0: none), gap %v; want both links", len(p.prev), len(p.numbers["2"]), p.gap)
	}
	if p.prev != page2 {
		t.Errorf("Prev on page 3 is %.60q…, want the link page 1 gave to page 2", p.prev)
	}
	if len(p.next) > maxURLBytes {
		t.Errorf("the Next link on page 3 is %d bytes long; a URL may be at most %d", len(p.next), maxURLBytes)
	}
	code, body = a.viewerGet(t, p.prev)
	if code != http.StatusOK {
		t.Fatalf("Prev from page 3: status %d", code)
	}
	mustContain(t, body, "event-number-2")
}

// The try form on /rules/cel bounds the expression (16 KiB) and the number of arguments and environment variables
// (256), but not how long a value of the sample execution is. cel-go charges for a regex match only after it has
// run, so the cost limit does not help: matching a 15 KB pattern against a 60 KB path took 4.9 s on the review
// machine, and it grows with the path. Front ends that cap a URL near 8 KB keep this to a tenth of a second; the
// handler itself accepts whatever reaches it. No real execution has a megabyte in one field.
func TestCELTry_RefusesOversizedSampleValues(t *testing.T) {
	a := newApp(t, fake.New(), 10)
	big := strings.Repeat("a", 1<<20)
	for name, q := range map[string]url.Values{
		"path":                 {"path": {big}},
		"working directory":    {"cwd": {big}},
		"argument":             {"args": {"--flag\n" + big}},
		"environment variable": {"envs": {"HOME=/Users/x\nBIG=" + big}},
		"signing ID":           {"signing_id": {big}},
		"team ID":              {"team_id": {big}},
	} {
		t.Run(name, func(t *testing.T) {
			q.Set("expr", "true")
			code, _ := a.viewerGet(t, "/rules/cel?"+q.Encode())
			if code != http.StatusBadRequest {
				t.Errorf("a sample execution with a 1 MiB %s: status %d, want 400", name, code)
			}
		})
	}
	// A sample execution of ordinary size is still tried.
	code, body := a.viewerGet(t, "/rules/cel?"+url.Values{"expr": {"path.startsWith('/Applications/')"}, "path": {"/Applications/" + strings.Repeat("a", 1000)}}.Encode())
	if code != http.StatusOK {
		t.Fatalf("an ordinary sample: status %d", code)
	}
	mustContain(t, body, "ALLOWLIST")
}

// ---------------------------------------------------------------------------
// Bounds that hold today and had no test.

// postBody posts a body as it is, from this site, as an admin.
func (a *app) postBody(t *testing.T, target string, body []byte, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, origin+target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", origin)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	req.AddCookie(a.cookie(t, auth.RoleViewer, auth.RoleAdmin))
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

// Form bodies are read through http.MaxBytesReader (64 KiB) and never decompressed, so a large or compressed body
// costs at most that much, and a body that is not a readable form is a 400 that writes nothing. Pinned because
// nothing else fails if the limit is dropped from parseForm, or if a later handler reads r.Body itself.
func TestForms_OversizedCompressedAndUnreadableBodiesWriteNothing(t *testing.T) {
	valid := globalRule().Encode()
	var zipped, bomb bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	_, _ = zw.Write([]byte(valid))
	_ = zw.Close()
	zw = gzip.NewWriter(&bomb)
	_, _ = zw.Write(bytes.Repeat([]byte("a=b&"), 4<<20))
	_ = zw.Close()

	for name, tc := range map[string]struct {
		body   []byte
		header map[string]string
	}{
		"over the limit":      {body: []byte(valid + "&description=" + strings.Repeat("d", 64<<10))},
		"padding after it":    {body: []byte(valid + "&" + strings.Repeat("x=1&", 20<<10))},
		"gzip":                {body: zipped.Bytes(), header: map[string]string{"Content-Encoding": "gzip"}},
		"gzip bomb":           {body: bomb.Bytes(), header: map[string]string{"Content-Encoding": "gzip"}},
		"deflate":             {body: zipped.Bytes(), header: map[string]string{"Content-Encoding": "deflate"}},
		"bad percent escape":  {body: []byte(valid + "&custom_message=%zz")},
		"JSON":                {body: []byte(`{"target":"global","type":"BINARY","identifier":"` + editSHA + `","policy":"BLOCKLIST"}`), header: map[string]string{"Content-Type": "application/json"}},
		"multipart":           {body: []byte("--b\r\nContent-Disposition: form-data; name=\"target\"\r\n\r\nglobal\r\n--b--\r\n"), header: map[string]string{"Content-Type": "multipart/form-data; boundary=b"}},
		"no content type":     {body: []byte(valid), header: map[string]string{"Content-Type": ""}},
		"semicolon separator": {body: []byte(strings.ReplaceAll(valid, "&", ";"))},
	} {
		t.Run(name, func(t *testing.T) {
			s := fake.New()
			a := newEditApp(t, s, true)
			rec := a.postBody(t, "/admin/rules", tc.body, tc.header)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400", rec.Code)
			}
			if got := globalRules(t, s); len(got) != 0 {
				t.Errorf("a rule was added: %+v", got)
			}
			if got := auditOf(t, s); len(got) != 0 {
				t.Errorf("the audit trail has %d entries, want none", len(got))
			}
		})
	}
	// The same form, whole and within the limit, is saved.
	s := fake.New()
	a := newEditApp(t, s, true)
	if rec := a.postBody(t, "/admin/rules", []byte(valid), nil); rec.Code != http.StatusSeeOther || len(globalRules(t, s)) != 1 {
		t.Fatalf("a valid form: status %d, %d rules; want 303 and 1", rec.Code, len(globalRules(t, s)))
	}
}

// A page of events is one DynamoDB Query, which returns at most 1 MB however long the rows' strings are, and the
// page prints each stored string once. Escaping multiplies a byte by at most five (an apostrophe becomes &#39;, a
// control character \x01 and then \\x01 in the Lambda's JSON), so the page stays under the 6 MB a Lambda may
// return, with about 1 MB to spare. Pinned because that room goes if a template prints a field a second time (in
// a title attribute, say) or the page size or Rudolph's cuts grow, and the page would then fail for everyone with a
// 502 until the rows expire.
func TestEvents_APageOfTheLongestValuesFitsALambdaResponse(t *testing.T) {
	for name, char := range map[string]string{"apostrophes": "'", "control characters": "\x01", "letters": "a"} {
		t.Run(name, func(t *testing.T) {
			s := fake.New()
			long := func(n int) string { return strings.Repeat(char, n) }
			stored := 0
			for i := 0; stored < maxQueryBytes; i++ {
				e := store.ExecutionEvent{
					MachineID: fmt.Sprintf("AAAAAAAA-1111-4111-8111-%012d", i), ExecutionTime: fixedNow.Add(-time.Duration(i) * time.Second),
					FileSHA256: longestSHA(char), FilePath: long(4096), FileName: long(4096), ExecutingUser: long(4096),
					SigningID: long(4096), CDHash: long(4096), TeamID: long(1024), Decision: store.Decision(long(1024)),
				}
				stored += len(e.MachineID) + len(e.FileSHA256) + len(e.FilePath) + len(e.FileName) + len(e.ExecutingUser) +
					len(e.SigningID) + len(e.CDHash) + len(e.TeamID) + len(e.Decision)
				s.AddExecutionEvent(e)
			}
			a := newApp(t, s, web.DefaultPageSize)
			c := a.cookie(t, auth.RoleViewer)
			req := events.APIGatewayV2HTTPRequest{RawPath: "/events", Cookies: []string{c.Name + "=" + c.Value}, Headers: map[string]string{"host": "rudolph-web.example.com"}}
			req.RequestContext.HTTP.Method = http.MethodGet
			resp, err := lambdaadapter.New(a.h)(context.Background(), req)
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d, err %v", resp.StatusCode, err)
			}
			if got := rows(resp.Body); got < 40 {
				t.Fatalf("the page shows %d events, want the 40 or more that fill a 1 MB Query", got)
			}
			// As aws-lambda-go writes the response: JSON without HTML escaping.
			var out bytes.Buffer
			enc := json.NewEncoder(&out)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(resp); err != nil {
				t.Fatal(err)
			}
			if out.Len() > maxLambdaResponseBytes {
				t.Errorf("%d bytes of events make a response of %d bytes; a Lambda returns at most %d", stored, out.Len(), maxLambdaResponseBytes)
			}
			t.Logf("%d bytes of events: response %d bytes", stored, out.Len())
		})
	}
}
