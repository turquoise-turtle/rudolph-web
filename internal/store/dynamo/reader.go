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

// QueryAPI is the one DynamoDB call the reader makes. Every page is a bounded
// Query, so the deployment can grant dynamodb:Query and nothing else.
type QueryAPI interface {
	Query(ctx context.Context, in *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

// Options configures New.
type Options struct {
	Client QueryAPI
	// Table is Rudolph's single table.
	Table string
	// EventsTable is Rudolph's execution events table (docs/events-table.md in
	// Rudolph). Empty means the deployment has none, and the event readers
	// answer store.ErrUnavailable.
	EventsTable string
	// Cursors signs pagination cursors. Every instance serving the same users
	// must share the key, or a cursor issued by one is refused by another.
	Cursors *store.CursorCodec
	// Now decides whether a rule's expiry has passed; time.Now if nil.
	Now func() time.Time
}

// Store reads Rudolph's table. It implements store.Reader.
type Store struct {
	client      QueryAPI
	table       string
	eventsTable string
	cursors     *store.CursorCodec
	now         func() time.Time
}

var _ store.Reader = (*Store)(nil)

// New returns a reader over o.Table.
func New(o Options) (*Store, error) {
	switch {
	case o.Client == nil:
		return nil, errors.New("dynamo: no client")
	case o.Table == "":
		return nil, errors.New("dynamo: no table name")
	case o.Cursors == nil:
		return nil, errors.New("dynamo: no cursor codec")
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Store{client: o.Client, table: o.Table, eventsTable: o.EventsTable, cursors: o.Cursors, now: now}, nil
}

// machineDetailLimit bounds the machine detail Query. Config, Current and
// SyncState are the first rows of the SK range below, so a handful is enough,
// and rows Rudolph may add inside the range (sync tracking items, say) cannot
// make the read unbounded.
const machineDetailLimit = 5

// GetMachine implements store.MachineReader with one Query on the
// Machine#<id> partition.
func (s *Store) GetMachine(ctx context.Context, machineID string) (store.MachineDetail, error) {
	if machineID == "" {
		return store.MachineDetail{}, store.ErrNotFound
	}
	// The SK range runs from Config to SyncState so that rows sorting outside
	// it, however many, never push the three rows the page needs past the
	// Limit.
	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.table),
		KeyConditionExpression: aws.String("PK = :pk AND SK BETWEEN :first AND :last"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":    &types.AttributeValueMemberS{Value: "Machine#" + machineID},
			":first": &types.AttributeValueMemberS{Value: skConfig},
			":last":  &types.AttributeValueMemberS{Value: skSyncState},
		},
		Limit: aws.Int32(machineDetailLimit),
	})
	if err != nil {
		return store.MachineDetail{}, fmt.Errorf("dynamo: reading machine: %w", err)
	}
	return DecodeMachinePartition(out.Items)
}

// ListMachineRules implements store.MachineReader.
func (s *Store) ListMachineRules(ctx context.Context, machineID string, p store.PageRequest) (store.Page[store.Rule], error) {
	if err := checkLimit(p); err != nil {
		return store.Page[store.Rule]{}, err
	}
	return s.queryRules(ctx, ruleQuery{
		method:    "ListMachineRules",
		partition: "MachineRules#" + machineID,
		machineID: machineID,
	}, p)
}

// ruleTypePrefixes are the SK prefixes Rudolph gives each rule type.
// Certificates are "Cert#", not "Certificate#".
var ruleTypePrefixes = map[store.RuleType]string{
	store.RuleTypeBinary:      "Binary#",
	store.RuleTypeCertificate: "Cert#",
	store.RuleTypeTeamID:      "TeamID#",
	store.RuleTypeSigningID:   "SigningID#",
	store.RuleTypeCDHash:      "CDHash#",
}

