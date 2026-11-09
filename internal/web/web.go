// Package web serves rudolph-web's server-rendered pages.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
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

// localTimeScript rewrites each <time datetime> element's text to show the time in the browser's zone beside UTC. No
// request header carries the browser's zone, so this is the one script the pages run; the CSP allows it by hash
// only. It is kept out of static so it is never served as a file the CSP would have to allow.
//
//go:embed localtime.js
var localTimeScript string

// localTimeScriptHash is the CSP source for localTimeScript. The layout inserts the script as template.JS, which
// html/template writes unchanged, so the hash matches the bytes the browser sees.
var localTimeScriptHash = func() string {
	sum := sha256.Sum256([]byte(localTimeScript))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

// staticFile is one embedded asset as served under a single URL.
type staticFile struct {
	name         string // the file's real name, which ServeContent uses for the content type
	data         []byte
	etag         string
	cacheControl string
}

// staticFiles maps each servable name to its file, and staticURLs maps each file's real name to the URL pages link.
//
// CloudFront's CachingOptimized policy ignores query strings, so a ?v= suffix would not reach the cache key; the
// content hash goes in the file name instead. That URL changes whenever the file does, so it can be cached for good
// and a deploy needs no invalidation. Any other hash is not served: caching it for good would pin whatever it
// returned. The plain name still works (old bookmarks, the cached HTML of an open tab) but is revalidated every time.
var staticFiles, staticURLs = loadStatic()

func loadStatic() (map[string]staticFile, map[string]string) {
	entries, err := fs.ReadDir(staticFS, "static")
	if err != nil {
		panic(err)
	}
	files := map[string]staticFile{}
	urls := map[string]string{}
	for _, e := range entries {
		// Only one path segment is ever served, so a subdirectory's files would be unreachable anyway.
		if e.IsDir() {
			continue
		}
		name := e.Name()
		data, err := fs.ReadFile(staticFS, "static/"+name)
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])[:16]
		etag := `"` + digest + `"`
		ext := path.Ext(name)
		hashed := strings.TrimSuffix(name, ext) + "." + digest + ext
		files[name] = staticFile{name: name, data: data, etag: etag, cacheControl: "no-cache"}
		files[hashed] = staticFile{name: name, data: data, etag: etag, cacheControl: "public, max-age=31536000, immutable"}
		urls[name] = "/static/" + hashed
	}
	return files, urls
}

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
	// TopListStaleAfter is how old a top list may be before its page warns
	// that the ranker may have stopped; 0 means DefaultTopListStaleAfter.
	TopListStaleAfter time.Duration
	// Writer lets Rudolph.Admin add and remove rules and set temporary client
	// modes, and shows them the audit trail. Nil keeps the view read-only:
	// no forms are shown and the write routes do not exist.
	Writer store.Writer
}

