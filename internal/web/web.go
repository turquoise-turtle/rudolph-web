// Package web serves rudolph-web's server-rendered pages.
package web

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/clicmd"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// DefaultPageSize is used when Options.PageSize is zero.
const DefaultPageSize = 50

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Options configures the web handler.
type Options struct {
	Auth     *auth.Authenticator
	Store    store.Reader
	PageSize int              // 1..store.MaxPageSize; 0 means DefaultPageSize
	Now      func() time.Time // optional; for tests
	Logger   *slog.Logger     // optional
	// CLIEnv is the Rudolph environment the copy-and-paste CLI commands name
	// (ENV=<CLIEnv>); empty leaves ENV to the operator's shell.
	CLIEnv string
}

type server struct {
	auth     *auth.Authenticator
	store    store.Reader
	pageSize int
	now      func() time.Time
	log      *slog.Logger
	pages    map[string]*template.Template
}

// New returns the complete application handler: auth routes, static assets
// and role-protected pages, wrapped with security headers and cross-origin
// protection for unsafe methods.
func New(opts Options) (http.Handler, error) {
	if opts.Auth == nil {
		return nil, errors.New("web: Auth is required")
	}
	if opts.Store == nil {
		return nil, errors.New("web: Store is required")
	}
	if opts.PageSize == 0 {
		opts.PageSize = DefaultPageSize
	}
	if opts.PageSize < 1 || opts.PageSize > store.MaxPageSize {
		return nil, fmt.Errorf("web: PageSize must be 1..%d", store.MaxPageSize)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	pages, err := parseTemplates(cliFuncs(clicmd.New(opts.CLIEnv)))
	if err != nil {
		return nil, err
	}
	s := &server{
		auth:     opts.Auth,
		store:    opts.Store,
		pageSize: opts.PageSize,
		now:      opts.Now,
		log:      opts.Logger,
		pages:    pages,
	}

	mux := http.NewServeMux()
	opts.Auth.Mount(mux)

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	// Only known files are served, one path segment deep: no directory
	// listings, no redirects into the tree.
	mux.Handle("GET /static/{file}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		data, err := fs.ReadFile(static, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	}))

	viewer := func(h http.HandlerFunc) http.Handler { return opts.Auth.Require(auth.RoleViewer, h) }
	mux.Handle("GET /{$}", viewer(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/machines", http.StatusFound)
	}))
	mux.Handle("GET /machines", viewer(s.machines))
	mux.Handle("GET /machines/{id}", viewer(s.machineDetail))
	mux.Handle("GET /rules", viewer(s.rules))
	mux.Handle("GET /groups", viewer(s.groups))
	mux.Handle("GET /groups/{name}", viewer(s.groupDetail))
	mux.Handle("GET /events", viewer(s.events))
	mux.Handle("GET /events/top-blocked", viewer(s.topBlocked))
	mux.Handle("GET /events/top-allowed", viewer(s.topAllowed))
	mux.Handle("GET /admin", opts.Auth.Require(auth.RoleAdmin, http.HandlerFunc(s.admin)))

	csp := strings.Join([]string{
		"default-src 'none'",
		"script-src 'none'",
		"style-src 'self'",
		"img-src 'self'",
		// Logout POSTs to self and is then redirected to Entra's end-session endpoint.
		"form-action 'self' " + opts.Auth.IssuerOrigin(),
		"frame-ancestors 'none'",
		"base-uri 'none'",
	}, "; ")
	// Unsafe methods must come from this site. Behind CloudFront or a custom
	// domain the Lambda sees its own host, so the browser-visible origin (the
	// redirect URL's) is trusted as well; every other cross-origin request is
	// rejected. The header middleware wraps the protection, so rejections
	// carry the same headers as any other response.
	protection := http.NewCrossOriginProtection()
	if o := opts.Auth.RedirectOrigin(); o != "" {
		if err := protection.AddTrustedOrigin(o); err != nil {
			return nil, fmt.Errorf("web: trusted origin: %w", err)
		}
	}
	return securityHeaders(csp, protection.Handler(mux)), nil
}

func securityHeaders(csp string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Rendering

type layoutData struct {
	Title   string
	Nav     string
	User    auth.Session
	IsAdmin bool
	Page    any
}

var funcs = template.FuncMap{
	"ts": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return timeText(t)
	},
	// The machine list's last seen is a UTC date (Rudolph's LastSeenDay), not
	// a time, so showing midnight would be misleading.
	"day": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.UTC().Format(time.DateOnly)
	},
	"machineURL": func(id string) string { return "/machines/" + url.PathEscape(id) },
	"eventsForMachineURL": func(id string) string {
		return "/events?" + url.Values{"machine": {id}}.Encode()
	},
	"legacySanta": store.LegacySantaVersion,
	"groupURL":    groupURL,
	"groupLabel":  groupLabel,
	"ungrouped":   func() string { return store.UngroupedGroup },
	"orDash": func(s string) string {
		if s == "" {
			return "—"
		}
		return s
	},
}

