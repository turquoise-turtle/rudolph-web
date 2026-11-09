# rudolph-web

A lightweight, read-mostly web view over [Rudolph](https://github.com/airbnb/rudolph),
the serverless sync server for North Pole Security's
[Santa](https://northpole.dev) binary authorization agent on macOS.

Admins use it to see:

- **Machines**: a paginated list you can filter by serial number or hostname prefix,
  plus a detail page with last sync, client mode, rule counts and machine-specific rules.
  Both mark a Santa version below 2026.8 as "legacy client" (Rudolph supports 2025.12 only for macOS 13 Macs, with limits); an empty or unparseable version is not marked.
  Its "Sync health" section lists signs that a Mac is not syncing as it should: not seen for more than a day, a last sync without postflight 30 minutes after its preflight, a pending clean sync request, and a temporary client mode (running, ended, or ignored because it ends more than 30 days ahead).
- **Global rules**: a paginated list you can filter by rule type and policy.
- **Device groups**: the registered groups (description, whether reported membership counts) and the reserved `rudolph:ungrouped`, shown as "Macs in no group", each with a paginated page of its rules. A Mac's page shows its assigned groups, its reported groups that count and the groups Santa sent, or "Ungrouped".
- **Execution events**: a paginated list you can filter by machine, decision and team ID. Events Santa marked identity_unverified (Santa 2026.9: it could not confirm the file it evaluated was the image that ran) show "identity not verified", since their hash and file details are not verified.
- **Top blocked and top allowed binaries**: the binaries blocked or allowed most often in the last 1, 7, 30 or 90 days, as computed by Rudolph's ranker, with the time the list was generated and a warning when that is longer ago than a configurable threshold (default 3 hours).

- **Editing (optional, v2)**: with editing turned on (`enable_editing`), people with the `Rudolph.Admin` role add and remove global rules on the rules page, rules for one Mac and its temporary client mode (a mode and a duration from 1m to 30d, or end it now) on that Mac's page, and group rules on each group's page. Every change is made with Rudolph's own Go packages, so validation and the rows written match the CLI exactly, and is recorded in Rudolph's audit trail with the signed-in Entra user (`web:<username> (<object ID>)`). `/admin/audit` lists the last 30 days of changes by the CLI and the web view, newest first, a page at a time.

Without editing the view is read-only: no forms, and the write routes do not exist. `/admin` needs the `Rudolph.Admin` role.

## Architecture

```
 Browser ──HTTPS──▶ (optional CloudFront) ──▶ Lambda Function URL
                         │  payload format 2.0
                         ▼
                 internal/lambdaadapter ──▶ http.Handler (internal/web)
                                               │
                     ┌─────────────────────────┼──────────────────────────┐
                     ▼                         ▼                          ▼
               internal/auth            html/template pages        internal/store
      (Entra ID OIDC + PKCE, AES-GCM    (embedded templates+CSS,     (read interfaces;
       cookie sessions, App Roles)       one inline time script)      fake in store/fake)
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
  renders as 400. The fake signs with a per-process random key. The DynamoDB implementation carries the marshalled `LastEvaluatedKey` as the position, and `cmd/lambda` derives its key with `store.DeriveCursorKey` (HKDF-SHA256 over the session cookie key, with a cursor-specific info string), so every instance signs alike and the cursor key stays independent of the cookie key.
- **Server-rendered.** It uses `net/http` and `html/template`, with contextual auto-escaping on all data and no `template.HTML` on data. The server renders every time in UTC inside a `<time datetime>` element. Each page carries one small inline script (`internal/web/localtime.js`), the only script it runs: it rewrites each time's text to add the viewer's own time zone, e.g. `2026-09-17 11:55:00 UTC (2026-09-17 20:55 JST)`, and does nothing else: it reads each time's `datetime` attribute and writes text, and loads nothing, stores nothing and builds no markup. The CSP's `script-src` allows that script by its SHA-256 hash and nothing else. If it does not run, the pages still work and show UTC only. Dates that are UTC days, such as the machine list's last seen, stay plain UTC dates.
- **Commands to copy, and plain forms for admins.** Without editing the pages are read-only. With it, admins get plain HTML forms that POST to `/admin/...` and redirect back (no script); every POST is same-origin only and admin-only. Event rows and top-list entries, the machine page, and global, machine and group rule rows offer Rudolph CLI commands (`internal/clicmd`) under a "Commands" disclosure, matching the contract in `testdata/webcontract/cli-examples.txt` that Rudolph checks against its real CLI. Event rows and top-list entries group their commands by the rule type the file can be matched by, most useful first: signing ID, team ID, CDHash (events only) and binary (SHA-256). The first group is open and the others are one click away. Each event group offers allow on that Mac for 24 hours, allow everywhere and block everywhere; each top-list group offers block everywhere and allow everywhere, since a top list spans machines. Each identifier is checked against its type's shape (SHA-256, CDHash, team ID, signing ID, machine UUID) and a type whose identifier does not fit is left out, so an unsigned file offers binary rules only and a platform binary offers no team ID rules; every value from a Mac is single-quoted. With no copy script there is no copy button: a click selects the whole command. Add `--dry-run` to a pasted command to see what it would change without writing anything.
- **Wide tables use the whole window.** The page has no maximum width, so the events and top list tables fill a wide window instead of scrolling sideways beside empty space; paragraphs are capped at about 100 characters to stay readable.
- **The same handler runs locally.** `cmd/local` serves it with `net/http`.

### Packages

| Path | Purpose |
|---|---|
| `cmd/local` | Local HTTP server using the in-memory fake store seeded with demo data |
| `cmd/lambda` | Lambda entrypoint (Function URL, payload format 2.0) |
| `internal/config` | Loads and validates configuration from environment variables, with the secrets optionally from SSM |
| `internal/awsssm` | Reads and caches SecureString parameters for the Lambda |
| `internal/auth` | OIDC login/callback/logout, encrypted cookies, role middleware |
| `internal/web` | Routes, handlers, templates, security headers |
| `internal/clicmd` | Builds the Rudolph CLI commands the pages offer to copy |
| `internal/store` | Read models and data-access interfaces |
| `internal/store/fake` | In-memory implementation that records calls (tests + local dev); also writes, so `cmd/local` has editing on |
| `internal/store/dynamo` | DynamoDB reader, and the writer over Rudolph's own packages (`github.com/airbnb/rudolph/pkg/...`) |
| `internal/store/rudolphrules` | Checks edits with Rudolph's validation, shared by the writer and the fake |
| `internal/lambdaadapter` | Payload format 2.0 event ⇄ `http.Handler` |
| `internal/testutil/fakeidp` | Test-only fake Entra-like OIDC provider |
| `deployments` | Terraform module, example environment and static checks on both |

### Data layer

The read models follow Rudolph's single-table DynamoDB layout (`PK`/`SK`):

| Read model | Rudolph item |
|---|---|
| `store.Machine` | `PK=Machine#<id>, SK=Current` (sensor data; adds Hostname and ModelIdentifier when Rudolph stores them, and SIPStatus, where a missing attribute means enabled) |
| `store.SyncState` | `PK=Machine#<id>, SK=SyncState` (PreflightAt, PostflightAt; adds LastSyncType, RulesProcessed) |
| `store.MachineDetail` config fields | `PK=Machine#<id>, SK=Config` (ClientMode, TemporaryClientMode with TemporaryClientModeExpiresAt, CleanSync; mistyped values are left out) |
| `store.Rule` (global) | `PK=GlobalRules, SK=<Type>#<identifier>` |
| `store.Rule` (machine) | `PK=MachineRules#<id>` |
| `store.MachineDetail` group fields | `PK=Machine#<id>, SK=Groups` (Assigned, Reported: the reported groups that count) and `SK=ReportedGroups` (PrimaryUserGroups as Santa sent them); mistyped values are left out |
| Global client mode (`GlobalClientMode`) | `PK=GlobalConfig, SK=Config` (ClientMode; no row means MONITOR, as Rudolph sends) |
| `store.Group` | `PK=GroupRegistry, SK=Current` (Groups map: name to Description, AcceptReported) |
| `store.Rule` (group) | `PK=GroupRules#<group>` |
| `store.ExecutionEvent` | events table: `PK=Exec#<id>, SK=<TS>#<sha256>#<pid>`, indexes `Fleet_TS`, `Decision_TS`, `TeamID_TS` |
| `store.TopBinary` | events table: pointer `PK=Top#<Blocked or Allowed>#<days>, SK=Current` (Snapshot, GeneratedAt), rows `PK=TopSnapshot#<kind>#<days>#<snapshot>, SK=<rank>` written by Rudolph's ranker |
| `store.FileAccessEvent` | not read yet: interface + fake only |

`internal/store/dynamo` reads the machine list (all, or by serial or hostname prefix), machine detail, machine rules, group rules and global rules (all, or by rule type, policy or both) from Rudolph's table, each page with one bounded Query; the group list is one GetItem of the registry. The machine page also reads the global config's client mode (one GetItem) to name the mode a Mac without its own follows; if that read fails, the page says only that the global config applies. The machine list reads Rudolph's `DataType_SerialKey` and `DataType_HostnameKey` indexes and the policy filter reads `GlobalRulePolicy_SK`, so they need a Rudolph version that has them; its last seen is a UTC date (`LastSeenDay`), while the machine detail page shows the exact time. `cmd/lambda` uses it when `RUDOLPH_WEB_DYNAMODB_TABLE` is set, and serves 503 for every data page otherwise. Execution events come from Rudolph's separate events table, named by `RUDOLPH_WEB_EVENTS_TABLE`, newest first: all of them (`Fleet_TS`), one Mac's (its `Exec#<id>` partition), one decision's (`Decision_TS`) or one team ID's (`TeamID_TS`), one bounded Query per page. Filters are used one at a time; a combination answers 400. Decisions the view does not know, such as `ALLOW_PLATFORM`, are shown as stored. An event row the reader cannot decode is logged and skipped, so its page may be short, rather than failing the page and hiding every older event until the row expires. The top blocked and top allowed lists (`/events/top-blocked` and `/events/top-allowed`, `days` 1, 7, 30 or 90, default 7) come from the same table: Rudolph's ranker writes each list as a snapshot and moves a pointer to it, and a page reads the pointer (one GetItem) and one page of the snapshot (one Query). The cursor pins the snapshot, so later pages stay consistent when the ranker runs meanwhile, and is bound to the kind and window. A list whose pointer does not exist yet shows "This list has not been computed yet". Machine counts exist for the first 100 rows of each list only and show as "—" further down. Without the events table the events and top list pages answer 503; file access events still answer 503 (see [docs/data-layer-design.md](docs/data-layer-design.md)).

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
  - Signing in without a role works, but every page returns 403. The 403 (also a viewer's on an admin-only route) is the site's usual page, with the navigation and Sign out, naming the role the account is missing; a request that is not signed in still gets the login redirect (GET and HEAD) or a plain 401.
  - Error pages show the status code with its name in the heading and title, e.g. "404 Not Found".
  - A signed-in user (any role) opening a path the site has no page for, such as `/admin/audit` with editing off, gets the same site page with "404 Not Found". A known path with the wrong method still answers 405 with its `Allow` header, and unknown `/static/` files are always a plain 404.
  - Without a valid session the site does not reveal which addresses exist: every path outside `/auth/` and `/static/`, whether it is a real page, a route that exists only with editing on, an unknown address or a real address used with the wrong method, gets exactly the response a real page gives, the sign-in redirect for GET and HEAD (with the address as `return_to`) or the same plain 401 for any other method. Only after sign-in does an unknown address get the 404 or 405. The login handler keeps only same-site relative `return_to` values, so an address such as `//evil.example/x` cannot become an open redirect. A cross-site POST is still refused with 403 by the cross-origin check first, whatever the path.
- Logout is `POST /auth/logout`. It clears the cookie and redirects to Entra's
  `end_session_endpoint`.
- Unsafe requests are checked by `http.CrossOriginProtection`. Behind CloudFront or
  another custom domain the Lambda sees its own host, so the origin of
  `RUDOLPH_WEB_REDIRECT_URL` is registered as a trusted origin; every other
  cross-origin request is rejected with 403. Set the redirect URL to the host
  users actually visit.
- Every response, including redirects, 403s, 404s and cross-origin rejections,
  carries the same headers: CSP (`default-src`, `frame-ancestors`
  and `base-uri` all `'none'`; `script-src` only the hash of the inline time script; `form-action` limited to self and the Entra origin),
  HSTS, `Referrer-Policy`, `X-Content-Type-Options: nosniff` and
  `Cache-Control: no-store`. Only the embedded stylesheet is cacheable, under a URL carrying a hash of its content, so a new stylesheet shows as soon as it is deployed.

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
| `RUDOLPH_WEB_CLI_ENV` | no | Rudolph environment name (letters, digits, `.`, `_`, `-`) the copy-and-paste CLI commands start with, as `ENV=<name> rudolph …`; unset, they start with `rudolph` |
| `RUDOLPH_WEB_EDITING` | no | `true` lets `Rudolph.Admin` add and remove rules and set temporary client modes (needs `RUDOLPH_WEB_DYNAMODB_TABLE`, and the role's write grant from `enable_editing`); `false` or unset keeps the view read-only; anything else is an error |
| `RUDOLPH_WEB_TOP_LIST_STALE_AFTER` | no | Go duration, 1h–168h, default `3h`: how old a top list may be before its page warns that Rudolph's ranker may have stopped |
| `RUDOLPH_WEB_EVENTS_TABLE` | no | Rudolph's execution events table (`<prefix>_rudolph_events`), read by `cmd/lambda`; unset, the events and top list pages return 503. Needs `RUDOLPH_WEB_DYNAMODB_TABLE` |

\* Each secret has exactly one source. Set the plain variable, as local development does, or name a
SecureString parameter with its `*_SSM_PARAMETER` variable; setting both is an error. `cmd/lambda`
reads the parameters once per cold start and caches them, so the deployed function carries no secret
in its environment. See [docs/deploy.md](docs/deploy.md).

## Running locally

You need Go 1.27 or newer; deploying also needs Terraform 1.10 or newer (see [docs/deploy.md](docs/deploy.md)).

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
CloudFront with a custom domain, plus a CloudWatch log group with retention, an execution role scoped to reading two SSM parameters and to `GetItem` and `Query` on Rudolph's table and, when configured, its events table, and nothing else.

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
make deps-check    # govulncheck, then outdated or deprecated modules
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

Integration tests (build tag `integration`) run the DynamoDB data layer against DynamoDB Local, with the tables created from `testdata/rudolph/dynamodb.tf` and `testdata/rudolph/events.tf`, copies of Rudolph's store and events table definitions. They need Java and DynamoDB Local: either set `DYNAMODB_LOCAL_DIR`, or keep a Rudolph checkout next to this repo (`../rudolph`) where `scripts/setup_test_deps.sh` has been run.

```sh
go test -tags integration ./...
```

The three copies (`rows.json`, `dynamodb.tf` and `events.tf`) are checked against Rudolph's originals whenever a Rudolph checkout is found at `../rudolph` or `$RUDOLPH_DIR`. When Rudolph changes them, copy them over and look at what changed. `testdata/webcontract/cli-examples.txt` goes the other way: Rudolph runs every line of its copy as a dry run through its real CLI, and `internal/clicmd` checks that the commands the pages offer match it.
