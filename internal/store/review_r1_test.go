package store_test

// Review of the input handling fixes, round 1. A cursor carries its position's own bytes and only a MAC of the
// query, so the MAC alone binds a cursor to its query and to where the query ends and the position begins. Nothing
// else fails if the query bytes or the length before them are left out of it.

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

func reviewCodec(t *testing.T) *store.CursorCodec {
	t.Helper()
	c, err := store.NewCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Two queries of the same length differ only in their bytes, so a MAC that covered the query's length but not the
// query itself would let a cursor of one page the other (another Mac's events, another team's).
func TestCursor_ReviewR1_RefusedUnderAnotherQueryOfTheSameLength(t *testing.T) {
	c := reviewCodec(t)
	cursor, err := c.Encode("q1", "position")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.Decode("q1", cursor); err != nil || got != "position" {
		t.Fatalf("Decode under its own query: %q, %v", got, err)
	}
	if got, err := c.Decode("q2", cursor); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("Decode under another query of the same length: %q, %v; want ErrInvalidCursor", got, err)
	}
}

// Query "ab" with position "c" and query "a" with position "bc" are the same bytes run together. Only the query's
// length in the MAC tells them apart, so a cursor carrying "bc" behind the MAC signed for ("ab", "c") must not
// verify under "a".
func TestCursor_ReviewR1_QueryLengthIsFramedInTheMAC(t *testing.T) {
	c := reviewCodec(t)
	cursor, err := c.Encode("ab", "c")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "c") {
		t.Fatalf("the cursor's bytes %q do not end with its position; a cursor carries the MAC, its form and the position", raw)
	}
	head := raw[:len(raw)-1]
	rebuild := func(position string) string {
		return base64.RawURLEncoding.EncodeToString(append(append([]byte{}, head...), position...))
	}
	// The rebuilt cursor is the original when the position is unchanged, so the splice itself is sound.
	if got, err := c.Decode("ab", rebuild("c")); err != nil || got != "c" {
		t.Fatalf("the cursor rebuilt unchanged: %q, %v; want c", got, err)
	}
	if got, err := c.Decode("a", rebuild("bc")); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("a cursor signed for query ab at c, carrying bc, decoded under query a: %q, %v; want ErrInvalidCursor", got, err)
	}
}