// ListGlobalRules implements store.RuleReader.
func (s *Store) ListGlobalRules(ctx context.Context, f store.RuleFilter, p store.PageRequest) (store.Page[store.Rule], error) {
	if err := checkLimit(p); err != nil {
		return store.Page[store.Rule]{}, err
	}
	prefix := ""
	if f.RuleType != "" {
		var ok bool
		if prefix, ok = ruleTypePrefixes[f.RuleType]; !ok {
			return store.Page[store.Rule]{}, fmt.Errorf("%w: unknown rule type %q", store.ErrInvalidArgument, f.RuleType)
		}
	}
	q := ruleQuery{
		method:    "ListGlobalRules",
		partition: "GlobalRules",
		ruleType:  f.RuleType,
		skPrefix:  prefix,
	}
	if f.Policy != "" {
		n, ok := policyNumbers[f.Policy]
		if !ok {
			return store.Page[store.Rule]{}, fmt.Errorf("%w: unknown policy %q", store.ErrInvalidArgument, f.Policy)
		}
		// A FilterExpression would apply after Limit and return short pages,
		// so a policy reads Rudolph's GlobalRulePolicy_SK index instead.
		q.index = indexGlobalRulePolicy
		q.policy = f.Policy
		q.policyNum = strconv.FormatInt(n, 10)
	}
	return s.queryRules(ctx, q, p)
}

// Rudolph sets GlobalRulePolicy, a copy of Policy, on global rule rows only,
// and indexes it with SK as the range key.
const (
	indexGlobalRulePolicy = "GlobalRulePolicy_SK"
	attrGlobalRulePolicy  = "GlobalRulePolicy"
)

// policyNumbers is the inverse of policies, Rudolph's numbering.
var policyNumbers = func() map[store.Policy]int64 {
	m := make(map[store.Policy]int64, len(policies))
	for n, pol := range policies {
		m[pol] = n
	}
	return m
}()

// Rudolph's machine list indexes. Both are keyed DataType = "SensorData" and
// project the attributes the list shows, so a page is one Query with no
// follow-up reads. A Mac is in an index only if it reported that attribute.
const (
	dataTypeSensorData = "SensorData"
	indexSerialKey     = "DataType_SerialKey"
	indexHostnameKey   = "DataType_HostnameKey"
	attrSerialKey      = "SerialKey"
	attrHostnameKey    = "HostnameKey"
	listMachinesMethod = "ListMachines"
)

// ListMachines implements store.MachineReader with one Query on
// DataType_SerialKey (no filter, or a serial prefix) or DataType_HostnameKey
// (a hostname prefix). Never a FilterExpression: Limit applies before the
// filter, so pages would come back short.
func (s *Store) ListMachines(ctx context.Context, f store.MachineFilter, p store.PageRequest) (store.Page[store.Machine], error) {
	if err := checkLimit(p); err != nil {
		return store.Page[store.Machine]{}, err
	}
	// Rudolph keeps SerialNum as reported (some VMs report mixed case) and
	// indexes SerialKey, the trimmed upper-case serial, and HostnameKey, the
	// trimmed lower-case hostname.
	serial := strings.ToUpper(strings.TrimSpace(f.SerialPrefix))
	hostname := strings.ToLower(strings.TrimSpace(f.HostnamePrefix))
	if serial != "" && hostname != "" {
		return store.Page[store.Machine]{}, fmt.Errorf("%w: filter by serial or hostname, not both", store.ErrInvalidArgument)
	}
	q := machineQuery{index: indexSerialKey, rangeKey: attrSerialKey, prefix: serial}
	if hostname != "" {
		q = machineQuery{index: indexHostnameKey, rangeKey: attrHostnameKey, prefix: hostname}
	}

	in := &dynamodb.QueryInput{
		TableName:              aws.String(s.table),
		IndexName:              aws.String(q.index),
		KeyConditionExpression: aws.String("DataType = :dt"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":dt": &types.AttributeValueMemberS{Value: dataTypeSensorData},
		},
		Limit:            aws.Int32(int32(p.Limit)),
		ScanIndexForward: aws.Bool(true),
	}
	if q.prefix != "" {
		in.KeyConditionExpression = aws.String("DataType = :dt AND begins_with(#rk, :prefix)")
		in.ExpressionAttributeNames = map[string]string{"#rk": q.rangeKey}
		in.ExpressionAttributeValues[":prefix"] = &types.AttributeValueMemberS{Value: q.prefix}
	}
	if p.Cursor != "" {
		start, err := s.decodeMachineCursor(q, p.Cursor)
		if err != nil {
			return store.Page[store.Machine]{}, err
		}
		in.ExclusiveStartKey = start
	}
	out, err := s.client.Query(ctx, in)
	if err != nil {
		return store.Page[store.Machine]{}, fmt.Errorf("dynamo: reading %s: %w", q.index, err)
	}
	page := store.Page[store.Machine]{Items: make([]store.Machine, 0, len(out.Items))}
	for _, it := range out.Items {
		m, err := DecodeMachineListItem(it)
		if err != nil {
			return store.Page[store.Machine]{}, err
		}
		page.Items = append(page.Items, m)
	}
	if len(out.LastEvaluatedKey) > 0 {
		if page.NextCursor, err = s.encodeCursor(q.fingerprint(), out.LastEvaluatedKey); err != nil {
			return store.Page[store.Machine]{}, err
		}
	}
	return page, nil
}

