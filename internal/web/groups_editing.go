package web

import (
	"context"
	"errors"
	"fmt"
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

// machineGroupFormsOf works out a Mac's group forms from the registered
// groups.
func machineGroupFormsOf(registered []store.Group, m store.MachineDetail) machineGroupForms {
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

// groupModeOptions are the choices of a group's client mode form. Unlike a
// configuration's, "not set" is always offered: it removes the group's mode.
func groupModeOptions(current store.ClientMode) []configOption {
	options := []configOption{{Value: "", Label: "not set", Selected: current == ""}}
	for _, m := range store.ClientModes {
		options = append(options, configOption{Value: string(m), Label: string(m), Selected: m == current})
	}
	return options
}

// updateGroupConfig changes a group's client mode, as rudolph config update
// --group (or --ungrouped) does. The form is the configuration form cut down
// to the one setting a group has.
func (s *server) updateGroupConfig(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	for name, values := range r.PostForm {
		switch name {
		case "name", "ClientMode", origPrefix + "ClientMode", "confirm_mode":
		default:
			s.renderError(w, r, http.StatusBadRequest, fmt.Sprintf("Not saved: %q is not a setting a group has; a group has only a client mode.", name))
			return
		}
		if len(values) != 1 {
			s.renderError(w, r, http.StatusBadRequest, fmt.Sprintf("Not saved: %s was sent more than once.", name))
			return
		}
	}
	modes, posted := r.PostForm["ClientMode"]
	if !posted {
		s.renderError(w, r, http.StatusBadRequest, "Not saved: no client mode was given.")
		return
	}
	mode := store.ClientMode(modes[0])
	if mode != "" && !slices.Contains(store.ClientModes, mode) {
		s.renderError(w, r, http.StatusBadRequest, "Not saved: unknown client mode; choose MONITOR, LOCKDOWN or STANDALONE, or not set.")
		return
	}
	// Consistent, so that the check against what the form showed sees a
	// change made a moment ago.
	registered, err := s.store.ListGroups(store.Consistent(r.Context()))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	name := r.PostForm.Get("name")
	group, ok := findGroup(registered, name)
	if !ok {
		s.renderError(w, r, http.StatusNotFound, "Group not found: it may have been deleted.")
		return
	}
	// As on the configuration forms: a mode left as the form showed it is not
	// a change, so a form drawn before someone else changed it does not put it
	// back; one changed on the form is refused when the stored mode is no
	// longer what the form showed, since saving would overwrite a change the
	// person has not seen.
	orig, hasOrig := r.PostForm[origPrefix+"ClientMode"]
	if hasOrig && mode == store.ClientMode(orig[0]) {
		http.Redirect(w, r, groupURL(name), http.StatusSeeOther)
		return
	}
	if hasOrig && mode != group.ClientMode && store.ClientMode(orig[0]) != group.ClientMode {
		s.renderError(w, r, http.StatusConflict, "Not saved: the group's client mode was changed since this page was loaded; reload it to see it as it is now, and make your change again.")
		return
	}
	if mode != group.ClientMode {
		if mode != "" && mode != store.ClientModeMonitor && !modeConfirmed(r.PostForm, string(mode)) {
			s.renderError(w, r, http.StatusBadRequest, fmt.Sprintf("Not saved: %s blocks unknown binaries on every Mac of the group without its own client mode. To confirm, type %s in the confirmation box.", mode, mode))
			return
		}
		err := s.writer.SetGroupClientMode(r.Context(), actorOf(r), name, mode)
		if err != nil && !errors.Is(err, store.ErrNothingToChange) {
			s.writeError(w, r, err, "Group not found: it may have been deleted.")
			return
		}
	}
	http.Redirect(w, r, groupURL(name), http.StatusSeeOther)
}
