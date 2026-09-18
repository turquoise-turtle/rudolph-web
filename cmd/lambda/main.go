// Command lambda runs rudolph-web on AWS Lambda behind a Function URL or an
// API Gateway HTTP API (payload format 2.0).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/awsssm"
	"github.com/turquoise-turtle/rudolph-web/internal/config"
	"github.com/turquoise-turtle/rudolph-web/internal/lambdaadapter"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx := context.Background()

	// The deployment points RUDOLPH_WEB_CLIENT_SECRET_SSM_PARAMETER and
	// RUDOLPH_WEB_COOKIE_KEY_SSM_PARAMETER at SecureString parameters, so the
	// secrets are not in the function's environment. They are read here, once
	// per cold start, and cached by the fetcher.
	fetch, err := awsssm.New(ctx)
	if err != nil {
		log.Error("aws configuration", "err", err)
		os.Exit(1)
	}
	cfg, err := config.Load(ctx, os.Getenv, fetch)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	ac := cfg.AuthConfig()
	ac.Logger = log
	a, err := auth.New(ac)
	if err != nil {
		log.Error("auth setup", "err", err)
		os.Exit(1)
	}
	// TODO: replace with the DynamoDB reader once Rudolph's storage changes land.
	h, err := web.New(web.Options{Auth: a, Store: store.Unavailable{}, Logger: log})
	if err != nil {
		log.Error("web setup", "err", err)
		os.Exit(1)
	}
	lambda.Start(lambdaadapter.New(h))
}
