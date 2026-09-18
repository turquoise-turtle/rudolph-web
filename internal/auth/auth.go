// Package auth implements Microsoft Entra ID single sign-on for rudolph-web:
// the OIDC authorization code flow with PKCE, stateless encrypted session
// cookies, and App Role based authorization.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Routes mounted by Mount.
const (
	LoginPath    = "/auth/login"
	CallbackPath = "/auth/callback"
	LogoutPath   = "/auth/logout"
)

// authPathPrefix is the route prefix owned by this package; return_to must
// never point into it.
const authPathPrefix = "/auth"

// flowTTL bounds how long a user may take at the IdP.
const flowTTL = 10 * time.Minute

// Config configures an Authenticator.
type Config struct {
	// Issuer is https://login.microsoftonline.com/<tenant-id>/v2.0 in production.
	Issuer                string
	TenantID              string
	ClientID              string
	ClientSecret          string
	RedirectURL           string
	PostLogoutRedirectURL string // optional
	CookieKey             []byte // 32 bytes, AES-256-GCM
	SessionTTL            time.Duration

	HTTPClient *http.Client     // optional; used for discovery, JWKS and token calls
	Now        func() time.Time // optional; for tests
	Logger     *slog.Logger     // optional
}

// Authenticator handles login, callback, logout and session checks.
type Authenticator struct {
	cfg    Config
	sealer *sealer
	log    *slog.Logger
	client *http.Client

	mu               sync.Mutex
	provider         *providerState
	discoveryErr     error
	discoveryRetryAt time.Time
}

// discoveryFailureTTL briefly caches a discovery failure, so a provider
// outage costs one request per interval instead of one per sign-in attempt.
const discoveryFailureTTL = 5 * time.Second

type providerState struct {
	oauth2     oauth2.Config
	verifier   *oidc.IDTokenVerifier
	endSession string
}

// New validates cfg and returns an Authenticator. It does not contact the
// identity provider; discovery happens on the first login or logout.
func New(cfg Config) (*Authenticator, error) {
	var missing []string
	for name, v := range map[string]string{
		"Issuer": cfg.Issuer, "TenantID": cfg.TenantID, "ClientID": cfg.ClientID,
		"ClientSecret": cfg.ClientSecret, "RedirectURL": cfg.RedirectURL,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("auth: missing config: %s", strings.Join(missing, ", "))
	}
	if cfg.SessionTTL <= 0 {
		return nil, errors.New("auth: SessionTTL must be positive")
	}
	s, err := newSealer(cfg.CookieKey)
	if err != nil {
		return nil, err
	}
	a := &Authenticator{cfg: cfg, sealer: s, log: cfg.Logger, client: cfg.HTTPClient}
	if a.log == nil {
		a.log = slog.Default()
	}
	if a.client == nil {
		a.client = &http.Client{Timeout: 10 * time.Second}
	}
	return a, nil
}

func (a *Authenticator) now() time.Time {
	if a.cfg.Now != nil {
		return a.cfg.Now()
	}
	return time.Now()
}

// Mount registers the login, callback and logout routes.
func (a *Authenticator) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET "+LoginPath, a.login)
	mux.HandleFunc("GET "+CallbackPath, a.callback)
	mux.HandleFunc("POST "+LogoutPath, a.logout)
}

func (a *Authenticator) getProvider() (*providerState, error) {
	a.mu.Lock()
	if p := a.provider; p != nil {
		a.mu.Unlock()
		return p, nil
	}
	if err := a.discoveryErr; err != nil && a.now().Before(a.discoveryRetryAt) {
		a.mu.Unlock()
		return nil, err
	}
	a.mu.Unlock()

	// Discovery runs without the lock held, so a slow or unreachable provider
	// does not queue every other request behind it.
	p, err := a.discover()

	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.discoveryErr, a.discoveryRetryAt = err, a.now().Add(discoveryFailureTTL)
		return nil, err
	}
	a.discoveryErr = nil
	if a.provider == nil {
		a.provider = p
	}
	return a.provider, nil
}

