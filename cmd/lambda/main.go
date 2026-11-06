// Command lambda runs rudolph-web on AWS Lambda behind a Function URL or an
// API Gateway HTTP API (payload format 2.0).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/awsssm"
	"github.com/turquoise-turtle/rudolph-web/internal/config"
	"github.com/turquoise-turtle/rudolph-web/internal/lambdaadapter"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
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
	reader, err := newReader(ctx, cfg, log)
	if err != nil {
		log.Error("data layer setup", "err", err)
		os.Exit(1)
	}
	h, err := web.New(web.Options{Auth: a, Store: reader, Logger: log, CLIEnv: cfg.CLIEnv})
	if err != nil {
		log.Error("web setup", "err", err)
		os.Exit(1)
	}
	lambda.Start(lambdaadapter.New(h))
}

// newReader returns the DynamoDB reader when a table is configured, and
// otherwise a reader that answers 503, so a deployment without the setting
// still signs people in.
func newReader(ctx context.Context, cfg config.Config, log *slog.Logger) (store.Reader, error) {
	if cfg.TableName == "" {
		return store.Unavailable{}, nil
	}
	// Deriving the cursor key from the cookie key means every instance signs
	// cursors alike without a third secret to manage.
	key, err := store.DeriveCursorKey(cfg.CookieKey)
	if err != nil {
		return nil, err
	}
	codec, err := store.NewCursorCodec(key)
	if err != nil {
		return nil, err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS configuration: %w", err)
	}
	return dynamo.New(dynamo.Options{
		Client:      dynamodb.NewFromConfig(awsCfg),
		Table:       cfg.TableName,
		EventsTable: cfg.EventsTableName,
		Cursors:     codec,
		Logger:      log,
	})
}
