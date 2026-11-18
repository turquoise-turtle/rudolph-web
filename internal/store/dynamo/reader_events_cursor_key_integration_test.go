//go:build integration

package dynamo_test

// The event cursor's key check (path traversal review round 1, finding F2). Once an index cursor may stand on a row of
// either list, the HMAC fingerprint and the hash attribute are what keep it inside its query, and no cursor a client
// can forge gets past the fingerprint to show that the key check holds on its own. These cursors are signed with the
// right fingerprint for their query (dynamo.SignEventCursor, export_test.go), so only the key check can refuse them: a
// partition outside the events' rows, another partition of the index, or for a Mac another partition of the table must
// be store.ErrInvalidCursor, while a key on the other list's row in the query's own index partition must resume the
// list.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
)

// cursorTS is the TS of minute i in the seed's timeline, between its older and newer rows when i is 20.
func cursorTS(i int) string {
	return fmt.Sprintf("%020d", eventsBase.Add(time.Duration(i)*time.Minute).UnixMicro())
}

// cursorKey is a key standing at minute 20 in partition pk, shaped like the rows of that partition, with the index
// attribute hashAttr = hashValue unless hashAttr is "" (a key of a Mac's partition of the table).
func cursorKey(pk, hashAttr, hashValue string) map[string]string {
	ts := cursorTS(20)
	sk := ts + "#" + strings.Repeat("e", 64) + "#120"
	if strings.HasPrefix(pk, "FileAccess#") {
		sk = ts + "#220#" + strings.Repeat("e", 16)
	}
	key := map[string]string{"PK": pk, "SK": sk}
	if hashAttr != "" {
		key[hashAttr] = hashValue
		key["TS"] = ts
	}
	return key
}

// cursorCase signs key for one query of one list and reads the page after it.
type cursorCase struct {
	name string
	key  map[string]string
	// exec is the execution filter, used unless access is set.
	exec   store.EventFilter
	access *store.FileAccessEventFilter
}

func (c cursorCase) page(t *testing.T, st *dynamo.Store) (string, error) {
	t.Helper()
	if c.access != nil {
		cursor, err := dynamo.SignFileAccessCursor(st, *c.access, c.key)
		if err != nil {
			t.Fatalf("signing: %v", err)
		}
		page, err := st.ListFileAccessEvents(ctx, *c.access, store.PageRequest{Limit: 10, Cursor: cursor})
		return faaNames(page.Items), err
	}
	cursor, err := dynamo.SignEventCursor(st, c.exec, c.key)
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	page, err := st.ListExecutionEvents(ctx, c.exec, store.PageRequest{Limit: 10, Cursor: cursor})
	return eventNames(page.Items), err
}

var (
	byBlockBinary = store.EventFilter{Decision: store.DecisionBlockBinary}
	byTeamGoogle  = store.EventFilter{TeamID: teamGoogle}
	byMachineA    = store.EventFilter{MachineID: machineA}
	accessDenied  = &store.FileAccessEventFilter{Decision: store.FileAccessDecisionDenied}
	accessFleet   = &store.FileAccessEventFilter{}
	accessMachine = &store.FileAccessEventFilter{MachineID: machineA}
)

// seedCursorRows: on machine A, executions 1, 2 and 30 (BLOCK_BINARY, Google) and file access events 3, 4 and 31
// (DENIED); a cursor at minute 20 resumes at 2 or 4.
func seedCursorRows(t *testing.T) *dynamo.Store {
	t.Helper()
	st := newStore(t)
	for _, i := range []int{1, 2, 30} {
		putEvent(t, machineA, i, "BLOCK_BINARY", teamGoogle)
	}
	for _, i := range []int{3, 4, 31} {
		putFileAccess(t, machineA, i, "FILE_ACCESS_DECISION_DENIED", "/bin/rm")
	}
	return st
}

