//go:build integration

package dynamo_test

// Foreign rows in an index partition and short pages (path traversal review round 1, findings F1 and F3). Filtering
// the other list's rows out of a single Query with Limit = page size leaves a page empty for every page of foreign
// rows, and one upload can hold 1,000 of them timed a day ahead: the user sees "No events match." with a Next link
// page after page, and the older executions stay hidden in practice. A page has to fill from older rows by repeating
// the Query from LastEvaluatedKey, within a small fixed budget of Queries per request so the read cost stays bounded;
// only a run of foreign rows longer than that budget may still give a short or empty page with Next, and following
// Next always reaches the older rows. Both lists share Decision_TS and Fleet_TS, and a row the store did not write
// itself can carry any index attribute, so every index of both lists is covered, not only the partition Rudolph could
// fill before its fix.

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

// maxQueriesPerPage is the most Queries one page request may make; the review asked for a small fixed budget, "say up
// to 5".
const maxQueriesPerPage = 5

// refillCase is one index partition of one list: own writes a row of the list, foreign a row of the other list, and
// moveForeign (if set) moves the foreign rows into the partition once written; list reads one page of the partition
// as the names of the rows it shows.
type refillCase struct {
	name        string
	own         func(t *testing.T, machine string, i int)
	foreign     func(t *testing.T, machine string, i int)
	moveForeign func(t *testing.T)
	list        func(s *dynamo.Store, p store.PageRequest) ([]string, string, error)
}

func (c refillCase) seed(t *testing.T, own, foreign []int) {
	t.Helper()
	for _, i := range own {
		c.own(t, []string{machineA, machineB}[i%2], i)
	}
	for _, i := range foreign {
		c.foreign(t, machineB, i)
	}
	if c.moveForeign != nil {
		c.moveForeign(t)
	}
}

