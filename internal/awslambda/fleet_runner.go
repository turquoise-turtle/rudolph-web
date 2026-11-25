// Package awslambda starts Rudolph's fleet job for the web view's "Run the
// fleet job now".
package awslambda

import (
	"context"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

// InvokeAPI is the one call the runner makes: the Invoke of the AWS SDK's
// Lambda client. The deployment grants lambda:InvokeFunction on the fleet
// function and nothing else.
type InvokeAPI interface {
	Invoke(ctx context.Context, in *lambda.InvokeInput, optFns ...func(*lambda.Options)) (*lambda.InvokeOutput, error)
}

// FleetRunner starts runs of one Lambda function. It implements
// web.FleetRunner.
type FleetRunner struct {
	client       InvokeAPI
	functionName string
}

// NewFleetRunner returns a runner of the function with that name
// (Rudolph's ${prefix}_rudolph_fleet).
func NewFleetRunner(client InvokeAPI, functionName string) *FleetRunner {
	return &FleetRunner{client: client, functionName: functionName}
}

// Start queues one run and returns without waiting for it: a run can take
// the function's whole 15 minutes, far longer than the web request that asked
// for it, so the invocation is asynchronous. Lambda answers 202 once the run
// is queued; anything else means it was not. No qualifier is sent, because
// the role may invoke the unqualified function only.
func (r *FleetRunner) Start(ctx context.Context) error {
	out, err := r.client.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:   aws.String(r.functionName),
		InvocationType: types.InvocationTypeEvent,
	})
	if err != nil {
		return fmt.Errorf("awslambda: invoking %s: %w", r.functionName, err)
	}
	if out.StatusCode != http.StatusAccepted {
		return fmt.Errorf("awslambda: invoking %s: status %d, want %d", r.functionName, out.StatusCode, http.StatusAccepted)
	}
	return nil
}
