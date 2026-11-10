package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// Group management (owner request), as rudolph group create|update|delete|
// add|remove do. The store normalises and checks names with Rudolph's own
// rules; the handlers only bound what the forms send.

// groupDescription reads a group's description from a form, refusing one
// longer than a rule's.
func (s *server) groupDescription(w http.ResponseWriter, r *http.Request) (string, bool) {
	d := strings.TrimSpace(r.PostForm.Get("description"))
	if len(d) > maxDescriptionBytes {
		s.renderError(w, r, http.StatusBadRequest, "Not saved: the description is longer than "+strconv.Itoa(maxDescriptionBytes)+" bytes.")
		return "", false
	}
	return d, true
}

// ticked reports whether a form's checkbox was ticked; an unticked box is
// not sent at all.
func ticked(r *http.Request, name string) bool { return r.PostForm.Get(name) == "on" }

func (s *server) createGroup(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	desc, ok := s.groupDescription(w, r)
	if !ok {
		return
	}
	name, err := s.writer.CreateGroup(r.Context(), actorOf(r), r.PostForm.Get("name"), desc, ticked(r, "accept_reported"))
	if err != nil {
		s.writeError(w, r, err, "")
		return
	}
	http.Redirect(w, r, groupURL(name), http.StatusSeeOther)
}

func (s *server) updateGroup(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	desc, ok := s.groupDescription(w, r)
	if !ok {
		return
	}
	name := r.PostForm.Get("name")
	accept := ticked(r, "accept_reported")
	err := s.writer.UpdateGroup(r.Context(), actorOf(r), name, &desc, &accept)
	// The form always sends both fields, so saving it unchanged is not a mistake.
	if err != nil && !errors.Is(err, store.ErrNothingToChange) {
		s.writeError(w, r, err, "Group not found: it may have been deleted.")
		return
	}
	http.Redirect(w, r, groupURL(name), http.StatusSeeOther)
}

func (s *server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	if err := s.writer.DeleteGroup(r.Context(), actorOf(r), r.PostForm.Get("name"), ticked(r, "force")); err != nil {
		s.writeError(w, r, err, "Group not found: it may have been deleted already.")
		return
	}
	http.Redirect(w, r, "/groups", http.StatusSeeOther)
}

func (s *server) assignGroup(w http.ResponseWriter, r *http.Request) {
	s.changeAssigned(w, r, s.writer.AssignGroup)
}

func (s *server) unassignGroup(w http.ResponseWriter, r *http.Request) {
	s.changeAssigned(w, r, s.writer.UnassignGroup)
}

func (s *server) changeAssigned(w http.ResponseWriter, r *http.Request, change func(context.Context, store.Actor, string, string) error) {
	if !s.parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := change(r.Context(), actorOf(r), id, r.PostForm.Get("group")); err != nil {
		s.writeError(w, r, err, "Machine not found.")
		return
	}
	http.Redirect(w, r, "/machines/"+url.PathEscape(id), http.StatusSeeOther)
}

// machineGroupForms is what a Mac's page offers an admin for its groups.
type machineGroupForms struct {
	// NewGroups are the names the Mac reports, as sent, that would make a
	// valid group but are not registered (one per normalised name).
	NewGroups []string
	// Assignable are the registered groups not assigned to the Mac.
	Assignable []string
}

// machineGroupForms reads the registered groups for a Mac's group forms. The
// page is still useful without them, so a failure only leaves them out.
func (s *server) machineGroupForms(r *http.Request, m store.MachineDetail) machineGroupForms {
	registered, err := s.store.ListGroups(r.Context())
	if err != nil {
		s.log.Warn("reading the groups for the machine's group forms failed", "err", err)
		return machineGroupForms{}
	}
	names := map[string]bool{}
	var f machineGroupForms
	for _, g := range registered {
		names[g.Name] = true
		if g.Name != store.UngroupedGroup && !slices.Contains(m.AssignedGroups, g.Name) {
			f.Assignable = append(f.Assignable, g.Name)
		}
	}
	for _, sent := range m.ReportedAsSent {
		n, err := rudolphrules.GroupName(sent)
		if err != nil || names[n] {
			continue
		}
		names[n] = true
		f.NewGroups = append(f.NewGroups, sent)
	}
	return f
}
