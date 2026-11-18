//go:build integration

package dynamo_test

// File access rows in an execution decision's Decision_TS partition (path traversal review, 2026-11-19). Rudolph
// before the fix stored a file access event's Decision as the Mac sent it, so a Mac could file FileAccess#<id> rows
// under BLOCK_BINARY; they stay for 90 days after Rudolph starts refusing them. ListExecutionEvents skips them, as it
// must, but the page's LastEvaluatedKey is then one of those rows, and the cursor signed from it has to resume the
// list rather than be refused as outside the executions' partitions: otherwise every older blocked execution is
// out of reach through the decision filter until the rows expire. The cursor must still be bound to its query.

import (
	"errors"
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
)

// followEvents pages through f like a user following Next, reporting rather than stopping at the first refused
// cursor so the test shows how far the list got.
func followEvents(t *testing.T, s *dynamo.Store, f store.EventFilter, limit int) []store.ExecutionEvent {
	t.Helper()
	var got []store.ExecutionEvent
	cursor := ""
	for pages := 1; pages <= 20; pages++ {
		page, err := s.ListExecutionEvents(ctx, f, store.PageRequest{Limit: limit, Cursor: cursor})
		if err != nil {
			t.Errorf("page %d (after %s): %v", pages, eventNames(got), err)
			return got
		}
		for _, e := range page.Items {
			if e.FileName == "" || e.FileSHA256 == "" {
				t.Errorf("page %d shows a row that is not an execution: %+v", pages, e)
			}
		}
		got = append(got, page.Items...)
		if page.NextCursor == "" {
			return got
		}
		cursor = page.NextCursor
	}
	t.Errorf("still paging after 20 pages: %s", eventNames(got))
	return got
}

func TestListExecutionEvents_DecisionPagesPastFileAccessRows(t *testing.T) {
	const limit = 2
	for _, c := range []struct {
		name string
		seed func(t *testing.T)
		want string
	}{
		{
			// A full page and more of foreign rows above every execution: the first page comes back empty.
			name: "newest",
			seed: func(t *testing.T) {
				putEvent(t, machineA, 1, "BLOCK_BINARY", "")
				putEvent(t, machineB, 2, "BLOCK_BINARY", "")
				putEvent(t, machineA, 3, "BLOCK_BINARY", "")
				for i := 10; i < 10+2*limit+1; i++ {
					putFileAccess(t, machineB, i, "BLOCK_BINARY", "/bin/rm")
				}
			},
			want: "3,2,1",
		},
		{
			name: "in the middle",
			seed: func(t *testing.T) {
				putEvent(t, machineA, 1, "BLOCK_BINARY", "")
				putEvent(t, machineB, 2, "BLOCK_BINARY", "")
				for i := 5; i < 5+limit+1; i++ {
					putFileAccess(t, machineB, i, "BLOCK_BINARY", "/bin/rm")
				}
				putEvent(t, machineA, 20, "BLOCK_BINARY", "")
				putEvent(t, machineB, 21, "BLOCK_BINARY", "")
				putEvent(t, machineA, 22, "BLOCK_BINARY", "")
			},
			want: "22,21,20,2,1",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			c.seed(t)
			// Executions of another decision share neither the partition nor the answer.
			putEvent(t, machineA, 30, "ALLOW_BINARY", "")
			if got := eventNames(followEvents(t, s, store.EventFilter{Decision: store.DecisionBlockBinary}, limit)); got != c.want {
				t.Errorf("BLOCK_BINARY executions: %s, want %s", got, c.want)
			}
		})
	}
}

func TestListExecutionEvents_CursorPastFileAccessRowsStaysBound(t *testing.T) {
	s := newStore(t)
	putEvent(t, machineA, 1, "BLOCK_BINARY", "")
	// More foreign rows than a page's whole budget of Queries can read, so the Next cursor still stands on one of
	// them once the page has tried to fill from older rows.
	for i := 10; i < 10+maxQueriesPerPage*2+2; i++ {
		putFileAccess(t, machineB, i, "BLOCK_BINARY", "/bin/rm")
	}
	page, err := s.ListExecutionEvents(ctx, store.EventFilter{Decision: store.DecisionBlockBinary}, store.PageRequest{Limit: 2})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("precondition: a page of foreign rows with a Next cursor, got %v %+v", err, page)
	}
	for name, f := range map[string]store.EventFilter{
		"another decision": {Decision: store.DecisionBlockCertificate},
		"the fleet":        {},
		"the Mac":          {MachineID: machineB},
	} {
		if _, err := s.ListExecutionEvents(ctx, f, store.PageRequest{Limit: 2, Cursor: page.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("%s: got %v, want store.ErrInvalidCursor", name, err)
		}
	}
	if _, err := s.ListFileAccessEvents(ctx, store.FileAccessEventFilter{MachineID: machineB}, store.PageRequest{Limit: 2, Cursor: page.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("the Mac's file access events: got %v, want store.ErrInvalidCursor", err)
	}
}
