package store

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"crypto/hkdf"
)

// cursorInfo separates the cursor key from any other key derived from the
// same secret (the session cookie key).
const cursorInfo = "rudolph-web/cursor/v1"

const cursorMACLen = 16

// DeriveCursorKey derives the cursor signing key from an application secret,
// typically the session cookie key, with HKDF-SHA256 and a cursor-specific
// info string, so the two keys are independent.
func DeriveCursorKey(secret []byte) ([]byte, error) {
	if len(secret) < 16 {
		return nil, errors.New("store: cursor secret must be at least 16 bytes")
	}
	return hkdf.Key(sha256.New, secret, nil, cursorInfo, 32)
}

// RandomCursorKey returns a fresh key, for single-process use (tests, local
// development) where cursors need not survive a restart.
func RandomCursorKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("store: cannot read random bytes: " + err.Error())
	}
	return key
}

// CursorCodec encodes and authenticates pagination cursors.
//
// A cursor carries the position to resume from (for DynamoDB, the marshalled
// LastEvaluatedKey) together with a fingerprint of the query that produced it,
// and is authenticated with HMAC-SHA256. Cursors are opaque to clients: one
// cannot be minted by hand, edited to skip ahead or to point at another
// partition, or reused for a different query or filter.
type CursorCodec struct {
	key []byte
}

// NewCursorCodec returns a codec signing with key (at least 32 bytes).
func NewCursorCodec(key []byte) (*CursorCodec, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("store: cursor key must be at least 32 bytes, got %d", len(key))
	}
	return &CursorCodec{key: key}, nil
}

type cursorPayload struct {
	Query    string `json:"q"`
	Position string `json:"k"`
}

// Encode returns an opaque cursor resuming query at position.
func (c *CursorCodec) Encode(query, position string) (string, error) {
	payload, err := json.Marshal(cursorPayload{Query: query, Position: position})
	if err != nil {
		return "", err
	}
	mac := c.mac(payload)
	return base64.RawURLEncoding.EncodeToString(append(mac[:cursorMACLen], payload...)), nil
}

// Decode verifies cursor and returns the position to resume from. It returns
// ErrInvalidCursor for anything malformed, unauthenticated, or issued for a
// different query.
func (c *CursorCodec) Decode(query, cursor string) (string, error) {
	// Strict decoding rejects non-canonical trailing bits, so no edit of the
	// encoded string can leave the decoded bytes unchanged.
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil || len(raw) <= cursorMACLen {
		return "", ErrInvalidCursor
	}
	mac, payload := raw[:cursorMACLen], raw[cursorMACLen:]
	want := c.mac(payload)
	if !hmac.Equal(mac, want[:cursorMACLen]) {
		return "", ErrInvalidCursor
	}
	var p cursorPayload
	if err := json.Unmarshal(payload, &p); err != nil || p.Query != query {
		return "", ErrInvalidCursor
	}
	return p.Position, nil
}

func (c *CursorCodec) mac(payload []byte) []byte {
	h := hmac.New(sha256.New, c.key)
	h.Write([]byte(cursorInfo))
	h.Write([]byte{0})
	h.Write(payload)
	return h.Sum(nil)
}
