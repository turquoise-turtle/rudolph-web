//go:build integration

package ddblocal

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestMain(m *testing.M) { os.Exit(Main(m)) }

func TestClientTalksToDynamoDBLocalAndIsCounted(t *testing.T) {
	Reset(t)
	ctx := context.Background()
	item := map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: "p"},
		"SK": &types.AttributeValueMemberS{Value: "s"},
	}
	if _, err := Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{TableName: aws.String(TableName), Item: item}); err != nil {
		t.Fatal(err)
	}
	out, err := Raw(t).Query(ctx, &awsdynamodb.QueryInput{
		TableName:                 aws.String(TableName),
		KeyConditionExpression:    aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": &types.AttributeValueMemberS{Value: "p"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Count != 1 {
		t.Fatalf("expected 1 item, got %d", out.Count)
	}
	if c := Counts(); c["PutItem"] != 1 || c["Query"] != 1 {
		t.Fatalf("proxy counts %v, want 1 PutItem and 1 Query", c)
	}
	Reset(t)
	if c := Counts(); len(c) != 0 {
		t.Fatalf("Reset must clear the counts, got %v", c)
	}
}

// The test table is created from the vendored copy of Rudolph's Terraform definition, so every index production has
// exists in tests too.
func TestTableIsCreatedFromTheTerraformDefinition(t *testing.T) {
	Reset(t)
	tf, err := os.ReadFile(StoreTerraformFile())
	if err != nil {
		t.Fatal(err)
	}
	desc, err := Raw(t).DescribeTable(context.Background(), &awsdynamodb.DescribeTableInput{TableName: aws.String(TableName)})
	if err != nil {
		t.Fatal(err)
	}
	indexes := map[string]bool{}
	for _, gsi := range desc.Table.GlobalSecondaryIndexes {
		indexes[*gsi.IndexName] = true
		if !strings.Contains(string(tf), `"`+*gsi.IndexName+`"`) {
			t.Errorf("index %s is not in %s", *gsi.IndexName, StoreTerraformFile())
		}
	}
	for _, want := range []string{"DataType_MachineID", "SerialNum_DataType_MachineID"} {
		if !indexes[want] {
			t.Errorf("index %s missing from the test table (have %v)", want, indexes)
		}
	}
	if got := strings.Count(string(tf), "global_secondary_index {"); got != len(indexes) {
		t.Errorf("Terraform defines %d indexes, the test table has %d", got, len(indexes))
	}
}
