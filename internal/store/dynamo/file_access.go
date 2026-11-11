package dynamo

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// Rudolph's file access events (Rudolph 85eee68 and later) share the events
// table with executions: items in FileAccess#<machine ID> partitions sorted
// by "<TS>#<pid>#<hash>", under Fleet "FileAccess" in Fleet_TS and Santa's
// prefixed decision names in Decision_TS, so neither list's partitions hold
// the other's items.
const (
	listFileAccessEventsMethod = "ListFileAccessEvents"
	fileAccessPartitionPrefix  = "FileAccess#"
	fleetFileAccess            = "FileAccess"
	fileAccessDecisionPrefix   = "FILE_ACCESS_DECISION_"
)

// newFileAccessQuery picks the one key condition a filter maps to; as for
// executions, a combination would need a FilterExpression and short pages.
func newFileAccessQuery(f store.FileAccessEventFilter) (eventQuery, error) {
	machine := strings.TrimSpace(f.MachineID)
	decision := store.FileAccessDecision(strings.TrimSpace(string(f.Decision)))
	if machine != "" && decision != "" {
		return eventQuery{}, fmt.Errorf("%w: filter file access events by machine or decision, one at a time", store.ErrInvalidArgument)
	}
	q := eventQuery{method: listFileAccessEventsMethod, prefix: fileAccessPartitionPrefix}
	switch {
	case machine != "":
		q.hashAttr, q.hashValue = "PK", fileAccessPartitionPrefix+machine
	case decision != "":
		if !slices.Contains(store.FileAccessDecisions, decision) {
			return eventQuery{}, fmt.Errorf("%w: unknown file access decision %q", store.ErrInvalidArgument, decision)
		}
		q.index, q.hashAttr, q.hashValue = indexDecisionTS, "Decision", fileAccessDecisionPrefix+string(decision)
	default:
		q.index, q.hashAttr, q.hashValue = indexFleetTS, "Fleet", fleetFileAccess
	}
	return q, nil
}

// ListFileAccessEvents implements store.EventReader with one Query per page,
// newest first: a machine filter reads the Mac's FileAccess#<id> partition, a
// decision Decision_TS and no filter Fleet_TS. Rows it cannot decode are
// logged and skipped, so a page may hold fewer items than the limit.
func (s *Store) ListFileAccessEvents(ctx context.Context, f store.FileAccessEventFilter, p store.PageRequest) (store.Page[store.FileAccessEvent], error) {
	if s.eventsTable == "" {
		return store.Page[store.FileAccessEvent]{}, fmt.Errorf("%w: no events table configured", store.ErrUnavailable)
	}
	if err := checkLimit(p); err != nil {
		return store.Page[store.FileAccessEvent]{}, err
	}
	q, err := newFileAccessQuery(f)
	if err != nil {
		return store.Page[store.FileAccessEvent]{}, err
	}
	out, err := s.queryEvents(ctx, q, p)
	if err != nil {
		return store.Page[store.FileAccessEvent]{}, fmt.Errorf("dynamo: reading file access events: %w", err)
	}
	page := store.Page[store.FileAccessEvent]{Items: make([]store.FileAccessEvent, 0, len(out.Items))}
	for _, it := range out.Items {
		e, err := DecodeFileAccessEvent(it)
		if err != nil {
			// As for executions: one bad row must not hide every older event
			// until it expires.
			s.log.Warn("skipping undecodable file access event", "pk", keyString(it["PK"]), "sk", keyString(it["SK"]), "err", err)
			continue
		}
		page.Items = append(page.Items, e)
	}
	if page.NextCursor, err = s.eventCursor(q, out); err != nil {
		return store.Page[store.FileAccessEvent]{}, err
	}
	return page, nil
}

// DecodeFileAccessEvent decodes one file access item of Rudolph's events
// table. Rudolph stores the first process's details as Process* attributes and
// only the paths of the whole chain in ProcessChain, so the parents carry
// their path alone. A decision without Santa's prefix, or one the view does
// not know, is shown as stored.
func DecodeFileAccessEvent(item map[string]types.AttributeValue) (store.FileAccessEvent, error) {
	r := &reader{item: item}
	e := store.FileAccessEvent{
		MachineID:   r.str("MachineID"),
		AccessTime:  r.microTS(attrTS),
		RuleVersion: r.str("RuleVersion"),
		RuleName:    r.str("RuleName"),
		Target:      r.str("Target"),
		Decision:    store.FileAccessDecision(strings.TrimPrefix(r.str("Decision"), fileAccessDecisionPrefix)),
	}
	if e.MachineID == "" {
		e.MachineID = strings.TrimPrefix(r.str("PK"), fileAccessPartitionPrefix)
	}
	chain := r.pathList("ProcessChain")
	first := store.Process{
		FilePath:   r.str("ProcessPath"),
		FileSHA256: r.str("ProcessSHA256"),
		CDHash:     r.str("ProcessCDHash"),
		SigningID:  r.str("ProcessSigningID"),
		TeamID:     r.str("ProcessTeamID"),
		PID:        r.pid("PID"),
	}
	if first != (store.Process{}) || len(chain) > 0 {
		if first.FilePath == "" && len(chain) > 0 {
			first.FilePath = chain[0]
		}
		e.ProcessChain = append(e.ProcessChain, first)
		for _, p := range chain[min(1, len(chain)):] {
			e.ProcessChain = append(e.ProcessChain, store.Process{FilePath: p})
		}
	}
	if r.err != nil {
		return store.FileAccessEvent{}, fmt.Errorf("dynamo: decoding file access event %q: %w", r.str("SK"), r.err)
	}
	return e, nil
}

// pathList reads an optional list of strings.
func (r *reader) pathList(name string) []string {
	switch v := r.item[name].(type) {
	case nil, *types.AttributeValueMemberNULL:
		return nil
	case *types.AttributeValueMemberL:
		out := make([]string, 0, len(v.Value))
		for _, el := range v.Value {
			s, ok := el.(*types.AttributeValueMemberS)
			if !ok {
				r.fail(fmt.Errorf("%s: want a list of strings, got a %T", name, el))
				return nil
			}
			out = append(out, s.Value)
		}
		return out
	default:
		r.fail(fmt.Errorf("%s: want a list, got %T", name, v))
		return nil
	}
}

// pid reads an optional process ID, which Santa sends as an int32.
func (r *reader) pid(name string) int32 {
	n := r.num(name)
	if n < math.MinInt32 || n > math.MaxInt32 {
		r.fail(fmt.Errorf("%s: %d is not a process ID", name, n))
		return 0
	}
	return int32(n)
}
