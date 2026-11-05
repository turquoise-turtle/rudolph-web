package dynamo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// Cursors signs pagination cursors. Every instance serving the same users
	// must share the key, or a cursor issued by one is refused by another.
	Cursors *store.CursorCodec
	// Now decides whether a rule's expiry has passed; time.Now if nil.
	Now func() time.Time
}

// Store reads Rudolph's table. It implements store.Reader.
type Store struct {
	client  QueryAPI
	table   string
	cursors *store.CursorCodec
	now     func() time.Time
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
	return &Store{client: o.Client, table: o.Table, cursors: o.Cursors, now: now}, nil
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
	if f.Policy != "" {
		known := false
		for _, pol := range store.Policies {
			known = known || pol == f.Policy
		}
		if !known {
			return store.Page[store.Rule]{}, fmt.Errorf("%w: unknown policy %q", store.ErrInvalidArgument, f.Policy)
		}
		// A FilterExpression would apply after Limit and return short pages;
		// filtering by policy waits for Rudolph's GlobalRulePolicy_SK index.
		return store.Page[store.Rule]{}, fmt.Errorf("%w: filtering rules by policy needs an index Rudolph does not have yet", store.ErrUnavailable)
	}
	return s.queryRules(ctx, ruleQuery{
		method:    "ListGlobalRules",
		partition: "GlobalRules",
		ruleType:  f.RuleType,
		skPrefix:  prefix,
	}, p)
}

// ListMachines implements store.MachineReader. It needs indexes Rudolph does not
// have yet, so it always answers store.ErrUnavailable.
func (s *Store) ListMachines(context.Context, store.MachineFilter, store.PageRequest) (store.Page[store.Machine], error) {
	return store.Page[store.Machine]{}, fmt.Errorf("%w: listing machines needs Rudolph's machine indexes", store.ErrUnavailable)
}

// ListExecutionEvents implements store.EventReader. Rudolph does not yet store
// events queryably, so it always answers store.ErrUnavailable; so do the other
// event readers.
func (s *Store) ListExecutionEvents(context.Context, store.EventFilter, store.PageRequest) (store.Page[store.ExecutionEvent], error) {
	return store.Page[store.ExecutionEvent]{}, fmt.Errorf("%w: execution events need Rudolph's events table", store.ErrUnavailable)
}

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
// one rule type's SK prefix.
type ruleQuery struct {
	method    string
	partition string
	machineID string
	ruleType  store.RuleType
	skPrefix  string
}

// fingerprint binds a cursor to the method, machine and filter, but not to
// the page size, so a reader may change the page size and keep going.
func (q ruleQuery) fingerprint() string {
	b, _ := json.Marshal([]string{q.method, q.machineID, string(q.ruleType)})
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
	if q.skPrefix != "" {
		in.KeyConditionExpression = aws.String("PK = :pk AND begins_with(SK, :prefix)")
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
		if page.NextCursor, err = s.encodeCursor(q, out.LastEvaluatedKey); err != nil {
			return store.Page[store.Rule]{}, err
		}
	}
	return page, nil
}

// encodeCursor serialises a base-table LastEvaluatedKey as a JSON object of
// its string attributes; encoding/json sorts map keys, so the form is
// canonical.
func (s *Store) encodeCursor(q ruleQuery, key map[string]types.AttributeValue) (string, error) {
	plain := make(map[string]string, len(key))
	for name, v := range key {
		sv, ok := v.(*types.AttributeValueMemberS)
		if !ok {
			return "", fmt.Errorf("dynamo: key attribute %s is %T, want a string", name, v)
		}
		plain[name] = sv.Value
	}
	position, err := json.Marshal(plain)
	if err != nil {
		return "", err
	}
	return s.cursors.Encode(q.fingerprint(), string(position))
}

// decodeCursor checks the key itself as well as the codec's HMAC, so that a
// cursor can never start a Query in a partition or SK range other than its
// own, even if the fingerprint ever stopped covering them.
func (s *Store) decodeCursor(q ruleQuery, cursor string) (map[string]types.AttributeValue, error) {
	position, err := s.cursors.Decode(q.fingerprint(), cursor)
	if err != nil {
		return nil, err
	}
	var plain map[string]string
	if err := json.Unmarshal([]byte(position), &plain); err != nil || len(plain) != 2 {
		return nil, store.ErrInvalidCursor
	}
	pk, hasPK := plain["PK"]
	sk, hasSK := plain["SK"]
	if !hasPK || !hasSK || pk != q.partition || !strings.HasPrefix(sk, q.skPrefix) {
		return nil, store.ErrInvalidCursor
	}
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: pk},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}, nil
}
