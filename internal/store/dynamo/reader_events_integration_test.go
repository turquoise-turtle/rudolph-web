//go:build integration

package dynamo_test

// Step 9 of docs/data-layer-design.md: execution events from Rudolph's events table (testdata/rudolph/events.tf;
// the rows are described in Rudolph's internal/handlers/webdata_events_integration_test.go and docs/events-table.md).
// Event item: PK "Exec#<machine>", SK "<TS>#<sha256>#<pid>", TS = execution time in µs as 20 digits; attributes
// MachineID, TS, Fleet ("Exec"), Decision, FileSHA256, FilePath, FileName, ExecutingUser, TeamID, SigningID, CDHash.
// GSIs Fleet_TS (Fleet, TS), Decision_TS (Decision, TS), TeamID_TS (TeamID, TS), projection ALL.
//
// ListExecutionEvents: newest first, one Query per page with Limit = page size, never a Scan or FilterExpression. A
// machine filter queries the Mac's partition, a decision Decision_TS, a team ID TeamID_TS, no filter Fleet_TS. More
// than one filter at once is refused (ErrInvalidArgument), as is an unknown decision. Without an events table
// configured (Options.EventsTable empty) the method answers ErrUnavailable. Decisions the web view does not know are
// passed through as they are.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

const teamGoogle = "EQHXZ8M8AV"

var eventsBase = time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

// putEvent writes an event the way Rudolph does; i orders them in time (higher is newer).
func putEvent(t *testing.T, machine string, i int, decision, team string) string {
	t.Helper()
	at := eventsBase.Add(time.Duration(i) * time.Minute).Add(123456 * time.Microsecond)
	ts := fmt.Sprintf("%020d", at.UnixMicro())
	sha := fmt.Sprintf("%064x", i)
	str := func(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
	item := map[string]types.AttributeValue{
		"PK": str("Exec#" + machine), "SK": str(ts + "#" + sha + "#" + fmt.Sprint(100+i)),
		"MachineID": str(machine), "TS": str(ts), "Fleet": str("Exec"), "Decision": str(decision), "FileSHA256": str(sha),
		"FilePath": str("/Applications/Tool.app/Contents/MacOS"), "FileName": str(fmt.Sprintf("tool-%d", i)),
		"ExecutingUser": str("alice"), "SigningID": str("X:com.example.tool"), "CDHash": str(strings.Repeat("c", 40)),
		"ExpiresAfter": n(int(eventsBase.Add(90 * 24 * time.Hour).Unix())),
	}
	if team != "" {
		item["TeamID"] = str(team)
	}
	if _, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{TableName: aws.String(ddblocal.EventsTableName), Item: item}); err != nil {
		t.Fatal(err)
	}
	return sha
}

// seedEvents: machine A events 1 (BLOCK_BINARY, Google), 3 (ALLOW_UNKNOWN), 5 (BLOCK_TEAMID, Google);
// machine B events 2 (BLOCK_BINARY), 4 (ALLOW_PLATFORM, a decision the web view may not know).
func seedEvents(t *testing.T) *dynamo.Store {
	t.Helper()
	s := newStore(t)
	putEvent(t, machineA, 1, "BLOCK_BINARY", teamGoogle)
	putEvent(t, machineB, 2, "BLOCK_BINARY", "")
	putEvent(t, machineA, 3, "ALLOW_UNKNOWN", "")
	putEvent(t, machineB, 4, "ALLOW_PLATFORM", "")
	putEvent(t, machineA, 5, "BLOCK_TEAMID", teamGoogle)
	ddblocal.ResetCounts()
	return s
}

func eventNames(es []store.ExecutionEvent) string {
	var out []string
	for _, e := range es {
		out = append(out, strings.TrimPrefix(e.FileName, "tool-"))
	}
	return strings.Join(out, ",")
}

func allEvents(t *testing.T, s *dynamo.Store, f store.EventFilter, limit int) ([]store.ExecutionEvent, int) {
	t.Helper()
	var got []store.ExecutionEvent
	cursor := ""
	pages := 0
	for {
		page, err := s.ListExecutionEvents(ctx, f, store.PageRequest{Limit: limit, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", pages+1, err)
		}
		pages++
		got = append(got, page.Items...)
		if page.NextCursor == "" || pages > 10 {
			return got, pages
		}
		cursor = page.NextCursor
	}
}

func TestListExecutionEvents_ByFilterNewestFirst(t *testing.T) {
	for _, c := range []struct {
		name string
		f    store.EventFilter
		want string
	}{
		{"fleet", store.EventFilter{}, "5,4,3,2,1"},
		{"machine A", store.EventFilter{MachineID: machineA}, "5,3,1"},
		{"machine B", store.EventFilter{MachineID: machineB}, "4,2"},
		{"decision", store.EventFilter{Decision: store.DecisionBlockBinary}, "2,1"},
		{"team", store.EventFilter{TeamID: teamGoogle}, "5,1"},
		{"unknown machine", store.EventFilter{MachineID: "CCCCCCCC-3333-4333-8333-CCCCCCCCCCCC"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := seedEvents(t)
			limits := limitsUsed(t)
			got, pages := allEvents(t, s, c.f, 2)
			if eventNames(got) != c.want {
				t.Errorf("got %s, want %s", eventNames(got), c.want)
			}
			queriesOnly(t, pages)
			for _, l := range *limits {
				if l != 2 {
					t.Errorf("Query Limit %d, want the page size 2", l)
				}
			}
		})
	}
}

func TestListExecutionEvents_Fields(t *testing.T) {
	s := seedEvents(t)
	page, err := s.ListExecutionEvents(ctx, store.EventFilter{MachineID: machineA}, store.PageRequest{Limit: 1})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("%v %+v", err, page)
	}
	e := page.Items[0]
	want := store.ExecutionEvent{
		MachineID: machineA, ExecutionTime: eventsBase.Add(5 * time.Minute).Add(123456 * time.Microsecond),
		Decision: store.DecisionBlockTeamID, FileSHA256: fmt.Sprintf("%064x", 5),
		FilePath: "/Applications/Tool.app/Contents/MacOS", FileName: "tool-5", ExecutingUser: "alice",
		TeamID: teamGoogle, SigningID: "X:com.example.tool", CDHash: strings.Repeat("c", 40),
	}
	if !e.ExecutionTime.Equal(want.ExecutionTime) {
		t.Errorf("ExecutionTime %v, want %v", e.ExecutionTime, want.ExecutionTime)
	}
	e.ExecutionTime = want.ExecutionTime
	if e != want {
		t.Errorf("got  %+v\nwant %+v", e, want)
	}
}

