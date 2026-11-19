package store

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"

	"crypto/hkdf"
)

// cursorInfo separates the cursor key from any other key derived from the
// same secret (the session cookie key).
const cursorInfo = "rudolph-web/cursor/v1"

// cursorMACInfo separates cursors of this form from those of the first one
// (JSON holding the query and the position), which are refused like any other
// cursor that does not verify.
const cursorMACInfo = "rudolph-web/cursor-mac/v2"

const cursorMACLen = 16

// cursorForm follows the MAC in every cursor, so a later form can be told
// from this one before anything is parsed.
const cursorForm = 2

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

// Encode returns an opaque cursor resuming query at position.
//
// The cursor is the truncated MAC, the form's number and the position's own
// bytes, in base64url. The query is signed but not carried, and nothing is escaped, so
// a cursor is about a third longer than its position however odd the
// position's bytes are: part of a DynamoDB key comes from what a Mac uploaded,
// and a cursor that grew with it could outgrow a URL.
func (c *CursorCodec) Encode(query, position string) (string, error) {
	mac := c.mac(query, position)
	return base64.RawURLEncoding.EncodeToString(append(append(mac[:cursorMACLen], cursorForm), position...)), nil
}

// Decode verifies cursor and returns the position to resume from. It returns
// ErrInvalidCursor for anything malformed, unauthenticated, or issued for a
// different query.
func (c *CursorCodec) Decode(query, cursor string) (string, error) {
	// Strict decoding rejects non-canonical trailing bits, so no edit of the
	// encoded string can leave the decoded bytes unchanged.
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil || len(raw) <= cursorMACLen || raw[cursorMACLen] != cursorForm {
		return "", ErrInvalidCursor
	}
	mac, position := raw[:cursorMACLen], string(raw[cursorMACLen+1:])
	want := c.mac(query, position)
	if !hmac.Equal(mac, want[:cursorMACLen]) {
		return "", ErrInvalidCursor
	}
	return position, nil
}

// mac signs the query and the position. The query's length comes first, so
// no other split of the same bytes into a query and a position signs alike.
func (c *CursorCodec) mac(query, position string) []byte {
	h := hmac.New(sha256.New, c.key)
	h.Write([]byte(cursorMACInfo))
	h.Write([]byte{0})
	h.Write(binary.AppendUvarint(nil, uint64(len(query))))
	h.Write([]byte(query))
	h.Write([]byte(position))
	return h.Sum(nil)
}
