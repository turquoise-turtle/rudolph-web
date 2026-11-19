package dynamo_test

// Input handling review, 2026-11-18: an event's sort key is "<TS>#<file SHA-256>#<pid>", and Rudolph stores the
// file_sha256 a Mac uploads as it came, up to DynamoDB's 1024 bytes for a sort key. The cursor to the next page is
// the last row's key, so the Mac chooses most of its bytes. The key is written as JSON inside JSON, which turns each
// "<", ">", "&" or control character into seven bytes before base64 adds a third: a 1 KB key becomes a 9.5 KB cursor,
// longer than the 8192 bytes CloudFront allows a whole URL, and nobody can page past that row until it expires 90
// days later.

import (
	"context"
	"errors"
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

// noQueryAPI fails the test when the reader asks DynamoDB for anything.
type noQueryAPI struct {
	dynamo.ReadAPI
	t *testing.T
}

func (a noQueryAPI) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	a.t.Errorf("Query with %d-byte partition key", len(in.ExpressionAttributeValues[":pk"].(*types.AttributeValueMemberS).Value))
	return &dynamodb.QueryOutput{}, nil
}

// Only a canonical UUID names a Mac, so any other ID from an address is not found without a read: DynamoDB allows
// a partition key 2048 bytes and refuses a longer one, which the page would report as a failure of the data store.
func TestGetMachine_NonUUIDIsNotFoundWithoutARead(t *testing.T) {
	codec, err := store.NewCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	s, err := dynamo.New(dynamo.Options{Client: noQueryAPI{t: t}, Table: "rudolph", Cursors: codec})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range append([]string{"", strings.Repeat("A", 3000)}, badMachineIDs...) {
		if _, err := s.GetMachine(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetMachine(%.40q): %v, want not found", id, err)
		}
	}
}

// A position signed for a list whose cursors are not DynamoDB keys (the audit trail's is Rudolph's own) never
// reaches a key decoder with a matching fingerprint; one that did, or a truncated one, is refused, not a panic.
func TestEventCursor_MalformedPositionsAreRefused(t *testing.T) {
	codec, err := store.NewCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	s, err := dynamo.New(dynamo.Options{Client: lastKeyAPI{}, Table: "rudolph", EventsTable: "events", Cursors: codec})
	if err != nil {
		t.Fatal(err)
	}
	const fleet = `["ListExecutionEvents","Fleet_TS","Fleet","Exec"]`
	// The fingerprint is the fleet list's own: a well-formed key signed with it resumes the list.
	good := "\x05Fleet\x04Exec\x02PK\x29Exec#" + machineA + "\x02SK\x01a\x02TS\x01b"
	cursor, err := codec.Encode(fleet, good)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListExecutionEvents(context.Background(), store.EventFilter{}, store.PageRequest{Limit: 50, Cursor: cursor}); err != nil {
		t.Fatalf("a well-formed position: %v", err)
	}
	for name, position := range map[string]string{
		"text":               "2026-11-18#0001",
		"length past end":    "\x02PK\xff\xff\x03",
		"name without value": "\x02PK",
		"a name twice":       "\x02PK\x01a\x02PK\x01b",
		"huge length":        "\xff\xff\xff\xff\xff\xff\xff\xff\xff\x01",
		"unending length":    "\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff",
	} {
		cursor, err := codec.Encode(fleet, position)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.ListExecutionEvents(context.Background(), store.EventFilter{}, store.PageRequest{Limit: 50, Cursor: cursor})
		if !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("%s: %v, want an invalid cursor", name, err)
		}
	}
}
