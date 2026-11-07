// Package config loads rudolph-web settings from environment variables, with
// the two secrets optionally coming from SSM Parameter Store instead.
package config

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
)

// Environment variable names.
const (
	EnvTenantID              = "RUDOLPH_WEB_TENANT_ID"
	EnvClientID              = "RUDOLPH_WEB_CLIENT_ID"
	EnvClientSecret          = "RUDOLPH_WEB_CLIENT_SECRET"
	EnvRedirectURL           = "RUDOLPH_WEB_REDIRECT_URL"
	EnvPostLogoutRedirectURL = "RUDOLPH_WEB_POST_LOGOUT_REDIRECT_URL"
	EnvCookieKey             = "RUDOLPH_WEB_COOKIE_KEY"
	EnvSessionTTL            = "RUDOLPH_WEB_SESSION_TTL"
	EnvAddr                  = "RUDOLPH_WEB_ADDR"

	// EnvTopListStaleAfter is how old a top list may be before its page warns
	// that the ranker may have stopped. A ranker scheduled for working hours
	// only needs more than the default.
	EnvTopListStaleAfter = "RUDOLPH_WEB_TOP_LIST_STALE_AFTER"

	// EnvTableName names Rudolph's DynamoDB table. Unset, cmd/lambda has no data
	// layer and the data pages answer 503.
	EnvTableName = "RUDOLPH_WEB_DYNAMODB_TABLE"

	// EnvEventsTableName names Rudolph's execution events table. Unset, the
	// events page answers 503 while the rest of the data pages work. It needs
	// EnvTableName.
	EnvEventsTableName = "RUDOLPH_WEB_EVENTS_TABLE"

	// EnvCLIEnv is the Rudolph environment name the copy-and-paste CLI
	// commands start with (ENV=<name> rudolph ...). Unset, they start with
	// plain rudolph and the operator's shell supplies ENV.
	EnvCLIEnv = "RUDOLPH_WEB_CLI_ENV"

	// Names of SSM Parameter Store parameters holding the secrets. When one of
	// these is set the matching plain variable must be unset, and the value is
	// read once, at cold start.
	EnvClientSecretSSMParameter = "RUDOLPH_WEB_CLIENT_SECRET_SSM_PARAMETER"
	EnvCookieKeySSMParameter    = "RUDOLPH_WEB_COOKIE_KEY_SSM_PARAMETER"
)

const (
	defaultSessionTTL = time.Hour
	minSessionTTL     = 5 * time.Minute
	maxSessionTTL     = 12 * time.Hour
	defaultAddr       = "127.0.0.1:8080"

	defaultTopListStaleAfter = 3 * time.Hour
	minTopListStaleAfter     = time.Hour
	maxTopListStaleAfter     = 168 * time.Hour
	entraAuthority           = "https://login.microsoftonline.com"
)

// cliEnvRe matches environment names that can follow ENV= in a shell command
// without quoting.
var cliEnvRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

var guidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Config is the validated application configuration.
type Config struct {
	TenantID              string
	ClientID              string
	ClientSecret          string
	RedirectURL           string
	PostLogoutRedirectURL string
	CookieKey             []byte
	SessionTTL            time.Duration
	Addr                  string // cmd/local only
	TableName             string // empty: no data layer
	EventsTableName       string // empty: no execution events
	CLIEnv                string // empty: CLI commands without ENV=
	TopListStaleAfter     time.Duration
}

