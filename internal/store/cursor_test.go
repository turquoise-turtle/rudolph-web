package store_test

// Direct tests of the cursor codec (Fable review, 2026-11-07: internal/store had no unit tests). Every list page link
// carries one of these cursors, so they must be unforgeable and bound to the query that issued them.

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

func codec(t *testing.T, key []byte) *store.CursorCodec {
	t.Helper()
	c, err := store.NewCursorCodec(key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCursor_RoundTrip(t *testing.T) {
	c := codec(t, store.RandomCursorKey())
	for _, pos := range []string{"", "x", `{"PK":{"S":"Machine#A"},"SK":{"S":"Current"}}`, strings.Repeat("é", 300)} {
		cur, err := c.Encode(`["ListMachines","serial",""]`, pos)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(cur, "+/=") {
			t.Errorf("cursor %q is not URL-safe unpadded base64", cur)
		}
		got, err := c.Decode(`["ListMachines","serial",""]`, cur)
		if err != nil || got != pos {
			t.Errorf("round trip of %q: %q, %v", pos, got, err)
		}
	}
}

func TestCursor_BoundToTheQuery(t *testing.T) {
	c := codec(t, store.RandomCursorKey())
	cur, _ := c.Encode(`["ListAuditPage"]`, "p")
	if _, err := c.Decode(`["ListGlobalRules","",""]`, cur); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("a cursor decoded for another query: %v", err)
	}
}

func TestCursor_BoundToTheKey(t *testing.T) {
	cur, _ := codec(t, store.RandomCursorKey()).Encode("q", "p")
	if _, err := codec(t, store.RandomCursorKey()).Decode("q", cur); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("a cursor decoded under another key: %v", err)
	}
}

func TestCursor_EveryEditIsRejected(t *testing.T) {
	c := codec(t, store.RandomCursorKey())
	cur, _ := c.Encode("q", `{"PK":{"S":"GlobalRules"}}`)
	raw, err := base64.RawURLEncoding.DecodeString(cur)
	if err != nil {
		t.Fatal(err)
	}
	// Flip every bit of the decoded cursor, one at a time.
	for i := range raw {
		for bit := 0; bit < 8; bit++ {
			edited := append([]byte(nil), raw...)
			edited[i] ^= 1 << bit
			if _, err := c.Decode("q", base64.RawURLEncoding.EncodeToString(edited)); !errors.Is(err, store.ErrInvalidCursor) {
				t.Fatalf("byte %d bit %d flipped: %v, want ErrInvalidCursor", i, bit, err)
			}
		}
	}
	// Standard base64 differs from URL-safe base64 only when the bytes encode to '+' or '/'.
	if std := base64.RawStdEncoding.EncodeToString(raw); std != cur {
		if _, err := c.Decode("q", std); !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("standard base64: %v, want ErrInvalidCursor", err)
		}
	}
	// Non-canonical encodings of the same bytes, truncation, garbage.
	for name, bad := range map[string]string{
		"padded":      base64.URLEncoding.EncodeToString(raw),
		"truncated":   cur[:len(cur)-1],
		"extended":    cur + "A",
		"empty":       "",
		"garbage":     "garbage",
		"mac only":    base64.RawURLEncoding.EncodeToString(raw[:16]),
		"with spaces": " " + cur,
	} {
		if _, err := c.Decode("q", bad); !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("%s: %v, want ErrInvalidCursor", name, err)
		}
	}
}

func TestCursorKeys(t *testing.T) {
	if _, err := store.NewCursorCodec(make([]byte, 31)); err == nil {
		t.Error("a 31-byte key accepted")
	}
	if _, err := store.DeriveCursorKey(make([]byte, 15)); err == nil {
		t.Error("a 15-byte secret accepted")
	}
	a1, err := store.DeriveCursorKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := store.DeriveCursorKey([]byte("0123456789abcdef0123456789abcdef"))
	b, _ := store.DeriveCursorKey([]byte("fedcba9876543210fedcba9876543210"))
	if string(a1) != string(a2) || string(a1) == string(b) || len(a1) != 32 {
		t.Error("DeriveCursorKey is not a deterministic 32-byte key per secret")
	}
	if string(a1) == "0123456789abcdef0123456789abcdef" {
		t.Error("the cursor key equals the cookie key")
	}
	k1, k2 := store.RandomCursorKey(), store.RandomCursorKey()
	if len(k1) != 32 || string(k1) == string(k2) {
		t.Error("RandomCursorKey does not return fresh 32-byte keys")
	}
}
