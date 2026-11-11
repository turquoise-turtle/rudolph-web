//go:build integration

package dynamo_test

// File access events from Rudolph's events table (Rudolph 85eee68, docs/events-table.md; the rows are described in
// Rudolph's internal/handlers/file_access_events_integration_test.go). Item: PK "FileAccess#<machine>",
// SK "<TS>#<pid>#<16 hex>", TS = access time in µs as 20 digits; attributes MachineID, TS, Fleet ("FileAccess"),
// Decision (Santa's name, e.g. "FILE_ACCESS_DECISION_DENIED"), RuleName, RuleVersion, Target, ProcessPath,
// ProcessName, ProcessSHA256, ProcessSigningID, ProcessTeamID, ProcessCDHash, PID (number), ParentPath and
// ProcessChain (a list of paths, first process first); empty ones are left out.
//
// ListFileAccessEvents: newest first, one Query per page with Limit = page size, never a Scan or FilterExpression. A
// machine filter queries the Mac's partition, a decision Decision_TS (with Santa's FILE_ACCESS_DECISION_ prefix), no
// filter Fleet_TS under "FileAccess". Executions never appear, and file access events never appear among executions.
// store.FileAccessEvent.Decision is Santa's name without the prefix (DENIED, DENIED_INVALID_SIGNATURE, AUDIT_ONLY;
// others pass through the same way). ProcessChain[0] carries the first process's details (FilePath from ProcessPath,
// FileSHA256, CDHash, SigningID, TeamID, PID); the others only their FilePath from ProcessChain. Machine and decision
// at once, an unknown decision and a bad limit are ErrInvalidArgument; cursors are bound to the filter; an
// undecodable row is skipped; without an events table the method answers ErrUnavailable.

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

// putFileAccess writes a file access event the way Rudolph does; i orders them in time (higher is newer) and names
// the rule "rule-<i>".
func putFileAccess(t *testing.T, machine string, i int, decision string, chain ...string) {
	t.Helper()
	at := eventsBase.Add(time.Duration(i) * time.Minute).Add(654321 * time.Microsecond)
	ts := fmt.Sprintf("%020d", at.UnixMicro())
	str := func(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
	item := map[string]types.AttributeValue{
		"PK": str("FileAccess#" + machine), "SK": str(fmt.Sprintf("%s#%d#%016x", ts, 200+i, i)),
		"MachineID": str(machine), "TS": str(ts), "Fleet": str("FileAccess"), "Decision": str(decision),
		"RuleName": str(fmt.Sprintf("rule-%d", i)), "RuleVersion": str("v7"), "Target": str("/usr/local/jamf/bin/jamf"),
		"ExpiresAfter": n(int(eventsBase.Add(90 * 24 * time.Hour).Unix())),
	}
	if len(chain) > 0 {
		item["ProcessPath"] = str(chain[0])
		item["ProcessName"] = str(chain[0][strings.LastIndex(chain[0], "/")+1:])
		item["ProcessSHA256"] = str(fmt.Sprintf("%064x", i))
		item["ProcessSigningID"] = str("platform:com.apple.rm")
		item["ProcessCDHash"] = str(strings.Repeat("d", 40))
		item["ProcessTeamID"] = str(teamGoogle)
		item["PID"] = n(200 + i)
		var paths []types.AttributeValue
		for _, p := range chain {
			paths = append(paths, str(p))
		}
		item["ProcessChain"] = &types.AttributeValueMemberL{Value: paths}
		if len(chain) > 1 {
			item["ParentPath"] = str(chain[1])
		}
	}
	if _, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{TableName: aws.String(ddblocal.EventsTableName), Item: item}); err != nil {
		t.Fatal(err)
	}
}

// seedFileAccess: machine A events 1 (DENIED), 3 (AUDIT_ONLY), 5 (DENIED_INVALID_SIGNATURE, no process); machine B
// events 2 (DENIED), 4 (AUDIT_ONLY); plus executions on both Macs, which must never show.
func seedFileAccess(t *testing.T) *dynamo.Store {
	t.Helper()
	s := newStore(t)
	putFileAccess(t, machineA, 1, "FILE_ACCESS_DECISION_DENIED", "/bin/rm", "/bin/zsh", "/usr/bin/sudo")
	putFileAccess(t, machineB, 2, "FILE_ACCESS_DECISION_DENIED", "/bin/mv")
	putFileAccess(t, machineA, 3, "FILE_ACCESS_DECISION_AUDIT_ONLY", "/bin/cp", "/bin/bash")
	putFileAccess(t, machineB, 4, "FILE_ACCESS_DECISION_AUDIT_ONLY", "/bin/cat")
	putFileAccess(t, machineA, 5, "FILE_ACCESS_DECISION_DENIED_INVALID_SIGNATURE")
	putEvent(t, machineA, 6, "BLOCK_BINARY", teamGoogle)
	putEvent(t, machineB, 7, "BLOCK_BINARY", "")
	ddblocal.ResetCounts()
	return s
}

func faaNames(es []store.FileAccessEvent) string {
	var out []string
	for _, e := range es {
		out = append(out, strings.TrimPrefix(e.RuleName, "rule-"))
	}
	return strings.Join(out, ",")
}

