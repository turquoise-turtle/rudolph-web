// Package awslambda starts Rudolph's scheduled Lambdas for the web view's
// "Run the fleet job now" and "Run the ranker now".
package awslambda

import (
	"context"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

// InvokeAPI is the one call a runner makes: the Invoke of the AWS SDK's
// Lambda client. The deployment grants lambda:InvokeFunction on the fleet
// and ranker functions it names and nothing else.
type InvokeAPI interface {
	Invoke(ctx context.Context, in *lambda.InvokeInput, optFns ...func(*lambda.Options)) (*lambda.InvokeOutput, error)
}

// Runner starts runs of one Lambda function. It implements web.FleetRunner
// and web.RankerRunner: both jobs take no input and are started the same
// way, so one runner serves either.
type Runner struct {
	client       InvokeAPI
	functionName string
}

// FleetRunner is the Runner of Rudolph's fleet function.
type FleetRunner = Runner

// NewRunner returns a runner of the function with that name.
func NewRunner(client InvokeAPI, functionName string) *Runner {
	return &Runner{client: client, functionName: functionName}
}

// NewFleetRunner returns a runner of Rudolph's fleet function
// (${prefix}_rudolph_fleet).
func NewFleetRunner(client InvokeAPI, functionName string) *FleetRunner {
	return NewRunner(client, functionName)
}

// NewRankerRunner returns a runner of Rudolph's ranker function
// (${prefix}_rudolph_ranker).
func NewRankerRunner(client InvokeAPI, functionName string) *Runner {
	return NewRunner(client, functionName)
}

// Start queues one run and returns without waiting for it: a run can take
// the function's whole timeout (15 minutes for the fleet job, 5 for the
// ranker), far longer than the web request that asked for it, so the
// invocation is asynchronous. Lambda answers 202 once the run is queued;
// anything else means it was not. No qualifier is sent, because the role may
// invoke the unqualified function only.
func (r *Runner) Start(ctx context.Context) error {
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
