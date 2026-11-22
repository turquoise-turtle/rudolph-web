package dynamo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/airbnb/rudolph/pkg/model/fleethealth"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// Rudolph's fleet job (internal/fleet, docs/fleet-health.md and
// docs/events-table.md "Fleet health and readiness rows") writes a snapshot a
// day to the events table as the ranker writes a top list: the list rows
// first, under FleetSnapshot#<snapshot ID>#..., then the pointer FleetPointer
// / Current, which also holds every count. The rows expire 3 days after they
// were written. The lists are read as top.go reads a top list.
const (
	listFleetHealthMethod   = "ListFleetHealth"
	listReadinessMethod     = "ListReadiness"
	listMachineUnknownQuery = "ListMachineUnknown"
	pkFleetPointer          = "FleetPointer"
	fleetSnapshotPrefix     = "FleetSnapshot#"
	fleetHealthSegment      = "#Health#"
	fleetReadyAllSegment    = "#Ready#All"
	fleetReadyGroupSegment  = "#Ready#Group#"
	fleetReadinessAllKey    = "All"
	unknownSeenPrefix       = "UnknownSeen#"
	attrLastDay             = "LastDay"
	// maxSortKeyBytes is DynamoDB's limit for a sort key.
	maxSortKeyBytes = 1024
)

// fleetList is one list of the fleet job, whatever snapshot it is read from.
type fleetList struct {
	method string
	// suffix follows the snapshot ID in the list's partition key.
	suffix string
	// skPrefix narrows the list to the sort keys that start with it: a
	// readiness bucket. Empty for the whole list.
	skPrefix string
	// bound is what a cursor is bound to besides the method: the category, or
	// the group and bucket.
	bound []string
}

func healthList(category store.FleetCategory) fleetList {
	return fleetList{method: listFleetHealthMethod, suffix: fleetHealthSegment + string(category), bound: []string{string(category)}}
}

func readinessList(f store.ReadinessFilter) fleetList {
	l := fleetList{method: listReadinessMethod, suffix: fleetReadyAllSegment, bound: []string{f.Group, string(f.Bucket)}}
	if f.Group != "" {
		l.suffix = fleetReadyGroupSegment + f.Group
	}
	if digit, ok := f.Bucket.SortDigit(); ok {
		l.skPrefix = string(digit) + "#"
	}
	return l
}

func (l fleetList) partition(snapshot string) string {
	return fleetSnapshotPrefix + snapshot + l.suffix
}

// fingerprint binds a cursor to the method and the category, or the group and
// bucket; the snapshot it pins is in the position, under the HMAC.
func (l fleetList) fingerprint() string {
	b, _ := json.Marshal(append([]string{l.method}, l.bound...))
	return string(b)
}

// FleetSummary implements store.FleetReader with one GetItem of the pointer.
func (s *Store) FleetSummary(ctx context.Context) (store.FleetSummary, error) {
	if s.eventsTable == "" {
		return store.FleetSummary{}, fmt.Errorf("%w: no events table configured", store.ErrUnavailable)
	}
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.eventsTable),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: pkFleetPointer},
			"SK": &types.AttributeValueMemberS{Value: skTopPointer},
		},
	})
	if err != nil {
		return store.FleetSummary{}, fmt.Errorf("dynamo: reading the fleet pointer: %w", err)
	}
	if len(out.Item) == 0 {
		return store.FleetSummary{}, nil
	}
	return DecodeFleetSummary(out.Item)
}

// FleetHealthConfig implements store.FleetReader with one GetItem of
// Rudolph's fleet health setting, decoded by Rudolph's own package, so a
// value the fleet job would ignore is ignored here too.
func (s *Store) FleetHealthConfig(ctx context.Context) (store.FleetHealthConfig, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.table),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: fleethealth.PartitionKey},
			"SK": &types.AttributeValueMemberS{Value: fleethealth.SortKey},
		},
		ConsistentRead: consistentRead(ctx),
	})
	if err != nil {
		return store.FleetHealthConfig{}, fmt.Errorf("dynamo: reading the fleet health setting: %w", err)
	}
	c := fleethealth.FromItem(out.Item)
	return store.FleetHealthConfig{StaleDays: c.NormalStaleDays(), HolidayStaleDays: c.HolidayStaleDays, HolidayUntil: c.HolidayUntil}, nil
}

