package config_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/config"
)

const (
	secretParam    = "/rudolph-web/example/client-secret"
	cookieKeyParam = "/rudolph-web/example/cookie-key"
)

// fakeFetcher stands in for SSM Parameter Store.
type fakeFetcher struct {
	values map[string]string
	err    error
	calls  []string
}

func (f *fakeFetcher) GetParameter(_ context.Context, name string) (string, error) {
	f.calls = append(f.calls, name)
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.values[name]
	if !ok {
		return "", errors.New("ParameterNotFound: " + name)
	}
	return v, nil
}

func secretEnv() map[string]string {
	env := validEnv()
	delete(env, config.EnvClientSecret)
	delete(env, config.EnvCookieKey)
	env[config.EnvClientSecretSSMParameter] = secretParam
	env[config.EnvCookieKeySSMParameter] = cookieKeyParam
	return env
}

func fetcherWithSecrets() *fakeFetcher {
	return &fakeFetcher{values: map[string]string{
		secretParam:    secret,
		cookieKeyParam: base64.StdEncoding.EncodeToString(key32),
	}}
}

// Without the SSM variables, Load behaves exactly like FromEnv and never calls SSM.
func TestLoad_EnvironmentOnly(t *testing.T) {
	f := fetcherWithSecrets()
	cfg, err := config.Load(context.Background(), getenv(validEnv()), f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ClientSecret != secret {
		t.Errorf("ClientSecret = %q, want the environment value", cfg.ClientSecret)
	}
	if string(cfg.CookieKey) != string(key32) {
		t.Error("cookie key not taken from the environment")
	}
	if len(f.calls) != 0 {
		t.Errorf("fetched %v from SSM although no *_SSM_PARAMETER is set", f.calls)
	}
}

// With the SSM variables set, both secrets come from the fetcher.
func TestLoad_FromParameterStore(t *testing.T) {
	f := fetcherWithSecrets()
	cfg, err := config.Load(context.Background(), getenv(secretEnv()), f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ClientSecret != secret {
		t.Errorf("ClientSecret = %q, want the parameter value", cfg.ClientSecret)
	}
	if string(cfg.CookieKey) != string(key32) {
		t.Error("cookie key not taken from Parameter Store")
	}
	want := map[string]bool{secretParam: true, cookieKeyParam: true}
	if len(f.calls) != 2 {
		t.Fatalf("fetched %v, want exactly the two configured parameters", f.calls)
	}
	for _, c := range f.calls {
		if !want[c] {
			t.Errorf("fetched unexpected parameter %q", c)
		}
	}
	// The rest of the configuration still comes from the environment.
	if cfg.TenantID != tenant || cfg.ClientID != client {
		t.Errorf("non-secret configuration not loaded: %+v", cfg)
	}
}

// Parameter values routinely carry a trailing newline; the key must still decode.
func TestLoad_TrimsParameterWhitespace(t *testing.T) {
	f := fetcherWithSecrets()
	f.values[cookieKeyParam] = "\n" + f.values[cookieKeyParam] + "\n"
	cfg, err := config.Load(context.Background(), getenv(secretEnv()), f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(cfg.CookieKey) != string(key32) {
		t.Error("cookie key with surrounding whitespace did not decode")
	}
}

// Only one source per secret, so a deployment cannot silently prefer the wrong one.
func TestLoad_RejectsBothSources(t *testing.T) {
	env := secretEnv()
	env[config.EnvClientSecret] = secret
	_, err := config.Load(context.Background(), getenv(env), fetcherWithSecrets())
	if err == nil {
		t.Fatal("Load accepted both an environment secret and an SSM parameter")
	}
	for _, want := range []string{config.EnvClientSecret, config.EnvClientSecretSSMParameter} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not mention %s", err, want)
		}
	}
}

func TestLoad_FetchFailure(t *testing.T) {
	f := fetcherWithSecrets()
	f.err = errors.New("AccessDeniedException")
	_, err := config.Load(context.Background(), getenv(secretEnv()), f)
	if err == nil {
		t.Fatal("Load succeeded although Parameter Store failed")
	}
	if !strings.Contains(err.Error(), secretParam) || !strings.Contains(err.Error(), "AccessDeniedException") {
		t.Errorf("error %v should name the parameter and the cause", err)
	}
}

func TestLoad_EmptyParameter(t *testing.T) {
	f := fetcherWithSecrets()
	f.values[secretParam] = "  "
	_, err := config.Load(context.Background(), getenv(secretEnv()), f)
	if err == nil {
		t.Fatal("Load accepted an empty parameter")
	}
	if !strings.Contains(err.Error(), secretParam) {
		t.Errorf("error %v does not name the empty parameter", err)
	}
}

func TestLoad_MissingFetcher(t *testing.T) {
	_, err := config.Load(context.Background(), getenv(secretEnv()), nil)
	if err == nil {
		t.Fatal("Load accepted an SSM parameter without a fetcher")
	}
}

// Errors name variables and parameters, never values.
func TestLoad_ErrorsNeverIncludeSecrets(t *testing.T) {
	f := fetcherWithSecrets()
	f.values[cookieKeyParam] = base64.StdEncoding.EncodeToString([]byte("too-short"))
	f.values[secretParam] = secret
	_, err := config.Load(context.Background(), getenv(secretEnv()), f)
	if err == nil {
		t.Fatal("Load accepted a short cookie key")
	}
	for _, leak := range []string{secret, f.values[cookieKeyParam]} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %v leaks a secret value", err)
		}
	}
}