type server struct {
	auth     *auth.Authenticator
	store    store.Reader
	pageSize int
	now      func() time.Time
	log      *slog.Logger
	pages    map[string]*template.Template

	topListStaleAfter time.Duration
	writer            store.Writer
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
	if opts.TopListStaleAfter == 0 {
		opts.TopListStaleAfter = DefaultTopListStaleAfter
	}
	if opts.TopListStaleAfter < 0 {
		return nil, errors.New("web: TopListStaleAfter must not be negative")
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

		topListStaleAfter: opts.TopListStaleAfter,
		writer:            opts.Writer,
	}

	mux := http.NewServeMux()
	opts.Auth.Mount(mux)

	// Only known files are served, one path segment deep: no directory
	// listings, no redirects into the tree.
	mux.Handle("GET /static/{file}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := staticFiles[r.PathValue("file")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", f.cacheControl)
		w.Header().Set("ETag", f.etag)
		http.ServeContent(w, r, f.name, time.Time{}, bytes.NewReader(f.data))
	}))

	viewer := func(h http.HandlerFunc) http.Handler { return opts.Auth.RequireOr(auth.RoleViewer, h, s.forbidden) }
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
	admin := func(h http.HandlerFunc) http.Handler { return opts.Auth.RequireOr(auth.RoleAdmin, h, s.forbidden) }
	mux.Handle("GET /admin", admin(s.admin))
	if s.writer != nil {
		// Every POST passes the cross-origin protection below before it gets
		// here, and only an admin's reaches the handler.
		mux.Handle("POST /admin/rules", admin(s.addRule))
		mux.Handle("POST /admin/rules/remove", admin(s.removeRule))
		mux.Handle("POST /admin/machines/{id}/temporary-mode", admin(s.setTemporaryMode))
		mux.Handle("POST /admin/machines/{id}/temporary-mode/end", admin(s.endTemporaryMode))
		mux.Handle("GET /admin/audit", admin(s.audit))
	}

	csp := strings.Join([]string{
		"default-src 'none'",
		"script-src " + localTimeScriptHash,
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
	return securityHeaders(csp, protection.Handler(s.notFound(mux))), nil
}

// notFound gives a signed-in user the site's 404 page, like the 403, for a path the mux has no route for (including
// the write routes and /admin/audit when editing is off). Without a session the mux's plain 404 stays, so nothing of
// the site shows before sign-in. Only unmatched requests are considered: a matched route's own 404s (an unknown static
// file, a missing machine) are its business, and the mux's 405 for a known path with the wrong method passes through
// with its Allow header.
func (s *server) notFound(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern != "" || strings.HasPrefix(r.URL.Path, "/static/") {
			mux.ServeHTTP(w, r)
			return
		}
		sr, ok := s.auth.WithSession(r)
		if !ok {
			mux.ServeHTTP(w, r)
			return
		}
		// The mux does not say whether an unmatched request is a 404 or a 405, only by the status it writes.
		mux.ServeHTTP(&notFoundWriter{ResponseWriter: w, render: func(w http.ResponseWriter) {
			s.renderError(w, sr, http.StatusNotFound, "There is no page at this address.")
		}}, r)
	})
}

// notFoundWriter replaces a 404 written through it with render's response and discards the 404's own body. Any other
// status passes through unchanged.
type notFoundWriter struct {
	http.ResponseWriter
	render   func(http.ResponseWriter)
	replaced bool
}

