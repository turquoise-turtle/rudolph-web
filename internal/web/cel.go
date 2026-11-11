package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/airbnb/rudolph/pkg/model/rules"

	"github.com/turquoise-turtle/rudolph-web/internal/santacel"
)

// celTry is the try form on /rules/cel, as submitted, and its answer.
type celTry struct {
	Expr              string
	Args              string
	Envs              string
	EUID              string
	Path              string
	CWD               string
	SigningID         string
	TeamID            string
	Platform          bool
	SigningTime       string
	SecureSigningTime string

	Tried     bool
	Result    string
	Error     string
	Cacheable bool
}

// maxTryLines bounds the args and envs of a sample execution; the form is
// open to every viewer.
const maxTryLines = 256

// celRules explains CEL rules, lists examples and, given an expression,
// evaluates it against the sample execution in the query. It only reads, so
// it is a GET open to viewers, and a bookmark keeps the example tried.
func (s *server) celRules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	t := celTry{
		Expr:              q.Get("expr"),
		Args:              q.Get("args"),
		Envs:              q.Get("envs"),
		EUID:              strings.TrimSpace(q.Get("euid")),
		Path:              q.Get("path"),
		CWD:               q.Get("cwd"),
		SigningID:         strings.TrimSpace(q.Get("signing_id")),
		TeamID:            strings.TrimSpace(q.Get("team_id")),
		Platform:          q.Get("platform") != "",
		SigningTime:       strings.TrimSpace(q.Get("signing_time")),
		SecureSigningTime: strings.TrimSpace(q.Get("secure_signing_time")),
	}
	status := http.StatusOK
	if strings.TrimSpace(t.Expr) != "" {
		t.Tried = true
		if err := t.evaluate(); err != "" {
			t.Error = err
			status = http.StatusBadRequest
		}
	}
	s.render(w, r, status, "cel", "CEL rules", "rules", struct {
		Try      celTry
		Examples []santacel.Example
		Policies []string
	}{t, santacel.Examples, santacel.Policies})
}

// evaluate fills in the answer, or returns why the sample execution itself
// could not be read; an expression Santa would refuse is an answer.
func (t *celTry) evaluate() string {
	if len(t.Expr) > rules.MaxCELExprBytes {
		return "the expression is longer than the " + strconv.Itoa(rules.MaxCELExprBytes) + " bytes Rudolph stores"
	}
	c := santacel.Context{
		SigningID: t.SigningID, TeamID: t.TeamID, PlatformBinary: t.Platform,
		Path: t.Path, CWD: t.CWD, Args: lines(t.Args),
	}
	if len(c.Args) > maxTryLines {
		return "give at most " + strconv.Itoa(maxTryLines) + " arguments"
	}
	if t.EUID != "" {
		euid, err := strconv.ParseInt(t.EUID, 10, 32)
		if err != nil || euid < 0 {
			return "euid must be a user ID such as 0 for root or 501"
		}
		c.EUID = int(euid)
	}
	envLines := lines(t.Envs)
	if len(envLines) > maxTryLines {
		return "give at most " + strconv.Itoa(maxTryLines) + " environment variables"
	}
	for _, l := range envLines {
		k, v, ok := strings.Cut(l, "=")
		if !ok || k == "" {
			return "each environment line must be NAME=value"
		}
		if c.Envs == nil {
			c.Envs = map[string]string{}
		}
		c.Envs[k] = v
	}
	var ok bool
	if c.SigningTime, ok = sampleTime(t.SigningTime); !ok {
		return "the signing time must be a date such as 2025-01-31 or a time such as 2025-01-31T09:00:00Z"
	}
	if c.SecureSigningTime, ok = sampleTime(t.SecureSigningTime); !ok {
		return "the secure signing time must be a date such as 2025-01-31 or a time such as 2025-01-31T09:00:00Z"
	}

	result, err := santacel.Evaluate(t.Expr, c)
	if err != nil {
		t.Error = err.Error()
		return ""
	}
	t.Result = result
	// Evaluate has checked the expression already, so this cannot fail.
	t.Cacheable, _ = santacel.Cacheable(t.Expr)
	return ""
}

// lines splits a textarea into its lines, as browsers submit them with CRLF,
// leaving out a trailing empty line but keeping every other one as typed, so
// an argument may be empty or have spaces.
func lines(v string) []string {
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.TrimSuffix(v, "\n")
	if v == "" {
		return nil
	}
	return strings.Split(v, "\n")
}

func sampleTime(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, true
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
