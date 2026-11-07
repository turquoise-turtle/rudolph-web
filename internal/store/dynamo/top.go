package dynamo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// Rudolph's ranker (internal/ranker, docs/events-table.md "Top
// binaries") writes each list as a snapshot of ranked rows in
// TopSnapshot#<kind>#<days>#<snapshot ID>, SK the rank as six digits, then
// moves the pointer Top#<kind>#<days> / Current to it. Snapshot rows expire 8
// days after they were written.
const (
	topBinariesMethod  = "TopBinaries"
	topPointerPrefix   = "Top#"
	topSnapshotPrefix  = "TopSnapshot#"
	skTopPointer       = "Current"
	topRankDigits      = 6
	attrTopSnapshot    = "Snapshot"
	attrTopGeneratedAt = "GeneratedAt" // on the pointer, and in cursors next to the snapshot row key
	attrTopMachines    = "Machines"
)

// topKindNames are the kind segments Rudolph writes in the keys.
var topKindNames = map[store.TopKind]string{
	store.TopKindBlocked: "Blocked",
	store.TopKindAllowed: "Allowed",
}

// topList is one validated kind and window.
type topList struct {
	kind string
	days int
}

func (l topList) pointerPK() string {
	return fmt.Sprintf("%s%s#%d", topPointerPrefix, l.kind, l.days)
}

// snapshotPrefix is the partition key of every snapshot of this list, up to
// the snapshot ID.
func (l topList) snapshotPrefix() string {
	return fmt.Sprintf("%s%s#%d#", topSnapshotPrefix, l.kind, l.days)
}

// fingerprint binds a cursor to the kind and window; the snapshot it pins is
// in the position, under the HMAC.
func (l topList) fingerprint() string {
	b, _ := json.Marshal([]string{topBinariesMethod, l.kind, strconv.Itoa(l.days)})
	return string(b)
}

// TopBinaries implements store.EventReader. The first page reads the pointer
// (one GetItem) and the snapshot it names (one Query, Limit = page size).
// Later pages read only the snapshot their cursor pins, so a listing stays
// consistent while the ranker moves the pointer, and show the generation time
// of that snapshot. A cursor to a snapshot that has expired returns
// ErrInvalidCursor.
func (s *Store) TopBinaries(ctx context.Context, f store.TopFilter, p store.PageRequest) (store.TopList, error) {
	if s.eventsTable == "" {
		return store.TopList{}, fmt.Errorf("%w: no execution events table configured", store.ErrUnavailable)
	}
	if err := f.Validate(); err != nil {
		return store.TopList{}, err
	}
	if err := checkLimit(p); err != nil {
		return store.TopList{}, err
	}
	l := topList{kind: topKindNames[f.Kind], days: f.WindowDays}

	var (
		partition   string
		generatedAt time.Time
		start       map[string]types.AttributeValue
	)
	if p.Cursor != "" {
		var err error
		if start, generatedAt, err = s.decodeTopCursor(l, p.Cursor); err != nil {
			return store.TopList{}, err
		}
		partition = start["PK"].(*types.AttributeValueMemberS).Value
	} else {
		snapshot, at, found, err := s.readTopPointer(ctx, l)
		if err != nil || !found {
			return store.TopList{}, err
		}
		partition, generatedAt = l.snapshotPrefix()+snapshot, at
	}

	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.eventsTable),
		KeyConditionExpression: aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: partition},
		},
		ExclusiveStartKey: start,
		Limit:             aws.Int32(int32(p.Limit)),
		ScanIndexForward:  aws.Bool(true),
	})
	if err != nil {
		return store.TopList{}, fmt.Errorf("dynamo: reading top list snapshot: %w", err)
	}
	if p.Cursor != "" && len(out.Items) == 0 {
		if err := s.checkTopSnapshotExists(ctx, partition); err != nil {
			return store.TopList{}, err
		}
	}
	list := store.TopList{Items: make([]store.TopBinary, 0, len(out.Items)), GeneratedAt: generatedAt}
	for _, it := range out.Items {
		b, err := DecodeTopBinary(it)
		if err != nil {
			// As with events: one bad row should not hide the rest of the list.
			s.log.Warn("skipping undecodable top list row", "pk", keyString(it["PK"]), "sk", keyString(it["SK"]), "err", err)
			continue
		}
		list.Items = append(list.Items, b)
	}
	if len(out.LastEvaluatedKey) > 0 {
		key := make(map[string]types.AttributeValue, len(out.LastEvaluatedKey)+1)
		for k, v := range out.LastEvaluatedKey {
			key[k] = v
		}
		key[attrTopGeneratedAt] = &types.AttributeValueMemberS{Value: generatedAt.Format(time.RFC3339Nano)}
		if list.NextCursor, err = s.encodeCursor(l.fingerprint(), key); err != nil {
			return store.TopList{}, err
		}
	}
	return list, nil
}