func allFileAccess(t *testing.T, s *dynamo.Store, f store.FileAccessEventFilter, limit int) ([]store.FileAccessEvent, int) {
	t.Helper()
	var got []store.FileAccessEvent
	cursor := ""
	pages := 0
	for {
		page, err := s.ListFileAccessEvents(ctx, f, store.PageRequest{Limit: limit, Cursor: cursor})
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

func TestListFileAccessEvents_ByFilterNewestFirst(t *testing.T) {
	for _, c := range []struct {
		name string
		f    store.FileAccessEventFilter
		want string
	}{
		{"fleet", store.FileAccessEventFilter{}, "5,4,3,2,1"},
		{"machine A", store.FileAccessEventFilter{MachineID: machineA}, "5,3,1"},
		{"machine B", store.FileAccessEventFilter{MachineID: machineB}, "4,2"},
		{"denied", store.FileAccessEventFilter{Decision: store.FileAccessDecisionDenied}, "2,1"},
		{"audit only", store.FileAccessEventFilter{Decision: store.FileAccessDecisionAuditOnly}, "4,3"},
		{"invalid signature", store.FileAccessEventFilter{Decision: store.FileAccessDecisionDeniedInvalidSignature}, "5"},
		{"unknown machine", store.FileAccessEventFilter{MachineID: "CCCCCCCC-3333-4333-8333-CCCCCCCCCCCC"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := seedFileAccess(t)
			limits := limitsUsed(t)
			got, pages := allFileAccess(t, s, c.f, 2)
			if faaNames(got) != c.want {
				t.Errorf("got %s, want %s", faaNames(got), c.want)
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

func TestListFileAccessEvents_ExecutionsStayApart(t *testing.T) {
	s := seedFileAccess(t)
	got, _ := allEvents(t, s, store.EventFilter{}, 10)
	if eventNames(got) != "7,6" {
		t.Errorf("the executions list shows %s, want only the executions 7,6", eventNames(got))
	}
	got, _ = allEvents(t, s, store.EventFilter{MachineID: machineA}, 10)
	if eventNames(got) != "6" {
		t.Errorf("machine A's executions: %s, want 6", eventNames(got))
	}
}

func TestListFileAccessEvents_Fields(t *testing.T) {
	s := seedFileAccess(t)
	page, err := s.ListFileAccessEvents(ctx, store.FileAccessEventFilter{Decision: store.FileAccessDecisionDenied}, store.PageRequest{Limit: 2})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("%v %+v", err, page)
	}
	e := page.Items[1]
	at := eventsBase.Add(time.Minute).Add(654321 * time.Microsecond)
	if !e.AccessTime.Equal(at) {
		t.Errorf("AccessTime %v, want %v", e.AccessTime, at)
	}
	if e.MachineID != machineA || e.RuleName != "rule-1" || e.RuleVersion != "v7" || e.Target != "/usr/local/jamf/bin/jamf" || e.Decision != store.FileAccessDecisionDenied {
		t.Errorf("event %+v", e)
	}
	want := []store.Process{
		{FilePath: "/bin/rm", FileSHA256: fmt.Sprintf("%064x", 1), CDHash: strings.Repeat("d", 40), SigningID: "platform:com.apple.rm", TeamID: teamGoogle, PID: 201},
		{FilePath: "/bin/zsh"},
		{FilePath: "/usr/bin/sudo"},
	}
	if fmt.Sprint(e.ProcessChain) != fmt.Sprint(want) {
		t.Errorf("ProcessChain\n got  %+v\n want %+v", e.ProcessChain, want)
	}

	page, err = s.ListFileAccessEvents(ctx, store.FileAccessEventFilter{Decision: store.FileAccessDecisionDeniedInvalidSignature}, store.PageRequest{Limit: 2})
	if err != nil || len(page.Items) != 1 || len(page.Items[0].ProcessChain) != 0 {
		t.Errorf("an event without a process: %v %+v, want an empty ProcessChain", err, page.Items)
	}
}

func TestListFileAccessEvents_UnknownDecisionPassesThrough(t *testing.T) {
	s := seedFileAccess(t)
	putFileAccess(t, machineB, 9, "FILE_ACCESS_DECISION_SOMETHING_NEW", "/bin/ls")
	page, err := s.ListFileAccessEvents(ctx, store.FileAccessEventFilter{MachineID: machineB}, store.PageRequest{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Decision != store.FileAccessDecision("SOMETHING_NEW") {
		t.Errorf("%v %+v, want the decision SOMETHING_NEW", err, page.Items)
	}
}

func TestListFileAccessEvents_InvalidArguments(t *testing.T) {
	s := seedFileAccess(t)
	for name, c := range map[string]struct {
		f store.FileAccessEventFilter
		p store.PageRequest
	}{
		"machine and decision": {store.FileAccessEventFilter{MachineID: machineA, Decision: store.FileAccessDecisionDenied}, store.PageRequest{Limit: 10}},
		"unknown decision":     {store.FileAccessEventFilter{Decision: "ALLOWED"}, store.PageRequest{Limit: 10}},
		"prefixed decision":    {store.FileAccessEventFilter{Decision: "FILE_ACCESS_DECISION_DENIED"}, store.PageRequest{Limit: 10}},
		"limit 0":              {store.FileAccessEventFilter{}, store.PageRequest{Limit: 0}},
		"limit over max":       {store.FileAccessEventFilter{}, store.PageRequest{Limit: store.MaxPageSize + 1}},
	} {
		if _, err := s.ListFileAccessEvents(ctx, c.f, c.p); !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("%s: got %v, want store.ErrInvalidArgument", name, err)
		}
	}
	queriesOnly(t, 0)
}

func TestListFileAccessEvents_CursorsAreBoundToTheFilter(t *testing.T) {
	s := seedFileAccess(t)
	fleet, err := s.ListFileAccessEvents(ctx, store.FileAccessEventFilter{}, store.PageRequest{Limit: 1})
	if err != nil || fleet.NextCursor == "" {
		t.Fatalf("precondition: %v %+v", err, fleet)
	}
	mA, err := s.ListFileAccessEvents(ctx, store.FileAccessEventFilter{MachineID: machineA}, store.PageRequest{Limit: 1})
	if err != nil || mA.NextCursor == "" {
		t.Fatalf("precondition: %v %+v", err, mA)
	}
	exec, err := s.ListExecutionEvents(ctx, store.EventFilter{MachineID: machineA}, store.PageRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	execFleet, err := s.ListExecutionEvents(ctx, store.EventFilter{}, store.PageRequest{Limit: 1})
	if err != nil || execFleet.NextCursor == "" {
		t.Fatalf("precondition: %v %+v", err, execFleet)
	}
	for name, c := range map[string]struct {
		f      store.FileAccessEventFilter
		cursor string
	}{
		"fleet cursor for a machine":     {store.FileAccessEventFilter{MachineID: machineA}, fleet.NextCursor},
		"machine A cursor for machine B": {store.FileAccessEventFilter{MachineID: machineB}, mA.NextCursor},
		"machine cursor for a decision":  {store.FileAccessEventFilter{Decision: store.FileAccessDecisionDenied}, mA.NextCursor},
		"execution fleet cursor":         {store.FileAccessEventFilter{}, execFleet.NextCursor},
		"garbage":                        {store.FileAccessEventFilter{}, "garbage"},
	} {
		if _, err := s.ListFileAccessEvents(ctx, c.f, store.PageRequest{Limit: 1, Cursor: c.cursor}); !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("%s: got %v, want store.ErrInvalidCursor", name, err)
		}
	}
	if exec.NextCursor != "" {
		if _, err := s.ListFileAccessEvents(ctx, store.FileAccessEventFilter{MachineID: machineA}, store.PageRequest{Limit: 1, Cursor: exec.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("an execution cursor for machine A's file access events: %v, want store.ErrInvalidCursor", err)
		}
	}
	if _, err := s.ListExecutionEvents(ctx, store.EventFilter{}, store.PageRequest{Limit: 1, Cursor: fleet.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("a file access cursor for the executions list: %v, want store.ErrInvalidCursor", err)
	}
}

func TestListFileAccessEvents_UnavailableWithoutAnEventsTable(t *testing.T) {
	codec, _ := store.NewCursorCodec(store.RandomCursorKey())
	s, err := dynamo.New(dynamo.Options{Client: ddblocal.Raw(t), Table: ddblocal.TableName, Cursors: codec})
	if err != nil {
		t.Fatal(err)
	}
	ddblocal.ResetCounts()
	if _, err := s.ListFileAccessEvents(ctx, store.FileAccessEventFilter{}, store.PageRequest{Limit: 10}); !errors.Is(err, store.ErrUnavailable) {
		t.Errorf("got %v, want store.ErrUnavailable", err)
	}
	queriesOnly(t, 0)
}

func TestListFileAccessEvents_UndecodableRowIsSkipped(t *testing.T) {
	s := seedFileAccess(t)
	bad := map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: "FileAccess#" + machineA}, "SK": &types.AttributeValueMemberS{Value: "99999999999999999999#1#0000000000000000"},
		"MachineID": &types.AttributeValueMemberS{Value: machineA}, "TS": &types.AttributeValueMemberS{Value: "not-a-time"},
		"Fleet": &types.AttributeValueMemberS{Value: "FileAccess"}, "Decision": &types.AttributeValueMemberS{Value: "FILE_ACCESS_DECISION_DENIED"},
		"RuleName": &types.AttributeValueMemberN{Value: "3"},
	}
	if _, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{TableName: aws.String(ddblocal.EventsTableName), Item: bad}); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]store.FileAccessEventFilter{"fleet": {}, "machine": {MachineID: machineA}} {
		got, _ := allFileAccess(t, s, f, 2)
		want := map[string]string{"fleet": "5,4,3,2,1", "machine": "5,3,1"}[name]
		if faaNames(got) != want {
			t.Errorf("%s: %s, want %s with the bad row skipped", name, faaNames(got), want)
		}
	}
}
