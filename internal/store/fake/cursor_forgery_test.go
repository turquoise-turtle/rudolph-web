package fake_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

// Cursors are authenticated: a cursor minted by hand for the same query, or a
// genuine cursor whose key was edited, is rejected with ErrInvalidCursor even
// though it is syntactically valid.
func TestCursors_ForgedOrEditedAreRejected(t *testing.T) {
	s := fake.New()
	for i := 0; i < 9; i++ {
		s.AddMachine(store.Machine{MachineID: fmt.Sprintf("m-%d", i)})
	}

	// Hand-minted in the unauthenticated layout: base64url(JSON{q, k}).
	raw, _ := json.Marshal(map[string]string{
		"q": fmt.Sprintf("%s|%s|%#v", "ListMachines", "", store.MachineFilter{}),
		"k": "m-5",
	})
	forged := base64.RawURLEncoding.EncodeToString(raw)
	if _, err := s.ListMachines(ctx, store.MachineFilter{}, store.PageRequest{Limit: 2, Cursor: forged}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("hand-minted cursor: err %v, want ErrInvalidCursor", err)
	}

	page, err := s.ListMachines(ctx, store.MachineFilter{}, store.PageRequest{Limit: 2})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("first page: %v, cursor %q", err, page.NextCursor)
	}
	if _, err := s.ListMachines(ctx, store.MachineFilter{}, store.PageRequest{Limit: 2, Cursor: page.NextCursor}); err != nil {
		t.Fatalf("genuine cursor rejected: %v", err)
	}
	if dec, err := base64.RawURLEncoding.DecodeString(page.NextCursor); err == nil && bytes.Contains(dec, []byte("m-1")) {
		edited := base64.RawURLEncoding.EncodeToString(bytes.ReplaceAll(dec, []byte("m-1"), []byte("m-6")))
		if _, err := s.ListMachines(ctx, store.MachineFilter{}, store.PageRequest{Limit: 2, Cursor: edited}); !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("edited genuine cursor: err %v, want ErrInvalidCursor", err)
		}
	}
}
