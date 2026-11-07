module github.com/turquoise-turtle/rudolph-web

go 1.27

toolchain go1.27.3

require (
	github.com/airbnb/rudolph v0.0.0-20261107050302-53fb7be5821b
	github.com/aws/aws-lambda-go v1.55.1
	github.com/aws/aws-sdk-go-v2 v1.47.8
	github.com/aws/aws-sdk-go-v2/config v1.33.16
	github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue v1.21.20
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.73.2
	github.com/aws/aws-sdk-go-v2/service/ssm v1.82.0
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/hashicorp/hcl/v2 v2.25.0
	golang.org/x/oauth2 v0.37.0
)

require (
	github.com/agext/levenshtein v1.2.3 // indirect
	github.com/apparentlymart/go-textseg/v15 v15.0.0 // indirect
	github.com/apparentlymart/go-textseg/v17 v17.0.1 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.20.16 // indirect
	github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression v1.10.8 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.8 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.11 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.11 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.11 // indirect
	github.com/aws/aws-sdk-go-v2/service/dynamodbstreams v1.46.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.24 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.13.11 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.11 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.12.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.39.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.44.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.9 // indirect
	github.com/aws/smithy-go v1.28.8 // indirect
	github.com/go-jose/go-jose/v4 v4.1.9 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/zclconf/go-cty v1.20.0 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
)

// Rudolph's module path is github.com/airbnb/rudolph; the web view uses the fork's commit with the audit trail and
// web contract (docs/deploy.md, "Updating the Rudolph pin").
replace github.com/airbnb/rudolph => github.com/turquoise-turtle/rudolph v0.0.0-20261107050302-53fb7be5821b
