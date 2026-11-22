# rudolph-web

A lightweight, read-mostly web view over [Rudolph](https://github.com/airbnb/rudolph),
the serverless sync server for North Pole Security's
[Santa](https://northpole.dev) binary authorization agent on macOS.

Admins use it to see:

- **Machines**: a paginated list you can filter by serial number or hostname prefix,
  plus a detail page with last sync, client mode, rule counts and machine-specific rules.
  The detail page names the model after its identifier (`Mac16,12 - MacBook Air (13-inch, M4, 2025)`) from a table copied by hand from Apple's support pages (`internal/macmodels`); an identifier the table does not know, and the list, show the identifier alone.
  Both mark a Santa version below 2026.8 as "legacy client" (Rudolph supports 2025.12 only for macOS 13 Macs, with limits); an empty or unparseable version is not marked.
  Both also mark a Mac "update needed" when its Santa version is below the minimum, `RUDOLPH_WEB_MIN_SANTA_VERSION` (default 2026.9, which fixed ways for a local user to pause enforcement), and the detail page names the minimum; versions compare part by part as numbers, so 2026.10 is above 2026.9.
  Its "Configuration" table shows each setting Rudolph sends the Mac: its own value, marked "(this Mac)", or the global one, marked "(global config)", as `rudolph config get --machine` works it out; URLs show only their scheme, host and path, since the rest may hold a token.
  Its "Sync health" section lists signs that a Mac is not syncing as it should: not seen for more than a day, a last sync without postflight 30 minutes after its preflight, a pending clean sync request, and a temporary client mode (running, ended, or ignored because it ends more than 30 days ahead).
  It also says how many unknown binaries the Mac ran in the last 30 and 7 days ("Unknown binaries: 12 in the last 30 days, 3 in the last 7", or "1,000+" for a Mac with more than the page reads), counted when the page is shown, and links to `/machines/<id>/unknown`, the list of them with the day the Mac last uploaded an execution of each. If the figure cannot be read the page says so and shows the rest.
- **Global rules**: a paginated list you can filter by rule type and policy.
- **Device groups**: the registered groups (description, whether reported membership counts, client mode, file access override) and the reserved `rudolph:ungrouped`, shown as "Macs in no group", each with a paginated page of its rules. A registered group's page links to its members page (`/groups/<name>/members`), a paginated table of its Macs in machine ID order: hostname (or machine ID for a Mac that has not synced yet) linking to the Mac's page, serial, model, whether the Mac is assigned the group, reports it or both, and when it was last seen. A group that counts only assigned Macs lists only those, and says so. The ungrouped group has no members page, as Rudolph keeps no list of the Macs in no group. A Mac's page shows its assigned groups, its reported groups that count and the groups Santa sent, or "Ungrouped"; its Configuration table marks a client mode that comes from one of its groups (the strictest of them, or the ungrouped group's for a Mac in none, when the Mac has no mode of its own) with `(group <name>)`, and an override file access action that comes from one of its groups (the most relaxed of them, DISABLE then AUDIT_ONLY then NONE, the group whose name sorts first between equal ones, when the Mac has no override of its own) the same way. A group's page offers the `rudolph config update --group` commands that set or remove its file access override, and each of its rules that can be collected the `rudolph rule update --group` command that turns collection on or off.
- **Execution events**: a paginated list you can filter by machine, decision and team ID. Events Santa marked identity_unverified (Santa 2026.9: it could not confirm the file it evaluated was the image that ran) show "identity not verified", since their hash and file details are not verified.
- **Top blocked and top allowed binaries**: the binaries blocked or allowed most often in the last 1, 7, 30 or 90 days, as computed by Rudolph's ranker, with the time the list was generated and a warning when that is longer ago than a configurable threshold (default 3 hours).
- **File access events**: `/events/file-access` lists Santa's file access authorisation events, newest first, with the time, the Mac (linked to its page), the rule name, the path accessed, the decision (`DENIED`, `DENIED_INVALID_SIGNATURE` or `AUDIT_ONLY`), and the process: its path, signing ID and team ID, and its parents' paths. Filter by machine or decision, one at a time; on a machine's list the Mac is shown by hostname. The events page links to it, and a Mac's page links to its own. Santa uploads these events whenever a sync server is configured, but the file access policy itself comes from the Macs' configuration profile (`FileAccessPolicy`), not from Rudolph. They need Rudolph 85eee68 or later.
- **Fleet health**: `/fleet` shows what Rudolph's fleet job found on its last daily run: how many Macs it walked, how many need attention, and one row per health list with its count, each linking to the list (`/fleet/health/stale`, `incomplete`, `mode`, `rules`, `sip` and `version`): Macs that have stopped syncing, that start syncs without finishing them, that are not in the client mode Rudolph intends, whose rules did not apply, that have SIP reduced, or that run a Santa below the job's minimum. A list is a paginated table of its Macs, each linking to the Mac's page, with the detail of its category. The page shows when the snapshot was generated, warns when that is longer ago than a configurable threshold (default 26 hours), names the stale threshold the run used and whether it was a holiday's, and warns when the job's minimum Santa version differs from this view's. It also shows the stale threshold stored now, which the job uses from its next run. See Rudolph's `docs/fleet-health.md` for what each list means.
- **Lockdown readiness**: `/fleet/readiness` shows, for the fleet and each device group, how many Macs are Ready (no unknown binary of the last 30 days is still pending, that is, not covered by a global rule), have Few (1 to 9) or Many pending, are Unmeasured (not syncing, not uploading unknown executions, reporting no rules, or seen for fewer than 30 days) or are already Locked. Each count links to its Macs (`/fleet/readiness/macs?group=<group>&bucket=<ready|few|many|unmeasured|locked>`), fewest pending first, with the 30-day and 7-day counts, both client modes and why a Mac is unmeasured. The figures are as old as the job's last run, and the buckets fill 30 days after the Rudolph that counts unknown binaries was deployed.
- **Top unknown binaries**: `/events/top-unknown` lists the binaries no rule decided (Santa's `BLOCK_UNKNOWN` in lockdown mode and `ALLOW_UNKNOWN` in monitor mode), the ones to decide on next, with a decision filter (all, blocked or allowed) and the same windows. Machines counts the Macs that uploaded the binary in the window. The first 100 rows put binaries without a global rule first, then the most Macs; a row that a global rule now covers says so and names the rule. Admins with editing on get an "Add a rule" link on the other rows, to a page of pre-filled global add-rule forms (signing ID, team ID and binary, allowlist by default). The lists need a Rudolph whose ranker writes them, and fill only from the day that Rudolph is deployed.

- **Editing (v2)**: editing is on unless you set `enable_editing` to `false`, and only people with the `Rudolph.Admin` role see the forms. They add and remove global rules on the rules page, rules for one Mac and its temporary client mode (a mode and a duration from 1m to 30d, or end it now) on that Mac's page, and group rules on each group's page; on all three the rules table comes first, each rule's buttons sit in a closed **Change** fold in its row, and the folded add-rule form follows the table. On a Mac's page they can also move its rules to another Mac (found by serial number or machine ID) when its user changes computers: the rules in force are copied as stored and, unless unticked, removed from the old Mac. On allowing global, machine and group rules (the ungrouped group's included) they turn collection of allowed executions on or off, as `rudolph rule update --collect-allowed-events` does; every rule list marks a collecting rule, and the rules page and each group's page warn admins when the global configuration does not turn on `EnableAllEventUpload`, without which Macs upload no allowed executions. They also manage device groups as `rudolph group` does: create a group on the groups page, change its description and whether reported membership counts or delete it (with its rules, if ticked) on its page, set or remove a group's client mode and its file access override (the ungrouped group's included) with the folded **Change settings** form on its page, as `rudolph config update --group` and `--ungrouped` do (changing both at once is one write and one audit entry, as with the CLI), and assign or remove a Mac's groups on that Mac's page, where each name Santa reports that is not a group yet has a "Make this a group" button. They change configuration as `rudolph config update` does: on a Mac's page the read-only Configuration table is followed by a folded **Change settings** form for the Mac's own settings, with an inherit box to make it follow the global configuration again for a setting, and buttons to request a clean sync or cancel the request; on `/admin/config` the same folded form, below the settings table, changes the global configuration. On `/fleet` they set and end the fleet job's holiday mode as `rudolph fleet holiday` does: a higher stale threshold (1 to 365 days, at least the normal one) until a UTC date at most 366 days ahead, after which the normal threshold applies again by itself; the job uses it from its next run. On a list of Ready Macs (`/fleet/readiness/macs?bucket=ready`, the fleet's or a group's) they tick Macs and add them to a group that has a LOCKDOWN or STANDALONE client mode, at most 50 at a time: each Mac is assigned the group as `rudolph group add` does, after the store has checked that the list's snapshot is the job's latest run (not older than 26 hours, else 409 Conflict) and that every ticked Mac is Ready in it. A group's page shows the group's readiness beside the confirmation of its client mode form ("12 of 120 Macs still have pending unknown binaries, 3 are unmeasured"). Path regexes are saved when Go's RE2 or an engine close to Santa's ICU (`github.com/dlclark/regexp2`, which knows lookahead, lookbehind, backreferences and atomic groups) compiles them; otherwise the form refuses them, since Santa ignores a regex it cannot compile, unless **Save path regexes unchecked** is ticked. Only the settings changed on the form are saved, so a form loaded before someone else changed another setting does not put that one back; a setting changed on the form that someone else has changed since it was loaded is refused with 409 Conflict, asking for a reload, and nothing is saved. A client mode other than MONITOR is saved only when its name is typed to confirm it, since on the global form it moves every Mac without its own, and so is inheriting the global client mode on a Mac's page when the global mode is not MONITOR. Every change is made with Rudolph's own Go packages, so validation and the rows written match the CLI exactly, and is recorded in Rudolph's audit trail with the signed-in Entra user (`web:<username> (<object ID>)`). `/admin/audit` lists the last 30 days of changes by the CLI and the web view, newest first, a page at a time; admins see an "Audit" link to it in the navigation, after Admin and Settings.

- **CEL rules**: every add-rule form (rules page, a Mac's page, a group's page and `/admin/rules/new`) offers the CEL policy with a **CEL expression** box, used only with that policy. The expression is trimmed and checked as Santa 2026.8 compiles it before anything is written (`internal/santacel`, built on `cel.dev/cel-go`). Rudolph syncs with Santa over sync protocol v1, so Santa evaluates its CEL rules with CEL v1: the variables `target` (`signing_id`, `team_id`, `is_platform_binary`, `signing_time`, `secure_signing_time`), `args`, `envs`, `euid`, `cwd` and `path`, the policies ALLOWLIST, ALLOWLIST_COMPILER, BLOCKLIST, SILENT_BLOCKLIST, SILENT_GUI_BLOCKLIST and SILENT_TTY_BLOCKLIST, CEL's standard library and the strings extension functions cel-cpp 0.15.0 has. The expression must return true (allow), false (block) or one of those policies. An expression using what only Workshop (North Pole Security's commercial sync server) has through CEL v2 (`ancestors`, `fds`, `target.entitlements`, `REQUIRE_TOUCHID`, `REQUIRE_TOUCHID_ONLY`, `SEATBELT`, `AUDIT`, the `FD_TYPE_*` values, `today()`, `now()`, `days()`, `weekdays()`, `policy_for_range()`, `kill_on_expiry()` and the TouchID cooldown functions) is refused with a message saying so, as is any other expression Santa could not compile, since Santa would skip the rule. `/rules/cel`, open to viewers, explains the variables, what needs Workshop and caching, lists examples (among them blocking profile removal by `/usr/bin/profiles`, with its limits), and has a GET form that tries an expression on a sample execution and shows the policy Santa would apply and whether the result is cached. The sample execution (arguments, environment, path, working directory, signing ID and team ID together) may be at most 8 KiB, and a longer one is a 400: a regex match is charged for only after it has run, so the evaluation's cost limit does not bound matching a long pattern against a long value. For the same reason a call that builds a string, such as `replace`, `join` or `format`, is charged only once it has built it, so `path.replace('a', path)` on an 8 KiB path would allocate 64 MiB before the limit stopped it. The try form therefore bounds what each evaluation actually builds, worked out from the call's own arguments just before it runs (`internal/santacel/try.go`), and an expression whose evaluation builds little runs and gives its real answer however long the sample is. A try is stopped with one of these messages: "the expression builds a string longer than the 1 MiB the try form allows" (concatenation, `replace`, `join` and `format`, each sized before it is built); "the expression builds more than the 4 MiB of strings the try form allows in all" (every string built, the parts of each `split`, the rune copies `charAt`, `indexOf`, `lastIndexOf`, `lowerAscii`, `upperAscii`, `reverse` and `substring` make, the keys and values of maps `format` writes again at each level they are nested, the errors of failed `int`, `uint`, `double`, `timestamp` and `duration` conversions of a string, which quote it, about 8 KiB for each call such as `getHours('Australia/Sydney')` with a named time zone, which cel-go loads afresh every time, plus 10 times the name's length, which loading copies into each path it tries and its error, and 5 times the length of a zone with a colon whose sides are not both short, whose offset errors copy and quote it, and compiled regex programs, added up over the evaluation); "a format string has more than 10,000 clauses, more than the try form formats"; "a regular expression is longer than the 16 KiB the try form compiles"; "a regular expression is too large for the try form: it compiles to more than 2,000 instructions" (counted on the parsed pattern, so `a{1000}` repeated is refused before it is compiled); "matching the expression's regular expressions would take longer than the try form allows" (each match counted as the pattern's instructions times the value's length, at most 10 million in all); "the expression took longer than the 1 second the try form allows" (a deadline checked every 100 iterations of the evaluation's loops, for calls cel-go's cost limit charges as one however long they take); before parsing, "the expression has more than 128 negative numbers, more than the try form parses", since cel-go's parser allocates about 40 KiB for each `-1` it reads (counted with cel-go's own lexer, so comments and quotes are read as the parser reads them, and a minus before a number counts even where it subtracts); and, after parsing but before checking, "the expression uses an undeclared name longer than 64 bytes, more than the try form checks" (struct type names and the whole of a qualified function name count as names; a leading dot, naming the root namespace, is ignored) and "the expression uses undeclared names more than 32 times, more than the try form checks", since cel-go's checker compares each undeclared name with every declared one to suggest a fix, and "the expression nests lists and maps more than 16 deep, more than the try form checks", since checking nested lists and maps costs about the fourth power of the depth (64 deep, 12 MiB). These last three refuse only expressions that would fail the check anyway or that no rule needs, and saving a rule checks as before. A pattern is compiled once per try however often it is matched. The evaluation's cost limit of 1,000,000 still applies on top, and the expression is parsed and checked once per try, the same check giving the cached answer. Two caveats: cel-go's standard library was not compared with cel-cpp's overload by overload (only the strings extension's extra `strings.quote` is refused), so a rare expression may pass here and still fail on a Mac; and "cached" is decided from the expression alone (it reads nothing but `target`), while Santa decides on each run from what the evaluation actually read, so it may cache an execution where a branch reading `args` or the like did not run.

- **Audit trail**: `/admin/audit` is a read-only admin page whether or not editing is on, so admins can see which person (`web:…`) or which user and Mac running the Rudolph CLI (`cli:<user>@<host>`) made each change. It reads the trail with the same `dynamodb:Query` the other pages use, so it needs no write permission.

- **Settings**: `/admin/config` is an admin page, whether or not editing is on, listing every global setting Rudolph sends (client mode, path regexes, batch size, bundles, transitive rules, full sync interval, upload logs URL, event upload, file access override, event detail URL and text), or "not set" (with what Rudolph sends instead where Rudolph decides it, such as a batch size of 50); without a global config it says Rudolph's defaults apply. With editing on, admins also get the form that changes them, folded under **Change settings** below the table (see Editing). USB settings are left out for now, as Santa 2026.8 replaces them with a removable media policy Rudolph does not model yet. Admins see a "Settings" link to it in the navigation.

With `enable_editing` set to `false` the view is read-only: no forms, and the write routes (the POSTs under `/admin/`, and the pre-filled add-rule page `/admin/rules/new`) do not exist; `/admin/audit` and `/admin/config` stay. `/admin` and everything under it need the `Rudolph.Admin` role.

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
- **Numbered pager without a count.** Lists show "Prev 1 2 [3] 4 Next". With cursors there is no total, so the pager numbers the pages reached so far and the next one. Each link carries its page's cursor (`cursor`), its number (`page`) and the cursors of up to four earlier pages (`back`, joined with "."); older pages collapse to "…" after page 1, which keeps URLs short. A cursor in `back` may be 2048 bytes, and a link is kept under 7680 bytes by leaving out the oldest of those pages first, so that the longest cursors (an event stored with an over-long `file_sha256` in its sort key, before Rudolph refused them) cannot make a link longer than the 8192 bytes CloudFront allows a URL. A malformed `page` or `back` is a 400, and a cursor in `back` is verified like any other when its link is followed.
- **Authenticated cursors.** A cursor carries the position to resume from with a
  fingerprint of the query that produced it, authenticated with HMAC-SHA256
  (`store.CursorCodec`). A cursor cannot be minted by hand, edited to skip ahead or
  to point at another partition, or replayed against a different query or filter;
  anything that fails verification is `store.ErrInvalidCursor`, which the web layer
  renders as 400. The fake signs with a per-process random key. A cursor is the truncated MAC, a format byte and the position's own bytes in base64url; the query is signed, not carried, and nothing is escaped, so a cursor is a third longer than its position whatever bytes that holds. The DynamoDB implementation carries the `LastEvaluatedKey` as the position (each attribute's name and value in name order, each behind its length), and `cmd/lambda` derives its key with `store.DeriveCursorKey` (HKDF-SHA256 over the session cookie key, with a cursor-specific info string), so every instance signs alike and the cursor key stays independent of the cookie key.
- **Server-rendered.** It uses `net/http` and `html/template`, with contextual auto-escaping on all data and no `template.HTML` on data. Text from Macs can also carry control, bidi and zero-width characters that HTML escaping leaves alone and that hide or reorder what a value says (a file named `invoice`, a right-to-left override, then `fdp.exe` reads as `invoiceexe.pdf`), so every value a page prints is shown with those characters as visible Go-style escapes (`\x1b`, `\n`, `\u202e`), exactly as Rudolph's CLI shows them: controls, invisible format characters, line and paragraph separators, every other default-ignorable code point (variation selectors, which can smuggle hidden bytes after a visible letter, the combining grapheme joiner, Khmer inherent vowels and reserved code points), and characters that render as blank (the Hangul fillers and the blank braille pattern). Zero-width joiners and non-joiners, the emoji presentation selector U+FE0F, the tags of a subdivision flag (the black flag, 2 to 7 tag letters or digits, then the cancel tag), and all other text, accents, emoji and CJK included, are shown as they are; tag characters anywhere else are escaped. This is done centrally: when the templates are parsed, every printing action is rewritten to end in the `show` function (`internal/web/display.go`, escaping by `internal/textsafe`), so a new template is covered without anyone remembering to. Numbers and booleans pass through unchanged, so `html/template` writes them exactly as before, in a script too, unless their type has its own `MarshalJSON` or `MarshalText`, whose output can hold anything; a type with its own `String` or `Error` method is escaped like text. Values that `html/template` percent-encodes keep their exact bytes, so invisible characters show there as `%E2%80%AE` and links still find what they name: URL-valued attributes (`href`, `src`, `data-href`, `xlink:href`, unquoted ones, a style's `url()` and the like) and `urlquery`. Which values those are is not guessed from the template text: the rewrite runs `html/template`'s own escaper over a throwaway copy of each page's templates, with every `{{template}}` call replaced by the called template's body, and reads the escapers it chooses for each action; a template called both inside a URL and outside one is refused when the templates are parsed. `html` and `js` written as calls (`{{html .X}}`) get `show` round their arguments; `urlquery` written as a call (`{{urlquery .X}}`) gets `asURL` round them instead, as it percent-encodes, so it keeps the exact bytes. Form values and textareas that are posted back opt out with `verbatim`, and tests check that every printed value goes through one of these functions and that `verbatim` is used only in posted form values. Their bytes are kept, and each page is written with every such character outside its scripts and style sheets as an HTML character reference (`&#x202e;`), which the browser reads back as the same character, so forms look and post exactly as before. Scripts and style sheets are found by following tag boundaries as a browser does, so `<script` inside an attribute value, a comment or a textarea is not taken for one; a script or style that is never closed is not left alone, so it cannot hide the rest of the page from the pass. The one exception is the C1 controls U+0080 to U+009F other than U+0081, U+008D, U+008F, U+0090 and U+009D: HTML reads a reference to one of them as a Windows-1252 character (`&#x85;` is an ellipsis), so in a posted form value they reach the browser raw, to post back their exact bytes; wherever such a value is displayed it shows them escaped (`\u0085`). Slices, maps, structs, errors and values with their own `MarshalJSON` (such as `time.Time` and `json.RawMessage`) or `MarshalText` are written into a script as `html/template` writes them (JSON: a `json.Marshaler`'s own JSON, never its `String` text, and an error struct's exported fields, not its `Error` text), except that a JSON string holding such a character raw is that string escaped, so no raw one reaches a script; outside a script they are printed as `html/template` prints them, with such characters escaped. Multi-line CEL expressions keep their line breaks. A copy-and-paste CLI command is never offered for a value with such characters, since the copied command would not name it, and the audit trail links a machine target only when it is a machine UUID. The server renders every time in UTC inside a `<time datetime>` element. Each page carries one small inline script (`internal/web/localtime.js`), the only script it runs: it rewrites each time's text to show it in the viewer's own time zone first, then UTC in brackets, e.g. `2026-09-17 20:55 JST (2026-09-17 11:55:00 UTC)`, and does nothing else: it reads each time's `datetime` attribute and writes text, and loads nothing, stores nothing and builds no markup. The CSP's `script-src` allows that script by its SHA-256 hash and nothing else. If it does not run, the pages still work and show UTC only. Dates that are UTC days, such as the machine list's last seen, stay plain UTC dates.
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
| `internal/macmodels` | Apple's names for Mac model identifiers, from an embedded table refreshed by hand |
| `internal/store` | Read models and data-access interfaces |
| `internal/store/fake` | In-memory implementation that records calls (tests + local dev); also writes, so `cmd/local` has editing on |
| `internal/store/dynamo` | DynamoDB reader, and the writer over Rudolph's own packages (`github.com/airbnb/rudolph/pkg/...`) |
| `internal/store/rudolphrules` | Checks edits with Rudolph's validation, shared by the writer and the fake |
| `internal/santacel` | Checks and evaluates CEL rule expressions as Santa's CEL v1 does (cel-go, with the `santa.cel.v1` types built in Go), and the examples on `/rules/cel` |
| `internal/lambdaadapter` | Payload format 2.0 event ⇄ `http.Handler` |
| `internal/testutil/fakeidp` | Test-only fake Entra-like OIDC provider |
| `deployments` | Terraform module, example environment and static checks on both |

