# Deploying rudolph-web

rudolph-web runs as a single arm64 Lambda behind a Lambda Function URL, optionally fronted by
CloudFront for a custom domain. Idle, it costs nothing.

```
 Browser ──HTTPS──▶ [CloudFront + ACM, optional] ──▶ Lambda Function URL (auth: NONE)
                                                          │
                                                      cmd/lambda
                                             ┌────────────┴─────────────┐
                                             ▼                          ▼
                                    SSM Parameter Store        DynamoDB (Rudolph's table)
                                    (client secret, cookie key)   GetItem / Query, read-only
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

* **Go** — the version in `go.mod`.
* **Terraform** 1.5 or newer (tested with 1.16), with the `hashicorp/aws` provider `~> 6.0`.
* **AWS CLI**, authenticated as a role that may create Lambda functions, IAM roles, SSM parameters,
  CloudWatch log groups and, if you enable it, CloudFront distributions, ACM certificates and
  Route 53 records.
* An **S3 bucket** for the Terraform state. It must already exist.

### A Rudolph deployment

rudolph-web reads Rudolph's DynamoDB table. Note its name (`<rudolph prefix>_rudolph_store` by
default) and, if the table is encrypted with a customer managed KMS key, that key's ARN.

The function reads the table named by its `RUDOLPH_TABLE_NAME` environment variable. Machine detail, machine rules and global rules come from it; the machine list, the policy filter on global rules and the event pages return 503 until Rudolph's storage changes land. Without `RUDOLPH_TABLE_NAME` every data page returns 503, while authentication, authorization and everything else in this guide still work.

The Terraform module does not set `RUDOLPH_TABLE_NAME` yet, so a deployment from it serves 503 for every data page. Setting the variable by hand (to the value of `dynamodb_table_name`) lasts only until the next `terraform apply` replaces the function's environment. The reader only calls `dynamodb:Query` on the table itself, which the `ReadRudolphStore` statement below already allows.

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
   * `Rudolph.Admin` — the admin-only routes; includes Viewer.

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

That copies the example into `deployments/environments/prod/` (which is gitignored, since it holds
account specific settings). Edit two files:

* **`_backend.tf`** — the state bucket, key, region and KMS key, and the region of the two AWS
  providers. Keep the `aws.us_east_1` alias even if you deploy elsewhere and disable CloudFront:
  CloudFront certificates only exist in us-east-1, and the module declares that provider.
* **`config.auto.tfvars.json`** — the settings, documented in
  [the example environment's README](../deployments/environments/example/README.md).

At a minimum set `aws_account_id`, `region`, `prefix`, `tenant_id`, `client_id` and
`dynamodb_table_name`.

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

### The role

Four statements, no AWS managed policies and no wildcard resources:

| Statement | Actions | Resources |
|---|---|---|
| `WriteOwnLogs` | `logs:CreateLogStream`, `logs:PutLogEvents` | its own log group only |
| `ReadSecrets` | `ssm:GetParameter` | the two parameter ARNs |
| `DecryptSecrets` | `kms:Decrypt` | the SSM key, conditioned on `kms:ViaService = ssm.<region>.amazonaws.com` |
| `ReadRudolphStore` | `dynamodb:GetItem`, `dynamodb:Query` | the table and `<table>/index/*` |

With `dynamodb_kms_key_arn` set there is a fifth, `kms:Decrypt` conditioned on the DynamoDB service.
There is deliberately no `dynamodb:Scan` — every list in the app is one bounded query — and no write
action of any kind, because the view is read-only.

Terraform creates the log group itself, so Lambda cannot create one without retention, and the
function does not need `logs:CreateLogGroup`.

### CloudFront, the Function URL and cross-origin protection

Worth knowing before you change any of it:

* **Caching is off for everything except `/static/*`.** Pages are rendered for the signed-in user
  and carry `Cache-Control: no-store`; only the embedded stylesheet is cacheable.
* **Cookies and query strings are forwarded in full.** The session lives in a cookie, and the OIDC
  callback and the pagination cursors are query parameters.
* **`Origin`, `Referer` and the four `Sec-Fetch-*` headers are forwarded**, because Go's
  `http.CrossOriginProtection` uses them to decide whether an unsafe request is cross-origin.
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
```

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
| Every request 500s, logs say `config: RUDOLPH_WEB_REDIRECT_URL is required` | `base_url` is still empty; see [First deploy](#first-deploy). |
| Logs say `config: ... parameter ... is empty` or `must decode to 32 bytes` | The parameter still holds the placeholder, or the cookie key is not base64 of 32 bytes. |
| Logs say `AccessDeniedException` on `ssm:GetParameter` | The parameter name does not match `client_secret_ssm_parameter` / `cookie_key_ssm_parameter`, or it is encrypted with a key the role cannot use — set `ssm_kms_key_arn`. |
| Entra shows `AADSTS50011: redirect URI mismatch` | The registered redirect URI is not exactly the `redirect_url` output. |
| Logout leaves you on Entra's signed-out page | `post_logout_redirect_url` is not registered on the app registration. |
| Sign-in works, every page is 403 | The account has no `Rudolph.Viewer` or `Rudolph.Admin` assignment. |
| `POST /auth/logout` returns 403 | The browser's `Origin` does not match `base_url`; you are reaching the app through a hostname it was not configured with. |
| Data pages return 503 | Every data page: `RUDOLPH_TABLE_NAME` is not set on the function. Only the machine list, the policy filter and events: expected until Rudolph's storage changes land. |
