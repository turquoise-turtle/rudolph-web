// Package awsssm reads secrets from AWS SSM Parameter Store.
//
// It exists so the Lambda can keep the Entra client secret and the cookie key
// out of its environment: Terraform passes only parameter names, and the
// function reads the values once, at cold start, with its own IAM role.
package awsssm

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// client is the slice of the SSM API this package uses.
type client interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// A Fetcher reads decrypted parameters and caches the values it has read.
// Its zero value is not usable; call New.
type Fetcher struct {
	api    client
	mu     sync.Mutex
	values map[string]string
}

// New builds a Fetcher from the ambient AWS configuration: on Lambda that is
// the function's execution role and region.
func New(ctx context.Context) (*Fetcher, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("awsssm: loading AWS configuration: %w", err)
	}
	return newFetcher(ssm.NewFromConfig(cfg)), nil
}

func newFetcher(api client) *Fetcher {
	return &Fetcher{api: api, values: map[string]string{}}
}

// GetParameter returns the decrypted value of the named parameter. Values are
// cached for the life of the process, so only the first call of a cold start
// reaches SSM; failures are not cached. Errors never include parameter values.
func (f *Fetcher) GetParameter(ctx context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.values[name]; ok {
		return v, nil
	}
	out, err := f.api.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(name),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("awsssm: get parameter %s: %w", name, err)
	}
	if out == nil || out.Parameter == nil || out.Parameter.Value == nil {
		return "", errors.New("awsssm: parameter " + name + " has no value")
	}
	f.values[name] = *out.Parameter.Value
	return f.values[name], nil
}
