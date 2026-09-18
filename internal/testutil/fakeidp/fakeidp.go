// Package fakeidp is a test-only OpenID Connect provider that imitates the
// parts of Microsoft Entra ID's v2.0 endpoints that rudolph-web uses:
// discovery, JWKS, the token endpoint (authorization code + PKCE) and the
// end-session endpoint. It signs ID tokens with an RSA test key.
//
// It must only be imported from tests and local tooling, never from
// production code paths.
package fakeidp

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TokenRequest is what the token endpoint received.
type TokenRequest struct {
	GrantType    string
	Code         string
	CodeVerifier string
	RedirectURI  string
	ClientID     string
	ClientSecret string

	// Where the client authenticated. ClientID/ClientSecret above hold the
	// effective credentials (Basic auth wins); these record each location
	// separately so tests can assert credentials were sent in one place only.
	BasicAuth        bool   // an Authorization: Basic header was present
	BasicClientID    string // client ID from the Basic header
	BasicSecret      string // client secret from the Basic header
	FormClientID     string // client_id form parameter
	FormClientSecret string // client_secret form parameter
}

type pendingCode struct {
	idToken       string
	codeChallenge string
	redirectURI   string
}

// IDP is a fake OIDC provider backed by an httptest.Server.
type IDP struct {
	Server       *httptest.Server
	TenantID     string
	ClientID     string
	ClientSecret string
	// Issuer mimics Entra: <base>/<tenant-id>/v2.0
	Issuer string

	Key *rsa.PrivateKey
	KID string

	mu       sync.Mutex
	codes    map[string]pendingCode
	requests []TokenRequest
}

// New starts a fake provider. It is closed automatically at test cleanup.
func New(t testing.TB, tenantID, clientID, clientSecret string) *IDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	p := &IDP{
		TenantID:     tenantID,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Key:          key,
		KID:          "test-key-1",
		codes:        map[string]pendingCode{},
	}
	mux := http.NewServeMux()
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Server.Close)
	p.Issuer = p.Server.URL + "/" + tenantID + "/v2.0"

	mux.HandleFunc("GET /"+tenantID+"/v2.0/.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /"+tenantID+"/discovery/v2.0/keys", p.jwks)
	mux.HandleFunc("POST /"+tenantID+"/oauth2/v2.0/token", p.token)
	mux.HandleFunc("GET /"+tenantID+"/oauth2/v2.0/logout", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return p
}

// AuthorizationEndpoint is the URL the app should redirect browsers to.
func (p *IDP) AuthorizationEndpoint() string {
	return p.Server.URL + "/" + p.TenantID + "/oauth2/v2.0/authorize"
}

// EndSessionEndpoint is the advertised logout URL.
func (p *IDP) EndSessionEndpoint() string {
	return p.Server.URL + "/" + p.TenantID + "/oauth2/v2.0/logout"
}

func (p *IDP) discovery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                p.Issuer,
		"authorization_endpoint":                p.AuthorizationEndpoint(),
		"token_endpoint":                        p.Server.URL + "/" + p.TenantID + "/oauth2/v2.0/token",
		"jwks_uri":                              p.Server.URL + "/" + p.TenantID + "/discovery/v2.0/keys",
		"end_session_endpoint":                  p.EndSessionEndpoint(),
		"response_types_supported":              []string{"code", "id_token", "code id_token"},
		"subject_types_supported":               []string{"pairwise"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "profile", "email", "offline_access"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (p *IDP) jwks(w http.ResponseWriter, r *http.Request) {
	pub := p.Key.PublicKey
	writeJSON(w, http.StatusOK, map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": p.KID,
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	})
}

func (p *IDP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	req := TokenRequest{
		GrantType:    r.PostForm.Get("grant_type"),
		Code:         r.PostForm.Get("code"),
		CodeVerifier: r.PostForm.Get("code_verifier"),
		RedirectURI:  r.PostForm.Get("redirect_uri"),
		ClientID:     r.PostForm.Get("client_id"),
		ClientSecret: r.PostForm.Get("client_secret"),

		FormClientID:     r.PostForm.Get("client_id"),
		FormClientSecret: r.PostForm.Get("client_secret"),
	}
	if id, secret, ok := r.BasicAuth(); ok {
		req.ClientID, req.ClientSecret = id, secret
		req.BasicAuth, req.BasicClientID, req.BasicSecret = true, id, secret
	}

	p.mu.Lock()
	p.requests = append(p.requests, req)
	pc, found := p.codes[req.Code]
	delete(p.codes, req.Code) // codes are single use
	p.mu.Unlock()

	switch {
	case req.ClientID != p.ClientID || req.ClientSecret != p.ClientSecret:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
	case req.GrantType != "authorization_code" || !found:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
	case req.RedirectURI != pc.redirectURI:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "redirect_uri mismatch"})
	case S256Challenge(req.CodeVerifier) != pc.codeChallenge:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "PKCE verification failed"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "fake-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     pc.idToken,
		})
	}
}

// IssueCode registers an authorization code that redeems to idToken, bound to
// the PKCE challenge and redirect URI the app sent to the authorize endpoint.
func (p *IDP) IssueCode(idToken, codeChallenge, redirectURI string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	code := base64.RawURLEncoding.EncodeToString(b)
	p.mu.Lock()
	p.codes[code] = pendingCode{idToken: idToken, codeChallenge: codeChallenge, redirectURI: redirectURI}
	p.mu.Unlock()
	return code
}

// TokenRequests returns the token endpoint requests received so far.
func (p *IDP) TokenRequests() []TokenRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]TokenRequest(nil), p.requests...)
}

// Claims returns a valid set of Entra-like v2.0 ID token claims.
func (p *IDP) Claims(nonce string, roles ...string) map[string]any {
	now := time.Now()
	c := map[string]any{
		"ver":                "2.0",
		"iss":                p.Issuer,
		"aud":                p.ClientID,
		"tid":                p.TenantID,
		"sub":                "pairwise-subject-123",
		"oid":                "00000000-0000-0000-0000-00000000a11c",
		"name":               "Alice Admin",
		"preferred_username": "alice@example.com",
		"iat":                now.Add(-time.Minute).Unix(),
		"nbf":                now.Add(-time.Minute).Unix(),
		"exp":                now.Add(time.Hour).Unix(),
		"nonce":              nonce,
	}
	if roles != nil {
		c["roles"] = roles
	}
	return c
}

// SignIDToken signs claims with the provider's published key.
func (p *IDP) SignIDToken(claims map[string]any) string {
	return SignRS256(p.Key, p.KID, claims)
}

// SignRS256 produces a compact JWS (RS256) over claims.
func SignRS256(key *rsa.PrivateKey, kid string, claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	payload, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// S256Challenge computes the PKCE S256 code challenge for a verifier.
func S256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