func (a *Authenticator) discover() (*providerState, error) {
	ctx, cancel := context.WithTimeout(oidc.ClientContext(context.Background(), a.client), 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, a.cfg.Issuer) // checks discovery issuer == cfg.Issuer
	if err != nil {
		return nil, fmt.Errorf("auth: oidc discovery: %w", err)
	}
	var extra struct {
		EndSession string `json:"end_session_endpoint"`
	}
	if err := p.Claims(&extra); err != nil {
		return nil, fmt.Errorf("auth: oidc discovery claims: %w", err)
	}
	endpoint := p.Endpoint()
	// Entra ID accepts HTTP Basic client authentication. Saying so explicitly
	// avoids x/oauth2's auto-detection, which retries a failed token request
	// with the credentials in the form body, sending the code and the secret
	// twice.
	endpoint.AuthStyle = oauth2.AuthStyleInHeader
	return &providerState{
		oauth2: oauth2.Config{
			ClientID:     a.cfg.ClientID,
			ClientSecret: a.cfg.ClientSecret,
			RedirectURL:  a.cfg.RedirectURL,
			Endpoint:     endpoint,
			Scopes:       []string{oidc.ScopeOpenID, "profile"},
		},
		verifier: p.Verifier(&oidc.Config{
			ClientID:             a.cfg.ClientID,
			SupportedSigningAlgs: []string{oidc.RS256},
			Now:                  a.now,
		}),
		endSession: extra.EndSession,
	}, nil
}

