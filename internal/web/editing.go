package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// Admin writes (plan F2). Every form is plain HTML (the CSP allows no
// script), posts to /admin/..., and redirects back with 303 to the page it
// came from. Validation is the store's, which is Rudolph's own; the handlers
// only check what the forms offer.

const (
	// maxFormBytes bounds a form body; the largest field, a custom message,
	// is at most 4 KiB in Rudolph.
	maxFormBytes = 16 << 10
	// maxDescriptionBytes bounds a rule's description, which Rudolph stores
	// but never sends to Santa.
	maxDescriptionBytes = 1024

	// defaultMachineRuleExpiry is the CLI's default for a machine rule.
	defaultMachineRuleExpiry = 24 * time.Hour
	// Rudolph's limits for a temporary client mode
	// (machineconfiguration.Min/MaxTemporaryClientModeDuration).
	minTemporaryMode = time.Minute
	maxTemporaryMode = 30 * 24 * time.Hour

	auditWindow = 30 * 24 * time.Hour
)

// canEdit reports whether the page should show edit forms: editing is on and
// the signed-in user is an admin.
func (s *server) canEdit(r *http.Request) bool {
	sess, _ := auth.SessionFromContext(r.Context())
	return s.writer != nil && sess.HasRole(auth.RoleAdmin)
}

// editForms is what a page needs to show its edit forms. On is false for
// viewers and when editing is off, and the page then shows none.
type editForms struct {
	On          bool
	RuleTypes   []store.RuleType
	Policies    []store.Policy
	ClientModes []store.ClientMode
}

func (s *server) editForms(r *http.Request) editForms {
	if !s.canEdit(r) {
		return editForms{}
	}
	return editForms{On: true, RuleTypes: store.RuleTypes, Policies: store.AddablePolicies, ClientModes: store.ClientModes}
}

// ruleFormData fills the "addrule" and "removerule" templates.
type ruleFormData struct {
	Title   string
	Target  string // global, machine or group
	Machine string
	Group   string
	Edit    editForms
	Rule    store.Rule
}

var editFuncs = map[string]any{
	"addRuleForm": func(title, target, id string, e editForms) ruleFormData {
		d := ruleFormData{Title: title, Target: target, Edit: e}
		d.setID(id)
		return d
	},
	"removeRuleForm": func(target, id string, r store.Rule) ruleFormData {
		d := ruleFormData{Target: target, Rule: r}
		d.setID(id)
		return d
	},
}

func (d *ruleFormData) setID(id string) {
	switch d.Target {
	case "machine":
		d.Machine = id
	case "group":
		d.Group = id
	}
}

func actorOf(r *http.Request) store.Actor {
	sess, _ := auth.SessionFromContext(r.Context())
	return store.Actor{Username: sess.Username, Name: sess.Name, Subject: sess.Subject}
}

func (s *server) parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "The form could not be read.")
		return false
	}
	return true
}

// parseDuration reads a Go duration such as 90m or 2h, or a whole number of
// days such as 7d.
func parseDuration(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 || n > 3650 {
			return 0, false
		}
		return time.Duration(n) * 24 * time.Hour, true
	}
	d, err := time.ParseDuration(v)
	return d, err == nil
}

// formTarget reads the rule target of a form and the page to go back to.
func formTarget(f url.Values) (store.RuleTarget, string, bool) {
	switch f.Get("target") {
	case "global":
		return store.RuleTarget{Kind: store.TargetGlobal}, "/rules", true
	case "machine":
		id := strings.TrimSpace(f.Get("machine"))
		return store.RuleTarget{Kind: store.TargetMachine, MachineID: id}, "/machines/" + url.PathEscape(id), true
	case "group":
		name := f.Get("group")
		return store.RuleTarget{Kind: store.TargetGroup, Group: name}, groupURL(name), true
	}
	return store.RuleTarget{}, "", false
}

