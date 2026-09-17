package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Cookie names. The __Host- prefix makes browsers insist on Secure, Path=/
// and no Domain, so a sibling subdomain cannot set or shadow them.
const (
	SessionCookieName = "__Host-rudolph_session"
	FlowCookieName    = "__Host-rudolph_oidc"
)

const aadPrefix = "rudolph-web/v1/"

var errBadCookie = errors.New("auth: invalid cookie")

// sealer encrypts and authenticates cookie payloads with AES-256-GCM. The
// cookie name is bound in as additional data so a value minted for one cookie
// cannot be replayed as another.
type sealer struct {
	aead cipher.AEAD
}

func newSealer(key []byte) (*sealer, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("auth: cookie key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead: aead}, nil
}

func (s *sealer) seal(name string, v any) (string, error) {
	plaintext, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize(), s.aead.NonceSize()+len(plaintext)+s.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, plaintext, []byte(aadPrefix+name))
	return base64.RawURLEncoding.EncodeToString(out), nil
}

func (s *sealer) open(name, value string, v any) error {
	// Strict decoding rejects non-canonical trailing bits, so every change to
	// the encoded string changes the ciphertext.
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(raw) < s.aead.NonceSize()+s.aead.Overhead() {
		return errBadCookie
	}
	nonce, ciphertext := raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():]
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, []byte(aadPrefix+name))
	if err != nil {
		return errBadCookie
	}
	if err := json.Unmarshal(plaintext, v); err != nil {
		return errBadCookie
	}
	return nil
}

func newCookie(name, value string, maxAge time.Duration) *http.Cookie {
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if maxAge > 0 {
		c.MaxAge = int(maxAge / time.Second)
		if c.MaxAge == 0 {
			c.MaxAge = 1
		}
	} else {
		c.MaxAge = -1
	}
	return c
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, newCookie(name, "", 0))
}