func TestListExecutionEvents_UnknownDecisionPassesThrough(t *testing.T) {
	s := seedEvents(t)
	page, err := s.ListExecutionEvents(ctx, store.EventFilter{MachineID: machineB}, store.PageRequest{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Decision != store.Decision("ALLOW_PLATFORM") {
		t.Errorf("%v %+v, want the stored ALLOW_PLATFORM decision", err, page.Items)
	}
}

func TestListExecutionEvents_InvalidArguments(t *testing.T) {
	s := seedEvents(t)
	for name, c := range map[string]struct {
		f store.EventFilter
		p store.PageRequest
	}{
		"machine and decision": {store.EventFilter{MachineID: machineA, Decision: store.DecisionBlockBinary}, store.PageRequest{Limit: 10}},
		"decision and team":    {store.EventFilter{Decision: store.DecisionBlockBinary, TeamID: teamGoogle}, store.PageRequest{Limit: 10}},
		"unknown decision":     {store.EventFilter{Decision: "BLOCK_EVERYTHING"}, store.PageRequest{Limit: 10}},
		"limit 0":              {store.EventFilter{}, store.PageRequest{Limit: 0}},
		"limit over max":       {store.EventFilter{}, store.PageRequest{Limit: store.MaxPageSize + 1}},
	} {
		if _, err := s.ListExecutionEvents(ctx, c.f, c.p); !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("%s: got %v, want store.ErrInvalidArgument", name, err)
		}
	}
	queriesOnly(t, 0)
}

func TestListExecutionEvents_CursorsAreBoundToTheFilter(t *testing.T) {
	s := seedEvents(t)
	fleet, err := s.ListExecutionEvents(ctx, store.EventFilter{}, store.PageRequest{Limit: 1})
	if err != nil || fleet.NextCursor == "" {
		t.Fatalf("precondition: %v %+v", err, fleet)
	}
	mA, err := s.ListExecutionEvents(ctx, store.EventFilter{MachineID: machineA}, store.PageRequest{Limit: 1})
	if err != nil || mA.NextCursor == "" {
		t.Fatalf("precondition: %v %+v", err, mA)
	}
	for name, c := range map[string]struct {
		f      store.EventFilter
		cursor string
	}{
		"fleet cursor for a machine":     {store.EventFilter{MachineID: machineA}, fleet.NextCursor},
		"machine A cursor for machine B": {store.EventFilter{MachineID: machineB}, mA.NextCursor},
		"machine cursor for a team":      {store.EventFilter{TeamID: teamGoogle}, mA.NextCursor},
		"garbage":                        {store.EventFilter{}, "garbage"},
	} {
		if _, err := s.ListExecutionEvents(ctx, c.f, store.PageRequest{Limit: 1, Cursor: c.cursor}); !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("%s: got %v, want store.ErrInvalidCursor", name, err)
		}
	}
}

func TestListExecutionEvents_UnavailableWithoutAnEventsTable(t *testing.T) {
	codec, _ := store.NewCursorCodec(store.RandomCursorKey())
	s, err := dynamo.New(dynamo.Options{Client: ddblocal.Raw(t), Table: ddblocal.TableName, Cursors: codec})
	if err != nil {
		t.Fatal(err)
	}
	ddblocal.ResetCounts()
	if _, err := s.ListExecutionEvents(ctx, store.EventFilter{}, store.PageRequest{Limit: 10}); !errors.Is(err, store.ErrUnavailable) {
		t.Errorf("got %v, want store.ErrUnavailable", err)
	}
	queriesOnly(t, 0)
}

// Events are only shown. A row the reader cannot decode (a malformed TS, a wrongly typed attribute, a newer Rudolph's
// format) is skipped and logged rather than failing its page, which would make every older event of the list
// unreachable until that row expires.
func TestListExecutionEvents_UndecodableRowIsSkipped(t *testing.T) {
	s := seedEvents(t)
	bad := map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: "Exec#" + machineA}, "SK": &types.AttributeValueMemberS{Value: "99999999999999999999#bad#1"},
		"MachineID": &types.AttributeValueMemberS{Value: machineA}, "TS": &types.AttributeValueMemberS{Value: "not-a-time"},
		"Fleet": &types.AttributeValueMemberS{Value: "Exec"}, "Decision": &types.AttributeValueMemberS{Value: "BLOCK_BINARY"},
		"FileName": &types.AttributeValueMemberN{Value: "3"},
	}
	if _, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{TableName: aws.String(ddblocal.EventsTableName), Item: bad}); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]store.EventFilter{"fleet": {}, "machine": {MachineID: machineA}} {
		got, _ := allEvents(t, s, f, 2)
		want := map[string]string{"fleet": "5,4,3,2,1", "machine": "5,3,1"}[name]
		if eventNames(got) != want {
			t.Errorf("%s: %s, want %s with the bad row skipped", name, eventNames(got), want)
		}
	}
}
