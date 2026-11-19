package dynamo

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// Rudolph's execution events table: event items in Exec#<machine ID>
// partitions sorted by "<TS>#<file SHA-256>#<pid>", and three indexes with TS
// as the range key. Fleet is "Exec" on every event, so Fleet_TS holds the
// whole fleet in one partition.
const (
	listExecutionEventsMethod = "ListExecutionEvents"
	execPartitionPrefix       = "Exec#"
	fleetExec                 = "Exec"
	attrTS                    = "TS"
	indexFleetTS              = "Fleet_TS"
	indexDecisionTS           = "Decision_TS"
	indexTeamIDTS             = "TeamID_TS"
)

// eventQuery is one paginated event list: either a machine's partition of
// the table, or one partition (hashAttr = hashValue) of an index.
type eventQuery struct {
	method    string // the list: executions and file access events share the indexes
	prefix    string // the table partitions the list's items live in
	index     string // empty: the table, PK = partition
	hashAttr  string
	hashValue string
}

// fingerprint binds a cursor to the list, the index and the normalised filter
// value. Each index has its own hash attribute, so index and value identify
// the query within a list; the method keeps a fleet cursor of one list from
// paging the other, since Fleet_TS serves both.
func (q eventQuery) fingerprint() string {
	b, _ := json.Marshal([]string{q.method, q.index, q.hashAttr, q.hashValue})
	return string(b)
}

// newEventQuery picks the one key condition a filter maps to. Rudolph's
// table has no index on two of these attributes, so a combined filter would
// need a FilterExpression that routinely gives short or empty pages, and is
// refused. The list prefix filter of queryEvents is different: it drops rows
// only when the other list's rows are in the partition, and its refill is
// bounded.
func newEventQuery(f store.EventFilter) (eventQuery, error) {
	machine := strings.TrimSpace(f.MachineID)
	teamID := strings.TrimSpace(f.TeamID)
	decision := store.Decision(strings.TrimSpace(string(f.Decision)))
	set := 0
	for _, v := range []string{machine, teamID, string(decision)} {
		if v != "" {
			set++
		}
	}
	if set > 1 {
		return eventQuery{}, fmt.Errorf("%w: filter events by machine, decision or team ID, one at a time", store.ErrInvalidArgument)
	}
	q := eventQuery{method: listExecutionEventsMethod, prefix: execPartitionPrefix}
	switch {
	case machine != "":
		q.hashAttr, q.hashValue = "PK", execPartitionPrefix+machine
	case decision != "":
		if !slices.Contains(store.Decisions, decision) {
			return eventQuery{}, fmt.Errorf("%w: unknown decision %q", store.ErrInvalidArgument, decision)
		}
		q.index, q.hashAttr, q.hashValue = indexDecisionTS, "Decision", string(decision)
	case teamID != "":
		q.index, q.hashAttr, q.hashValue = indexTeamIDTS, "TeamID", teamID
	default:
		q.index, q.hashAttr, q.hashValue = indexFleetTS, "Fleet", fleetExec
	}
	return q, nil
}

// ListExecutionEvents implements store.EventReader, newest first: a machine
// filter reads the Mac's Exec#<id> partition, a decision Decision_TS, a team
// ID TeamID_TS and no filter Fleet_TS. Rows it cannot decode are logged and
// skipped, so a page may hold fewer items than the limit; so may one after a
// run of file access rows in the index partition longer than the refill
// budget reads, which can come back short or empty with a Next cursor.
func (s *Store) ListExecutionEvents(ctx context.Context, f store.EventFilter, p store.PageRequest) (store.Page[store.ExecutionEvent], error) {
	if s.eventsTable == "" {
		return store.Page[store.ExecutionEvent]{}, fmt.Errorf("%w: no execution events table configured", store.ErrUnavailable)
	}
	if err := checkLimit(p); err != nil {
		return store.Page[store.ExecutionEvent]{}, err
	}
	q, err := newEventQuery(f)
	if err != nil {
		return store.Page[store.ExecutionEvent]{}, err
	}
	rows, err := s.queryEvents(ctx, q, p)
	if err != nil {
		return store.Page[store.ExecutionEvent]{}, fmt.Errorf("dynamo: reading execution events: %w", err)
	}
	page := store.Page[store.ExecutionEvent]{Items: make([]store.ExecutionEvent, 0, len(rows.items))}
	for _, it := range rows.items {
		e, err := DecodeExecutionEvent(it)
		if err != nil {
			// Failing the page would hide every older event of the list until
			// this row expires, 90 days later. The page comes back short instead;
			// the cursor still resumes after the skipped row.
			s.log.Warn("skipping undecodable execution event", "pk", keyString(it["PK"]), "sk", keyString(it["SK"]), "err", err)
			continue
		}
		page.Items = append(page.Items, e)
	}
	if page.NextCursor, err = s.eventCursor(q, rows); err != nil {
		return store.Page[store.ExecutionEvent]{}, err
	}
	return page, nil
}

