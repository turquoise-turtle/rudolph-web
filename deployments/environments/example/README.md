# Setting up a new environment

An environment is one deployment of rudolph-web, described by six files in
`deployments/environments/<ENV>/`. This directory is the example they are copied from.

```sh
ENV=prod make init-env
```

creates `deployments/environments/prod/` with all of them. Everything you then edit is in
`_backend.tf` and `config.auto.tfvars.json`.

## The files

### `main.tf`

Instantiates the module from this checkout:

```hcl
module "rudolph_web" {
  source = "../../terraform_modules/rudolph_web"
  ...
}
```

Keep the local `source`. Pointing it at a remote repository deploys whatever is on that repository's
default branch instead of the code you built and tested.

It also passes both AWS providers. The `aws.us_east_1` alias exists because CloudFront only accepts
certificates from us-east-1; it is required even with CloudFront disabled.

### `_variables.tf`

Declares the variables `config.auto.tfvars.json` and `make deploy` supply. `make init-env` copies it and `main.tf` from this example once; when you upgrade rudolph-web, compare both with this example to pick up new settings.

`make plan`, `make deploy` and `make justdeploy` check this first (`go run ./cmd/envcheck deployments/environments/$ENV`) and refuse to run Terraform when the environment lacks a variable the example declares or an argument the example's `module "rudolph_web"` block sets, or when a `*.auto.tfvars.json` or `terraform.tfvars.json` file sets a value the environment does not declare. Without the check, Terraform only warns "Value for undeclared variable" and plans without the setting. To fix it, compare the files (`diff -u deployments/environments/example/main.tf deployments/environments/$ENV/main.tf`, and the same for `_variables.tf`); copy the example's `main.tf` and `_variables.tf` over yours if you have not customised them, otherwise add what is missing. Variables and module arguments the example does not have are allowed, and variables may be declared in any `.tf` file in the environment. The check reads `.tf` files, not `.tf.json` files (except that the module arguments in `override.tf.json` and `*_override.tf.json` count, as Terraform merges them in), and only JSON variable files, not `.tfvars` ones; it skips the files Terraform ignores and the `"//"` comment key. Because the check runs with Go, `make plan` now needs Go installed, as `make deploy` already did.

### `versions.tf`

Pins Terraform and the AWS provider, with the same constraint as the module
(`deployments/terraform_modules/rudolph_web/_providers.tf`).

### `_backend.tf`

Where the Terraform state lives, and the two providers. The S3 bucket must already exist, and the
`key` must be unique within it.

### `config.auto.tfvars.json`

The settings for this deployment. The file must have exactly this name. **No secret ever goes in
here**: the Entra client secret and the cookie key live in SSM Parameter Store, and this file is
committed and ends up in the state.

| Variable | Default | Description |
|---|---|---|
| `aws_account_id` | (required) | AWS account the deployment lives in. |
| `region` | (required) | AWS region. Must match the providers in `_backend.tf`. |
| `prefix` | (required) | Prefixes every resource name; unique within the account. |
| `tenant_id` | (required) | Entra ID tenant GUID. |
| `client_id` | (required) | Entra ID application (client) ID. |
| `base_url` | `""` | The origin users visit, no trailing slash. Defaults to `https://<domain_name>` when CloudFront is on. See the two-phase first deploy in [docs/deploy.md](../../../docs/deploy.md). |
| `post_logout_redirect_url` | `""` | Where Entra sends the browser after logout. Must be registered on the app registration. |
| `session_ttl` | `"1h"` | Session cookie lifetime, 5m to 12h. |
| `cli_env` | `""` | Rudolph environment name (e.g. `prod`) the pages' copy-and-paste CLI commands start with, as `ENV=<name> rudolph …`. Empty leaves `ENV` to the operator's shell. |
| `top_list_stale_after` | `"3h"` | How old a top blocked or allowed list may be before its page warns that Rudolph's ranker may have stopped, in whole hours from `1h` to `168h`. Raise it if the ranker runs only on a schedule such as weekday working hours; see [docs/deploy.md](../../../docs/deploy.md). |
| `display_time_zone` | `"UTC"` | IANA time zone name, e.g. `Asia/Tokyo`. Pages show each time in UTC with the time in this zone beside it; `UTC` shows UTC only. |
| `create_ssm_parameters` | `false` | Create the two SecureString parameters with a placeholder instead of creating them yourself. |
| `ssm_kms_key_arn` | `""` | KMS key for those parameters. Empty uses the AWS managed `alias/aws/ssm` key. |
| `dynamodb_table_name` | (required) | Rudolph's table, typically `<rudolph prefix>_rudolph_store`. |
| `dynamodb_kms_key_arn` | `""` | The CMK encrypting that table, if it has one. |
| `events_dynamodb_table_name` | `""` | Rudolph's execution events table, typically `<rudolph prefix>_rudolph_events`. Empty leaves the events pages unavailable. |
| `lambda_memory_size` | `256` | Function memory in MB. |
| `lambda_timeout_seconds` | `10` | Function timeout in seconds. |
| `log_retention_in_days` | `30` | Retention of the function's log group. |
| `enable_cloudfront` | `false` | Put CloudFront in front of the Function URL. Needed for a custom domain. |
| `domain_name` | `""` | The custom domain, for example `rudolph-web.example.com`. |
| `acm_certificate_arn` | `""` | An existing us-east-1 certificate for it. Empty requests one. |
| `route53_zone_name` | `""` | Hosted zone for the alias record and certificate validation. |
| `use_existing_route53_zone` | `true` | Look the zone up instead of creating it. |
| `cloudfront_price_class` | `"PriceClass_100"` | Cheapest class covering North America and Europe. |
| `monthly_budget_usd` | `0` | Monthly AWS Budgets cost budget in USD for resources tagged `Name = rudolph-web`. `0` creates no budget. It alerts and never caps spending; see [docs/deploy.md](../../../docs/deploy.md#the-monthly-cost-budget). |
| `budget_alert_emails` | `[]` | Where the budget emails at 80% and 100% of actual spend and 100% of forecast spend. Required when `monthly_budget_usd` is above `0`. |
| `tags` | `{}` | Extra tags merged into every taggable resource. |

### `_outputs.tf`

Prints the URL, the redirect URI to register with Entra, and the two parameter names, after each
apply.
