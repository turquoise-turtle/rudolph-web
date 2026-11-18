package dynamo_test

// Input handling review, 2026-11-18: an event's sort key is "<TS>#<file SHA-256>#<pid>", and Rudolph stores the
// file_sha256 a Mac uploads as it came, up to DynamoDB's 1024 bytes for a sort key. The cursor to the next page is
// the last row's key, so the Mac chooses most of its bytes. The key is written as JSON inside JSON, which turns each
// "<", ">", "&" or control character into seven bytes before base64 adds a third: a 1 KB key becomes a 9.5 KB cursor,
// longer than the 8192 bytes CloudFront allows a whole URL, and nobody can page past that row until it expires 90
// days later.

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
)

// lastKeyAPI answers every Query with no rows and the given LastEvaluatedKey, as DynamoDB does for a page whose
// rows the reader then skips.
type lastKeyAPI struct {
	dynamo.ReadAPI
	key map[string]types.AttributeValue
}

func (a lastKeyAPI) Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{LastEvaluatedKey: a.key}, nil
}

func TestEventCursor_DoesNotMultiplyTheKeyItCarries(t *testing.T) {
	codec, err := store.NewCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	const ts = "00001794000000000000"
	// 20 digits, "#", the SHA-256, "#" and the largest pid fill the 1024 bytes of a sort key.
	shaBytes := 1024 - len(ts) - len("#") - len("#2147483647")
	for name, sha := range map[string]string{
		"a real SHA-256":     strings.Repeat("ab", 32),
		"letters":            strings.Repeat("a", shaBytes),
		"angle brackets":     strings.Repeat("<", shaBytes),
		"control characters": strings.Repeat("\x01", shaBytes),
		"quotes":             strings.Repeat(`"`, shaBytes),
		"backslashes":        strings.Repeat(`\`, shaBytes),
	} {
		t.Run(name, func(t *testing.T) {
			key := map[string]types.AttributeValue{
				"PK":    str("Exec#" + machineA),
				"SK":    str(ts + "#" + sha + "#2147483647"),
				"Fleet": str("Exec"),
				"TS":    str(ts),
			}
			keyBytes := 0
			for attr, v := range key {
				keyBytes += len(attr) + len(v.(*types.AttributeValueMemberS).Value)
			}
			s, err := dynamo.New(dynamo.Options{Client: lastKeyAPI{key: key}, Table: "rudolph", EventsTable: "events", Cursors: codec})
			if err != nil {
				t.Fatal(err)
			}
			page, err := s.ListExecutionEvents(context.Background(), store.EventFilter{}, store.PageRequest{Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			if page.NextCursor == "" {
				t.Fatal("no cursor to the next page")
			}
			// Base64 and the signature cost a third and a few dozen bytes; twice the key leaves room for both and
			// keeps a link with the longest key well inside a URL.
			if limit := 2*keyBytes + 256; len(page.NextCursor) > limit {
				t.Errorf("a %d-byte key makes a cursor of %d bytes, want at most %d", keyBytes, len(page.NextCursor), limit)
			}
			// Whatever its form, the cursor still resumes at exactly this key.
			var started map[string]types.AttributeValue
			resume, err := dynamo.New(dynamo.Options{Client: startKeyAPI{started: &started}, Table: "rudolph", EventsTable: "events", Cursors: codec})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := resume.ListExecutionEvents(context.Background(), store.EventFilter{}, store.PageRequest{Limit: 50, Cursor: page.NextCursor}); err != nil {
				t.Fatalf("resuming at the cursor: %v", err)
			}
			for attr, v := range key {
				got, _ := started[attr].(*types.AttributeValueMemberS)
				if got == nil || got.Value != v.(*types.AttributeValueMemberS).Value {
					t.Errorf("resumed without the key's own %s", attr)
				}
			}
		})
	}
}

// startKeyAPI records the ExclusiveStartKey of the Query it is asked for.
type startKeyAPI struct {
	dynamo.ReadAPI
	started *map[string]types.AttributeValue
}

func (a startKeyAPI) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	*a.started = in.ExclusiveStartKey
	return &dynamodb.QueryOutput{}, nil
}