// maxEventQueries is the most Queries one page of an event list may make.
// Each reads at most the rows still missing from the page, so a request reads
// at most this many pages' worth of rows, and the page itself never holds more
// than the limit, far under Lambda's 6 MB response.
const maxEventQueries = 5

// eventRows is one page of an event list as DynamoDB returned it: the rows
// kept, at most the page size, and the last key evaluated, nil on the last
// page.
type eventRows struct {
	items   []map[string]types.AttributeValue
	lastKey map[string]types.AttributeValue
}

// queryEvents reads one page of q, newest first, resuming after p.Cursor.
// When the filter drops the other list's rows, it queries again from the last
// key evaluated for the rows still missing, so a run of foreign rows does not
// leave the page empty, within maxEventQueries. Errors from DynamoDB come back
// unwrapped for the caller to name its list; a bad cursor is
// store.ErrInvalidCursor.
func (s *Store) queryEvents(ctx context.Context, q eventQuery, p store.PageRequest) (eventRows, error) {
	in := &dynamodb.QueryInput{
		TableName:                aws.String(s.eventsTable),
		KeyConditionExpression:   aws.String("#h = :h"),
		ExpressionAttributeNames: map[string]string{"#h": q.hashAttr},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":h": &types.AttributeValueMemberS{Value: q.hashValue},
		},
		ScanIndexForward: aws.Bool(false),
	}
	if q.index != "" {
		in.IndexName = aws.String(q.index)
		// An index partition can also hold the other list's rows: Rudolph
		// before its path traversal review (2026-11-19) stored a file access
		// event's Decision as the Mac sent it, so a Mac could file
		// FileAccess#<id> rows under BLOCK_BINARY for 90 days, and a row the
		// store did not write can carry any index attribute. The filter keeps
		// them off the page; it applies after Limit, hence the refill below.
		in.FilterExpression = aws.String("begins_with(PK, :prefix)")
		in.ExpressionAttributeValues[":prefix"] = &types.AttributeValueMemberS{Value: q.prefix}
	}
	if p.Cursor != "" {
		key, err := s.decodeEventCursor(q, p.Cursor)
		if err != nil {
			return eventRows{}, err
		}
		in.ExclusiveStartKey = key
	}
	var rows eventRows
	for range maxEventQueries {
		in.Limit = aws.Int32(int32(p.Limit - len(rows.items)))
		out, err := s.client.Query(ctx, in)
		if err != nil {
			return eventRows{}, err
		}
		rows.items = append(rows.items, out.Items...)
		rows.lastKey = out.LastEvaluatedKey
		// A Query that evaluated no rows made no progress, so querying again
		// from its key would read nothing new; the page ends there with its
		// cursor.
		if len(rows.items) >= p.Limit || len(out.LastEvaluatedKey) == 0 || out.ScannedCount == 0 {
			break
		}
		in.ExclusiveStartKey = out.LastEvaluatedKey
	}
	return rows, nil
}

// eventCursor is the cursor to the page after rows, or "" on the last page.
func (s *Store) eventCursor(q eventQuery, rows eventRows) (string, error) {
	if len(rows.lastKey) == 0 {
		return "", nil
	}
	return s.encodeCursor(q.fingerprint(), rows.lastKey)
}

