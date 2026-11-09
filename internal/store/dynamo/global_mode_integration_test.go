//go:build integration

package dynamo_test

// The machine page names the global config's client mode (owner feedback from the first production deployment).
// Rudolph keeps it in the row PK=GlobalConfig, SK=Config, and sends MONITOR when there is no such row
// (machineconfiguration.GetUniversalDefaultConfig). GetMachine stays one Query; the global mode is its own read.

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

func putGlobalConfig(t *testing.T, mode string) {
	t.Helper()
	if _, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{
		TableName: aws.String(ddblocal.TableName),
		Item: map[string]types.AttributeValue{
			"PK":         &types.AttributeValueMemberS{Value: "GlobalConfig"},
			"SK":         &types.AttributeValueMemberS{Value: "Config"},
			"ClientMode": &types.AttributeValueMemberN{Value: mode},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalClientMode(t *testing.T) {
	s := newStore(t)
	putGlobalConfig(t, "2")
	got, err := s.GlobalClientMode(ctx)
	if err != nil || got != store.ClientModeLockdown {
		t.Errorf("global config LOCKDOWN (2): got %q, %v", got, err)
	}

	putGlobalConfig(t, "1")
	if got, err := s.GlobalClientMode(ctx); err != nil || got != store.ClientModeMonitor {
		t.Errorf("global config MONITOR (1): got %q, %v", got, err)
	}
}

func TestGlobalClientMode_NoRowIsMonitor(t *testing.T) {
	s := newStore(t)
	if _, err := ddblocal.Raw(t).DeleteItem(ctx, &awsdynamodb.DeleteItemInput{
		TableName: aws.String(ddblocal.TableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "GlobalConfig"},
			"SK": &types.AttributeValueMemberS{Value: "Config"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GlobalClientMode(ctx); err != nil || got != store.ClientModeMonitor {
		t.Errorf("no global config row: got %q, %v; want MONITOR, as Rudolph sends", got, err)
	}
}

// The machine page reads it through store.Reader.
func TestGlobalClientMode_IsPartOfTheReader(t *testing.T) {
	var r store.Reader = newStore(t)
	putGlobalConfig(t, "2")
	if got, err := r.GlobalClientMode(ctx); err != nil || got != store.ClientModeLockdown {
		t.Errorf("through store.Reader: got %q, %v", got, err)
	}
}