// machineQuery is one paginated machine list: an index and an optional,
// already normalised, prefix of its range key.
type machineQuery struct {
	index    string
	rangeKey string
	prefix   string
}

// fingerprint binds a cursor to the index and the normalised prefix, so the
// same prefix typed in another case or with spaces keeps the cursor valid.
func (q machineQuery) fingerprint() string {
	b, _ := json.Marshal([]string{listMachinesMethod, q.index, q.prefix})
	return string(b)
}

// decodeMachineCursor checks that the position is a key of q's index within
// q's prefix, on top of the codec's HMAC.
func (s *Store) decodeMachineCursor(q machineQuery, cursor string) (map[string]types.AttributeValue, error) {
	plain, err := s.decodePosition(q.fingerprint(), cursor)
	if err != nil {
		return nil, err
	}
	if len(plain) != 4 {
		return nil, store.ErrInvalidCursor
	}
	pk, hasPK := plain["PK"]
	sk, hasSK := plain["SK"]
	dt, hasDT := plain["DataType"]
	rk, hasRK := plain[q.rangeKey]
	if !hasPK || !hasSK || !hasDT || !hasRK || !strings.HasPrefix(pk, "Machine#") ||
		sk != skCurrent || dt != dataTypeSensorData || !strings.HasPrefix(rk, q.prefix) {
		return nil, store.ErrInvalidCursor
	}
	key := make(map[string]types.AttributeValue, len(plain))
	for name, v := range plain {
		key[name] = &types.AttributeValueMemberS{Value: v}
	}
	return key, nil
}

// TopBlockedBinaries and ListFileAccessEvents answer store.ErrUnavailable
// until the web view reads Rudolph's day counters and file access events.
func (s *Store) TopBlockedBinaries(context.Context, store.TopBlockedFilter, store.PageRequest) (store.Page[store.BlockedBinary], error) {
	return store.Page[store.BlockedBinary]{}, fmt.Errorf("%w: blocked binaries need Rudolph's events table", store.ErrUnavailable)
}

func (s *Store) ListFileAccessEvents(context.Context, store.FileAccessEventFilter, store.PageRequest) (store.Page[store.FileAccessEvent], error) {
	return store.Page[store.FileAccessEvent]{}, fmt.Errorf("%w: file access events need Rudolph's events table", store.ErrUnavailable)
}

func checkLimit(p store.PageRequest) error {
	if p.Limit < 1 || p.Limit > store.MaxPageSize {
		return fmt.Errorf("%w: limit %d outside 1..%d", store.ErrInvalidArgument, p.Limit, store.MaxPageSize)
	}
	return nil
}

// ruleQuery is one paginated rule list: a partition, optionally narrowed to
// one rule type's SK prefix, or, with a policy, the global rules of that
// policy from the GlobalRulePolicy_SK index.
type ruleQuery struct {
	method    string
	partition string
	machineID string
	ruleType  store.RuleType
	skPrefix  string
	index     string
	policy    store.Policy
	policyNum string
}

// fingerprint binds a cursor to the method, machine, index and filter, but
// not to the page size, so a reader may change the page size and keep going.
func (q ruleQuery) fingerprint() string {
	b, _ := json.Marshal([]string{q.method, q.machineID, string(q.ruleType), q.index, string(q.policy)})
	return string(b)
}

