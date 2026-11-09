package auth

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"time"
)

// Entra ID App Role values carried in the ID token's roles claim.
const (
	RoleViewer = "Rudolph.Viewer"
	RoleAdmin  = "Rudolph.Admin"
)

// clockSkew tolerates small differences between Lambda hosts.
const clockSkew = time.Minute

// Session is the authenticated user, stored encrypted in the session cookie.
type Session struct {
	Subject   string // Entra object ID (oid), falling back to sub
	Name      string
	Username  string // preferred_username
	TenantID  string
	Roles     []string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// HasRole reports whether the session grants role. Rudolph.Admin implies
// Rudolph.Viewer. Role names are compared exactly (case-sensitive), as Entra
// emits the configured value verbatim.
func (s Session) HasRole(role string) bool {
	if slices.Contains(s.Roles, role) {
		return true
	}
	return role == RoleViewer && slices.Contains(s.Roles, RoleAdmin)
}

type sessionPayload struct {
	Subject  string   `json:"sub"`
	Name     string   `json:"name,omitempty"`
	Username string   `json:"upn,omitempty"`
	TenantID string   `json:"tid"`
	Roles    []string `json:"roles,omitempty"`
	IssuedAt int64    `json:"iat"`
	Expires  int64    `json:"exp"`
}

type ctxKey struct{}

// SessionFromContext returns the session attached by Require.
func SessionFromContext(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(ctxKey{}).(Session)
	return s, ok
}

// NewSessionCookie encrypts s into a session cookie. Zero IssuedAt defaults to
// now and zero ExpiresAt to IssuedAt + SessionTTL.
func (a *Authenticator) NewSessionCookie(s Session) (*http.Cookie, error) {
	now := a.now()
	if s.IssuedAt.IsZero() {
		s.IssuedAt = now
	}
	if s.ExpiresAt.IsZero() {
		s.ExpiresAt = s.IssuedAt.Add(a.cfg.SessionTTL)
	}
	value, err := a.sealer.seal(SessionCookieName, sessionPayload{
		Subject:  s.Subject,
		Name:     s.Name,
		Username: s.Username,
		TenantID: s.TenantID,
		Roles:    s.Roles,
		IssuedAt: s.IssuedAt.Unix(),
		Expires:  s.ExpiresAt.Unix(),
	})
	if err != nil {
		return nil, err
	}
	return newCookie(SessionCookieName, value, s.ExpiresAt.Sub(now)), nil
}

// sessionFromRequest returns the valid session, or ok=false. present reports
// whether a (possibly invalid) session cookie was sent.
func (a *Authenticator) sessionFromRequest(r *http.Request) (s Session, ok, present bool) {
	c, err := r.Cookie(SessionCookieName)
	if err != nil {
		return Session{}, false, false
	}
	var p sessionPayload
	if err := a.sealer.open(SessionCookieName, c.Value, &p); err != nil {
		return Session{}, false, true
	}
	now := a.now()
	iat, exp := time.Unix(p.IssuedAt, 0), time.Unix(p.Expires, 0)
	if !now.Before(exp) || iat.After(now.Add(clockSkew)) || p.TenantID != a.cfg.TenantID || p.Subject == "" {
		return Session{}, false, true
	}
	return Session{
		Subject:   p.Subject,
		Name:      p.Name,
		Username:  p.Username,
		TenantID:  p.TenantID,
		Roles:     p.Roles,
		IssuedAt:  iat,
		ExpiresAt: exp,
	}, true, true
}

// ForbiddenFunc writes the response for a signed-in user whose session lacks
// role. The session is in r's context (SessionFromContext), so a page can show
// who is signed in. The Cache-Control: no-store header is already set.
type ForbiddenFunc func(w http.ResponseWriter, r *http.Request, role string)

// Require wraps next so it runs only for a valid session holding role.
// Unauthenticated GET/HEAD requests are redirected to the login page with the
// original URL as return_to; other methods get 401. Sessions without the
// role get a plain-text 403.
func (a *Authenticator) Require(role string, next http.Handler) http.Handler {
	return a.RequireOr(role, next, nil)
}

// RequireOr is Require with forbidden writing the 403 for a session without
// role, so the caller can render it in its own page layout without this
// package depending on the caller's templates. Nil forbidden gives Require's
// plain-text 403.
func (a *Authenticator) RequireOr(role string, next http.Handler, forbidden ForbiddenFunc) http.Handler {
	if forbidden == nil {
		forbidden = plainForbidden
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok, present := a.sessionFromRequest(r)
		if !ok {
			if present {
				clearCookie(w, SessionCookieName)
			}
			w.Header().Set("Cache-Control", "no-store")
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				http.Redirect(w, r, LoginPath+"?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
			http.Error(w, "Authentication required.", http.StatusUnauthorized)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, s))
		if !s.HasRole(role) {
			w.Header().Set("Cache-Control", "no-store")
			forbidden(w, r, role)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ForbiddenMessage is the explanation shown to a signed-in user without role.
func ForbiddenMessage(role string) string {
	return "Your account has not been assigned the " + role + " role for Rudolph."
}

func plainForbidden(w http.ResponseWriter, _ *http.Request, role string) {
	http.Error(w, "Forbidden: "+ForbiddenMessage(role), http.StatusForbidden)
}