// cliCommand is one labelled command in a "Commands" disclosure.
type cliCommand struct {
	Label   string
	Command string
}

// cliFuncs gives the templates the copy-and-paste CLI commands. Each returns
// "" when no command can be built (an identifier of the wrong shape), and the
// templates then show nothing.
func cliFuncs(b clicmd.Builder) template.FuncMap {
	str := func(cmd string, ok bool) string {
		if !ok {
			return ""
		}
		return cmd
	}
	// Machine rules made from the web view are for a quick unblock, so they
	// expire after a day.
	const machineRuleExpiry = "24h"
	return template.FuncMap{
		"cliRuleGlobal": func(verb string, t store.RuleType, id string) string {
			return str(b.Rule(verb, t, id, clicmd.Global()))
		},
		"cliRuleMachine": func(verb string, t store.RuleType, id, machine string) string {
			return str(b.Rule(verb, t, id, clicmd.Machine(machine, machineRuleExpiry)))
		},
		"cliRuleGroup": func(verb string, t store.RuleType, id, group string) string {
			return str(b.Rule(verb, t, id, clicmd.Group(group)))
		},
		"cliRemoveGlobal": func(t store.RuleType, id string) string {
			return str(b.Remove(t, id, clicmd.Global()))
		},
		"cliRemoveMachine": func(t store.RuleType, id, machine string) string {
			return str(b.Remove(t, id, clicmd.Machine(machine, "")))
		},
		"cmd":             func(label, command string) cliCommand { return cliCommand{label, command} },
		"binary":          func() store.RuleType { return store.RuleTypeBinary },
		"cliMachineRules": func(machine string) string { return str(b.MachineRules(machine)) },
		"cliConfigGet":    func(machine string) string { return str(b.ConfigGet(machine)) },
		"cliEndTemporary": func(machine string) string { return str(b.EndTemporaryMode(machine)) },
		"cliMonitorFor2h": func(machine string) string { return str(b.TemporaryMode(machine, store.ClientModeMonitor, "2h")) },
	}
}

func parseTemplates(extra template.FuncMap) (map[string]*template.Template, error) {
	base, err := template.New("layout.html").Funcs(funcs).Funcs(extra).ParseFS(templateFS, "templates/layout.html")
	if err != nil {
		return nil, err
	}
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	pages := map[string]*template.Template{}
	for _, name := range names {
		short := strings.TrimSuffix(strings.TrimPrefix(name, "templates/"), ".html")
		if short == "layout" {
			continue
		}
		t, err := template.Must(base.Clone()).ParseFS(templateFS, name)
		if err != nil {
			return nil, err
		}
		pages[short] = t
	}
	return pages, nil
}

func (s *server) render(w http.ResponseWriter, r *http.Request, status int, page, title, nav string, data any) {
	sess, _ := auth.SessionFromContext(r.Context())
	var buf bytes.Buffer
	err := s.pages[page].ExecuteTemplate(&buf, "layout.html", layoutData{
		Title: title, Nav: nav, User: sess, IsAdmin: sess.HasRole(auth.RoleAdmin), Page: data,
	})
	if err != nil {
		s.log.Error("render failed", "page", page, "err", err)
		http.Error(w, "Internal error.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *server) renderError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.render(w, r, status, "error", http.StatusText(status), "", struct {
		Status  int
		Message string
	}{status, msg})
}

// storeError maps data-layer errors to responses without leaking details.
func (s *server) storeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrInvalidCursor):
		s.renderError(w, r, http.StatusBadRequest, "This page link is invalid or has expired. Start again from the first page.")
	case errors.Is(err, store.ErrInvalidArgument):
		s.renderError(w, r, http.StatusBadRequest, "Invalid filter.")
	case errors.Is(err, store.ErrNotFound):
		s.renderError(w, r, http.StatusNotFound, "Not found.")
	case errors.Is(err, store.ErrUnavailable):
		s.renderError(w, r, http.StatusServiceUnavailable, "The data store is not available.")
	default:
		s.log.Error("store error", "path", r.URL.Path, "err", err)
		s.renderError(w, r, http.StatusInternalServerError, "Something went wrong while reading data.")
	}
}