func (w *notFoundWriter) WriteHeader(code int) {
	if code == http.StatusNotFound && !w.replaced {
		w.replaced = true
		w.render(w.ResponseWriter)
		return
	}
	if w.replaced {
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *notFoundWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *notFoundWriter) Write(b []byte) (int, error) {
	if w.replaced {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
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
	// The machine list's last seen is a UTC date (Rudolph's LastSeenDay), not
	// a time, so showing midnight would be misleading.
	"day": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.UTC().Format(time.DateOnly)
	},
	// The layout's one script, written unchanged (see localTimeScriptHash).
	"localTimeScript": func() template.JS { return template.JS(localTimeScript) },
	// The content-hashed URL of an embedded static file (see staticFiles). An unknown name fails the render rather
	// than linking a 404.
	"static": func(name string) (string, error) {
		u, ok := staticURLs[name]
		if !ok {
			return "", fmt.Errorf("web: no static file %q", name)
		}
		return u, nil
	},
	"machineURL": func(id string) string { return "/machines/" + url.PathEscape(id) },
	"eventsForMachineURL": func(id string) string {
		return "/events?" + url.Values{"machine": {id}}.Encode()
	},
	"legacySanta":    store.LegacySantaVersion,
	"groupURL":       groupURL,
	"groupLabel":     groupLabel,
	"ungrouped":      func() string { return store.UngroupedGroup },
	"addRuleForm":    editFuncs["addRuleForm"],
	"removeRuleForm": editFuncs["removeRuleForm"],
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

// cliCommandGroup is the commands for one rule type a table row can be
// matched by, shown as its own disclosure inside "Commands".
type cliCommandGroup struct {
	Label      string
	Identifier string
	Commands   []cliCommand
}

// ruleTypeChoice is a rule type a row offers and the row's identifier for it.
type ruleTypeChoice struct {
	label string
	t     store.RuleType
	id    string
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
	// groups builds one group per rule type, in the order given, leaving out
	// a type whose identifier the CLI would refuse (the builder checks each
	// type's shape) so the page never shows a command that fails.
	groups := func(choices []ruleTypeChoice, build func(t store.RuleType, id string) []cliCommand) []cliCommandGroup {
		var out []cliCommandGroup
		for _, c := range choices {
			var cmds []cliCommand
			for _, cc := range build(c.t, c.id) {
				if cc.Command != "" {
					cmds = append(cmds, cc)
				}
			}
			if len(cmds) > 0 {
				out = append(out, cliCommandGroup{Label: c.label, Identifier: c.id, Commands: cmds})
			}
		}
		return out
	}
	global := func(verb string, t store.RuleType, id string) string {
		return str(b.Rule(verb, t, id, clicmd.Global()))
	}
	return template.FuncMap{
		// Most useful first: a signing ID rule survives updates and is
		// narrower than a team ID; a CDHash or binary rule breaks on the next
		// update, so it is the last resort.
		"eventCmdGroups": func(signingID, teamID, cdhash, sha256, machine string) []cliCommandGroup {
			return groups([]ruleTypeChoice{
				{"Signing ID", store.RuleTypeSigningID, signingID},
				{"Team ID", store.RuleTypeTeamID, teamID},
				{"CDHash", store.RuleTypeCDHash, cdhash},
				{"Binary", store.RuleTypeBinary, sha256},
			}, func(t store.RuleType, id string) []cliCommand {
				return []cliCommand{
					{"Allow on this Mac for 24 hours", str(b.Rule("allow", t, id, clicmd.Machine(machine, machineRuleExpiry)))},
					{"Allow everywhere", global("allow", t, id)},
					{"Block everywhere", global("deny", t, id)},
				}
			})
		},
		// Top lists span machines, so they offer global rules only.
		"topCmdGroups": func(signingID, teamID, sha256 string) []cliCommandGroup {
			return groups([]ruleTypeChoice{
				{"Signing ID", store.RuleTypeSigningID, signingID},
				{"Team ID", store.RuleTypeTeamID, teamID},
				{"Binary", store.RuleTypeBinary, sha256},
			}, func(t store.RuleType, id string) []cliCommand {
				return []cliCommand{
					{"Block everywhere", global("deny", t, id)},
					{"Allow everywhere", global("allow", t, id)},
				}
			})
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
		"cliMachineRules": func(machine string) string { return str(b.MachineRules(machine)) },
		"cliConfigGet":    func(machine string) string { return str(b.ConfigGet(machine)) },
		"cliEndTemporary": func(machine string) string { return str(b.EndTemporaryMode(machine)) },
		"cliMonitorFor2h": func(machine string) string { return str(b.TemporaryMode(machine, store.ClientModeMonitor, "2h")) },
	}
}

func parseTemplates(extra ...template.FuncMap) (map[string]*template.Template, error) {
	base := template.New("layout.html").Funcs(funcs)
	for _, fm := range extra {
		base = base.Funcs(fm)
	}
	base, err := base.ParseFS(templateFS, "templates/layout.html")
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
	title := fmt.Sprintf("%d %s", status, http.StatusText(status))
	s.render(w, r, status, "error", title, "", struct {
		Status  int
		Message string
	}{status, msg})
}

// forbidden renders the 403 for a signed-in user without the route's role in
// the site's layout, so they still see who they are signed in as and can sign
// out and try another account.
func (s *server) forbidden(w http.ResponseWriter, r *http.Request, role string) {
	s.renderError(w, r, http.StatusForbidden, auth.ForbiddenMessage(role))
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