### Data layer

The read models follow Rudolph's single-table DynamoDB layout (`PK`/`SK`):

| Read model | Rudolph item |
|---|---|
| `store.Machine` | `PK=Machine#<id>, SK=Current` (sensor data; adds Hostname and ModelIdentifier when Rudolph stores them, and SIPStatus, where a missing attribute means enabled) |
| `store.SyncState` | `PK=Machine#<id>, SK=SyncState` (PreflightAt, PostflightAt; adds LastSyncType, RulesProcessed) |
| `store.MachineDetail` config fields | `PK=Machine#<id>, SK=Config` (the settings as `store.Config`, TemporaryClientMode with TemporaryClientModeExpiresAt, CleanSync; mistyped values are left out) |
| `store.Rule` (global) | `PK=GlobalRules, SK=<Type>#<identifier>` |
| `store.Rule` (machine) | `PK=MachineRules#<id>` |
| `store.MachineDetail` group fields | `PK=Machine#<id>, SK=Groups` (Assigned, Reported: the reported groups that count) and `SK=ReportedGroups` (PrimaryUserGroups as Santa sent them); mistyped values are left out |
| `store.Config` (`GlobalConfig`), global client mode (`GlobalClientMode`) | `PK=GlobalConfig, SK=Config` (the settings `rudolph config update` sets, by Rudolph's attribute names, without USB settings; mistyped values are left out; no row means MONITOR, as Rudolph sends) |
| `store.Group` | `PK=GroupRegistry, SK=Current` (Groups map: name to Description, AcceptReported, ClientMode; a mode Rudolph cannot decode is shown as none) |
| `store.Rule` (group) | `PK=GroupRules#<group>` |
| `store.GroupMember` | `PK=GroupMembers#<group>, SK=<machine id>` (Assigned, Reported: BOOL true or absent), with the Mac's `SK=Current` sensor data |
| `store.ExecutionEvent` | events table: `PK=Exec#<id>, SK=<TS>#<sha256>#<pid>`, indexes `Fleet_TS`, `Decision_TS`, `TeamID_TS` |
| `store.TopBinary` | events table: pointer `PK=Top#<kind>#<days>, SK=Current` (Snapshot, GeneratedAt), kind `Blocked`, `Allowed`, `Unknown`, `UnknownBlocked` or `UnknownAllowed`; rows `PK=TopSnapshot#<kind>#<days>#<snapshot>, SK=<rank>` (with `GlobalRule` on the first 100 rows of the unknown lists) written by Rudolph's ranker |
| `store.FileAccessEvent` | events table: `PK=FileAccess#<id>, SK=<TS>#<pid>#<hash>`, indexes `Fleet_TS` (`FileAccess`) and `Decision_TS` (Santa's `FILE_ACCESS_DECISION_` names) |
| `store.FleetSummary` | events table: `PK=FleetPointer, SK=Current` (Snapshot, GeneratedAt and every count), written by Rudolph's fleet job |
| `store.FleetHealthRow` | events table: `PK=FleetSnapshot#<snapshot>#Health#<category>`, in sort key order |
| `store.ReadinessRow` | events table: `PK=FleetSnapshot#<snapshot>#Ready#All` or `#Ready#Group#<group>, SK=<bucket>#<pending, 6 digits>#<machine id>` |
| `store.UnknownCounts`, `store.UnknownBinary` | events table: `PK=UnknownSeen#<id>, SK=<file SHA-256>` (LastDay, FileName, SigningID, TeamID), written by eventupload |
| `store.FleetHealthConfig` | `PK=FleetHealth, SK=Config` (StaleDays, HolidayStaleDays, HolidayUntil; 10 days when StaleDays is absent) |

`internal/store/dynamo` reads the machine list (all, or by serial or hostname prefix), machine detail, machine rules, group rules and global rules (all, or by rule type, policy or both) from Rudolph's table, each page with one bounded Query; the group list is one GetItem of the registry. A group's members page is one Query of its `GroupMembers#<group>` partition and one BatchGetItem of that page's Macs' sensor data. The machine page also reads the global config (one GetItem) for the settings a Mac does not set itself, and the group registry (one GetItem) for the client mode its groups give it; if either read fails, the page leaves that source out and says it could not be read. `/admin/config` makes the same one GetItem. The machine list reads Rudolph's `DataType_SerialKey` and `DataType_HostnameKey` indexes and the policy filter reads `GlobalRulePolicy_SK`, so they need a Rudolph version that has them; its last seen is a UTC date (`LastSeenDay`), while the machine detail page shows the exact time. `cmd/lambda` uses it when `RUDOLPH_WEB_DYNAMODB_TABLE` is set, and serves 503 for every data page otherwise. Execution events come from Rudolph's separate events table, named by `RUDOLPH_WEB_EVENTS_TABLE`, newest first: all of them (`Fleet_TS`), one Mac's (its `Exec#<id>` partition), one decision's (`Decision_TS`) or one team ID's (`TeamID_TS`), one bounded Query per page on a Mac's partition. An index query keeps only its own list's partitions (a FilterExpression on `PK`), since older Rudolph versions could file a Mac's file access rows under an execution decision: those rows are never shown. As the filter applies after DynamoDB's Limit, a page the filter left short is refilled from older rows, querying again from the last key read for only the rows still missing, up to five Queries per page, so a request reads at most five pages' worth of rows and returns at most one page. Only a run of the other list's rows longer than that gives a short or empty page with a Next link, which goes on to the older rows. The cursor, which may stand on such a row, still resumes the list and stays bound to it. Filters are used one at a time; a combination answers 400. A text filter (serial number, hostname, machine or team ID) may be 256 bytes, upper- and lower-cased as well, since it is looked up in one case or the other, and one that is not valid UTF-8 answers 400 ("Filter value is not valid text."), as no such value is; otherwise case mapping would write each invalid byte as a three-byte U+FFFD and the filter would reach the query, its cursor and the page links three times as long. Decisions the view does not know, such as `ALLOW_PLATFORM`, are shown as stored. Rudolph writes only canonical UUIDs (8-4-4-4-12 hexadecimal digits, either case) as machine IDs, and the view builds links and forms from them, so a machine, group member, execution or file access row with any other machine ID, in either its `MachineID` attribute or its partition key (both are checked when the row has them, and must then be exactly the same ID, case included), is logged and skipped in lists, and a machine page for one answers 404. An event row the reader cannot decode is logged and skipped, so its page may be short, rather than failing the page and hiding every older event until the row expires. The top blocked and top allowed lists (`/events/top-blocked` and `/events/top-allowed`, `days` 1, 7, 30 or 90, default 7) come from the same table: Rudolph's ranker writes each list as a snapshot and moves a pointer to it, and a page reads the pointer (one GetItem) and one page of the snapshot (one Query). The cursor pins the snapshot, so later pages stay consistent when the ranker runs meanwhile, and is bound to the kind and window. A list whose pointer does not exist yet shows "This list has not been computed yet". Machine counts exist for the first 100 rows of each list only and show as "—" further down. `/events/top-unknown` (`decision` all, blocked or allowed, default all) reads Rudolph's `Unknown`, `UnknownBlocked` or `UnknownAllowed` list the same way, keeping the ranker's order, and shows each row's `GlobalRule` (the sort key of a global rule added since, such as `SigningID#…`) as that rule's type and identifier. `GET /admin/rules/new?signing_id=…&team_id=…&sha256=…` (admins, editing on) checks each identifier with Rudolph's `rules.ValidIdentifier`, leaves out the invalid ones, and answers 400 when none is left; its forms post to `/admin/rules` like the rules page's. File access events (`/events/file-access`) come from the same table in the same way: all of them (`Fleet_TS` under `FileAccess`), one Mac's (its `FileAccess#<id>` partition) or one decision's (`Decision_TS`, where Rudolph keeps Santa's `FILE_ACCESS_DECISION_` prefix, which the view drops), with the same filter and refill on the indexes, one filter at a time, undecodable rows logged and skipped. Their cursors are bound to the list, so a cursor from the executions list is refused there and the reverse. The first process of the chain carries its details; Rudolph stores only the paths of its parents. The fleet list shows machine IDs, as naming each Mac would cost a read per Mac; a machine's list names it from the read that finds its stored spelling. Without the events table the events, file access and top list pages answer 503.

The fleet pages read the snapshot Rudolph's fleet job writes to the events table once a day, as the top lists are read. `/fleet` and `/fleet/readiness` read the pointer (one GetItem), which holds every count; `/fleet` also reads the stored stale threshold from the store table (one GetItem), and shows the rest without it when that read fails. A health or readiness list reads the pointer and one page of the snapshot it names (one Query, Limit = page size); later pages read only the snapshot their cursor pins, so a listing stays on one snapshot when the job runs meanwhile, and the cursor is bound to the list (the category, or the group and bucket). A readiness bucket is a `begins_with` range on the sort key, never a filter, so pages are full. Snapshot rows expire 3 days after the run: a page link into an expired snapshot answers 400, and a pointer that does not exist yet shows "The fleet lists have not been computed yet". An unknown category or a group the snapshot does not name is a 404, an unknown bucket a 400. The machine page counts a Mac's unknown binaries with one Query of its `UnknownSeen#<id>` partition, at most 1,000 markers, counting the two windows by `LastDay` (the UTC day of the upload); `/machines/<id>/unknown` pages the same partition, one Query a page, without filtering by date. Rows whose machine ID is not a canonical UUID are logged and skipped, as elsewhere. Without the events table the fleet pages answer 503 and the machine page says the unknown figure could not be read.

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
  - Every 4xx a signed-in user (any role) can meet is the site's page with the navigation and Sign out: 400 for an invalid filter, page link or form; 403 for a missing role; "404 Not Found" for a path the site has no page for (such as a write route with editing off) or a missing machine; "405 Method Not Allowed", keeping its `Allow` header, for a known path used with the wrong method; and "403 Forbidden" when the cross-origin check refuses a POST from another site. Unknown `/static/` files are always a plain 404.
  - Without a valid session the site does not reveal which addresses exist: every path outside `/auth/` and `/static/`, apart from GET and HEAD of `/favicon.ico` (which browsers request on their own, so it answers with the icon for anyone), whether it is a real page, a route that exists only with editing on, an unknown address or a real address used with the wrong method, gets exactly the response a real page gives, the sign-in redirect for GET and HEAD (with the address as `return_to`, left out when it is longer than the login handler keeps, see below) or the same plain 401 for any other method. Signed in, the same holds for anyone without a role: every path and method outside `/auth/`, `/static/` and the `/favicon.ico` GET gets the one 403 page naming `Rudolph.Viewer`, whether or not the address exists and whether or not editing is on. A viewer likewise gets one 403 naming `Rudolph.Admin` for `/admin` and every address under `/admin/`, so they cannot tell which admin routes exist or whether editing is on. Only a viewer outside `/admin` and an admin anywhere get the real 404 or 405 for an unknown address or wrong method (an admin also sees the editing-off 404 for the write routes). The login handler keeps only same-site relative `return_to` values, so an address such as `//evil.example/x` cannot become an open redirect. A `return_to` so long (about 2.5 KB) that the sign-in cookie holding it would be larger than browsers keep is replaced with `/`: the person signs in and lands on the home page, where a dropped cookie would fail every sign-in from that link. The sign-in redirect itself carries `return_to` only when the login handler would keep it, and only while its `Location`, with `return_to` escaped for the query (`=`, `/` and `%` take three bytes each), is at most 4096 bytes; otherwise it sends `/auth/login` alone, so the person signs in and lands on the home page. A cross-site POST is still refused by the cross-origin check first, whatever the path, with a plain-text 403 when there is no session.
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
  `Cache-Control: no-store`. Only the embedded static files (the stylesheet and the icons) are cacheable, each under a URL carrying a hash of its content, so a new stylesheet or icon shows as soon as it is deployed. `/favicon.ico`, which browsers request on their own, answers with the PNG icon and `Cache-Control: no-cache`.

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
| `RUDOLPH_WEB_EDITING` | no | `true` lets `Rudolph.Admin` add, remove and move rules, collect their allowed executions, set temporary client modes, manage device groups, change a Mac's and the global configuration, request or cancel a clean sync, set the fleet's holiday mode and add ready Macs to a Lockdown group (needs `RUDOLPH_WEB_DYNAMODB_TABLE`, and the role's write grant from `enable_editing`); `false` or unset keeps the view read-only; anything else is an error |
| `RUDOLPH_WEB_TOP_LIST_STALE_AFTER` | no | Go duration, 1h–168h, default `3h`: how old a top list may be before its page warns that Rudolph's ranker may have stopped |
| `RUDOLPH_WEB_FLEET_STALE_AFTER` | no | Go duration, 1h–336h, default `26h`: how old the fleet job's snapshot may be before the fleet health and readiness pages warn that Rudolph's fleet job may have stopped. Terraform's `fleet_stale_after` sets it |
| `RUDOLPH_WEB_EVENTS_TABLE` | no | Rudolph's execution events table (`<prefix>_rudolph_events`), read by `cmd/lambda`; unset, the events and top list pages return 503. Needs `RUDOLPH_WEB_DYNAMODB_TABLE` |
| `RUDOLPH_WEB_MIN_SANTA_VERSION` | no | Santa version, of at least two numeric parts (`2026.9`, `2026.10.1`), below which the machine list and page mark a Mac "update needed"; unset or empty, `2026.9`; `off` turns the marker off; anything else is an error. Terraform's `min_santa_version` sets it |

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

### Upgrading: group file access override and group rule collection

This release shows and edits each device group's file access override and lets admins collect the allowed executions of group rules. Deploy Rudolph through its upgrade step 30 (fork commit `019bf42`) first, IAM before code as that step says. rudolph-web itself needs no Terraform or IAM change: run `make deploy` to ship the new build. See [docs/deploy.md](docs/deploy.md#upgrading-group-file-access-override-and-group-rule-collection).

### Upgrading: fleet health pages

This release adds the fleet health and Lockdown readiness pages, which read what Rudolph's fleet job writes. Deploy Rudolph's upgrade step 28 first, then add the `fleet_stale_after` Terraform variable (default `"26h"`) to an existing environment's `_variables.tf`, `main.tf` and `config.auto.tfvars.json`, and run `terraform apply`: with editing on, the role gains the `FleetHealth` partition for holiday mode. Until the job's first run the pages say the lists have not been computed yet, and readiness is accurate 30 days after Rudolph's deploy. See [docs/deploy.md](docs/deploy.md#upgrading-fleet-health-pages).

### Upgrading: minimum Santa version

This release adds the `min_santa_version` Terraform variable (default `"2026.9"`; `""` turns the marker off). An existing environment must add it to its `_variables.tf`, `main.tf` and `config.auto.tfvars.json` before `make plan` runs, as envcheck requires. It is a code and configuration change only, with no IAM change. See [docs/deploy.md](docs/deploy.md#upgrading-minimum-santa-version).

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
- Decoding of Rudolph's DynamoDB rows, against `testdata/webcontract/rows.json` and `fleet-rows.json` (the fleet job's rows), copies of Rudolph's web contract fixtures.

Integration tests (build tag `integration`) run the DynamoDB data layer against DynamoDB Local, with the tables created from `testdata/rudolph/dynamodb.tf` and `testdata/rudolph/events.tf`, copies of Rudolph's store and events table definitions. They need Java and DynamoDB Local: either set `DYNAMODB_LOCAL_DIR`, or keep a Rudolph checkout next to this repo (`../rudolph`) where `scripts/setup_test_deps.sh` has been run.

```sh
go test -tags integration ./...
```

The four copies (`rows.json`, `fleet-rows.json`, `dynamodb.tf` and `events.tf`) are checked against Rudolph's originals whenever a Rudolph checkout is found at `../rudolph` or `$RUDOLPH_DIR`. When Rudolph changes them, copy them over and look at what changed. `testdata/webcontract/cli-examples.txt` goes the other way: Rudolph runs every line of its copy as a dry run through its real CLI, and `internal/clicmd` checks that the commands the pages offer match it.