func (s *server) addRule(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	f := r.PostForm
	target, back, ok := formTarget(f)
	if !ok {
		s.renderError(w, r, http.StatusBadRequest, "Not saved: unknown target; choose global, a machine or a group.")
		return
	}
	rule := store.NewRule{
		RuleType:      store.RuleType(f.Get("type")),
		Identifier:    strings.TrimSpace(f.Get("identifier")),
		Policy:        store.Policy(f.Get("policy")),
		CustomMessage: strings.TrimSpace(f.Get("custom_message")),
		Description:   strings.TrimSpace(f.Get("description")),
	}
	if !slices.Contains(store.RuleTypes, rule.RuleType) {
		s.renderError(w, r, http.StatusBadRequest, "Not saved: unknown rule type.")
		return
	}
	if !slices.Contains(store.AddablePolicies, rule.Policy) {
		s.renderError(w, r, http.StatusBadRequest, "Not saved: a rule cannot be added with this policy. To take a rule away, remove it.")
		return
	}
	if len(rule.Description) > maxDescriptionBytes {
		s.renderError(w, r, http.StatusBadRequest, "Not saved: the description is longer than "+strconv.Itoa(maxDescriptionBytes)+" bytes.")
		return
	}
	expires := strings.TrimSpace(f.Get("expires"))
	switch {
	case target.Kind != store.TargetMachine && expires != "":
		s.renderError(w, r, http.StatusBadRequest, "Not saved: only machine rules expire.")
		return
	case target.Kind == store.TargetMachine && expires == "never":
	case target.Kind == store.TargetMachine:
		d := defaultMachineRuleExpiry
		if expires != "" {
			var ok bool
			if d, ok = parseDuration(expires); !ok || d <= 0 {
				s.renderError(w, r, http.StatusBadRequest, "Not saved: expires must be a duration such as 2h or 7d, or never.")
				return
			}
		}
		rule.ExpiresAt = s.now().Add(d).UTC()
	}
	if err := s.writer.AddRule(r.Context(), actorOf(r), target, rule); err != nil {
		s.writeError(w, r, err, "Not saved: Rudolph has nothing for this machine (no sync, configuration or rules), so a rule for it would never reach a Mac.")
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *server) removeRule(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	f := r.PostForm
	target, back, ok := formTarget(f)
	if !ok {
		s.renderError(w, r, http.StatusBadRequest, "Not removed: unknown target.")
		return
	}
	ruleType := store.RuleType(f.Get("type"))
	if !slices.Contains(store.RuleTypes, ruleType) {
		s.renderError(w, r, http.StatusBadRequest, "Not removed: unknown rule type.")
		return
	}
	err := s.writer.RemoveRule(r.Context(), actorOf(r), target, ruleType, strings.TrimSpace(f.Get("identifier")))
	if err != nil {
		s.writeError(w, r, err, "There is no such rule to remove: it may have been removed already, or Rudolph has nothing for this machine.")
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *server) setTemporaryMode(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	if _, err := s.store.GetMachine(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.renderError(w, r, http.StatusNotFound, "Machine not found.")
			return
		}
		s.storeError(w, r, err)
		return
	}
	mode := store.ClientMode(r.PostForm.Get("mode"))
	if !slices.Contains(store.ClientModes, mode) {
		s.renderError(w, r, http.StatusBadRequest, "Not saved: unknown client mode.")
		return
	}
	d, ok := parseDuration(r.PostForm.Get("for"))
	if !ok || d < minTemporaryMode || d > maxTemporaryMode {
		s.renderError(w, r, http.StatusBadRequest, "Not saved: for must be a duration from 1m to 30d, such as 2h or 3d.")
		return
	}
	if err := s.writer.SetTemporaryMode(r.Context(), actorOf(r), id, mode, s.now().Add(d).UTC()); err != nil {
		s.writeError(w, r, err, "Machine not found.")
		return
	}
	http.Redirect(w, r, "/machines/"+url.PathEscape(id), http.StatusSeeOther)
}

func (s *server) endTemporaryMode(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.writer.EndTemporaryMode(r.Context(), actorOf(r), id); err != nil {
		s.writeError(w, r, err, "Machine not found.")
		return
	}
	http.Redirect(w, r, "/machines/"+url.PathEscape(id), http.StatusSeeOther)
}

// auditRow is one audit entry, with a link to what it changed when that has
// a page.
type auditRow struct {
	store.AuditEntry
	TargetURL string
}

func (s *server) audit(w http.ResponseWriter, r *http.Request) {
	pr := s.pageRequest(r.URL.Query())
	page, err := s.writer.ListAuditPage(r.Context(), s.now().Add(-auditWindow), pr)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	rows := make([]auditRow, len(page.Items))
	for i, e := range page.Items {
		rows[i] = auditRow{AuditEntry: e}
		switch {
		case e.Target == "global":
			rows[i].TargetURL = "/rules"
		case e.Target == "ungrouped":
			rows[i].TargetURL = groupURL(store.UngroupedGroup)
		case strings.HasPrefix(e.Target, "machine:"):
			rows[i].TargetURL = "/machines/" + url.PathEscape(strings.TrimPrefix(e.Target, "machine:"))
		case strings.HasPrefix(e.Target, "group:"):
			rows[i].TargetURL = groupURL(strings.TrimPrefix(e.Target, "group:"))
		}
	}
	s.render(w, r, http.StatusOK, "audit", "Audit trail", "admin", struct {
		Entries []auditRow
		Days    int
		Pager   pager
	}{rows, int(auditWindow / (24 * time.Hour)), links("/admin/audit", url.Values{}, pr.Cursor, page.NextCursor)})
}

// writeError maps a write's error to a response. Invalid input is the
// person's to fix, so its message (Rudolph's validation) is shown; anything
// else is logged and described without details.
func (s *server) writeError(w http.ResponseWriter, r *http.Request, err error, notFound string) {
	// Not audited comes first: a write that failed part-way and could not be
	// audited either may also carry an invalid-argument error, and the
	// unaudited change is what the person must hear about.
	switch {
	case errors.Is(err, store.ErrNotAudited):
		s.log.Error("write not audited", "path", r.URL.Path, "err", err)
		s.renderError(w, r, http.StatusInternalServerError, "The change was saved, at least in part, but it could not be recorded in the audit trail. Tell whoever looks after Rudolph.")
	case errors.Is(err, store.ErrInvalidArgument):
		// The store's own messages, from Rudolph's validation; never an AWS error.
		msg := strings.TrimPrefix(err.Error(), store.ErrInvalidArgument.Error()+": ")
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		s.renderError(w, r, http.StatusBadRequest, "Not saved: "+msg)
	case errors.Is(err, store.ErrNotFound):
		if notFound == "" {
			notFound = "Not found."
		}
		s.renderError(w, r, http.StatusNotFound, notFound)
	case errors.Is(err, store.ErrUnavailable):
		s.renderError(w, r, http.StatusServiceUnavailable, "The data store is not available.")
	default:
		s.log.Error("write failed", "path", r.URL.Path, "err", err)
		s.renderError(w, r, http.StatusInternalServerError, "Something went wrong while saving. Reload the page to see whether anything changed.")
	}
}
