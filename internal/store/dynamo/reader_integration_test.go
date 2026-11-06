//go:build integration

package dynamo_test

// The DynamoDB reader against DynamoDB Local, with the web contract fixture loaded into a table created from Rudolph's
// table definition. Steps 1 to 3 of docs/data-layer-design.md: machine detail, machine rules and global rules. Machine
// lists, the policy filter and events need Rudolph changes first and answer store.ErrUnavailable until then.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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

func TestMain(m *testing.M) { os.Exit(ddblocal.Main(m)) }

var ctx = context.Background()

// newStore loads the fixture into a fresh table and returns a reader over it, with operation counts cleared.
func newStore(t *testing.T) *dynamo.Store {
	t.Helper()
	ddblocal.Reset(t)
	for _, it := range loadRows(t) {
		if _, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{TableName: aws.String(ddblocal.TableName), Item: it}); err != nil {
			t.Fatal(err)
		}
	}
	codec, err := store.NewCursorCodec(store.RandomCursorKey())
	if err != nil {
		t.Fatal(err)
	}
	s, err := dynamo.New(dynamo.Options{
		Client:      ddblocal.Raw(t),
		Table:       ddblocal.TableName,
		EventsTable: ddblocal.EventsTableName,
		Cursors:     codec,
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	ddblocal.ResetCounts()
	return s
}

// queriesOnly checks that the calls since the last ResetCounts were exactly n Query calls: no Scan, no GetItem, no
// BatchGetItem.
func queriesOnly(t *testing.T, n int) {
	t.Helper()
	counts := ddblocal.Counts()
	for op, c := range counts {
		if op != "Query" && c > 0 {
			t.Errorf("%d %s calls; the reader may only Query (counts %v)", c, op, counts)
		}
	}
	if counts["Query"] != n {
		t.Errorf("%d Query calls, want %d (counts %v)", counts["Query"], n, counts)
	}
}

// limitsUsed records the Limit of every Query from now on.
func limitsUsed(t *testing.T) *[]int {
	t.Helper()
	var limits []int
	ddblocal.AddHook(t, ddblocal.Hook{Before: func(c *ddblocal.Call) *ddblocal.Fault {
		if c.Op == "Query" {
			// The proxy decodes requests with UseNumber.
			n, _ := c.Input["Limit"].(json.Number)
			l, _ := n.Int64()
			limits = append(limits, int(l))
		}
		return nil
	}})
	return &limits
}

func TestNewRejectsIncompleteOptions(t *testing.T) {
	codec, _ := store.NewCursorCodec(store.RandomCursorKey())
	for name, o := range map[string]dynamo.Options{
		"no client":  {Table: "t", Cursors: codec},
		"no table":   {Client: ddblocal.Raw(t), Cursors: codec},
		"no cursors": {Client: ddblocal.Raw(t), Table: "t"},
	} {
		if _, err := dynamo.New(o); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestStoreIsAReader(t *testing.T) {
	var _ store.Reader = (*dynamo.Store)(nil)
}

func TestGetMachine_OneQuery(t *testing.T) {
	s := newStore(t)
	got, err := s.GetMachine(ctx, machineA)
	if err != nil {
		t.Fatal(err)
	}
	queriesOnly(t, 1)
	if got.MachineID != machineA || got.SerialNum != "C02AAAAAAAA1" || got.Sync == nil || got.Sync.RulesProcessed != 13 {
		t.Errorf("got %+v sync %+v", got.Machine, got.Sync)
	}
}

func TestGetMachine_NotFound(t *testing.T) {
	s := newStore(t)
	for name, id := range map[string]string{
		"config only (sensor data expired)": machineB,
		"never seen":                        "CCCCCCCC-3333-4333-8333-CCCCCCCCCCCC",
		"same UUID in another case":         strings.ToLower(machineA),
		"empty":                             "",
	} {
		if _, err := s.GetMachine(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: got %v, want store.ErrNotFound", name, err)
		}
	}
}

// Rudolph's sensor data, sync state and config rows share the partition with others Rudolph may add (tracking items
// of a sync, for one). The detail page still needs a single Query and finds the rows it needs.
func TestGetMachine_ManyOtherRowsInThePartition(t *testing.T) {
	s := newStore(t)
	limits := limitsUsed(t)
	for i := 0; i < 60; i++ {
		// Half sort before Config and half after SyncState, where Rudolph's archived SyncState@<time> rows go.
		prefix := "Aaa#"
		if i%2 == 1 {
			prefix = "SyncState@"
		}
		it := map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "Machine#" + machineA},
			"SK": &types.AttributeValueMemberS{Value: prefix + string(rune('a'+i%26)) + strings.Repeat("x", i)},
		}
		if _, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{TableName: aws.String(ddblocal.TableName), Item: it}); err != nil {
			t.Fatal(err)
		}
	}
	ddblocal.ResetCounts()
	got, err := s.GetMachine(ctx, machineA)
	if err != nil {
		t.Fatal(err)
	}
	if got.MachineID != machineA || got.Sync == nil {
		t.Errorf("got %+v sync %+v", got.Machine, got.Sync)
	}
	queriesOnly(t, 1)
	// Read capacity is charged for every row a Query reads, so the detail page reads only the few rows it shows.
	if len(*limits) != 1 || (*limits)[0] < 3 || (*limits)[0] > 10 {
		t.Errorf("GetMachine queried with Limit %v, want one Query with a Limit of 3 to 10", *limits)
	}
}