func TestEventCursor_KeyOutsideItsQueryIsRefused(t *testing.T) {
	st := seedCursorRows(t)
	execA, accessA := "Exec#"+machineA, "FileAccess#"+machineA
	for _, c := range []cursorCase{
		// A partition that holds no event rows: the top lists share the table, and a prefix without its separator
		// names no Mac.
		{name: "decision, top list partition", exec: byBlockBinary, key: cursorKey("Top#Blocked#7", "Decision", "BLOCK_BINARY")},
		{name: "decision, Exec without separator", exec: byBlockBinary, key: cursorKey("Exec"+machineA, "Decision", "BLOCK_BINARY")},
		{name: "decision, FileAccess without separator", exec: byBlockBinary, key: cursorKey("FileAccess"+machineA, "Decision", "BLOCK_BINARY")},
		{name: "team ID, machine partition", exec: byTeamGoogle, key: cursorKey("Machine#"+machineA, "TeamID", teamGoogle)},
		{name: "fleet, top list partition", exec: store.EventFilter{}, key: cursorKey("Top#Allowed#30", "Fleet", "Exec")},
		{name: "file access decision, top list partition", access: accessDenied, key: cursorKey("Top#Blocked#1", "Decision", "FILE_ACCESS_DECISION_DENIED")},
		{name: "file access fleet, machine partition", access: accessFleet, key: cursorKey("Machine#"+machineA, "Fleet", "FileAccess")},

		// Another partition of the query's index.
		{name: "decision, another decision", exec: byBlockBinary, key: cursorKey(execA, "Decision", "BLOCK_CERTIFICATE")},
		{name: "decision, another decision on a file access row", exec: byBlockBinary, key: cursorKey(accessA, "Decision", "FILE_ACCESS_DECISION_DENIED")},
		{name: "team ID, another team", exec: byTeamGoogle, key: cursorKey(execA, "TeamID", "ABCDE12345")},
		{name: "fleet, the file access fleet", exec: store.EventFilter{}, key: cursorKey(accessA, "Fleet", "FileAccess")},
		{name: "file access decision, another decision", access: accessDenied, key: cursorKey(accessA, "Decision", "FILE_ACCESS_DECISION_AUDIT_ONLY")},
		// Santa's prefix is part of the stored decision; the filter value alone is another partition.
		{name: "file access decision, unprefixed", access: accessDenied, key: cursorKey(accessA, "Decision", "DENIED")},
		{name: "file access decision, an execution decision", access: accessDenied, key: cursorKey(execA, "Decision", "BLOCK_BINARY")},
		{name: "file access fleet, the execution fleet", access: accessFleet, key: cursorKey(execA, "Fleet", "Exec")},

		// Another partition of the table for a Mac's list.
		{name: "machine, another Mac", exec: byMachineA, key: cursorKey("Exec#"+machineB, "", "")},
		{name: "machine, the Mac's file access partition", exec: byMachineA, key: cursorKey(accessA, "", "")},
		{name: "file access machine, another Mac", access: accessMachine, key: cursorKey("FileAccess#"+machineB, "", "")},
		{name: "file access machine, the Mac's execution partition", access: accessMachine, key: cursorKey(execA, "", "")},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got, err := c.page(t, st); !errors.Is(err, store.ErrInvalidCursor) {
				t.Errorf("got [%s] %v, want store.ErrInvalidCursor", got, err)
			}
		})
	}
}

func TestEventCursor_KeyInsideItsQueryResumes(t *testing.T) {
	st := seedCursorRows(t)
	execA, accessA := "Exec#"+machineA, "FileAccess#"+machineA
	for _, c := range []struct {
		cursorCase
		want string
	}{
		// The list's own rows: these show the signing hook gives cursors the store accepts, so the refusals above come
		// from the key check.
		{cursorCase{name: "decision, execution row", exec: byBlockBinary, key: cursorKey(execA, "Decision", "BLOCK_BINARY")}, "2,1"},
		{cursorCase{name: "team ID, execution row", exec: byTeamGoogle, key: cursorKey(execA, "TeamID", teamGoogle)}, "2,1"},
		{cursorCase{name: "fleet, execution row", exec: store.EventFilter{}, key: cursorKey(execA, "Fleet", "Exec")}, "2,1"},
		{cursorCase{name: "machine, execution row", exec: byMachineA, key: cursorKey(execA, "", "")}, "2,1"},
		{cursorCase{name: "file access decision, file access row", access: accessDenied, key: cursorKey(accessA, "Decision", "FILE_ACCESS_DECISION_DENIED")}, "4,3"},
		{cursorCase{name: "file access fleet, file access row", access: accessFleet, key: cursorKey(accessA, "Fleet", "FileAccess")}, "4,3"},
		{cursorCase{name: "file access machine, file access row", access: accessMachine, key: cursorKey(accessA, "", "")}, "4,3"},

		// The other list's row in the query's own index partition, where a page of foreign rows ends.
		{cursorCase{name: "decision, file access row", exec: byBlockBinary, key: cursorKey(accessA, "Decision", "BLOCK_BINARY")}, "2,1"},
		{cursorCase{name: "team ID, file access row", exec: byTeamGoogle, key: cursorKey(accessA, "TeamID", teamGoogle)}, "2,1"},
		{cursorCase{name: "fleet, file access row", exec: store.EventFilter{}, key: cursorKey(accessA, "Fleet", "Exec")}, "2,1"},
		{cursorCase{name: "file access decision, execution row", access: accessDenied, key: cursorKey(execA, "Decision", "FILE_ACCESS_DECISION_DENIED")}, "4,3"},
		{cursorCase{name: "file access fleet, execution row", access: accessFleet, key: cursorKey(execA, "Fleet", "FileAccess")}, "4,3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.page(t, st)
			if err != nil || got != c.want {
				t.Errorf("got [%s] %v, want [%s]", got, err, c.want)
			}
		})
	}
}
