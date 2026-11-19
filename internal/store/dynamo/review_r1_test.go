package dynamo

// Review of the input handling fixes, round 1: the longest cursor a real list produces, and the position decoder's
// refusal of an attribute named twice, which the signature alone would let through.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// reviewPagerCursorBytes is the longest cursor the web pager keeps in a page link (maxCursorLen in internal/web).
const reviewPagerCursorBytes = 2048

// teamPageAPI answers the team ID list with one page ending at last, and notes where a later page starts.
type teamPageAPI struct {
	ReadAPI
	last    map[string]types.AttributeValue
	started map[string]types.AttributeValue
}

func (a *teamPageAPI) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	if in.ExclusiveStartKey != nil {
		a.started = in.ExclusiveStartKey
		return &dynamodb.QueryOutput{}, nil
	}
	return &dynamodb.QueryOutput{LastEvaluatedKey: a.last}, nil
}

func reviewStore(t *testing.T, api ReadAPI) (*Store, *store.CursorCodec) {
	t.Helper()
	codec, err := store.NewCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Client: api, Table: "rudolph", EventsTable: "events", Cursors: codec})
	if err != nil {
		t.Fatal(err)
	}
	return s, codec
}

// The largest key of a real list is the team ID list's: a 256-byte team ID (the longest filter) beside an event's
// sort key filled to DynamoDB's 1024 bytes with a file_sha256 a Mac chose, here control characters, which JSON
// would write six bytes each. Its cursor must fit what the pager keeps, or past such a row the earlier pages lose
// their links.
func TestEventCursor_ReviewR1_TheLargestRealKeyFitsThePager(t *testing.T) {
	teamID := strings.Repeat("T", 256)
	sk := "00001794000000000000#" + strings.Repeat("\x01", 1024-len("00001794000000000000#")-len("#2147483647")) + "#2147483647"
	last := map[string]types.AttributeValue{
		"PK":     &types.AttributeValueMemberS{Value: "Exec#AAAAAAAA-1111-4111-8111-000000000001"},
		"SK":     &types.AttributeValueMemberS{Value: sk},
		"TeamID": &types.AttributeValueMemberS{Value: teamID},
		"TS":     &types.AttributeValueMemberS{Value: "00001794000000000000"},
	}
	api := &teamPageAPI{last: last}
	s, _ := reviewStore(t, api)
	page, err := s.ListExecutionEvents(context.Background(), store.EventFilter{TeamID: teamID}, store.PageRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor == "" {
		t.Fatal("no cursor past a page with a LastEvaluatedKey")
	}
	if len(page.NextCursor) > reviewPagerCursorBytes {
		t.Errorf("the cursor past a %d-byte sort key in the team ID list is %d bytes; the pager keeps at most %d", len(sk), len(page.NextCursor), reviewPagerCursorBytes)
	}
	t.Logf("the largest real cursor is %d bytes", len(page.NextCursor))
	// And it resumes the list exactly where it stopped.
	if _, err := s.ListExecutionEvents(context.Background(), store.EventFilter{TeamID: teamID}, store.PageRequest{Limit: 50, Cursor: page.NextCursor}); err != nil {
		t.Fatalf("resuming: %v", err)
	}
	for name, v := range last {
		if got, _ := api.started[name].(*types.AttributeValueMemberS); got == nil || got.Value != v.(*types.AttributeValueMemberS).Value {
			t.Errorf("resumed with %s of %d bytes, want the %d bytes the page stopped at", name, len(keyString(api.started[name])), len(keyString(v)))
		}
	}
}

// A position is each name and value behind its length. One that names an attribute twice parses otherwise, so only
// the duplicate check stops the second value silently replacing the first; the event decoder's count of names would
// not notice either, since the map then holds as many names as a real key.
func TestDecodePosition_ReviewR1_AnAttributeNamedTwiceIsRefused(t *testing.T) {
	s, codec := reviewStore(t, &teamPageAPI{})
	const fingerprint = "review"
	decode := func(position string) (map[string]string, error) {
		cursor, err := codec.Encode(fingerprint, position)
		if err != nil {
			t.Fatal(err)
		}
		return s.decodePosition(fingerprint, cursor)
	}
	// The same position without the repeat decodes, so the refusal below is the duplicate's.
	got, err := decode("\x02PK\x01a\x02SK\x01b")
	if err != nil || len(got) != 2 || got["PK"] != "a" || got["SK"] != "b" {
		t.Fatalf("a well-formed position: %v, %v; want PK=a SK=b", got, err)
	}
	for name, position := range map[string]string{
		"adjacent":           "\x02PK\x01a\x02PK\x01b",
		"apart":              "\x02PK\x01a\x02SK\x01b\x02PK\x01c",
		"same value":         "\x02PK\x01a\x02PK\x01a",
		"empty name, twice":  "\x00\x01a\x00\x01b",
		"after a valid pair": "\x02SK\x01b\x02PK\x01a\x02PK\x01a",
	} {
		if got, err := decode(position); !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("%s: %v, %v; want ErrInvalidCursor", name, got, err)
		}
	}
}