func TestListMachineRules_PagesInSortKeyOrder(t *testing.T) {
	s := newStore(t)
	limits := limitsUsed(t)
	var got []store.Rule
	cursor := ""
	pages := 0
	for {
		page, err := s.ListMachineRules(ctx, machineA, store.PageRequest{Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if len(page.Items) > 3 {
			t.Fatalf("page of %d items, limit 3", len(page.Items))
		}
		got = append(got, page.Items...)
		if page.NextCursor == "" {
			break
		}
		if pages > 5 {
			t.Fatal("too many pages")
		}
		cursor = page.NextCursor
	}
	queriesOnly(t, pages)
	for _, l := range *limits {
		if l != 3 {
			t.Errorf("Query limits %v, want each to be the page size 3", *limits)
			break
		}
	}
	want := []struct {
		id    string
		state store.RuleState
	}{{sha1, store.RuleStateRemoving}, {shaB, store.RuleStateInForce}, {shaC, store.RuleStateRemoving}, {shaD, store.RuleStateExpired}}
	if len(got) != len(want) {
		t.Fatalf("got %d rules, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Identifier != w.id || got[i].State != w.state {
			t.Errorf("rule %d: got %s %s, want %s %s", i, got[i].Identifier, got[i].State, w.id, w.state)
		}
	}
}

func TestListMachineRules_NoRules(t *testing.T) {
	s := newStore(t)
	page, err := s.ListMachineRules(ctx, machineB, store.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.NextCursor != "" {
		t.Errorf("got %+v", page)
	}
	queriesOnly(t, 1)
}

// A cursor for one machine's rules cannot be used to read another's, nor for global rules, nor be edited.
func TestListMachineRules_CursorsAreBoundToTheQuery(t *testing.T) {
	s := newStore(t)
	page, err := s.ListMachineRules(ctx, machineA, store.PageRequest{Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("precondition: a second page: %v %+v", err, page)
	}
	if _, err := s.ListMachineRules(ctx, machineB, store.PageRequest{Limit: 1, Cursor: page.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("another machine: got %v, want store.ErrInvalidCursor", err)
	}
	if _, err := s.ListGlobalRules(ctx, store.RuleFilter{}, store.PageRequest{Limit: 1, Cursor: page.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("global rules: got %v, want store.ErrInvalidCursor", err)
	}
	tampered := page.NextCursor[:len(page.NextCursor)-2] + "AA"
	if tampered == page.NextCursor {
		tampered = page.NextCursor[:len(page.NextCursor)-2] + "BB"
	}
	if _, err := s.ListMachineRules(ctx, machineA, store.PageRequest{Limit: 1, Cursor: tampered}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("tampered: got %v, want store.ErrInvalidCursor", err)
	}
	if _, err := s.ListMachineRules(ctx, machineA, store.PageRequest{Limit: 1, Cursor: "garbage"}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("garbage: got %v, want store.ErrInvalidCursor", err)
	}
}

func TestListGlobalRules_AllAndByType(t *testing.T) {
	s := newStore(t)
	cases := []struct {
		ruleType store.RuleType
		want     []string // identifiers in sort key order
	}{
		{"", []string{sha1, "0000000000000000000000000000000000000000000000000000000000000002",
			"0000000000000000000000000000000000000000000000000000000000000003", "0000000000000000000000000000000000000000000000000000000000000004",
			"0000000000000000000000000000000000000000000000000000000000000005", "0000000000000000000000000000000000000000000000000000000000000006",
			"a1b2c3d4e5f60718293a4b5c6d7e8f9001122334", "0000000000000000000000000000000000000000000000000000000000000007",
			"EQHXZ8M8AV:com.google.Chrome", "EQHXZ8M8AV"}},
		{store.RuleTypeCertificate, []string{"0000000000000000000000000000000000000000000000000000000000000007"}},
		{store.RuleTypeTeamID, []string{"EQHXZ8M8AV"}},
		{store.RuleTypeSigningID, []string{"EQHXZ8M8AV:com.google.Chrome"}},
		{store.RuleTypeCDHash, []string{"a1b2c3d4e5f60718293a4b5c6d7e8f9001122334"}},
	}
	for _, c := range cases {
		t.Run(string(c.ruleType), func(t *testing.T) {
			ddblocal.ResetCounts()
			var got []string
			cursor := ""
			pages := 0
			for {
				page, err := s.ListGlobalRules(ctx, store.RuleFilter{RuleType: c.ruleType}, store.PageRequest{Limit: 4, Cursor: cursor})
				if err != nil {
					t.Fatal(err)
				}
				pages++
				for _, r := range page.Items {
					if c.ruleType != "" && r.RuleType != c.ruleType {
						t.Errorf("filter %s returned a %s rule", c.ruleType, r.RuleType)
					}
					got = append(got, r.Identifier)
				}
				if page.NextCursor == "" || pages > 6 {
					break
				}
				cursor = page.NextCursor
			}
			queriesOnly(t, pages)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("got\n%v\nwant\n%v", got, c.want)
			}
		})
	}
}

func TestListGlobalRules_BinaryFilterDoesNotMatchOtherTypes(t *testing.T) {
	s := newStore(t)
	page, err := s.ListGlobalRules(ctx, store.RuleFilter{RuleType: store.RuleTypeBinary}, store.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 6 {
		t.Errorf("got %d binary rules, want 6", len(page.Items))
	}
}

// A cursor from one filter cannot continue another.
func TestListGlobalRules_CursorBoundToTheFilter(t *testing.T) {
	s := newStore(t)
	page, err := s.ListGlobalRules(ctx, store.RuleFilter{RuleType: store.RuleTypeBinary}, store.PageRequest{Limit: 2})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("precondition: %v %+v", err, page)
	}
	if _, err := s.ListGlobalRules(ctx, store.RuleFilter{}, store.PageRequest{Limit: 2, Cursor: page.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("got %v, want store.ErrInvalidCursor", err)
	}
	if _, err := s.ListGlobalRules(ctx, store.RuleFilter{RuleType: store.RuleTypeBinary}, store.PageRequest{Limit: 3, Cursor: page.NextCursor}); err != nil {
		t.Errorf("the same filter with another page size may continue: %v", err)
	}
}

func TestListGlobalRules_InvalidArguments(t *testing.T) {
	s := newStore(t)
	for name, c := range map[string]struct {
		f store.RuleFilter
		p store.PageRequest
	}{
		"limit 0":           {store.RuleFilter{}, store.PageRequest{Limit: 0}},
		"limit over max":    {store.RuleFilter{}, store.PageRequest{Limit: store.MaxPageSize + 1}},
		"unknown rule type": {store.RuleFilter{RuleType: "PATH"}, store.PageRequest{Limit: 10}},
	} {
		if _, err := s.ListGlobalRules(ctx, c.f, c.p); !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("%s: got %v, want store.ErrInvalidArgument", name, err)
		}
	}
	if _, err := s.ListMachineRules(ctx, machineA, store.PageRequest{Limit: 0}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("machine rules limit 0: got %v, want store.ErrInvalidArgument", err)
	}
	queriesOnly(t, 0)
}

// Lists that need Rudolph changes first (the events table) answer store.ErrUnavailable without
// touching DynamoDB.
func TestNotYetAvailable(t *testing.T) {
	s := newStore(t)
	p := store.PageRequest{Limit: 10}
	if _, err := s.TopBlockedBinaries(ctx, store.TopBlockedFilter{}, p); !errors.Is(err, store.ErrUnavailable) {
		t.Errorf("TopBlockedBinaries: %v", err)
	}
	if _, err := s.ListFileAccessEvents(ctx, store.FileAccessEventFilter{}, p); !errors.Is(err, store.ErrUnavailable) {
		t.Errorf("ListFileAccessEvents: %v", err)
	}
	queriesOnly(t, 0)
}

// A rule row the reader cannot decode fails the page rather than being silently skipped.
func TestListGlobalRules_UndecodableRowIsAnError(t *testing.T) {
	s := newStore(t)
	bad := map[string]types.AttributeValue{
		"PK":         &types.AttributeValueMemberS{Value: "GlobalRules"},
		"SK":         &types.AttributeValueMemberS{Value: "Binary#00"},
		"RuleType":   &types.AttributeValueMemberS{Value: "BINARY"},
		"Policy":     &types.AttributeValueMemberN{Value: "1"},
		"Identifier": &types.AttributeValueMemberS{Value: "00"},
	}
	if _, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{TableName: aws.String(ddblocal.TableName), Item: bad}); err != nil {
		t.Fatal(err)
	}
	_, err := s.ListGlobalRules(ctx, store.RuleFilter{}, store.PageRequest{Limit: 2})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, sentinel := range []error{store.ErrNotFound, store.ErrInvalidArgument, store.ErrInvalidCursor, store.ErrUnavailable} {
		if errors.Is(err, sentinel) {
			t.Errorf("a decoding failure is a server error, not %v", sentinel)
		}
	}
}
