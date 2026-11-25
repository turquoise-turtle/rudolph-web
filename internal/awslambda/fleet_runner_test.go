package awslambda_test

// The production web.FleetRunner (owner request, 2026-11-25, "Run the fleet job now"): it starts one run of Rudolph's
// fleet Lambda, named by RUDOLPH_WEB_FLEET_FUNCTION, with the AWS SDK's Lambda client, and does not wait for it.
//
//	awslambda.InvokeAPI   interface{ Invoke(ctx, *lambda.InvokeInput, ...func(*lambda.Options)) (*lambda.InvokeOutput, error) }
//	awslambda.NewFleetRunner(client InvokeAPI, functionName string) *awslambda.FleetRunner
//	(*FleetRunner).Start(ctx) error
//
// Start makes exactly one Invoke of that function with InvocationType Event (asynchronous: Lambda queues the run and
// answers 202 at once; a run takes up to 15 minutes, far longer than the web request) and no qualifier. It returns
// nil for 202, and an error for an Invoke error or any other status. Lambda's own retries of a failed asynchronous
// run are Rudolph's to set (its fleet function retries none).

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/turquoise-turtle/rudolph-web/internal/awslambda"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

type invokeClient struct {
	calls  []*lambda.InvokeInput
	status int32
	err    error
}

func (c *invokeClient) Invoke(_ context.Context, in *lambda.InvokeInput, _ ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	c.calls = append(c.calls, in)
	if c.err != nil {
		return nil, c.err
	}
	return &lambda.InvokeOutput{StatusCode: c.status}, nil
}

var _ awslambda.InvokeAPI = (*invokeClient)(nil)

func TestFleetRunner_InvokesTheFunctionAsynchronously(t *testing.T) {
	c := &invokeClient{status: 202}
	var r web.FleetRunner = awslambda.NewFleetRunner(c, "example_rudolph_fleet")
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(c.calls) != 1 {
		t.Fatalf("%d Invoke calls, want 1", len(c.calls))
	}
	in := c.calls[0]
	if aws.ToString(in.FunctionName) != "example_rudolph_fleet" {
		t.Errorf("FunctionName %q, want example_rudolph_fleet", aws.ToString(in.FunctionName))
	}
	if in.InvocationType != types.InvocationTypeEvent {
		t.Errorf("InvocationType %q, want Event", in.InvocationType)
	}
	if in.Qualifier != nil {
		t.Errorf("Qualifier %q; the role may invoke the unqualified function only", aws.ToString(in.Qualifier))
	}
}

func TestFleetRunner_Failures(t *testing.T) {
	denied := errors.New("operation error Lambda: Invoke, AccessDeniedException")
	c := &invokeClient{err: denied}
	if err := awslambda.NewFleetRunner(c, "example_rudolph_fleet").Start(context.Background()); !errors.Is(err, denied) {
		t.Errorf("an Invoke error: Start returned %v, want it wrapped", err)
	}
	for _, status := range []int32{200, 204, 500} {
		c := &invokeClient{status: status}
		if err := awslambda.NewFleetRunner(c, "example_rudolph_fleet").Start(context.Background()); err == nil {
			t.Errorf("status %d: Start returned nil; only 202 means the run was queued", status)
		}
	}
}
