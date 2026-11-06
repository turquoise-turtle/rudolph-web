# rudolph-web

A lightweight, read-mostly web view over [Rudolph](https://github.com/airbnb/rudolph),
the serverless sync server for North Pole Security's
[Santa](https://northpole.dev) binary authorization agent on macOS.

Admins use it to see:

- **Machines**: a paginated list you can filter by serial number or hostname prefix,
  plus a detail page with last sync, client mode, rule counts and machine-specific rules.
- **Global rules**: a paginated list you can filter by rule type and policy.
- **Execution events**: a paginated list you can filter by machine, decision and team ID.
- **Top blocked binaries**: the binaries blocked most often in a time window.

v1 is read-only. `/admin` is a placeholder for future write actions and needs the
`Rudolph.Admin` role.

## Architecture

```
 Browser ──HTTPS──▶ Lambda Function URL / API Gateway (HTTP API)
                         │  payload format 2.0
                         ▼
                 internal/lambdaadapter ──▶ http.Handler (internal/web)
                                               │
                     ┌─────────────────────────┼──────────────────────────┐
                     ▼                         ▼                          ▼
               internal/auth            html/template pages        internal/store
      (Entra ID OIDC + PKCE, AES-GCM    (embedded templates+CSS,     (read interfaces;
       cookie sessions, App Roles)       no JavaScript)               fake in store/fake)
```

- **Serverless and cheap.** It runs as one arm64 Lambda (`cmd/lambda`) and costs
  almost nothing when idle. Sessions are stateless encrypted cookies, so an
  authenticated page view needs no session store and no call to Entra ID. OIDC
  discovery runs lazily, only on login and logout.
- **Bounded reads.** Every list issues exactly one paginated query with
  `Limit = page size` and passes an opaque cursor through the URL. There are no scans,
  and handlers never fetch everything and slice it in memory. The tests enforce
  this through the call-recording fake.
- **Authenticated cursors.** A cursor carries the position to resume from with a
  fingerprint of the query that produced it, authenticated with HMAC-SHA256
  (`store.CursorCodec`). A cursor cannot be minted by hand, edited to skip ahead or
  to point at another partition, or replayed against a different query or filter;
  anything that fails verification is `store.ErrInvalidCursor`, which the web layer
  renders as 400. The fake signs with a per-process random key. The DynamoDB
  implementation will carry the marshalled `LastEvaluatedKey` as the position and
  derive its key with `store.DeriveCursorKey` (HKDF-SHA256 over the session cookie
  key, with a cursor-specific info string), so every instance signs alike and the
  cursor key stays independent of the cookie key.
- **Server-rendered.** It uses `net/http` and `html/template`, with contextual
  auto-escaping on all data and no `template.HTML` on data. It has no JavaScript,
  and the CSP includes `script-src 'none'`.
- **The same handler runs locally.** `cmd/local` serves it with `net/http`.

### Packages

| Path | Purpose |
|---|---|
| `cmd/local` | Local HTTP server using the in-memory fake store seeded with demo data |
| `cmd/lambda` | Lambda entrypoint (Function URL / API Gateway HTTP API) |
| `internal/config` | Loads and validates configuration from environment variables, with the secrets optionally from SSM |
| `internal/awsssm` | Reads and caches SecureString parameters for the Lambda |
| `internal/auth` | OIDC login/callback/logout, encrypted cookies, role middleware |
| `internal/web` | Routes, handlers, templates, security headers |
| `internal/store` | Read models and data-access interfaces |
| `internal/store/fake` | In-memory implementation that records calls (tests + local dev) |
| `internal/lambdaadapter` | Payload format 2.0 event ⇄ `http.Handler` |
| `internal/testutil/fakeidp` | Test-only fake Entra-like OIDC provider |
| `deployments` | Terraform module, example environment and static checks on both |

### Data layer

The read models follow Rudolph's single-table DynamoDB layout (`PK`/`SK`):

| Read model | Rudolph item |
|---|---|
| `store.Machine` | `PK=Machine#<id>, SK=Current` (sensor data; adds Hostname and ModelIdentifier when Rudolph stores them, and SIPStatus, where a missing attribute means enabled) |
| `store.SyncState` | `PK=Machine#<id>, SK=SyncState` (PreflightAt, PostflightAt; adds LastSyncType, RulesProcessed) |
| `store.Rule` (global) | `PK=GlobalRules, SK=<Type>#<identifier>` |
| `store.Rule` (machine) | `PK=MachineRules#<id>` |
| `store.ExecutionEvent` | events table: `PK=Exec#<id>, SK=<TS>#<sha256>#<pid>`, indexes `Fleet_TS`, `Decision_TS`, `TeamID_TS` |
| `store.BlockedBinary`, `store.FileAccessEvent` | not read yet: interface + fake only |

`internal/store/dynamo` reads the machine list (all, or by serial or hostname prefix), machine detail, machine rules and global rules (all, or by rule type, policy or both) from Rudolph's table, each page with one bounded Query. The machine list reads Rudolph's `DataType_SerialKey` and `DataType_HostnameKey` indexes and the policy filter reads `GlobalRulePolicy_SK`, so they need a Rudolph version that has them; its last seen is a UTC date (`LastSeenDay`), while the machine detail page shows the exact time. `cmd/lambda` uses it when `RUDOLPH_WEB_DYNAMODB_TABLE` is set, and serves 503 for every data page otherwise. Execution events come from Rudolph's separate events table, named by `RUDOLPH_WEB_EVENTS_TABLE`, newest first: all of them (`Fleet_TS`), one Mac's (its `Exec#<id>` partition), one decision's (`Decision_TS`) or one team ID's (`TeamID_TS`), one bounded Query per page. Filters are used one at a time; a combination answers 400. Decisions the view does not know, such as `ALLOW_PLATFORM`, are shown as stored. An event row the reader cannot decode is logged and skipped, so its page may be short, rather than failing the page and hiding every older event until the row expires. Without the events table the events page answers 503; top blocked binaries and file access events still answer 503 (see [docs/data-layer-design.md](docs/data-layer-design.md)).

One thing the event implementation will need:

- **Top blocked binaries.** Ranking by block count cannot come from one bounded
  query over raw events, so this view assumes a precomputed aggregate (for example
  a per-day, per-binary counter item updated on event upload) that can be read in
  key order and paginated. Its next links carry the window start, so later pages
  read exactly the window the first page did.

## Authentication and authorization

- Microsoft Entra ID, single tenant, uses the OIDC authorization code flow with PKCE (S256),
  built on `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2`.
- The app validates the ID token signature (RS256, JWKS from discovery), `iss`
  (`https://login.microsoftonline.com/<tenant>/v2.0`), `aud` (the client ID),
  `exp`, `nonce` and `tid` (it must equal the configured tenant). It checks `state`
  against an encrypted, 10-minute, one-time flow cookie before redeeming the code.
- The session is an AES-256-GCM encrypted and authenticated cookie named
  `__Host-rudolph_session`. It is `Secure`, `HttpOnly`, `SameSite=Lax`, has `Path=/`,
  no `Domain`, and lasts one hour by default. The cookie name is bound into the
  AEAD additional data, so a flow cookie cannot be replayed as a session.
- Authorization comes from Entra **App Roles** in the `roles` claim:
  - `Rudolph.Viewer` grants the read pages.
  - `Rudolph.Admin` grants the admin-only routes and includes Viewer.
  - Signing in without a role works, but every page returns 403.
- Logout is `POST /auth/logout`. It clears the cookie and redirects to Entra's
  `end_session_endpoint`.
- Unsafe requests are checked by `http.CrossOriginProtection`. Behind CloudFront or
  another custom domain the Lambda sees its own host, so the origin of
  `RUDOLPH_WEB_REDIRECT_URL` is registered as a trusted origin; every other
  cross-origin request is rejected with 403. Set the redirect URL to the host
  users actually visit.
- Every response, including redirects, 403s, 404s and cross-origin rejections,
  carries the same headers: CSP (`default-src`, `script-src`, `frame-ancestors`
  and `base-uri` all `'none'`; `form-action` limited to self and the Entra origin),
  HSTS, `Referrer-Policy`, `X-Content-Type-Options: nosniff` and
  `Cache-Control: no-store`. Only the embedded stylesheet is cacheable.

Role changes take effect at the next sign-in, which is at most one session TTL later.

### Entra app registration

1. Register a single-tenant web application. Add the redirect URI
   `https://<host>/auth/callback`, and `http://localhost:8080/auth/callback` for
   local development.
2. Create a client secret.
3. Under **App roles**, add `Rudolph.Viewer` and `Rudolph.Admin` for Users/Groups.
   Assign them in Enterprise applications → Users and groups. You can require
   assignment so that unassigned users cannot sign in at all.
4. If you set `RUDOLPH_WEB_POST_LOGOUT_REDIRECT_URL`, register that exact URL as a
   front-channel logout / post-logout redirect URI on the app registration.
   Entra ignores an unregistered `post_logout_redirect_uri` and leaves the user on
   its own signed-out page.

## Configuration

| Variable | Required | Description |
|---|---|---|
| `RUDOLPH_WEB_TENANT_ID` | yes | Entra tenant ID (GUID) |
| `RUDOLPH_WEB_CLIENT_ID` | yes | Application (client) ID |
| `RUDOLPH_WEB_CLIENT_SECRET` | yes* | Client secret |
| `RUDOLPH_WEB_REDIRECT_URL` | yes | `https://<host>/auth/callback` (`http://localhost…` allowed for dev) |
| `RUDOLPH_WEB_COOKIE_KEY` | yes* | 32 random bytes, base64 (std or url-safe): `openssl rand -base64 32` |
| `RUDOLPH_WEB_POST_LOGOUT_REDIRECT_URL` | no | Where Entra sends the browser after logout |
| `RUDOLPH_WEB_SESSION_TTL` | no | Go duration, 5m–12h, default `1h` |
| `RUDOLPH_WEB_ADDR` | no | Listen address for `cmd/local`, default `127.0.0.1:8080` |
| `RUDOLPH_WEB_CLIENT_SECRET_SSM_PARAMETER` | no | SSM parameter holding the client secret, instead of the variable above |
| `RUDOLPH_WEB_COOKIE_KEY_SSM_PARAMETER` | no | SSM parameter holding the cookie key, instead of the variable above |
| `RUDOLPH_WEB_DYNAMODB_TABLE` | no | Rudolph's DynamoDB table, read by `cmd/lambda`; unset, data pages return 503. Cursors are signed with a key derived from the cookie key |
| `RUDOLPH_WEB_EVENTS_TABLE` | no | Rudolph's execution events table (`<prefix>_rudolph_events`), read by `cmd/lambda`; unset, the events page returns 503. Needs `RUDOLPH_WEB_DYNAMODB_TABLE` |

\* Each secret has exactly one source. Set the plain variable, as local development does, or name a
SecureString parameter with its `*_SSM_PARAMETER` variable; setting both is an error. `cmd/lambda`
reads the parameters once per cold start and caches them, so the deployed function carries no secret
in its environment. See [docs/deploy.md](docs/deploy.md).

## Running locally

```sh
export RUDOLPH_WEB_TENANT_ID=<tenant-guid>
export RUDOLPH_WEB_CLIENT_ID=<client-id>
export RUDOLPH_WEB_CLIENT_SECRET=<secret>
export RUDOLPH_WEB_REDIRECT_URL=http://localhost:8080/auth/callback
export RUDOLPH_WEB_COOKIE_KEY=$(openssl rand -base64 32)
go run ./cmd/local
open http://localhost:8080/
```

Use `localhost`, not `127.0.0.1`, in the browser. The cookies are `Secure` with the
`__Host-` prefix. Chrome and Firefox treat `http://localhost` as a secure context and
accept these cookies there. Safari may not.

Local mode serves demo data from the in-memory fake store.

## Deploying

`deployments/` holds Terraform for one arm64 Lambda behind a Function URL, optionally fronted by
CloudFront with a custom domain, plus a CloudWatch log group with retention, an execution role
scoped to reading two SSM parameters and querying Rudolph's table, and nothing else.

```sh
export ENV=prod
make init-env      # create deployments/environments/prod from the example
make deploy        # test, build the arm64 zip, terraform init and apply
```

[docs/deploy.md](docs/deploy.md) covers the prerequisites, the Entra app registration, creating the
two SecureString parameters, the first deploy, and how to roll the cookie key and the client secret.
`make build` alone produces `build/package/rudolph_web_deployment.zip`
(runtime `provided.al2023`, architecture `arm64`), and `make tf-check` runs `terraform fmt -check`,
`init -backend=false` and `validate` without touching an AWS account.

## Tests

```sh
go vet ./...
go test ./...
```

The tests cover the following:

- The full OIDC round trip against a local fake provider.
- Rejection of invalid tokens, state, and cookies.
- Role gating.
- Logout.
- Cookie attributes.
- Handler output against the fake store, including pagination, filters and escaping.
- A query-budget guard.
- The Lambda adapter.
- The SSM-backed secret loading, against a fake fetcher.
- The Terraform, through HCL-parsing static checks in `deployments/terraform_test.go`: least
  privilege on the role, no secret anywhere in the configuration, and the Function URL and
  CloudFront wiring.
- Decoding of Rudolph's DynamoDB rows, against `testdata/webcontract/rows.json`, a copy of Rudolph's web contract fixture.

Integration tests (build tag `integration`) run the DynamoDB data layer against DynamoDB Local, with the table created from `testdata/rudolph/dynamodb.tf`, a copy of Rudolph's table definition. They need Java and DynamoDB Local: either set `DYNAMODB_LOCAL_DIR`, or keep a Rudolph checkout next to this repo (`../rudolph`) where `scripts/setup_test_deps.sh` has been run.

```sh
go test -tags integration ./...
```

Both copies are checked against Rudolph's originals whenever a Rudolph checkout is found at `../rudolph` or `$RUDOLPH_DIR`. When Rudolph changes them, copy them over and look at what changed.
