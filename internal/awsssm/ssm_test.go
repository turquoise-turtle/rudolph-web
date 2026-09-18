package awsssm

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

type fakeClient struct {
	values map[string]string
	err    error
	calls  []ssm.GetParameterInput
}

func (c *fakeClient) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	c.calls = append(c.calls, *in)
	if c.err != nil {
		return nil, c.err
	}
	v, ok := c.values[aws.ToString(in.Name)]
	if !ok {
		return &ssm.GetParameterOutput{}, nil
	}
	return &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: aws.String(v)}}, nil
}

func TestGetParameter_DecryptsAndCaches(t *testing.T) {
	c := &fakeClient{values: map[string]string{"/rudolph-web/cookie-key": "value"}}
	f := newFetcher(c)

	for range 3 {
		got, err := f.GetParameter(context.Background(), "/rudolph-web/cookie-key")
		if err != nil {
			t.Fatalf("GetParameter: %v", err)
		}
		if got != "value" {
			t.Fatalf("GetParameter = %q, want %q", got, "value")
		}
	}
	if len(c.calls) != 1 {
		t.Errorf("called SSM %d times, want 1: cold-start values must be cached", len(c.calls))
	}
	if !aws.ToBool(c.calls[0].WithDecryption) {
		t.Error("GetParameter did not ask for decryption; SecureString values come back encrypted")
	}
	if got := aws.ToString(c.calls[0].Name); got != "/rudolph-web/cookie-key" {
		t.Errorf("requested %q", got)
	}
}

func TestGetParameter_DoesNotCacheFailures(t *testing.T) {
	c := &fakeClient{err: errors.New("Throttling")}
	f := newFetcher(c)
	if _, err := f.GetParameter(context.Background(), "/p"); err == nil {
		t.Fatal("GetParameter succeeded although SSM failed")
	}
	c.err = nil
	c.values = map[string]string{"/p": "ok"}
	got, err := f.GetParameter(context.Background(), "/p")
	if err != nil {
		t.Fatalf("GetParameter after a retry: %v", err)
	}
	if got != "ok" {
		t.Errorf("GetParameter = %q, want %q", got, "ok")
	}
}

func TestGetParameter_MissingValue(t *testing.T) {
	c := &fakeClient{values: map[string]string{}}
	f := newFetcher(c)
	if _, err := f.GetParameter(context.Background(), "/missing"); err == nil {
		t.Fatal("GetParameter succeeded for a parameter with no value")
	}
}
