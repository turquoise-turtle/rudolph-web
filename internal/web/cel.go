package web

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/airbnb/rudolph/pkg/model/rules"

	"github.com/turquoise-turtle/rudolph-web/internal/santacel"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/textsafe"
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
	Cacheable bool
	// SampleError is why the sample execution could not be read; Error is why
	// Santa could not compile the expression or, with Compiled, how running
	// it on the sample failed. The three read differently to the person: fix
	// the sample, fix the expression, or the rule decides nothing for such an
	// execution.
	SampleError string
	Error       string
	Compiled    bool
	// Where shows the places a compile error names, in the expression.
	Where []celWhere
	// AddURL is the add-rule page pre-filled with the sample's signing ID and
	// team ID, for admins once the expression has given an answer.
	AddURL string
}

// celWhere is one line of the expression, or the part of a long one around
// the error, and a caret under the character a compile error names.
type celWhere struct {
	Line  string
	Caret string
}

// celExample is an example and where its links go.
type celExample struct {
	santacel.Example
	TryURL string
	AddURL string
}

// Outcome names how the answer is coloured.
func (t celTry) Outcome() string {
	switch {
	case t.SampleError != "" || t.Error != "":
		return "error"
	case strings.HasPrefix(t.Result, "ALLOWLIST"):
		return "allow"
	}
	return "block"
}

// ExprRows makes the expression box tall enough to show a long or multi-line
// expression whole after a try, as no script can grow it while typing.
func (t celTry) ExprRows() int {
	rows := 0
	for line := range strings.SplitSeq(t.Expr, "\n") {
		rows += 1 + len(line)/exprBoxColumns
	}
	return min(max(rows, 3), 16)
}

// exprBoxColumns is about how many characters fit across the expression box
// at its narrowest on a laptop; a wider box only leaves a spare row.
const exprBoxColumns = 80

// maxTryLines bounds the args and envs of a sample execution; the form is
// open to every viewer.
const maxTryLines = 256

// maxTrySampleBytes bounds the sample execution as a whole. cel-go charges
// for a regex match only once it has run, so the cost limit does not stop an
// expression matching a long pattern against a long value: the time grows
// with both lengths, and only the expression's is bounded otherwise. A real
// execution's path, working directory and identifiers are far shorter.
const maxTrySampleBytes = 8 << 10

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
	canEdit := s.canEdit(r)
	status := http.StatusOK
	if strings.TrimSpace(t.Expr) != "" {
		t.Tried = true
		if err := t.evaluate(); err != "" {
			t.SampleError = err
			status = http.StatusBadRequest
		}
		if canEdit && t.Result != "" {
			t.AddURL = newRuleURL(store.TopBinary{SigningID: t.SigningID, TeamID: t.TeamID})
		}
	}
	examples := make([]celExample, len(santacel.Examples))
	for i, e := range santacel.Examples {
		examples[i] = celExample{Example: e, TryURL: tryURL(e)}
		if canEdit {
			examples[i].AddURL = exampleAddURL(e)
		}
	}
	s.render(w, r, status, "cel", "CEL rules", "rules", struct {
		Try      celTry
		Examples []celExample
		Policies []string
		CanEdit  bool
	}{t, examples, santacel.Policies, canEdit})
}

// tryURL is the try form filled in with an example and its sample execution,
// landing on the form as a submitted try does.
func tryURL(e santacel.Example) string {
	c := e.Sample
	v := url.Values{
		"args": {strings.Join(c.Args, "\n")},
		"euid": {strconv.Itoa(c.EUID)},
	}
	set := func(name, value string) {
		if value != "" {
			v.Set(name, value)
		}
	}
	set("path", c.Path)
	set("cwd", c.CWD)
	set("signing_id", c.SigningID)
	set("team_id", c.TeamID)
	if c.PlatformBinary {
		v.Set("platform", "1")
	}
	var envs []string
	for name, value := range c.Envs {
		envs = append(envs, name+"="+value)
	}
	slices.Sort(envs)
	set("envs", strings.Join(envs, "\n"))
	if !c.SigningTime.IsZero() {
		v.Set("signing_time", c.SigningTime.UTC().Format(time.RFC3339))
	}
	if !c.SecureSigningTime.IsZero() {
		v.Set("secure_signing_time", c.SecureSigningTime.UTC().Format(time.RFC3339))
	}
	// expr leads, so the address reads as a try of that expression.
	return "/rules/cel?expr=" + url.QueryEscape(e.Expression) + "&" + v.Encode() + "#try"
}

// exampleAddURL is the add-rule page pre-filled with an example's identifier,
// or "" for an example that has none or one that page does not take.
func exampleAddURL(e santacel.Example) string {
	switch store.RuleType(e.RuleType) {
	case store.RuleTypeSigningID:
		return newRuleURL(store.TopBinary{SigningID: e.Identifier})
	case store.RuleTypeTeamID:
		return newRuleURL(store.TopBinary{TeamID: e.Identifier})
	}
	return ""
}

// evaluate fills in the answer, or returns why the sample execution itself
// could not be read; an expression Santa would refuse is an answer.
func (t *celTry) evaluate() string {
	if len(t.Expr) > rules.MaxCELExprBytes {
		return "the expression is longer than the " + strconv.Itoa(rules.MaxCELExprBytes) + " bytes Rudolph stores"
	}
	if len(t.Args)+len(t.Envs)+len(t.Path)+len(t.CWD)+len(t.SigningID)+len(t.TeamID) > maxTrySampleBytes {
		return "the sample execution is longer than the " + strconv.Itoa(maxTrySampleBytes) + " bytes the try form takes"
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

	result, cacheable, compiled, err := santacel.TryCompiled(t.Expr, c)
	if err != nil {
		t.Error, t.Compiled = err.Error(), compiled
		t.Where = whereInExpr(t.Expr, santacel.Positions(err))
		return ""
	}
	t.Result, t.Cacheable = result, cacheable
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

const (
	// maxWhere bounds the places shown: an expression with many errors is
	// fixed from the first few.
	maxWhere = 3
	// whereBefore and whereAfter are how much of a long line is shown around
	// an error, so the caret stays on screen without scrolling sideways.
	whereBefore = 60
	whereAfter  = 40
)

// whereInExpr shows each place a compile error names. The expression box has
// no line or column numbers, so "line 2, column 14" alone leaves the person
// counting characters.
func whereInExpr(expr string, at []santacel.Position) []celWhere {
	if len(at) == 0 {
		return nil
	}
	lines := strings.Split(expr, "\n")
	var where []celWhere
	for _, p := range at[:min(len(at), maxWhere)] {
		if p.Line < 1 || p.Line > len(lines) {
			continue
		}
		line := []rune(strings.TrimSuffix(lines[p.Line-1], "\r"))
		col := min(max(p.Column-1, 0), len(line))
		start, end := max(col-whereBefore, 0), min(col+whereAfter, len(line))
		// Escaped as the page will show it, so the caret counts the
		// characters actually printed before it.
		before, after := textsafe.String(string(line[start:col])), textsafe.String(string(line[col:end]))
		if start > 0 {
			before = "..." + before
		}
		if end < len(line) {
			after += "..."
		}
		where = append(where, celWhere{Line: before + after, Caret: strings.Repeat(" ", utf8.RuneCountInString(before)) + "^"})
	}
	return where
}