type flowState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	ReturnTo string `json:"r"`
	Created  int64  `json:"c"`
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// safeReturnTo accepts only same-origin absolute paths that do not lead back
// into the auth routes.
//
// The check is made on the normalised path: browsers resolve dot segments
// (including percent-encoded ones) before requesting a Location, and ServeMux
// decodes percent-encoded characters when matching, so "/./auth/callback",
// "/machines/../auth/logout" and "/%61uth/callback" all end up at /auth/.
func safeReturnTo(s string) string {
	if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.ContainsAny(s, "\\\r\n\t") {
		return "/"
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	// u.Path is percent-decoded; path.Clean resolves . and .. segments.
	clean := path.Clean("/" + strings.TrimPrefix(u.Path, "/"))
	if clean == authPathPrefix || strings.HasPrefix(clean, authPathPrefix+"/") {
		return "/"
	}
	return s
}

func (a *Authenticator) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, err := a.getProvider()
	if err != nil {
		a.log.Error("sign-in unavailable", "err", err)
		http.Error(w, "Sign-in is temporarily unavailable.", http.StatusServiceUnavailable)
		return
	}
	state, err1 := randomToken()
	nonce, err2 := randomToken()
	if err := errors.Join(err1, err2); err != nil {
		http.Error(w, "Internal error.", http.StatusInternalServerError)
		return
	}
	fs := flowState{
		State:    state,
		Nonce:    nonce,
		Verifier: oauth2.GenerateVerifier(),
		ReturnTo: safeReturnTo(r.URL.Query().Get("return_to")),
		Created:  a.now().Unix(),
	}
	value, err := a.sealer.seal(FlowCookieName, fs)
	if err != nil {
		http.Error(w, "Internal error.", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, newCookie(FlowCookieName, value, flowTTL))
	authURL := p.oauth2.AuthCodeURL(state,
		oauth2.S256ChallengeOption(fs.Verifier),
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("response_mode", "query"),
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// idTokenClaims are the Entra v2.0 claims rudolph-web uses.
type idTokenClaims struct {
	Nonce             string   `json:"nonce"`
	TenantID          string   `json:"tid"`
	ObjectID          string   `json:"oid"`
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
	Roles             []string `json:"roles"`
}

func (a *Authenticator) reject(w http.ResponseWriter, reason string, attrs ...any) {
	a.log.Warn("sign-in rejected", append([]any{"reason", reason}, attrs...)...)
	http.Error(w, "Sign-in failed. Please try again.", http.StatusUnauthorized)
}

func (a *Authenticator) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()

	// The flow cookie is single use: clear it whatever happens next.
	fc, err := r.Cookie(FlowCookieName)
	clearCookie(w, FlowCookieName)
	if err != nil {
		a.reject(w, "missing flow cookie")
		return
	}
	var fs flowState
	if err := a.sealer.open(FlowCookieName, fc.Value, &fs); err != nil {
		a.reject(w, "invalid flow cookie")
		return
	}
	now := a.now()
	created := time.Unix(fs.Created, 0)
	if now.Sub(created) > flowTTL || created.After(now.Add(clockSkew)) {
		a.reject(w, "expired flow cookie")
		return
	}
	state := q.Get("state")
	if state == "" || fs.State == "" || subtle.ConstantTimeCompare([]byte(state), []byte(fs.State)) != 1 {
		a.reject(w, "state mismatch")
		return
	}
	if e := q.Get("error"); e != "" {
		a.reject(w, "provider returned error", "error", e)
		return
	}
	code := q.Get("code")
	if code == "" {
		a.reject(w, "missing code")
		return
	}

	p, err := a.getProvider()
	if err != nil {
		a.log.Error("sign-in unavailable", "err", err)
		http.Error(w, "Sign-in is temporarily unavailable.", http.StatusServiceUnavailable)
		return
	}
	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, a.client)
	tok, err := p.oauth2.Exchange(ctx, code, oauth2.VerifierOption(fs.Verifier))
	if err != nil {
		a.reject(w, "code exchange failed", "err", err)
		return
	}
	rawIDToken, _ := tok.Extra("id_token").(string)
	if rawIDToken == "" {
		a.reject(w, "no id_token in token response")
		return
	}
	// Verifies signature (RS256 via JWKS), iss, aud and exp.
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		a.reject(w, "id token verification failed", "err", err)
		return
	}
	var claims idTokenClaims
	if err := idToken.Claims(&claims); err != nil {
		a.reject(w, "malformed id token claims", "err", err)
		return
	}
	if claims.Nonce == "" || subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(fs.Nonce)) != 1 {
		a.reject(w, "nonce mismatch")
		return
	}
	if claims.TenantID != a.cfg.TenantID {
		a.reject(w, "tenant mismatch", "tid", claims.TenantID)
		return
	}
	subject := claims.ObjectID
	if subject == "" {
		subject = idToken.Subject
	}
	if subject == "" {
		a.reject(w, "no subject")
		return
	}

	cookie, err := a.NewSessionCookie(Session{
		Subject:  subject,
		Name:     claims.Name,
		Username: claims.PreferredUsername,
		TenantID: claims.TenantID,
		Roles:    claims.Roles,
	})
	if err != nil {
		http.Error(w, "Internal error.", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, cookie)
	a.log.Info("sign-in", "subject", subject, "roles", claims.Roles)
	http.Redirect(w, r, safeReturnTo(fs.ReturnTo), http.StatusFound)
}

func (a *Authenticator) logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	clearCookie(w, SessionCookieName)

	target := "/"
	if p, err := a.getProvider(); err != nil {
		a.log.Error("logout: provider unavailable; cleared local session only", "err", err)
	} else if p.endSession != "" {
		if u, err := url.Parse(p.endSession); err == nil {
			q := u.Query()
			q.Set("client_id", a.cfg.ClientID)
			if a.cfg.PostLogoutRedirectURL != "" {
				q.Set("post_logout_redirect_uri", a.cfg.PostLogoutRedirectURL)
			}
			u.RawQuery = q.Encode()
			target = u.String()
		}
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// IssuerOrigin returns the scheme://host of the issuer, for use in a
// Content-Security-Policy form-action directive.
func (a *Authenticator) IssuerOrigin() string {
	u, err := url.Parse(a.cfg.Issuer)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// RedirectOrigin returns the scheme://host of the configured redirect URL.
// Behind CloudFront or another custom domain the Lambda sees its own host, so
// this is the origin browsers actually send, and the only cross-host origin
// the app trusts for unsafe requests.
func (a *Authenticator) RedirectOrigin() string {
	u, err := url.Parse(a.cfg.RedirectURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