// ListFleetHealth implements store.FleetReader. The first page reads the
// pointer (one GetItem) and the list of the snapshot it names (one Query,
// Limit = page size); later pages read only the snapshot their cursor pins.
func (s *Store) ListFleetHealth(ctx context.Context, category store.FleetCategory, p store.PageRequest) (store.FleetList[store.FleetHealthRow], error) {
	var none store.FleetList[store.FleetHealthRow]
	if s.eventsTable == "" {
		return none, fmt.Errorf("%w: no events table configured", store.ErrUnavailable)
	}
	if !slices.Contains(store.FleetCategories, category) {
		return none, fmt.Errorf("%w: unknown fleet health category %q", store.ErrInvalidArgument, category)
	}
	if err := checkLimit(p); err != nil {
		return none, err
	}
	return queryFleetList(ctx, s, healthList(category), p, nil, DecodeFleetHealthRow)
}

// ListReadiness implements store.FleetReader like ListFleetHealth. A bucket
// is a begins_with range on the sort key, never a filter: Limit applies
// before a filter, so pages would come back short or empty.
func (s *Store) ListReadiness(ctx context.Context, f store.ReadinessFilter, p store.PageRequest) (store.FleetList[store.ReadinessRow], error) {
	var none store.FleetList[store.ReadinessRow]
	if s.eventsTable == "" {
		return none, fmt.Errorf("%w: no events table configured", store.ErrUnavailable)
	}
	if err := f.Validate(); err != nil {
		return none, err
	}
	if err := checkLimit(p); err != nil {
		return none, err
	}
	// The pointer names every group the snapshot has a list for, so a group
	// it does not name is told apart from a group with no Macs without
	// another read.
	known := func(sum store.FleetSummary) error {
		if _, ok := sum.ReadinessGroups[f.Group]; f.Group != "" && !ok {
			return fmt.Errorf("%w: the snapshot has no readiness list for group %q", store.ErrNotFound, f.Group)
		}
		return nil
	}
	return queryFleetList(ctx, s, readinessList(f), p, known, DecodeReadinessRow)
}

