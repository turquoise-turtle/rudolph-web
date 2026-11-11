package dynamo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	rudolphgroups "github.com/airbnb/rudolph/pkg/model/groups"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

const listGroupMembersMethod = "ListGroupMembers"

// unprocessedAttempts bounds the BatchGetItem retries of keys DynamoDB leaves
// unprocessed (it does so when throttled), so a page never waits forever.
const unprocessedAttempts = 4

// groupMemberQuery is one paginated member list: a group's partition, with or
// without the Macs that only report the group.
type groupMemberQuery struct {
	group        string
	assignedOnly bool
}

func (q groupMemberQuery) partition() string {
	return rudolphgroups.GroupMembersPKPrefix + q.group
}

// fingerprint binds a cursor to the group and the filter: a position from the
// unfiltered list would otherwise resume a filtered one, or another group's.
func (q groupMemberQuery) fingerprint() string {
	filter := "all"
	if q.assignedOnly {
		filter = "assigned"
	}
	b, _ := json.Marshal([]string{listGroupMembersMethod, q.group, filter})
	return string(b)
}

// ListGroupMembers implements store.GroupReader with one Query of the group's
// GroupMembers# partition, in machine ID order, and one BatchGetItem of the
// page's sensor data rows. AssignedOnly is a FilterExpression: DynamoDB applies
// it after Limit, so a page can come back short, or empty with a cursor. The
// cursor is the Query's LastEvaluatedKey, past the rows the filter skipped,
// so the next page still starts in the right place. Rudolph keeps no index of
// assigned members only, and reported-only rows are few next to a group's
// assigned Macs in the groups that count only those.
func (s *Store) ListGroupMembers(ctx context.Context, group string, f store.GroupMemberFilter, p store.PageRequest) (store.Page[store.GroupMember], error) {
	if err := checkLimit(p); err != nil {
		return store.Page[store.GroupMember]{}, err
	}
	q := groupMemberQuery{group: group, assignedOnly: f.AssignedOnly}
	in := &dynamodb.QueryInput{
		TableName:              aws.String(s.table),
		KeyConditionExpression: aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: q.partition()},
		},
		Limit:            aws.Int32(int32(p.Limit)),
		ScanIndexForward: aws.Bool(true),
		ConsistentRead:   consistentRead(ctx),
	}
	if q.assignedOnly {
		in.FilterExpression = aws.String("Assigned = :true")
		in.ExpressionAttributeValues[":true"] = &types.AttributeValueMemberBOOL{Value: true}
	}
	if p.Cursor != "" {
		start, err := s.decodeGroupMemberCursor(q, p.Cursor)
		if err != nil {
			return store.Page[store.GroupMember]{}, err
		}
		in.ExclusiveStartKey = start
	}
	out, err := s.client.Query(ctx, in)
	if err != nil {
		return store.Page[store.GroupMember]{}, fmt.Errorf("dynamo: reading %s: %w", q.partition(), err)
	}
	page := store.Page[store.GroupMember]{Items: make([]store.GroupMember, 0, len(out.Items))}
	for _, it := range out.Items {
		r := reader{item: it}
		m := store.GroupMember{
			Machine:  store.Machine{MachineID: r.str("SK")},
			Assigned: r.boolean("Assigned"),
			Reported: r.boolean("Reported"),
		}
		if r.err != nil {
			return store.Page[store.GroupMember]{}, fmt.Errorf("dynamo: decoding %s row %q: %w", q.partition(), r.str("SK"), r.err)
		}
		page.Items = append(page.Items, m)
	}
	if err := s.addSensorData(ctx, page.Items); err != nil {
		return store.Page[store.GroupMember]{}, err
	}
	if len(out.LastEvaluatedKey) > 0 {
		if page.NextCursor, err = s.encodeCursor(q.fingerprint(), out.LastEvaluatedKey); err != nil {
			return store.Page[store.GroupMember]{}, err
		}
	}
	return page, nil
}

// addSensorData fills in each member's sensor data (Machine#<id>/Current) with
// one BatchGetItem; a page holds at most store.MaxPageSize members, which is
// BatchGetItem's limit of 100 keys. Members without sensor data keep only
// their MachineID.
func (s *Store) addSensorData(ctx context.Context, members []store.GroupMember) error {
	if len(members) == 0 {
		return nil
	}
	keys := make([]map[string]types.AttributeValue, 0, len(members))
	for _, m := range members {
		keys = append(keys, map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "Machine#" + m.Machine.MachineID},
			"SK": &types.AttributeValueMemberS{Value: skCurrent},
		})
	}
	request := map[string]types.KeysAndAttributes{s.table: {Keys: keys, ConsistentRead: consistentRead(ctx)}}
	sensors := make(map[string]store.Machine, len(members))
	for attempt := 0; len(request) > 0; attempt++ {
		if attempt == unprocessedAttempts {
			return fmt.Errorf("dynamo: reading the sensor data of a group's Macs: %d keys still unprocessed", len(request[s.table].Keys))
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(50<<attempt) * time.Millisecond):
			}
		}
		out, err := s.client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: request})
		if err != nil {
			return fmt.Errorf("dynamo: reading the sensor data of a group's Macs: %w", err)
		}
		for _, it := range out.Responses[s.table] {
			r := reader{item: it}
			m := r.machine()
			if r.err != nil {
				return fmt.Errorf("dynamo: decoding %s row %q: %w", r.str("PK"), skCurrent, r.err)
			}
			sensors[strings.TrimPrefix(r.str("PK"), "Machine#")] = m
		}
		request = out.UnprocessedKeys
	}
	for i := range members {
		id := members[i].Machine.MachineID
		if m, ok := sensors[id]; ok {
			// The member row's sort key names the Mac; the sensor data's own
			// MachineID attribute is not needed to find it.
			m.MachineID = id
			members[i].Machine = m
		}
	}
	return nil
}

// decodeGroupMemberCursor checks that the position is a key of q's partition,
// on top of the codec's HMAC.
func (s *Store) decodeGroupMemberCursor(q groupMemberQuery, cursor string) (map[string]types.AttributeValue, error) {
	plain, err := s.decodePosition(q.fingerprint(), cursor)
	if err != nil {
		return nil, err
	}
	pk, hasPK := plain["PK"]
	sk, hasSK := plain["SK"]
	if len(plain) != 2 || !hasPK || !hasSK || pk != q.partition() {
		return nil, store.ErrInvalidCursor
	}
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: pk},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}, nil
}