// retag sets attr on the item with the given partition and sort key, to put a row into an index partition its list
// does not use.
func retag(t *testing.T, pk, sk, attr, value string) {
	t.Helper()
	_, err := ddblocal.Raw(t).UpdateItem(ctx, &awsdynamodb.UpdateItemInput{
		TableName:                 aws.String(ddblocal.EventsTableName),
		Key:                       map[string]types.AttributeValue{"PK": s(pk), "SK": s(sk)},
		UpdateExpression:          aws.String("SET #a = :v"),
		ExpressionAttributeNames:  map[string]string{"#a": attr},
		ExpressionAttributeValues: map[string]types.AttributeValue{":v": s(value)},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// retagAll sets attr on every item whose partition key starts with prefix.
func retagAll(t *testing.T, prefix, attr, value string) {
	t.Helper()
	out, err := ddblocal.Raw(t).Scan(ctx, &awsdynamodb.ScanInput{TableName: aws.String(ddblocal.EventsTableName)})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.LastEvaluatedKey) != 0 {
		t.Fatal("events table larger than one Scan page")
	}
	for _, it := range out.Items {
		pk, sk := it["PK"].(*types.AttributeValueMemberS), it["SK"].(*types.AttributeValueMemberS)
		if strings.HasPrefix(pk.Value, prefix) {
			retag(t, pk.Value, sk.Value, attr, value)
		}
	}
}

func listExecutions(f store.EventFilter) func(*dynamo.Store, store.PageRequest) ([]string, string, error) {
	return func(st *dynamo.Store, p store.PageRequest) ([]string, string, error) {
		page, err := st.ListExecutionEvents(ctx, f, p)
		var names []string
		for _, e := range page.Items {
			names = append(names, strings.TrimPrefix(e.FileName, "tool-"))
		}
		return names, page.NextCursor, err
	}
}

func listFileAccess(f store.FileAccessEventFilter) func(*dynamo.Store, store.PageRequest) ([]string, string, error) {
	return func(st *dynamo.Store, p store.PageRequest) ([]string, string, error) {
		page, err := st.ListFileAccessEvents(ctx, f, p)
		var names []string
		for _, e := range page.Items {
			names = append(names, strings.TrimPrefix(e.RuleName, "rule-"))
		}
		return names, page.NextCursor, err
	}
}

// refillCases covers each index of both lists. Rudolph files the foreign rows of the first case; the others need a
// row Rudolph does not write (an execution under a file access decision, a file access row with a TeamID or the
// other list's Fleet), and the store treats every index the same way.
func refillCases() []refillCase {
	execRow := func(decision, team string) func(*testing.T, string, int) {
		return func(t *testing.T, machine string, i int) { putEvent(t, machine, i, decision, team) }
	}
	accessRow := func(decision string) func(*testing.T, string, int) {
		return func(t *testing.T, machine string, i int) { putFileAccess(t, machine, i, decision, "/bin/rm") }
	}
	return []refillCase{
		{
			name:    "executions by decision, file access rows in BLOCK_BINARY",
			own:     execRow("BLOCK_BINARY", ""),
			foreign: accessRow("BLOCK_BINARY"),
			list:    listExecutions(store.EventFilter{Decision: store.DecisionBlockBinary}),
		},
		{
			name:        "executions by team ID, file access rows with that TeamID",
			own:         execRow("BLOCK_TEAMID", teamGoogle),
			foreign:     accessRow("FILE_ACCESS_DECISION_DENIED"),
			moveForeign: func(t *testing.T) { retagAll(t, "FileAccess#", "TeamID", teamGoogle) },
			list:        listExecutions(store.EventFilter{TeamID: teamGoogle}),
		},
		{
			name:        "executions of the fleet, file access rows under Fleet Exec",
			own:         execRow("ALLOW_UNKNOWN", ""),
			foreign:     accessRow("FILE_ACCESS_DECISION_DENIED"),
			moveForeign: func(t *testing.T) { retagAll(t, "FileAccess#", "Fleet", "Exec") },
			list:        listExecutions(store.EventFilter{}),
		},
		{
			name:    "file access by decision, executions in FILE_ACCESS_DECISION_DENIED",
			own:     accessRow("FILE_ACCESS_DECISION_DENIED"),
			foreign: execRow("FILE_ACCESS_DECISION_DENIED", ""),
			list:    listFileAccess(store.FileAccessEventFilter{Decision: store.FileAccessDecisionDenied}),
		},
		{
			name:        "file access of the fleet, executions under Fleet FileAccess",
			own:         accessRow("FILE_ACCESS_DECISION_DENIED"),
			foreign:     execRow("BLOCK_BINARY", ""),
			moveForeign: func(t *testing.T) { retagAll(t, "Exec#", "Fleet", "FileAccess") },
			list:        listFileAccess(store.FileAccessEventFilter{}),
		},
	}
}

// pageRead is one page request: what it showed and what it cost.
type pageRead struct {
	names   []string
	next    string
	queries int
	limits  []int
}

// readPage reads one page and counts the DynamoDB calls it made, failing the test on any call but Query and on any
// Query reading more rows than the page holds, either of which would make the read cost unbounded.
func readPage(t *testing.T, st *dynamo.Store, c refillCase, limit int, cursor string, limits *[]int) (pageRead, error) {
	t.Helper()
	ddblocal.ResetCounts()
	before := len(*limits)
	names, next, err := c.list(st, store.PageRequest{Limit: limit, Cursor: cursor})
	counts := ddblocal.Counts()
	for op, n := range counts {
		if op != "Query" && n > 0 {
			t.Errorf("%d %s calls for one page; the reader may only Query (counts %v)", n, op, counts)
		}
	}
	r := pageRead{names: names, next: next, queries: counts["Query"], limits: append([]int(nil), (*limits)[before:]...)}
	if r.queries > maxQueriesPerPage {
		t.Errorf("one page made %d Queries, want at most %d", r.queries, maxQueriesPerPage)
	}
	for _, l := range r.limits {
		if l < 1 || l > limit {
			t.Errorf("a Query of a %d-row page had Limit %d", limit, l)
		}
	}
	return r, err
}

func seq(from, to int) []int {
	var out []int
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func TestEventLists_PageFillsPastForeignRows(t *testing.T) {
	const limit = 2
	for _, c := range refillCases() {
		t.Run(c.name, func(t *testing.T) {
			st := newStore(t)
			// Five foreign rows above three of the list's: more than two pages of them, well inside the budget.
			c.seed(t, seq(1, 3), seq(10, 14))
			limits := limitsUsed(t)
			first, err := readPage(t, st, c, limit, "", limits)
			if err != nil {
				t.Fatalf("first page: %v", err)
			}
			if got := strings.Join(first.names, ","); got != "3,2" || first.next == "" {
				t.Errorf("first page: [%s] after %d Queries (Limits %v), Next cursor %t; want [3,2] and a Next cursor", got, first.queries, first.limits, first.next != "")
			}
			if first.next == "" {
				return
			}
			second, err := readPage(t, st, c, limit, first.next, limits)
			if err != nil {
				t.Fatalf("second page: %v", err)
			}
			if got := strings.Join(second.names, ","); got != "1" {
				t.Errorf("second page: [%s], want [1]", got)
			}
		})
	}
}

func TestEventLists_FloodOfForeignRowsStillReachesOlderRows(t *testing.T) {
	const (
		limit   = 2
		foreign = 50
	)
	for _, c := range refillCases() {
		t.Run(c.name, func(t *testing.T) {
			st := newStore(t)
			// Far more foreign rows than the budget of Queries can read in one request, so some pages may be short.
			c.seed(t, seq(1, 3), seq(10, 10+foreign-1))
			limits := limitsUsed(t)
			var got []string
			cursor := ""
			// Without any refill, paging needs one request per page of foreign rows; the bound only stops a loop.
			for pages := 1; ; pages++ {
				if pages > foreign {
					t.Fatalf("still paging after %d pages: [%s]", foreign, strings.Join(got, ","))
				}
				r, err := readPage(t, st, c, limit, cursor, limits)
				if err != nil {
					t.Fatalf("page %d (after [%s]): %v", pages, strings.Join(got, ","), err)
				}
				// A page may come back short with a Next cursor only once it has tried to fill from older rows.
				if len(r.names) < limit && r.next != "" && r.queries < 2 {
					t.Fatalf("page %d: %d of %d rows and a Next cursor after %d Query (Limits %v); want the page refilled from older rows first", pages, len(r.names), limit, r.queries, r.limits)
				}
				got = append(got, r.names...)
				if r.next == "" {
					break
				}
				cursor = r.next
			}
			if g := strings.Join(got, ","); g != "3,2,1" {
				t.Errorf("rows reached: [%s], want [3,2,1]", g)
			}
		})
	}
}