// queryFleetList reads one page of l. check, when set, sees the pointer on a
// first page before the list is read.
func queryFleetList[T any](ctx context.Context, s *Store, l fleetList, p store.PageRequest, check func(store.FleetSummary) error,
	decode func(map[string]types.AttributeValue) (T, error)) (store.FleetList[T], error) {
	var none store.FleetList[T]
	var (
		snapshot    string
		generatedAt time.Time
		start       map[string]types.AttributeValue
	)
	if p.Cursor != "" {
		var err error
		if start, snapshot, generatedAt, err = s.decodeFleetCursor(l, p.Cursor); err != nil {
			return none, err
		}
	} else {
		sum, err := s.FleetSummary(ctx)
		if err != nil || sum.GeneratedAt.IsZero() {
			return none, err
		}
		if check != nil {
			if err := check(sum); err != nil {
				return none, err
			}
		}
		snapshot, generatedAt = sum.Snapshot, sum.GeneratedAt
	}
	partition := l.partition(snapshot)

	in := &dynamodb.QueryInput{
		TableName:              aws.String(s.eventsTable),
		KeyConditionExpression: aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: partition},
		},
		ExclusiveStartKey: start,
		Limit:             aws.Int32(int32(p.Limit)),
		ScanIndexForward:  aws.Bool(true),
	}
	if l.skPrefix != "" {
		in.KeyConditionExpression = aws.String("PK = :pk AND begins_with(SK, :prefix)")
		in.ExpressionAttributeValues[":prefix"] = &types.AttributeValueMemberS{Value: l.skPrefix}
	}
	out, err := s.client.Query(ctx, in)
	if err != nil {
		return none, fmt.Errorf("dynamo: reading fleet snapshot: %w", err)
	}
	if p.Cursor != "" && len(out.Items) == 0 {
		// As for a top list: an empty page behind a cursor is either the end
		// of the list or a snapshot whose rows have expired, and only the
		// second must not be shown as an empty list.
		if err := s.checkSnapshotExists(ctx, partition); err != nil {
			return none, err
		}
	}
	list := store.FleetList[T]{Items: make([]T, 0, len(out.Items)), Snapshot: snapshot, GeneratedAt: generatedAt}
	for _, it := range out.Items {
		row, err := decode(it)
		if err != nil {
			// As with events: one bad row should not hide the rest of the list.
			s.log.Warn("skipping undecodable fleet row", "pk", keyString(it["PK"]), "sk", keyString(it["SK"]), "err", err)
			continue
		}
		list.Items = append(list.Items, row)
	}
	if len(out.LastEvaluatedKey) > 0 {
		key := make(map[string]types.AttributeValue, len(out.LastEvaluatedKey)+1)
		for k, v := range out.LastEvaluatedKey {
			key[k] = v
		}
		key[attrTopGeneratedAt] = &types.AttributeValueMemberS{Value: generatedAt.Format(time.RFC3339Nano)}
		if list.NextCursor, err = s.encodeCursor(l.fingerprint(), key); err != nil {
			return none, err
		}
	}
	return list, nil
}

// decodeFleetCursor checks, on top of the codec's HMAC, that the position is
// a key of this list in one of the job's snapshots, inside the list's bucket,
// and returns it with that snapshot's ID and generation time.
func (s *Store) decodeFleetCursor(l fleetList, cursor string) (map[string]types.AttributeValue, string, time.Time, error) {
	invalid := func() (map[string]types.AttributeValue, string, time.Time, error) {
		return nil, "", time.Time{}, store.ErrInvalidCursor
	}
	plain, err := s.decodePosition(l.fingerprint(), cursor)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	if len(plain) != 3 {
		return invalid()
	}
	pk, sk, at := plain["PK"], plain["SK"], plain[attrTopGeneratedAt]
	rest, ok := strings.CutPrefix(pk, fleetSnapshotPrefix)
	if !ok {
		return invalid()
	}
	// A snapshot ID holds no "#", so a key that is the prefix, a snapshot ID
	// and this list's suffix is in this list's partition and no other's,
	// whatever a group is called.
	snapshot, ok := strings.CutSuffix(rest, l.suffix)
	if _, isID := store.SnapshotTime(snapshot); !ok || !isID {
		return invalid()
	}
	if sk == "" || len(sk) > maxSortKeyBytes || !strings.HasPrefix(sk, l.skPrefix) {
		return invalid()
	}
	generatedAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return invalid()
	}
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: pk},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}, snapshot, generatedAt.UTC(), nil
}

