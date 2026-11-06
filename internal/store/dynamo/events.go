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
	index     string // empty: the table, PK = partition
	hashAttr  string
	hashValue string
}

// fingerprint binds a cursor to the index and the normalised filter value.
// Each index has its own hash attribute, so index and value identify the
// query.
func (q eventQuery) fingerprint() string {
	b, _ := json.Marshal([]string{listExecutionEventsMethod, q.index, q.hashAttr, q.hashValue})
	return string(b)
}

// newEventQuery picks the one key condition a filter maps to. Rudolph's
// table has no index on two of these attributes, and a FilterExpression would
// apply after Limit and return short pages, so combined filters are refused.
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
	switch {
	case machine != "":
		return eventQuery{hashAttr: "PK", hashValue: execPartitionPrefix + machine}, nil
	case decision != "":
		if !slices.Contains(store.Decisions, decision) {
			return eventQuery{}, fmt.Errorf("%w: unknown decision %q", store.ErrInvalidArgument, decision)
		}
		return eventQuery{index: indexDecisionTS, hashAttr: "Decision", hashValue: string(decision)}, nil
	case teamID != "":
		return eventQuery{index: indexTeamIDTS, hashAttr: "TeamID", hashValue: teamID}, nil
	default:
		return eventQuery{index: indexFleetTS, hashAttr: "Fleet", hashValue: fleetExec}, nil
	}
}

// ListExecutionEvents implements store.EventReader with one Query per page,
// newest first; rows it cannot decode are logged and skipped, so a page may
// hold fewer items than the limit: a machine filter reads the Mac's Exec#<id> partition, a
// decision Decision_TS, a team ID TeamID_TS and no filter Fleet_TS.
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
	in := &dynamodb.QueryInput{
		TableName:                aws.String(s.eventsTable),
		KeyConditionExpression:   aws.String("#h = :h"),
		ExpressionAttributeNames: map[string]string{"#h": q.hashAttr},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":h": &types.AttributeValueMemberS{Value: q.hashValue},
		},
		Limit:            aws.Int32(int32(p.Limit)),
		ScanIndexForward: aws.Bool(false),
	}
	if q.index != "" {
		in.IndexName = aws.String(q.index)
	}
	if p.Cursor != "" {
		if in.ExclusiveStartKey, err = s.decodeEventCursor(q, p.Cursor); err != nil {
			return store.Page[store.ExecutionEvent]{}, err
		}
	}
	out, err := s.client.Query(ctx, in)
	if err != nil {
		return store.Page[store.ExecutionEvent]{}, fmt.Errorf("dynamo: reading execution events: %w", err)
	}
	page := store.Page[store.ExecutionEvent]{Items: make([]store.ExecutionEvent, 0, len(out.Items))}
	for _, it := range out.Items {
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
	if len(out.LastEvaluatedKey) > 0 {
		if page.NextCursor, err = s.encodeCursor(q.fingerprint(), out.LastEvaluatedKey); err != nil {
			return store.Page[store.ExecutionEvent]{}, err
		}
	}
	return page, nil
}

// decodeEventCursor checks, on top of the codec's HMAC, that the position is
// a key of q's table or index inside q's partition: PK and SK for a machine,
// and for an index also its hash attribute and TS.
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
	if plain[q.hashAttr] != q.hashValue || !strings.HasPrefix(plain["PK"], execPartitionPrefix) {
		return nil, store.ErrInvalidCursor
	}
	return key, nil
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
	}
	if e.MachineID == "" {
		e.MachineID = strings.TrimPrefix(r.str("PK"), execPartitionPrefix)
	}
	if r.err != nil {
		return store.ExecutionEvent{}, fmt.Errorf("dynamo: decoding execution event %q: %w", r.str("SK"), r.err)
	}
	return e, nil
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
