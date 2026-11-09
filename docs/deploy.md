# Deploying rudolph-web

rudolph-web runs as a single arm64 Lambda behind a Lambda Function URL, optionally fronted by
CloudFront for a custom domain. Idle, it costs nothing.

```
 Browser ──HTTPS──▶ [CloudFront + ACM, optional] ──▶ Lambda Function URL (auth: NONE)
                                                          │
                                                      cmd/lambda
                                             ┌────────────┴─────────────┐
                                             ▼                          ▼
                                    SSM Parameter Store        DynamoDB (Rudolph's tables)
                                    (client secret, cookie key)   GetItem / Query; writes only
                                                                  with enable_editing
```

Everything is in `deployments/`:

```
deployments/terraform_modules/rudolph_web/   the module
deployments/environments/example/            an environment that sources it
deployments/terraform_test.go                static checks, run by `go test ./...`
```

---

## Getting ready

You only do this once.

### Tools

* **Go** 1.27 or newer (`go.mod` names the toolchain, which the `go` command downloads when yours is older), for `make plan` as well as `make deploy`: both check the environment with Go before running Terraform.
* **Terraform** 1.10 or newer (tested with 1.17), with the `hashicorp/aws` provider `~> 6.72`.
* **AWS CLI**, authenticated as a role that may create Lambda functions, IAM roles, SSM parameters,
  CloudWatch log groups and, if you enable it, CloudFront distributions, ACM certificates and
  Route 53 records. With a cost budget (see [The monthly cost budget](#the-monthly-cost-budget)) the role also needs `budgets:ViewBudget`, `budgets:ModifyBudget`, `budgets:TagResource`, `budgets:UntagResource` and `budgets:ListTagsForResource`; the tag permissions are needed because the provider's default tags are applied to the budget.
* An **S3 bucket** for the Terraform state. It must already exist.

### A Rudolph deployment

rudolph-web reads Rudolph's DynamoDB table. Note its name (`<rudolph prefix>_rudolph_store` by
default) and, if the table is encrypted with a customer managed KMS key, that key's ARN.

The function reads the table named by its `RUDOLPH_WEB_DYNAMODB_TABLE` environment variable. The machine list, machine detail, machine rules and global rules come from it; the machine list needs Rudolph's `DataType_SerialKey` and `DataType_HostnameKey` indexes and the policy filter on global rules needs `GlobalRulePolicy_SK`; each fails against a Rudolph table without its index. Execution events come from Rudolph's events table, named by `RUDOLPH_WEB_EVENTS_TABLE` (it needs the `Fleet_TS`, `Decision_TS` and `TeamID_TS` indexes); the top blocked and top allowed lists come from the same table, from the snapshots Rudolph's ranker writes (a Rudolph version with the ranker; until its first run each list says it has not been computed yet). Without the events table these pages return 503. File access events return 503 for now. Without `RUDOLPH_WEB_DYNAMODB_TABLE` every data page returns 503, while authentication, authorization and everything else in this guide still work.

The Terraform module sets `RUDOLPH_WEB_DYNAMODB_TABLE` from `dynamodb_table_name` and `RUDOLPH_WEB_EVENTS_TABLE` from `events_dynamodb_table_name` (Rudolph's `events_dynamodb_table_name` output, typically `<rudolph prefix>_rudolph_events`; leave it empty if your Rudolph has no events table). The reader only calls `dynamodb:Query` and `dynamodb:GetItem` (the latter for the group registry, the global config's client mode on the machine page, and the top list pointers), which the `ReadRudolphStore` and `ReadRudolphEvents` statements below allow. The events table is encrypted with the same key as the store table, so `dynamodb_kms_key_arn` covers both.

The pages offer Rudolph CLI commands to copy (block or allow a binary from an event or a top list, a Mac's configuration and temporary monitor mode, removing a rule). Set `cli_env` (the module sets `RUDOLPH_WEB_CLI_ENV` from it) to the Rudolph environment name, e.g. `prod`, so each command starts with `ENV=prod rudolph`; left empty, the commands start with `rudolph` and the operator's shell supplies `ENV`. Unless you turn on [editing](#editing-optional), the web view itself never writes anything.

Each top list page shows when the list was generated and warns when it is older than `top_list_stale_after` (the module sets `RUDOLPH_WEB_TOP_LIST_STALE_AFTER` from it; whole hours from `1h` to `168h`, default `3h`). The default suits a ranker that runs every hour around the clock: the warning means about three runs were missed. If Rudolph's ranker runs on a schedule, fit the threshold to it. For example, with the ranker running hourly on weekdays from 7am to 5pm, a list is normally up to 14 hours old overnight and about 62 hours old over a weekend. `15h` warns about missed weekday runs but also warns every weekend; `63h` never warns on a healthy schedule, at the cost of noticing a stopped ranker only after days.

Pages show each time in UTC and, beside it, in the viewer's own time zone, converted in the browser: a viewer in Asia/Tokyo sees `2026-09-17 11:55:00 UTC (2026-09-17 20:55 JST)`, one in America/New_York sees `2026-09-17 11:55:00 UTC (2026-09-17 07:55 EDT)`, and one whose browser is in UTC sees UTC only. There is nothing to configure. The server renders UTC only; one small inline script, the only script the pages run, rewrites the text of each time and does nothing else, and the CSP allows it by its SHA-256 hash alone (`script-src 'sha256-…'`). If the script does not run, the pages still work and show UTC only. The short zone name (`JST`, or `GMT+9` in some locales) comes from the browser's language settings. Dates that are UTC days, such as the machine list's last seen and the top lists' windows, stay in UTC.

#### Upgrading from a release with `display_time_zone`

`display_time_zone` is removed. Before planning, remove `display_time_zone = var.display_time_zone` from your environment's `main.tf`, or Terraform fails with "Unsupported argument"; also delete the variable from `_variables.tf` and `config.auto.tfvars.json`. The old function keeps showing the configured zone until you apply; the apply deploys the new build and removes `RUDOLPH_WEB_DISPLAY_TIME_ZONE` from the function.

### A domain (optional)

Only needed for `enable_cloudfront = true`. Without it the deployment is reachable at its Function
URL, `https://<id>.lambda-url.<region>.on.aws`, which is a perfectly good address for an internal
admin tool.

---

## The Entra ID app registration

In **Microsoft Entra admin center → App registrations → New registration**:

1. **Single tenant.** Multi-tenant is rejected by the app: it pins `tid` to your tenant.
2. **Redirect URI**, platform *Web*:
   * `https://<host>/auth/callback` — the deployed host, exactly as users reach it.
   * `http://localhost:8080/auth/callback` — for local development.

   If you are deploying with a Function URL and do not know the host yet, come back after the first
   apply; see [First deploy](#first-deploy) below.
3. **Front-channel logout / post-logout redirect URI.** If you set `post_logout_redirect_url`,
   register that exact URL here as well. Entra silently ignores an unregistered
   `post_logout_redirect_uri` and leaves the user on its own signed-out page.
4. **Certificates & secrets → New client secret.** Copy the value now; it is shown once. It goes
   into Parameter Store, never into Terraform.
5. **App roles → Create app role**, twice, both *Users/Groups*:
   * `Rudolph.Viewer` — the read pages.
   * `Rudolph.Admin` — the admin-only routes, and with [editing](#editing-optional) the forms that change rules and temporary client modes; includes Viewer.

   The value must match exactly; the app reads them from the `roles` claim.
6. **Enterprise applications → your app → Properties → Assignment required: Yes.** Then
   **Users and groups → Add user/group** and assign the roles. Without assignment required, anyone
   in the tenant can sign in; they will see 403 on every page, but they can sign in.

Role changes take effect at the next sign-in, which is at most one `session_ttl` away.

---

## Create the environment

```sh
export ENV=prod
make init-env
```

That copies the example into `deployments/environments/prod/`. Edit two files:

* **`_backend.tf`** — the state bucket, key, region and KMS key, and the region of the two AWS
  providers. Keep the `aws.us_east_1` alias even if you deploy elsewhere and disable CloudFront:
  CloudFront certificates only exist in us-east-1, and the module declares that provider.
* **`config.auto.tfvars.json`** — the settings, documented in
  [the example environment's README](../deployments/environments/example/README.md).

At a minimum set `aws_account_id`, `region`, `prefix`, `tenant_id`, `client_id` and
`dynamodb_table_name`, and `events_dynamodb_table_name` to show execution events and the top lists. Set `cli_env` too if the copy-and-paste commands should name the Rudolph environment.

Environment directories hold the account ID, the tenant and client IDs and the domain, so Git never sees them: the repository's `.gitignore` ignores everything under `deployments/environments/` except `example/`, and `make init-env` also writes a `.gitignore` of `*` into the new directory. Keep your own copy of an environment's `_backend.tf` and `config.auto.tfvars.json` somewhere safe; they are not in the repository.

### Upgrading an existing environment

`make init-env` copies the example's `main.tf` and `_variables.tf` once, so an environment created before a release that added a module variable (for example `display_time_zone`) lacks its declaration and module argument. Terraform then only warns "Value for undeclared variable" about the value in `config.auto.tfvars.json`, and plans and applies without the setting. `make plan`, `make deploy` and `make justdeploy` therefore run `go run ./cmd/envcheck deployments/environments/$ENV` first and refuse to go on when the environment:

* does not declare, in any `.tf` file, a variable the example's `_variables.tf` declares;
* has no `module "rudolph_web"` block, or its block does not set an argument the example's `main.tf` sets (`source` and `version`, which choose where the module comes from, are not compared);
* sets a value in `config.auto.tfvars.json`, another `*.auto.tfvars.json` file or `terraform.tfvars.json` that it does not declare.

Variables and module arguments of your own that the example does not have are allowed, and module arguments set in override files (`override.tf`, `*_override.tf`, `override.tf.json`, `*_override.tf.json`) count, as Terraform merges them in. Apart from those override files, the check reads `.tf` files only, not `.tf.json`, and only JSON variable files, not `.tfvars` ones. Like Terraform, it skips hidden files (names starting with `.`), backup files ending in `~` and lock files named `#...#`, and the `"//"` comment key in JSON variable files. `make tf-init` and `make destroy` are not checked. The check runs with Go, so `make plan` now needs Go installed, as `make deploy` already did. To fix a refused environment, compare it with the example:

```sh
diff -u deployments/environments/example/main.tf deployments/environments/$ENV/main.tf
diff -u deployments/environments/example/_variables.tf deployments/environments/$ENV/_variables.tf
```

If the environment has no customisations, copy the example's `main.tf` and `_variables.tf` over it; otherwise add the missing declarations and module arguments by hand. Then run `make plan` again and check the plan picks up the settings.

### The monthly cost budget

Set `monthly_budget_usd` (for example `10`) and `budget_alert_emails` to give rudolph-web its own AWS Budgets cost budget. It emails every address when actual spend passes 80% and 100% of the amount, and when forecast spend for the month passes 100%. `0`, the default, creates no budget, and a budget above `0` with no addresses fails the plan.

The deploying role needs `budgets:ViewBudget`, `budgets:ModifyBudget`, `budgets:TagResource`, `budgets:UntagResource` and `budgets:ListTagsForResource`. The tag permissions are needed because the provider's default tags are applied to the budget itself.

A budget **only alerts**: it never stops, throttles or caps anything, and rudolph-web keeps running and costing money past it.

The account may hold other resources, so the budget counts only costs tagged `Name = rudolph-web`, which every rudolph-web resource carries (the module's own tags and the provider default tag in `_backend.tf`; do not override `Name` in `tags`). AWS ignores a tag in budgets until it is a cost allocation tag: once, in the management (payer) account, open Billing and Cost Management, Cost allocation tags, and activate the user-defined tag `Name`. It only appears there after a tagged resource has existed for a while (up to a day), it only counts costs from activation onwards, and until it is active the budget reports nothing.

---

## Create the secrets

Neither secret is a Lambda environment variable, and neither passes through Terraform. The function
is given two parameter *names* and reads the values at cold start with its own role, caching them
for the life of the execution environment.

The parameter names default to `/<prefix>/rudolph-web/client-secret` and
`/<prefix>/rudolph-web/cookie-key`.

### The cookie key

32 random bytes, base64. It encrypts the session cookie (AES-256-GCM) and, through HKDF, derives the
key that signs pagination cursors:

```sh
openssl rand -base64 32
```

### Writing both parameters

```sh
PREFIX=prod
REGION=us-east-1

aws ssm put-parameter --region "$REGION" \
  --name "/$PREFIX/rudolph-web/cookie-key" \
  --type SecureString \
  --value "$(openssl rand -base64 32)"

aws ssm put-parameter --region "$REGION" \
  --name "/$PREFIX/rudolph-web/client-secret" \
  --type SecureString \
  --value "$(cat)"   # paste the Entra client secret, then press ctrl-D
```

Reading the secret from stdin keeps it out of your shell history and out of `ps`.

They are encrypted with the AWS managed `alias/aws/ssm` key. To use your own key, pass
`--key-id <arn>` here and set `ssm_kms_key_arn` in `config.auto.tfvars.json` so the role is granted
`kms:Decrypt` on it.

**Alternative:** set `create_ssm_parameters = true` and Terraform creates both parameters with a
placeholder value, then ignores every later change to it. Use it to bootstrap a fresh account in one
apply; run the two `put-parameter --overwrite` commands afterwards. The app refuses to start while
the placeholder is in place, since it is not valid base64 of 32 bytes.

---

## First deploy

```sh
export ENV=prod
make deploy
```

`make deploy` runs the tests, builds `bootstrap` for linux/arm64, zips it, runs `terraform init` and
then `terraform apply`.

### Without CloudFront: two applies

The app has to know the origin users visit — Entra redirects back to it, and it is the origin the
app trusts for cross-origin protection — but the Function URL only exists after the first apply. So:

1. `make deploy` with `base_url` empty. The apply prints:

   ```
   function_url = "https://abc123def456.lambda-url.us-east-1.on.aws/"
   ```

2. Put that into `config.auto.tfvars.json` **without the trailing slash**:

   ```json
   "base_url": "https://abc123def456.lambda-url.us-east-1.on.aws"
   ```

3. Register `https://abc123def456.lambda-url.us-east-1.on.aws/auth/callback` as a redirect URI on
   the app registration.

4. `make deploy` again. The `redirect_url` output is the exact value you registered.

Until step 4 the function starts with no redirect URL and logs `config: ... is required`; every
request returns a 500. That is the only window where that happens.

### With CloudFront and a custom domain: one apply

```json
"enable_cloudfront": true,
"domain_name": "rudolph-web.example.com",
"route53_zone_name": "example.com",
"use_existing_route53_zone": true
```

`base_url` then defaults to `https://rudolph-web.example.com`, so register
`https://rudolph-web.example.com/auth/callback` with Entra before deploying. The apply requests the
certificate, writes the DNS validation record, waits for validation and creates the distribution;
allow 5–15 minutes. If DNS is managed outside Route 53, leave `route53_zone_name` empty, pass an
already validated us-east-1 certificate in `acm_certificate_arn`, and point your own CNAME at the
`cloudfront_domain_name` output.

### Check it

Open the `url` output. You should be redirected to Entra, sign in, and land on the machines page —
or on a 403 if your account has neither role.

---

## What gets created

| Resource | Notes |
|---|---|
| `aws_lambda_function` | `<prefix>_rudolph_web`, arm64, `provided.al2023`, 256 MB, 10 s |
| `aws_lambda_function_url` | `authorization_type = NONE`; the app authenticates its own users |
| `aws_cloudwatch_log_group` | `/aws/lambda/<prefix>_rudolph_web`, retention `log_retention_in_days` |
| `aws_iam_role` + inline policy | see below |
| `aws_ssm_parameter` ×2 | only with `create_ssm_parameters = true` |
| `aws_cloudfront_distribution` | only with `enable_cloudfront = true` |
| `aws_cloudfront_origin_request_policy` | what CloudFront forwards to the function |
| `aws_acm_certificate` (+ validation) | us-east-1, only when a custom domain needs one |
| `aws_route53_record` | validation record and the A/AAAA aliases |
| `aws_budgets_budget` | `<prefix>_rudolph_web_monthly`, only with `monthly_budget_usd` above 0 |

### The role

Four to seven statements, no AWS managed policies and no wildcard resources:

| Statement | Actions | Resources |
|---|---|---|
| `WriteOwnLogs` | `logs:CreateLogStream`, `logs:PutLogEvents` | its own log group only |
| `ReadSecrets` | `ssm:GetParameter` | the two parameter ARNs |
| `DecryptSecrets` | `kms:Decrypt` | the SSM key, conditioned on `kms:ViaService = ssm.<region>.amazonaws.com` |
| `ReadRudolphStore` | `dynamodb:GetItem`, `dynamodb:Query` | the table and `<table>/index/*` |
| `ReadRudolphEvents` | `dynamodb:GetItem`, `dynamodb:Query` | the events table and `<events table>/index/*`; only with `events_dynamodb_table_name` set |

With `dynamodb_kms_key_arn` set there is one more, `kms:Decrypt` conditioned on the DynamoDB service. With `enable_editing` there are two more, `WriteRudolphRules` and `AppendRudolphAudit`, described under [Editing](#editing-optional); without it the role has no write action of any kind. There is deliberately no `dynamodb:Scan`: every list in the app is one bounded query.

Terraform creates the log group itself, so Lambda cannot create one without retention, and the
function does not need `logs:CreateLogGroup`.

### CloudFront, the Function URL and cross-origin protection

Worth knowing before you change any of it:

* **Caching is off for everything except `/static/*`.** Pages are rendered for the signed-in user
  and carry `Cache-Control: no-store`; only the embedded stylesheet is cacheable.
* **Cookies and query strings are forwarded in full.** The session lives in a cookie, and the OIDC
  callback and the pagination cursors are query parameters.
* **Only `Content-Type`, `Origin` and `Sec-Fetch-Site` are forwarded.** Go's
  `http.CrossOriginProtection` uses `Sec-Fetch-Site` and `Origin` to decide whether an unsafe request
  is cross-origin, and the editing forms need `Content-Type`. Nothing else the browser sends is read by
  the app. CloudFront allows at most 10 headers in an origin request policy and refuses
  `Authorization` in one at all (the app authenticates with its session cookie, not that header).
* **The viewer's `Host` header is *not* forwarded.** A Lambda Function URL only answers requests
  addressed to its own domain; this is why AWS publishes a managed policy called
  *AllViewerExceptHostHeader* for exactly this kind of origin. The function therefore sees
  `<id>.lambda-url.<region>.on.aws` as its host, while the browser sends
  `Origin: https://rudolph-web.example.com` — which, compared against the host, would look
  cross-origin and be rejected with 403.

  That is why the app registers the origin of `RUDOLPH_WEB_REDIRECT_URL` as a trusted origin. It
  follows that **`base_url` must be the host users actually visit**. If you point a second hostname
  at the same distribution, sign-in through it will fail the cross-origin check on `POST
  /auth/logout` and on any future write route.

  The Function URL stays publicly reachable when CloudFront is in front of it. Restricting that
  needs OAC with `authorization_type = AWS_IAM`, which changes how the app is invoked, and is not
  part of this module.

---

## Editing (optional)

By default the view is read-only. With `enable_editing = true` (in `config.auto.tfvars.json`), people with the `Rudolph.Admin` app role can, from the pages:

* add a global rule (type, identifier, policy, custom message, description) on **Rules**, and remove any global rule with its **Remove rule** button;
* add a rule for one Mac (with an expiry: a duration such as `2h` or `7d`, default `24h`, or `never`) and remove its rules, on the Mac's page;
* set a temporary client mode for a Mac (monitor, lockdown or standalone, for `1m` to `30d`) and end it now, on the Mac's page;
* add and remove the rules of a device group, or of the Macs in no group, on the group's page.

Macs get each change at their next sync. Rules and temporary modes for a Mac go under its machine ID exactly as Rudolph stores it: like the CLI, the web view looks the ID up as given, in upper case and in lower case (sensor data first, then a machine configuration, then machine rules). A machine Rudolph has nothing for answers 404 rather than getting a rule no Mac would ever read. The shortest temporary mode, `1m`, gets a few seconds' allowance for the time the request takes before Rudolph checks its one-minute minimum. Removing asks nothing more than the clearly labelled button: the forms are plain HTML, since the only script the CSP allows is the one that shows times in the viewer's zone. Viewers never see the forms and get 403 from the write routes; every write is a same-origin POST (`http.CrossOriginProtection`). Without editing the forms are not shown and the write routes do not exist (404).

### What it changes

Every write goes through Rudolph's own Go packages (`pkg/model/globalrules`, `machinerules`, `groups`, `machineconfiguration`, `eventcollection` and `audit`, with Rudolph's `pkg/dynamodb` client), so the web view refuses exactly what the CLI refuses (identifier shapes, policies, machine UUIDs, group names, the temporary mode limits) and writes the same rows, feed entries included. Invalid input answers 400 with the reason; removing a rule that does not exist answers 404.

`enable_editing` sets `RUDOLPH_WEB_EDITING=true` on the function and adds two statements to its role:

| Statement | Actions | Resources | Condition |
|---|---|---|---|
| `WriteRudolphRules` | `dynamodb:PutItem`, `dynamodb:UpdateItem`, `dynamodb:DeleteItem` | the store table only (no index, not the events table) | `ForAllValues:StringLike` on `dynamodb:LeadingKeys`: `GlobalRules`, `RulesFeed`, `MachineRules#*`, `Machine#*`, `GroupRules#*`, `GroupRegistry`, `EventCollection` |
| `AppendRudolphAudit` | `dynamodb:PutItem` only | the store table only | `ForAllValues:StringLike` on `dynamodb:LeadingKeys`: `Audit#*` |

Global rule and group rule changes are `TransactWriteItems` calls, which have no IAM action of their own: DynamoDB checks `PutItem`, `UpdateItem` or `DeleteItem` for each item in them. The partitions are the ones these writes touch: global rules and their rules feed rows, machine rules, the machine config row (`Machine#<id>`, `SK=Config`) holding a temporary mode, group rules and the group registry's rules version, the index of rules whose allowed executions are collected (a replaced or removed rule is taken out of it, as the CLI does), and the audit trail's day partitions. Audit entries are in a statement of their own with `PutItem` only, so the role cannot update or delete one (see the note on overwriting below).

IAM cannot restrict a write by sort key: `dynamodb:LeadingKeys` matches the partition key only. So `Machine#*`, which the temporary mode needs for a Mac's config row (`SK=Config`), also lets the role write that Mac's sensor data (`SK=Current`), sync state (`SK=SyncState`) and group rows. The code writes only the config row; the grant is broader than the code. Nothing else of the table can be written. With `dynamodb_kms_key_arn` set, the existing `kms:Decrypt` grant is also what writes need.

- **Audit entries.** `AppendRudolphAudit` grants `PutItem` only, so the role cannot delete or update an audit entry. A `PutItem` could still replace an existing entry with the same key: what prevents that is Rudolph's conditional write (`attribute_not_exists(PK)` in `pkg/model/audit`), which the code always uses, not the role. IAM cannot express "create only" for `PutItem`.
- **Groups.** `GroupRegistry` and the Macs' `Groups` rows (under `Machine#*`) let the role create and delete groups and change which groups a Mac belongs to, which changes the rules that Mac receives. This is the broadest effect the role can have; the web view uses it only for group rules and the reserved ungrouped group.
- **Page links.** Cursors are signed with a key derived from the cookie key; they do not expire and are not tied to a session. Rolling the cookie key invalidates them all, as it signs everyone out.

To turn it on: assign `Rudolph.Admin` to the people who may change rules (see [the app registration](#the-entra-id-app-registration)), set `enable_editing` to `true`, and `make deploy`.

Role changes take effect at the next sign-in. A session keeps the roles it was signed in with until it expires, `session_ttl` later (default one hour), and the app does not ask Entra again before then. So removing someone's `Rudolph.Admin` assignment stops their edits only when their session ends: wait up to `session_ttl`, or, to cut every session off now, [roll the cookie key](#the-cookie-key-1), which signs everyone out.

To turn editing off again, set `enable_editing` back to `false` and deploy: the two statements and the forms go away together.

### The audit trail

Every change is recorded in Rudolph's audit trail (Rudolph's `pkg/model/audit`, in the store table under `Audit#<UTC day>`), exactly as the CLI records its own: the same actions (`rule.add`, `rule.remove`, `config.temporary-mode`, `config.end-temporary-mode`), targets (`global`, `machine:<machine ID>`, `group:<name>`, `ungrouped`) and summaries, with the actor `web:<username> (<Entra object ID>)` instead of the CLI's `cli:<user>@<host>`. Entries are never expired. A write that fails records nothing; a write that succeeded but could not be audited is kept, as with the CLI, and the page says so (500) and the function logs it.

`/admin/audit` (admins only, linked from `/admin`) lists the entries of the last 30 days, newest first, from the CLI and the web view alike, one page at a time like the other lists (50 entries a page, with a next-page link). The next-page link carries Rudolph's audit cursor inside the web view's signed cursor, so it cannot be edited or reused for another list. `rudolph audit` reads older entries.

### The Rudolph module pin

The writer imports Rudolph's packages under their module path, `github.com/airbnb/rudolph`, from the owner's fork, which has the audit trail and the web contract. `go get` cannot resolve a fork under another module path, so `go.mod` pins it with a `replace` to a pseudo-version of a pushed commit:

```
require github.com/airbnb/rudolph v0.0.0-20261107050302-53fb7be5821b
replace github.com/airbnb/rudolph => github.com/turquoise-turtle/rudolph v0.0.0-20261107050302-53fb7be5821b
```

To move to a newer Rudolph commit, push it, then:

```sh
COMMIT=<full or 12-character commit hash>
V=$(go list -m -f '{{.Version}}' github.com/turquoise-turtle/rudolph@$COMMIT)
go mod edit -require=github.com/airbnb/rudolph@$V -replace=github.com/airbnb/rudolph=github.com/turquoise-turtle/rudolph@$V
go mod tidy
go test ./...    # and the integration tests, against DynamoDB Local
```

Check `git diff go.mod` afterwards: Rudolph's own requirements must not force an upgrade you did not intend. When Rudolph changes a writer the web view uses, or adds a partition one of them writes, update the `WriteRudolphRules` leading keys to match.

To develop against a local Rudolph checkout without pushing, do not commit a `replace` to a local path. Use a workspace instead, which `go.mod` never sees:

```sh
go work init . ../rudolph      # go.work is ignored by .gitignore
go test ./...
rm go.work go.work.sum         # back to the pinned commit
```

---

## Rolling the secrets

Both values are read once per cold start and cached, so a rotation takes effect as execution
environments recycle. To make that immediate, force new ones:

```sh
aws lambda update-function-configuration --region "$REGION" \
  --function-name "${PREFIX}_rudolph_web" \
  --description "rolled $(date -u +%FT%TZ)"
```

Every existing execution environment is replaced, so the next request is a cold start that re-reads
Parameter Store. `make deploy` has the same effect.

### The cookie key

```sh
aws ssm put-parameter --region "$REGION" \
  --name "/$PREFIX/rudolph-web/cookie-key" \
  --type SecureString --overwrite \
  --value "$(openssl rand -base64 32)"
```

Then force new execution environments as above.

Rolling the cookie key invalidates every session: users sign in again. It also changes the derived
cursor key, so a page link someone is holding returns 400 until they reload the first page. During
the roll, requests served by an old execution environment still accept old cookies; the two keys
coexist only that briefly, and there is no dual-key window by design.

### The Entra client secret

Order matters, so that no request is served with a secret Entra has already rejected:

1. In the app registration, **create a second client secret**. Both are valid at once.
2. Write it over the parameter:

   ```sh
   aws ssm put-parameter --region "$REGION" \
     --name "/$PREFIX/rudolph-web/client-secret" \
     --type SecureString --overwrite --value "$(cat)"
   ```

3. Force new execution environments, and sign in once to confirm.
4. **Delete the old secret** in Entra.

The secret is only used to redeem the authorization code at `/auth/callback`, so a stale one breaks
new sign-ins, not existing sessions.

---

## Day to day

```sh
make plan                  # terraform plan for $ENV
make deploy                # test, build, init, apply
make destroy               # tear the environment down
make tf-check              # fmt -check, init -backend=false, validate; touches no AWS account
make test                  # go vet + go test, including the Terraform static checks
make deps-check            # govulncheck (fails on a vulnerability), then outdated or deprecated modules
```

`make deps-check` runs [govulncheck](https://go.dev/doc/security/vuln/) (pinned in the `Makefile`), which fails if a known vulnerability affects the web view's code, then lists every module in the build graph that has a newer version or is deprecated. That list is for information: it includes modules that only appear in other modules' `go.mod` files and are never built. Run it before a release and after updating dependencies (`go get -u ./...`, `go get -u -t ./...`, `go mod tidy`). The AWS SDK for Go v1 must not come back: `internal/depcheck` fails if it is required or built.

`make destroy` leaves the two SSM parameters behind unless Terraform created them; delete them with
`aws ssm delete-parameter` if you mean to.

Logs:

```sh
aws logs tail "/aws/lambda/${PREFIX}_rudolph_web" --follow
```

The function logs JSON to stderr and never logs secrets, cookies or tokens.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `make plan` or `make deploy` stops with "does not match the example environment" | The environment was created from an older release; see [Upgrading an existing environment](#upgrading-an-existing-environment). |
| Every request 500s, logs say `config: RUDOLPH_WEB_REDIRECT_URL is required` | `base_url` is still empty; see [First deploy](#first-deploy). |
| Logs say `config: ... parameter ... is empty` or `must decode to 32 bytes` | The parameter still holds the placeholder, or the cookie key is not base64 of 32 bytes. |
| Logs say `AccessDeniedException` on `ssm:GetParameter` | The parameter name does not match `client_secret_ssm_parameter` / `cookie_key_ssm_parameter`, or it is encrypted with a key the role cannot use — set `ssm_kms_key_arn`. |
| Entra shows `AADSTS50011: redirect URI mismatch` | The registered redirect URI is not exactly the `redirect_url` output. |
| Logout leaves you on Entra's signed-out page | `post_logout_redirect_url` is not registered on the app registration. |
| Sign-in works, every page is 403 | The account has no `Rudolph.Viewer` or `Rudolph.Admin` assignment. |
| `POST /auth/logout` returns 403 | The browser's `Origin` does not match `base_url`; you are reaching the app through a hostname it was not configured with. |
| Data pages return 503 | Every data page: `RUDOLPH_WEB_DYNAMODB_TABLE` is not set on the function (a deployment not made from the module). Only the events and top list pages: `RUDOLPH_WEB_EVENTS_TABLE` is not set (`events_dynamodb_table_name` empty). |