func (s *Store) queryRules(ctx context.Context, q ruleQuery, p store.PageRequest) (store.Page[store.Rule], error) {
	in := &dynamodb.QueryInput{
		TableName:              aws.String(s.table),
		KeyConditionExpression: aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: q.partition},
		},
		Limit:            aws.Int32(int32(p.Limit)),
		ScanIndexForward: aws.Bool(true),
	}
	if q.index != "" {
		in.IndexName = aws.String(q.index)
		in.KeyConditionExpression = aws.String("GlobalRulePolicy = :policy")
		in.ExpressionAttributeValues = map[string]types.AttributeValue{
			":policy": &types.AttributeValueMemberN{Value: q.policyNum},
		}
	}
	if q.skPrefix != "" {
		in.KeyConditionExpression = aws.String(*in.KeyConditionExpression + " AND begins_with(SK, :prefix)")
		in.ExpressionAttributeValues[":prefix"] = &types.AttributeValueMemberS{Value: q.skPrefix}
	}
	if p.Cursor != "" {
		start, err := s.decodeCursor(q, p.Cursor)
		if err != nil {
			return store.Page[store.Rule]{}, err
		}
		in.ExclusiveStartKey = start
	}
	out, err := s.client.Query(ctx, in)
	if err != nil {
		return store.Page[store.Rule]{}, fmt.Errorf("dynamo: reading %s: %w", q.partition, err)
	}
	now := s.now()
	page := store.Page[store.Rule]{Items: make([]store.Rule, 0, len(out.Items))}
	for _, it := range out.Items {
		r, err := DecodeRule(it, now)
		if err != nil {
			return store.Page[store.Rule]{}, err
		}
		page.Items = append(page.Items, r)
	}
	if len(out.LastEvaluatedKey) > 0 {
		if page.NextCursor, err = s.encodeCursor(q.fingerprint(), out.LastEvaluatedKey); err != nil {
			return store.Page[store.Rule]{}, err
		}
	}
	return page, nil
}

// encodeCursor serialises a LastEvaluatedKey as a JSON object of its string
// and number attributes (numbers as their decimal text); encoding/json sorts
// map keys, so the form is canonical. Decoders know which attributes are
// numbers.
func (s *Store) encodeCursor(fingerprint string, key map[string]types.AttributeValue) (string, error) {
	plain := make(map[string]string, len(key))
	for name, v := range key {
		switch v := v.(type) {
		case *types.AttributeValueMemberS:
			plain[name] = v.Value
		case *types.AttributeValueMemberN:
			plain[name] = v.Value
		default:
			return "", fmt.Errorf("dynamo: key attribute %s is %T, want a string or number", name, v)
		}
	}
	position, err := json.Marshal(plain)
	if err != nil {
		return "", err
	}
	return s.cursors.Encode(fingerprint, string(position))
}

// decodePosition verifies a cursor issued for fingerprint and returns the key
// attributes it carries. Callers still check the names and values against
// their own query.
func (s *Store) decodePosition(fingerprint, cursor string) (map[string]string, error) {
	position, err := s.cursors.Decode(fingerprint, cursor)
	if err != nil {
		return nil, err
	}
	var plain map[string]string
	if err := json.Unmarshal([]byte(position), &plain); err != nil {
		return nil, store.ErrInvalidCursor
	}
	return plain, nil
}

// decodeCursor checks the key itself as well as the codec's HMAC, so that a
// cursor can never start a Query in a partition or SK range other than its
// own, even if the fingerprint ever stopped covering them.
func (s *Store) decodeCursor(q ruleQuery, cursor string) (map[string]types.AttributeValue, error) {
	plain, err := s.decodePosition(q.fingerprint(), cursor)
	if err != nil {
		return nil, err
	}
	want := 2
	if q.index != "" {
		want = 3
	}
	if len(plain) != want {
		return nil, store.ErrInvalidCursor
	}
	pk, hasPK := plain["PK"]
	sk, hasSK := plain["SK"]
	if !hasPK || !hasSK || pk != q.partition || !strings.HasPrefix(sk, q.skPrefix) {
		return nil, store.ErrInvalidCursor
	}
	key := map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: pk},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}
	if q.index != "" {
		if plain[attrGlobalRulePolicy] != q.policyNum {
			return nil, store.ErrInvalidCursor
		}
		key[attrGlobalRulePolicy] = &types.AttributeValueMemberN{Value: q.policyNum}
	}
	return key, nil
}