// FromEnv reads and validates configuration. Error messages name variables
// but never include secret values.
func FromEnv(getenv func(string) string) (Config, error) {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	c := Config{
		TenantID:              get(EnvTenantID),
		ClientID:              get(EnvClientID),
		ClientSecret:          getenv(EnvClientSecret),
		RedirectURL:           get(EnvRedirectURL),
		PostLogoutRedirectURL: get(EnvPostLogoutRedirectURL),
		SessionTTL:            defaultSessionTTL,
		Addr:                  get(EnvAddr),
		TableName:             get(EnvTableName),
		EventsTableName:       get(EnvEventsTableName),
		CLIEnv:                get(EnvCLIEnv),
		TopListStaleAfter:     defaultTopListStaleAfter,
	}
	var errs []error
	required := func(name, v string) bool {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
			return false
		}
		return true
	}

	if required(EnvTenantID, c.TenantID) && !guidRe.MatchString(c.TenantID) {
		errs = append(errs, fmt.Errorf("%s must be a tenant GUID (multi-tenant values like 'common' are not allowed)", EnvTenantID))
	}
	required(EnvClientID, c.ClientID)
	required(EnvClientSecret, c.ClientSecret)
	if required(EnvRedirectURL, c.RedirectURL) {
		if err := checkURL(c.RedirectURL, auth.CallbackPath); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", EnvRedirectURL, err))
		}
	}
	if c.PostLogoutRedirectURL != "" {
		if err := checkURL(c.PostLogoutRedirectURL, ""); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", EnvPostLogoutRedirectURL, err))
		}
	}
	if raw := get(EnvCookieKey); required(EnvCookieKey, raw) {
		key, err := decodeKey(raw)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s must be base64", EnvCookieKey))
		case len(key) != 32:
			errs = append(errs, fmt.Errorf("%s must decode to 32 bytes, got %d", EnvCookieKey, len(key)))
		default:
			c.CookieKey = key
		}
	}
	if v := get(EnvSessionTTL); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s must be a Go duration such as 1h", EnvSessionTTL))
		case d < minSessionTTL || d > maxSessionTTL:
			errs = append(errs, fmt.Errorf("%s must be between %v and %v", EnvSessionTTL, minSessionTTL, maxSessionTTL))
		default:
			c.SessionTTL = d
		}
	}
	if v := get(EnvTopListStaleAfter); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s must be a Go duration such as 15h", EnvTopListStaleAfter))
		case d < minTopListStaleAfter || d > maxTopListStaleAfter:
			errs = append(errs, fmt.Errorf("%s must be between %v and %v", EnvTopListStaleAfter, minTopListStaleAfter, maxTopListStaleAfter))
		default:
			c.TopListStaleAfter = d
		}
	}
	if c.EventsTableName != "" && c.TableName == "" {
		errs = append(errs, fmt.Errorf("%s needs %s", EnvEventsTableName, EnvTableName))
	}
	if c.CLIEnv != "" && !cliEnvRe.MatchString(c.CLIEnv) {
		errs = append(errs, fmt.Errorf("%s must be letters, digits, '.', '_' or '-' (at most 64)", EnvCLIEnv))
	}
	if c.Addr == "" {
		c.Addr = defaultAddr
	}
	if len(errs) > 0 {
		return Config{}, fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return c, nil
}

// A ParameterFetcher reads a single decrypted parameter value by name. The
// implementation used in production is internal/awsssm; it caches values for
// the life of the process, so a warm Lambda never calls SSM again.
type ParameterFetcher interface {
	GetParameter(ctx context.Context, name string) (string, error)
}

// Load reads the same configuration as FromEnv, except that the client secret
// and the cookie key come from SSM Parameter Store when their
// *_SSM_PARAMETER variables name a parameter. That keeps secrets out of the
// Lambda's environment, which is visible to anyone who can describe the
// function. Local development keeps using the plain variables, and fetch may
// then be nil.
//
// A secret may have exactly one source: setting both the plain variable and
// its *_SSM_PARAMETER variable is an error rather than a silent preference.
// Like FromEnv, errors name variables and parameters but never values.
func Load(ctx context.Context, getenv func(string) string, fetch ParameterFetcher) (Config, error) {
	resolved := map[string]string{}
	var errs []error
	for _, s := range []struct{ value, parameter string }{
		{EnvClientSecret, EnvClientSecretSSMParameter},
		{EnvCookieKey, EnvCookieKeySSMParameter},
	} {
		name := strings.TrimSpace(getenv(s.parameter))
		if name == "" {
			continue
		}
		if strings.TrimSpace(getenv(s.value)) != "" {
			errs = append(errs, fmt.Errorf("%s and %s are both set; use exactly one", s.value, s.parameter))
			continue
		}
		if fetch == nil {
			errs = append(errs, fmt.Errorf("%s is set but no parameter store is configured", s.parameter))
			continue
		}
		v, err := fetch.GetParameter(ctx, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: reading %s: %w", s.parameter, name, err))
			continue
		}
		if strings.TrimSpace(v) == "" {
			errs = append(errs, fmt.Errorf("%s: parameter %s is empty", s.parameter, name))
			continue
		}
		resolved[s.value] = strings.TrimSpace(v)
	}
	if len(errs) > 0 {
		return Config{}, fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return FromEnv(func(k string) string {
		if v, ok := resolved[k]; ok {
			return v
		}
		return getenv(k)
	})
}

// Issuer is the Entra ID v2.0 issuer for the tenant.
func (c Config) Issuer() string {
	return entraAuthority + "/" + c.TenantID + "/v2.0"
}

// AuthConfig maps the configuration onto auth.Config.
func (c Config) AuthConfig() auth.Config {
	return auth.Config{
		Issuer:                c.Issuer(),
		TenantID:              c.TenantID,
		ClientID:              c.ClientID,
		ClientSecret:          c.ClientSecret,
		RedirectURL:           c.RedirectURL,
		PostLogoutRedirectURL: c.PostLogoutRedirectURL,
		CookieKey:             c.CookieKey,
		SessionTTL:            c.SessionTTL,
	}
}

// checkURL requires https (or http on localhost) and, if wantPath is set, that path.
func checkURL(raw, wantPath string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("must be an absolute URL")
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return errors.New("must use https (http is allowed only for localhost)")
	}
	if wantPath != "" && u.Path != wantPath {
		return fmt.Errorf("path must be %s", wantPath)
	}
	return nil
}

func decodeKey(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("invalid base64")
}