// DecodeFleetSummary decodes the fleet job's pointer. Health holds the
// categories the pointer has a count for, and ReadinessGroups every entry of
// its Readiness map but the fleet's own.
func DecodeFleetSummary(item map[string]types.AttributeValue) (store.FleetSummary, error) {
	r := &reader{item: item}
	sum := store.FleetSummary{
		Snapshot:        r.str(attrTopSnapshot),
		GeneratedAt:     r.timestamp(attrTopGeneratedAt),
		Macs:            int(r.num("Macs")),
		Attention:       int(r.num("Attention")),
		VersionUnknown:  int(r.num("VersionUnknown")),
		MinSantaVersion: r.str("MinSantaVersion"),
		StaleDays:       int(r.num("StaleDays")),
		Holiday:         r.boolean("Holiday"),
		HolidayUntil:    r.str("HolidayUntil"),
		Capped:          int(r.num("Capped")),
	}
	if health := r.mapOf("Health"); health != nil {
		sum.Health = map[store.FleetCategory]int{}
		counts := &reader{item: health}
		for _, c := range store.FleetCategories {
			if counts.present(string(c)) {
				sum.Health[c] = int(counts.num(string(c)))
			}
		}
		r.adopt("Health", counts)
	}
	if readiness := r.mapOf("Readiness"); readiness != nil {
		sum.ReadinessGroups = map[string]store.ReadinessCounts{}
		scope := &reader{item: readiness}
		for name := range readiness {
			counts := &reader{item: scope.mapOf(name)}
			c := store.ReadinessCounts{
				Macs: int(counts.num("Macs")), Ready: int(counts.num("Ready")), Few: int(counts.num("Few")),
				Many: int(counts.num("Many")), Unmeasured: int(counts.num("Unmeasured")), Locked: int(counts.num("Locked")),
			}
			r.adopt("Readiness."+name, counts)
			if name == fleetReadinessAllKey {
				sum.ReadinessAll = c
			} else {
				sum.ReadinessGroups[name] = c
			}
		}
		r.adopt("Readiness", scope)
	}
	if r.err == nil && (sum.Snapshot == "" || sum.GeneratedAt.IsZero()) {
		r.fail(errors.New("Snapshot and GeneratedAt are required"))
	}
	if _, ok := store.SnapshotTime(sum.Snapshot); r.err == nil && !ok {
		r.fail(fmt.Errorf("Snapshot: %q is not a snapshot ID", sum.Snapshot))
	}
	if r.err != nil {
		return store.FleetSummary{}, fmt.Errorf("dynamo: decoding the fleet pointer: %w", r.err)
	}
	return sum, nil
}

// mapOf returns a map attribute, or nil if it is absent or NULL.
func (r *reader) mapOf(name string) map[string]types.AttributeValue {
	switch v := r.item[name].(type) {
	case nil, *types.AttributeValueMemberNULL:
		return nil
	case *types.AttributeValueMemberM:
		return v.Value
	default:
		r.fail(fmt.Errorf("%s: want a map, got %T", name, v))
		return nil
	}
}

// adopt takes the first error of a reader of the nested map called name.
func (r *reader) adopt(name string, nested *reader) {
	if nested.err != nil {
		r.fail(fmt.Errorf("%s: %w", name, nested.err))
	}
}

// DecodeFleetHealthRow decodes a row of any health list: the Mac, and the
// attributes of the row's category, which the other categories' rows leave
// out. Modes are stored by name.
func DecodeFleetHealthRow(item map[string]types.AttributeValue) (store.FleetHealthRow, error) {
	r := &reader{item: item}
	row := store.FleetHealthRow{
		MachineID:              r.str("MachineID"),
		Hostname:               r.str("Hostname"),
		SerialNum:              r.str("SerialNum"),
		LastSeen:               r.timestamp("LastSeen"),
		LastCompleted:          r.timestamp("LastCompleted"),
		Never:                  r.boolean("Never"),
		SyncsWithoutPostflight: int(r.num("SyncsWithoutPostflight")),
		Reported:               store.ClientMode(r.str("Reported")),
		Intended:               store.ClientMode(r.str("Intended")),
		Source:                 r.str("Source"),
		Reason:                 r.str("Reason"),
		RulesReceived:          int(r.num("RulesReceived")),
		RulesProcessed:         int(r.num("RulesProcessed")),
		Hash:                   r.str("Hash"),
		ExpectedHash:           r.str("ExpectedHash"),
		Flags:                  r.str("Flags"),
		SantaVersion:           r.str("SantaVersion"),
	}
	if r.err == nil {
		// The list links to the Mac's page by this ID.
		r.err = checkMachineID(row.MachineID)
	}
	if r.err != nil {
		return store.FleetHealthRow{}, fmt.Errorf("dynamo: decoding fleet health row %q: %w", r.str("SK"), r.err)
	}
	return row, nil
}

