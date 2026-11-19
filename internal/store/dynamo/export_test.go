package dynamo

import (
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// The HMAC fingerprint refuses every cursor a client can forge, so the key checks behind it are reached only by a
// cursor the store itself signed. These sign an arbitrary key for a query with the fingerprint the store gives that
// query, which a real LastEvaluatedKey of a future or buggy Query could also carry.

// SignEventCursor signs key as a cursor of the execution list filtered by f.
func SignEventCursor(s *Store, f store.EventFilter, key map[string]string) (string, error) {
	q, err := newEventQuery(f)
	if err != nil {
		return "", err
	}
	return s.encodeCursor(q.fingerprint(), stringKey(key))
}

// SignFileAccessCursor signs key as a cursor of the file access list filtered by f.
func SignFileAccessCursor(s *Store, f store.FileAccessEventFilter, key map[string]string) (string, error) {
	q, err := newFileAccessQuery(f)
	if err != nil {
		return "", err
	}
	return s.encodeCursor(q.fingerprint(), stringKey(key))
}

func stringKey(key map[string]string) map[string]types.AttributeValue {
	out := make(map[string]types.AttributeValue, len(key))
	for name, v := range key {
		out[name] = &types.AttributeValueMemberS{Value: v}
	}
	return out
}