// decodeEventCursor checks, on top of the codec's HMAC, that the position is
// a key of q's table or index inside q's partition: PK and SK for a machine,
// and for an index also its hash attribute and TS. An index cursor may stand
// on a row of the other list, as DynamoDB's LastEvaluatedKey is the last row
// the Query read, including one the filter left out; it is still bound to q
// by the fingerprint and the hash attribute.
func (s *Store) decodeEventCursor(q eventQuery, cursor string) (map[string]types.AttributeValue, error) {
	plain, err := s.decodePosition(q.fingerprint(), cursor)
	if err != nil {
		return nil, err
	}
	names := []string{"PK", "SK"}
	if q.index != "" {
		names = append(names, q.hashAttr, attrTS)
	}
	if len(plain) != len(names) {
		return nil, store.ErrInvalidCursor
	}
	key := make(map[string]types.AttributeValue, len(names))
	for _, name := range names {
		v, ok := plain[name]
		if !ok || v == "" {
			return nil, store.ErrInvalidCursor
		}
		key[name] = &types.AttributeValueMemberS{Value: v}
	}
	if plain[q.hashAttr] != q.hashValue || !isEventPartition(plain["PK"]) {
		return nil, store.ErrInvalidCursor
	}
	return key, nil
}

// isEventPartition reports whether pk is a partition of the events table's
// event rows, the only rows with TS and so the only ones in its indexes.
func isEventPartition(pk string) bool {
	return strings.HasPrefix(pk, execPartitionPrefix) || strings.HasPrefix(pk, fileAccessPartitionPrefix)
}

// DecodeExecutionEvent decodes one event item of Rudolph's events table. The
// decision is kept as stored, so one the web view does not know (Santa adds
// them) is shown rather than failing the page.
func DecodeExecutionEvent(item map[string]types.AttributeValue) (store.ExecutionEvent, error) {
	r := &reader{item: item}
	e := store.ExecutionEvent{
		MachineID:     r.str("MachineID"),
		ExecutionTime: r.microTS(attrTS),
		Decision:      store.Decision(r.str("Decision")),
		FileSHA256:    r.str("FileSHA256"),
		FilePath:      r.str("FilePath"),
		FileName:      r.str("FileName"),
		ExecutingUser: r.str("ExecutingUser"),
		TeamID:        r.str("TeamID"),
		SigningID:     r.str("SigningID"),
		CDHash:        r.str("CDHash"),
		// Rudolph stores this only when true; anything but a BOOL is read as
		// false rather than failing the page.
		IdentityUnverified: boolOrFalse(item["IdentityUnverified"]),
	}
	if e.MachineID == "" {
		e.MachineID = strings.TrimPrefix(r.str("PK"), execPartitionPrefix)
	}
	if r.err != nil {
		return store.ExecutionEvent{}, fmt.Errorf("dynamo: decoding execution event %q: %w", r.str("SK"), r.err)
	}
	if err := checkRowMachineIDs(r, e.MachineID, execPartitionPrefix); err != nil {
		return store.ExecutionEvent{}, fmt.Errorf("dynamo: decoding execution event %q: %w", r.str("SK"), err)
	}
	return e, nil
}

func boolOrFalse(v types.AttributeValue) bool {
	b, ok := v.(*types.AttributeValueMemberBOOL)
	return ok && b.Value
}

// microTS reads a required time stored as zero-padded decimal microseconds
// since the Unix epoch, in UTC.
func (r *reader) microTS(name string) time.Time {
	s := r.str(name)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		r.fail(fmt.Errorf("%s: want microseconds since the epoch, got %q", name, s))
		return time.Time{}
	}
	return time.UnixMicro(n).UTC()
}

// keyString returns a string key attribute for logging, or "" for anything
// else.
func keyString(v types.AttributeValue) string {
	if s, ok := v.(*types.AttributeValueMemberS); ok {
		return s.Value
	}
	return ""
}