// DecodeReadinessRow decodes a readiness row. Its bucket is the digit its
// sort key starts with. A Locked row has no unknown counts, as the job does
// not read a locked Mac's markers: Counted says whether the row has them.
func DecodeReadinessRow(item map[string]types.AttributeValue) (store.ReadinessRow, error) {
	r := &reader{item: item}
	row := store.ReadinessRow{
		MachineID:    r.str("MachineID"),
		Hostname:     r.str("Hostname"),
		SerialNum:    r.str("SerialNum"),
		Counted:      r.present("Unknown30") && r.present("Pending30"),
		Unknown30:    int(r.num("Unknown30")),
		Pending30:    int(r.num("Pending30")),
		Unknown7:     int(r.num("Unknown7")),
		Pending7:     int(r.num("Pending7")),
		Capped:       r.boolean("Capped"),
		ReportedMode: store.ClientMode(r.str("ReportedMode")),
		IntendedMode: store.ClientMode(r.str("IntendedMode")),
		ObservedDays: int(r.num("ObservedDays")),
		Why:          r.str("Why"),
	}
	sk := r.str("SK")
	if i := int(firstByte(sk)) - '0'; len(sk) >= 2 && sk[1] == '#' && i >= 0 && i < len(store.ReadinessBuckets) {
		row.Bucket = store.ReadinessBuckets[i]
	} else {
		r.fail(fmt.Errorf("SK: %q does not start with a readiness bucket", sk))
	}
	if r.err == nil {
		// The list links to the Mac's page, and the cohort form posts this ID.
		r.err = checkMachineID(row.MachineID)
	}
	if r.err != nil {
		return store.ReadinessRow{}, fmt.Errorf("dynamo: decoding readiness row %q: %w", sk, r.err)
	}
	return row, nil
}

func firstByte(s string) byte {
	if s == "" {
		return 0
	}
	return s[0]
}

// DecodeUnknownBinary decodes an unknown marker: the file's SHA-256 is its
// sort key, and the display fields are there only when eventupload found
// them usable.
func DecodeUnknownBinary(item map[string]types.AttributeValue) (store.UnknownBinary, error) {
	r := &reader{item: item}
	b := store.UnknownBinary{
		FileSHA256: r.str("SK"),
		FileName:   r.str("FileName"),
		SigningID:  r.str("SigningID"),
		TeamID:     r.str("TeamID"),
	}
	day := r.str(attrLastDay)
	last, err := time.Parse(time.DateOnly, day)
	switch {
	case r.err != nil:
	case b.FileSHA256 == "":
		r.fail(errors.New("SK: missing"))
	case err != nil:
		r.fail(fmt.Errorf("%s: want a date, got %q", attrLastDay, day))
	}
	if r.err != nil {
		return store.UnknownBinary{}, fmt.Errorf("dynamo: decoding unknown marker %q: %w", r.str("SK"), r.err)
	}
	b.LastDay = last.UTC()
	return b, nil
}

// unknownQuery is the Query of a Mac's unknown markers, or false for an ID
// that names no Mac: DynamoDB refuses a partition key over 2048 bytes, and
// only a canonical UUID has markers.
func (s *Store) unknownQuery(machineID string, limit int) (*dynamodb.QueryInput, bool) {
	if checkMachineID(machineID) != nil {
		return nil, false
	}
	return &dynamodb.QueryInput{
		TableName:              aws.String(s.eventsTable),
		KeyConditionExpression: aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: unknownSeenPrefix + machineID},
		},
		Limit:            aws.Int32(int32(limit)),
		ScanIndexForward: aws.Bool(true),
	}, true
}

