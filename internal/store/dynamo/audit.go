package dynamo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/airbnb/rudolph/pkg/model/audit"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// Reading the audit trail needs only the reader's client (Query, which the
// web role is granted with or without editing), never Rudolph's write client.

// ListAudit reads up to limit entries with Rudolph's audit.ListContext: one
// bounded Query per UTC day, never a Scan. It is not part of store.Reader,
// whose pages use ListAuditPage; the integration tests check writes with it.
func (s *Store) ListAudit(ctx context.Context, since time.Time, limit int) ([]store.AuditEntry, error) {
	if limit < 1 || limit > store.MaxAuditEntries {
		return nil, store.Invalid("limit %d outside 1..%d", limit, store.MaxAuditEntries)
	}
	entries, err := audit.ListContext(ctx, s.client, s.table, since, limit)
	if err != nil {
		return nil, fmt.Errorf("dynamo: %w", err)
	}
	return auditEntries(entries), nil
}

// auditListing is the fingerprint of audit cursors. It leaves out since,
// which moves with the clock between pages; Rudolph's cursor is a position
// in the trail, valid for any since.
const auditListing = `["ListAuditPage"]`

// ListAuditPage implements store.AuditReader with Rudolph's
// audit.ListPageContext.
// Rudolph's cursor is unsigned, so it travels inside the store's signed
// cursor and is only handed back to Rudolph once the signature checks out.
func (s *Store) ListAuditPage(ctx context.Context, since time.Time, p store.PageRequest) (store.Page[store.AuditEntry], error) {
	if err := checkLimit(p); err != nil {
		return store.Page[store.AuditEntry]{}, err
	}
	var after string
	if p.Cursor != "" {
		var err error
		if after, err = s.cursors.Decode(auditListing, p.Cursor); err != nil {
			return store.Page[store.AuditEntry]{}, err
		}
		if after == "" {
			return store.Page[store.AuditEntry]{}, store.ErrInvalidCursor
		}
	}
	entries, next, err := audit.ListPageContext(ctx, s.client, s.table, since, p.Limit, after)
	if errors.Is(err, audit.ErrInvalidCursor) {
		// Signed by this store but refused by Rudolph: dated after tomorrow
		// because of clock skew, or from an older cursor format.
		return store.Page[store.AuditEntry]{}, store.ErrInvalidCursor
	}
	if err != nil {
		return store.Page[store.AuditEntry]{}, fmt.Errorf("dynamo: %w", err)
	}
	page := store.Page[store.AuditEntry]{Items: auditEntries(entries)}
	if next != "" {
		if page.NextCursor, err = s.cursors.Encode(auditListing, next); err != nil {
			return store.Page[store.AuditEntry]{}, err
		}
	}
	return page, nil
}

func auditEntries(entries []audit.Entry) []store.AuditEntry {
	out := make([]store.AuditEntry, len(entries))
	for i, e := range entries {
		out[i] = store.AuditEntry{Time: e.Time, Actor: e.Actor, Action: e.Action, Target: e.Target, Summary: e.Summary}
	}
	return out
}
