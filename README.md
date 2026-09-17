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
- **Server-rendered.** It uses `net/http` and `html/template`, with contextual
  auto-escaping on all data and no `template.HTML` on data. It has no JavaScript,
  and the CSP includes `script-src 'none'`.
- **The same handler runs locally.** `cmd/local` serves it with `net/http`.

### Packages

| Path | Purpose |
|---|---|
| `cmd/local` | Local HTTP server using the in-memory fake store seeded with demo data |
| `cmd/lambda` | Lambda entrypoint (Function URL / API Gateway HTTP API) |
| `internal/config` | Loads and validates configuration from environment variables |
| `internal/auth` | OIDC login/callback/logout, encrypted cookies, role middleware |
| `internal/web` | Routes, handlers, templates, security headers |
| `internal/store` | Read models and data-access interfaces |
| `internal/store/fake` | In-memory implementation that records calls (tests + local dev) |
| `internal/lambdaadapter` | Payload format 2.0 event ⇄ `http.Handler` |
| `internal/testutil/fakeidp` | Test-only fake Entra-like OIDC provider |

### Data layer

The read models follow Rudolph's single-table DynamoDB layout (`PK`/`SK`):

| Read model | Rudolph item |
|---|---|
| `store.Machine` | `PK=Machine#<id>, SK=Current` (sensor data; adds Hostname, ModelIdentifier, SIPStatus when Rudolph stores them) |
| `store.SyncState` | `PK=Machine#<id>, SK=SyncState` (PreflightAt, PostflightAt; adds LastSyncType, RulesProcessed) |
| `store.Rule` (global) | `PK=GlobalRules, SK=<Type>#<identifier>` |
| `store.Rule` (machine) | `PK=MachineRules#<id>` |
| `store.ExecutionEvent`, `store.FileAccessEvent` | not queryable in Rudolph yet: interface + fake only |

The DynamoDB implementation comes after Rudolph's storage changes land.
Until then, `cmd/lambda` serves 503 for data pages.

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
- Logout is `POST /auth/logout`. It is protected against cross-origin requests by
  `http.CrossOriginProtection`. It clears the cookie and redirects to Entra's
  `end_session_endpoint`.

Role changes take effect at the next sign-in, which is at most one session TTL later.

### Entra app registration

1. Register a single-tenant web application. Add the redirect URI
   `https://<host>/auth/callback`, and `http://localhost:8080/auth/callback` for
   local development.
2. Create a client secret.
3. Under **App roles**, add `Rudolph.Viewer` and `Rudolph.Admin` for Users/Groups.
   Assign them in Enterprise applications → Users and groups. You can require
   assignment so that unassigned users cannot sign in at all.

## Configuration

| Variable | Required | Description |
|---|---|---|
| `RUDOLPH_WEB_TENANT_ID` | yes | Entra tenant ID (GUID) |
| `RUDOLPH_WEB_CLIENT_ID` | yes | Application (client) ID |
| `RUDOLPH_WEB_CLIENT_SECRET` | yes | Client secret (planned move to SSM Parameter Store) |
| `RUDOLPH_WEB_REDIRECT_URL` | yes | `https://<host>/auth/callback` (`http://localhost…` allowed for dev) |
| `RUDOLPH_WEB_COOKIE_KEY` | yes | 32 random bytes, base64 (std or url-safe): `openssl rand -base64 32` |
| `RUDOLPH_WEB_POST_LOGOUT_REDIRECT_URL` | no | Where Entra sends the browser after logout |
| `RUDOLPH_WEB_SESSION_TTL` | no | Go duration, 5m–12h, default `1h` |
| `RUDOLPH_WEB_ADDR` | no | Listen address for `cmd/local`, default `127.0.0.1:8080` |

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

## Building for Lambda

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -o bootstrap ./cmd/lambda
zip rudolph-web.zip bootstrap   # runtime: provided.al2023, architecture: arm64
```

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