// MachineUnknown implements store.FleetReader with one Query of the Mac's
// markers, at most store.MaxUnknownCounted of them, counted here: "ran in the
// last N days" is LastDay on or after today minus N - 1, today being the UTC
// date of the store's clock. Markers expire 35 days after their upload and
// TTL deletion lags, so the window is checked rather than trusted.
func (s *Store) MachineUnknown(ctx context.Context, machineID string) (store.UnknownCounts, error) {
	if s.eventsTable == "" {
		return store.UnknownCounts{}, fmt.Errorf("%w: no events table configured", store.ErrUnavailable)
	}
	in, ok := s.unknownQuery(machineID, store.MaxUnknownCounted)
	if !ok {
		return store.UnknownCounts{}, nil
	}
	// Only the date is counted, so the names are not sent.
	in.ProjectionExpression = aws.String(attrLastDay)
	out, err := s.client.Query(ctx, in)
	if err != nil {
		return store.UnknownCounts{}, fmt.Errorf("dynamo: reading unknown markers: %w", err)
	}
	today := s.now().UTC()
	from30 := today.AddDate(0, 0, -29).Format(time.DateOnly)
	from7 := today.AddDate(0, 0, -6).Format(time.DateOnly)
	counts := store.UnknownCounts{More: len(out.LastEvaluatedKey) > 0}
	for _, it := range out.Items {
		day := keyString(it[attrLastDay])
		// Dates in this form compare as strings; anything else is in neither window.
		if _, err := time.Parse(time.DateOnly, day); err != nil {
			continue
		}
		if day >= from30 {
			counts.Last30++
		}
		if day >= from7 {
			counts.Last7++
		}
	}
	return counts, nil
}

// ListMachineUnknown implements store.FleetReader with one Query a page, in
// sort key (SHA-256) order. Nothing is filtered, as a filter would leave
// pages short; a marker it cannot decode is logged and skipped.
func (s *Store) ListMachineUnknown(ctx context.Context, machineID string, p store.PageRequest) (store.Page[store.UnknownBinary], error) {
	var none store.Page[store.UnknownBinary]
	if s.eventsTable == "" {
		return none, fmt.Errorf("%w: no events table configured", store.ErrUnavailable)
	}
	if err := checkLimit(p); err != nil {
		return none, err
	}
	in, ok := s.unknownQuery(machineID, p.Limit)
	if !ok {
		if p.Cursor != "" {
			return none, store.ErrInvalidCursor
		}
		return store.Page[store.UnknownBinary]{Items: []store.UnknownBinary{}}, nil
	}
	partition := unknownSeenPrefix + machineID
	fingerprint, _ := json.Marshal([]string{listMachineUnknownQuery, machineID})
	if p.Cursor != "" {
		plain, err := s.decodePosition(string(fingerprint), p.Cursor)
		if err != nil {
			return none, err
		}
		sk := plain["SK"]
		if len(plain) != 2 || plain["PK"] != partition || sk == "" || len(sk) > maxSortKeyBytes {
			return none, store.ErrInvalidCursor
		}
		in.ExclusiveStartKey = map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: partition},
			"SK": &types.AttributeValueMemberS{Value: sk},
		}
	}
	out, err := s.client.Query(ctx, in)
	if err != nil {
		return none, fmt.Errorf("dynamo: reading unknown markers: %w", err)
	}
	page := store.Page[store.UnknownBinary]{Items: make([]store.UnknownBinary, 0, len(out.Items))}
	for _, it := range out.Items {
		b, err := DecodeUnknownBinary(it)
		if err != nil {
			s.log.Warn("skipping undecodable unknown marker", "pk", keyString(it["PK"]), "sk", keyString(it["SK"]), "err", err)
			continue
		}
		page.Items = append(page.Items, b)
	}
	if len(out.LastEvaluatedKey) > 0 {
		if page.NextCursor, err = s.encodeCursor(string(fingerprint), out.LastEvaluatedKey); err != nil {
			return none, err
		}
	}
	return page, nil
}