// checkTopSnapshotExists runs after a cursor's page came back empty, which
// happens either when the pinned snapshot has expired (its rows are deleted 8
// days after the ranker wrote them) or when the previous page ended exactly on
// the snapshot's last row: a Query that stops at its Limit returns a
// LastEvaluatedKey without knowing whether more rows follow, so such a page
// still hands out a cursor. Telling the two apart costs one more Query, but
// only on this empty page, never on a page with rows. An expired snapshot
// returns ErrInvalidCursor so the page asks the user to start again rather
// than showing an empty list as if nothing had been blocked or allowed.
func (s *Store) checkTopSnapshotExists(ctx context.Context, partition string) error {
	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.eventsTable),
		KeyConditionExpression: aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: partition},
		},
		ProjectionExpression: aws.String("PK"),
		Limit:                aws.Int32(1),
	})
	if err != nil {
		return fmt.Errorf("dynamo: checking top list snapshot: %w", err)
	}
	if len(out.Items) == 0 {
		return store.ErrInvalidCursor
	}
	return nil
}

// readTopPointer reads the list's pointer. found is false when the ranker has
// not written it yet.
func (s *Store) readTopPointer(ctx context.Context, l topList) (snapshot string, generatedAt time.Time, found bool, err error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.eventsTable),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: l.pointerPK()},
			"SK": &types.AttributeValueMemberS{Value: skTopPointer},
		},
	})
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("dynamo: reading top list pointer: %w", err)
	}
	if len(out.Item) == 0 {
		return "", time.Time{}, false, nil
	}
	r := &reader{item: out.Item}
	snapshot = r.str(attrTopSnapshot)
	generatedAt = r.timestamp(attrTopGeneratedAt)
	if r.err == nil && (snapshot == "" || generatedAt.IsZero()) {
		r.fail(errors.New("Snapshot and GeneratedAt are required"))
	}
	if r.err != nil {
		return "", time.Time{}, false, fmt.Errorf("dynamo: decoding top list pointer %s: %w", l.pointerPK(), r.err)
	}
	return snapshot, generatedAt, true, nil
}

// decodeTopCursor checks, on top of the codec's HMAC, that the position is a
// key of one of this list's snapshots with a rank sort key, and returns it
// with the generation time of that snapshot.
func (s *Store) decodeTopCursor(l topList, cursor string) (map[string]types.AttributeValue, time.Time, error) {
	plain, err := s.decodePosition(l.fingerprint(), cursor)
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(plain) != 3 {
		return nil, time.Time{}, store.ErrInvalidCursor
	}
	pk, sk, at := plain["PK"], plain["SK"], plain[attrTopGeneratedAt]
	snapshot, ok := strings.CutPrefix(pk, l.snapshotPrefix())
	if !ok || snapshot == "" || !isRank(sk) {
		return nil, time.Time{}, store.ErrInvalidCursor
	}
	generatedAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, time.Time{}, store.ErrInvalidCursor
	}
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: pk},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}, generatedAt.UTC(), nil
}

func isRank(sk string) bool {
	if len(sk) != topRankDigits {
		return false
	}
	for _, c := range sk {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// DecodeTopBinary decodes one snapshot row. Machines is absent below the
// first 100 rows, where the ranker does not count Macs.
func DecodeTopBinary(item map[string]types.AttributeValue) (store.TopBinary, error) {
	r := &reader{item: item}
	b := store.TopBinary{
		FileSHA256:    r.str("FileSHA256"),
		FileName:      r.str("FileName"),
		TeamID:        r.str("TeamID"),
		SigningID:     r.str("SigningID"),
		Count:         int(r.requiredNum("Count")),
		MachinesKnown: r.present(attrTopMachines),
	}
	if b.MachinesKnown {
		b.Machines = int(r.num(attrTopMachines))
	}
	if r.err == nil && b.FileSHA256 == "" {
		r.fail(errors.New("FileSHA256: missing"))
	}
	if r.err != nil {
		return store.TopBinary{}, fmt.Errorf("dynamo: decoding top list row %q: %w", r.str("SK"), r.err)
	}
	return b, nil
}
