package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// maxMoveTargetBytes bounds the new Mac field: a machine ID is 36 bytes and a
// serial number far shorter.
const maxMoveTargetBytes = 128

// errAmbiguousSerial is findMachine's answer when more than one Mac reports
// the serial number.
var errAmbiguousSerial = errors.New("more than one Mac has this serial number")

// moveMachineRules moves a Mac's rules in force to another Mac, for when its
// user changes computers.
func (s *server) moveMachineRules(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	from := r.PathValue("id")
	if _, err := s.store.GetMachine(r.Context(), from); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.renderError(w, r, http.StatusNotFound, "Machine not found.")
			return
		}
		s.storeError(w, r, err)
		return
	}
	to := strings.TrimSpace(r.PostForm.Get("to"))
	switch {
	case to == "":
		s.renderError(w, r, http.StatusBadRequest, "Not moved: enter the new Mac's serial number or machine ID.")
		return
	case len(to) > maxMoveTargetBytes:
		s.renderError(w, r, http.StatusBadRequest, "Not moved: that is not a serial number or machine ID.")
		return
	}
	newID, err := s.findMachine(r.Context(), to)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.renderError(w, r, http.StatusBadRequest, fmt.Sprintf("Not moved: no Mac has the serial number or machine ID %q.", to))
		return
	case errors.Is(err, errAmbiguousSerial):
		s.renderError(w, r, http.StatusBadRequest, fmt.Sprintf("Not moved: more than one Mac has the serial number %q; enter the new Mac's machine ID instead.", to))
		return
	case err != nil:
		s.storeError(w, r, err)
		return
	}
	if strings.EqualFold(newID, from) {
		s.renderError(w, r, http.StatusBadRequest, "Not moved: that is this Mac; enter the new Mac's serial number or machine ID.")
		return
	}
	if _, err := s.writer.MoveMachineRules(r.Context(), actorOf(r), from, newID, ticked(r, "remove")); err != nil {
		s.writeError(w, r, err, "Machine not found: Rudolph has nothing for one of the two Macs.")
		return
	}
	http.Redirect(w, r, "/machines/"+url.PathEscape(newID), http.StatusSeeOther)
}

// findMachine finds the Mac a person typed: by machine ID, as stored in any
// of the spellings Rudolph may hold it under, or else by its exact serial
// number on the serial index, ignoring case either way.
func (s *server) findMachine(ctx context.Context, typed string) (string, error) {
	tried := map[string]bool{}
	for _, candidate := range []string{typed, strings.ToUpper(typed), strings.ToLower(typed)} {
		if tried[candidate] {
			continue
		}
		tried[candidate] = true
		_, err := s.store.GetMachine(ctx, candidate)
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}
	// The serial index is ordered by serial number, so an exact match comes
	// before every longer serial that starts with it: the first page holds it.
	page, err := s.store.ListMachines(ctx, store.MachineFilter{SerialPrefix: typed}, store.PageRequest{Limit: 10})
	if err != nil {
		return "", err
	}
	var found []string
	for _, m := range page.Items {
		if strings.EqualFold(strings.TrimSpace(m.SerialNum), typed) {
			found = append(found, m.MachineID)
		}
	}
	switch len(found) {
	case 0:
		return "", store.ErrNotFound
	case 1:
		return found[0], nil
	}
	return "", errAmbiguousSerial
}
