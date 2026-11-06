# Data layer design

Status: proposed 2026-11-04; events scope decided 2026-11-05 (blocked and allowed binaries in v1, see "Decisions" below). Built in rudolph-web so far: steps 1 to 3, 6 and 7 of "Implementation order" (machine list, machine detail, machine rules, global rules all, by type and by policy) and the readers of step 9: execution events (by fleet, Mac, decision or team ID, with its IAM statement) and the top blocked and top allowed lists from Rudolph's ranker snapshots (R8). Prices are us-east-1 on-demand and should be re-checked: DynamoDB $0.625 per million write units, $0.125 per million read units, $0.25/GB-month; Firehose Direct PUT $0.029/GB (each record rounded up to 5 KB); S3 $0.023/GB-month; Athena $5/TB scanned (10 MB minimum per query); CloudWatch Logs $0.50/GB ingested.

## Cost yardstick

A Mac syncs at most every 600 s, so 144 syncs a day: 4.32 million syncs a month per 1,000 Macs. One extra write unit per sync costs about $2.70 a month at 1,000 Macs and $270 at 100,000. A steady-state sync uses 3 write units today (COST2 budget: 4). The design adds nothing to a steady-state sync.

## Findings in the current code (Rudolph)

- `pkg/model/sensordata/model.go` stores `Hostname` without `omitempty`, so a Mac without a hostname is stored with `""`. A GSI keyed on it would make preflight's PutItem fail (index keys cannot be empty strings). Must be fixed before any hostname index.
- Sensor data (about 500 B) is written on every preflight. New GSIs cost nothing per sync only if they project attributes that rarely change: never `Time` or `ExpiresAfter`.
- The existing `DataType_MachineID` index is KEYS_ONLY, so listing machines from it would need a BatchGetItem per page.
- Certificate rule sort keys start with `Cert#`, not `Certificate#` as the web contract README says.
- The default Firehose writes uncompressed JSON, one record per event, under `YYYY/MM/DD/HH/` (arrival time), with no S3 lifecycle rule.
- `ddblocal.LoadTableSchema` builds the test table from `dynamodb.tf`, so new GSIs reach the integration tests automatically.

## Access per page

| Page / method | Access |
|---|---|
| Machines, no filter | Query GSI `DataType_SerialKey`: `DataType = "SensorData"`, Limit n. Ordered by serial. |
| Machines, serial prefix | Same index, `begins_with(SerialKey, upper(trim(p)))` (case-insensitive; SerialNum stays as reported and is what the page shows, since some VMs report mixed-case serials) |
| Machines, hostname prefix | Query GSI `DataType_HostnameKey`: `begins_with(HostnameKey, lower(trim(p)))` (case-insensitive) |
| Machine list projection | INCLUDE MachineID, SerialNum, Hostname, ModelIdentifier, OSVersion, SantaVersion, ClientMode, PrimaryUser, `LastSeenDay`. About 4 read units per page of 100. |
| Machine detail | Query `PK = Machine#<id>`, Limit 5: config, current sensor data and sync state in one call, leaving out tracking internals. No sensor data row (90-day TTL) means not found. |
| Machine rules | Query `PK = MachineRules#<id>`; state (in force, removing, expired) computed per the contract |
| Global rules, all or by type | Query `PK = GlobalRules` [`begins_with(SK, "Binary#" / "Cert#" / "TeamID#" / "SigningID#" / "CDHash#")`] |
| Global rules by policy | Query GSI `GlobalRulePolicy_SK`, `GlobalRulePolicy = :n`, plus `begins_with(SK)` when a rule type is also given (never a FilterExpression: Limit applies before the filter, so pages would come back short). The cursor is the index key: PK, SK and GlobalRulePolicy. |
| Execution events (blocked and allowed) | New events table: per machine `PK = Exec#<m>` newest first; fleet-wide GSI `Fleet_TS`; by decision `Decision_TS`; by team ID `TeamID_TS`. Combined filters are refused in v1. |
| File access events | v2. Interface kept; the DynamoDB reader answers `ErrUnavailable` until then. |
| Top blocked binaries, top allowed binaries | GetItem pointer `Top#<Blocked or Allowed>#<window>` / `Current` (not cached: one small read per first page), then Query `TopSnapshot#<kind>#<window>#<snapshot>` with Limit = page size. The cursor carries the snapshot row key and the snapshot's GeneratedAt, so later pages skip the GetItem and stay on the snapshot of the first page; it is bound to kind and window, and a decoded key outside that list's snapshots or with a sort key other than six digits is refused. |
| Optional event search (ALLOW history) | Athena with parameterised SQL; cursor = HMAC(query execution ID, NextToken) bound to the filter |

Cursors: LastEvaluatedKey as canonical JSON inside `CursorCodec.Encode(fingerprint, json)`; the fingerprint covers method, index and normalised filter. A decoded key whose names, partition or index differ from the query's is refused, on top of the HMAC. For the machine list the key is the GSI's LastEvaluatedKey (PK, SK, DataType and the index range key); a prefix spelt with other case or spaces normalises to the same filter, so its cursors stay valid.

The machine list's last seen is `LastSeenDay` read as midnight UTC and shown as a date; a missing or unreadable value is shown as unknown rather than failing the page. A Mac with no serial is only found by hostname, and one with no hostname only by serial.

## Rudolph-side changes

| # | Change | On the sync path? | Cost |
|---|---|---|---|
| R1 | `Hostname` becomes `omitempty` | yes | 0. Required before any hostname GSI. |
| R2 | `HostnameKey = lower(trim(Hostname))` on sensor data, omitempty | yes, about 40 B | 0 while the item stays under 1 KB |
| R3 (optional) | `LastSeenDay` (UTC date) projected into both machine GSIs | yes | One index write per GSI per Mac per day: about $0.04/month at 1k Macs, $3.75 at 100k. Projecting the exact `Time` instead would add 2 write units per sync (about $5.40 at 1k, $540 at 100k). |
| R4 | `SerialKey = upper(trim(SerialNum))` on sensor data (omitempty), GSIs `DataType_SerialKey` and `DataType_HostnameKey` (INCLUDE projection above), one per apply | index only | 0 in steady state; an OS, Santa, hostname, mode or user change costs 1 to 2 write units per GSI |
| R5 | `GlobalRulePolicy` (number) on global rule rows only, GSI `GlobalRulePolicy_SK`, one-off backfill | no | Backfill of 80k rules about $0.10 once |
| R6 | New table `<prefix>_rudolph_events` (on-demand, TTL, KMS) with GSIs `Fleet_TS`, `Decision_TS`, `TeamID_TS`; read-only IAM for the web role | no | see below |
| R7 | Event writer in the eventupload Lambda for every uploaded execution event (blocked and allowed); file access, audit and bundle events skipped in v1 | no | An upload without execution events makes no DynamoDB calls |
| R7b | `CollectAllowedEvents` on rule rows, the `EventCollection` index item, CLI flags and warnings; the writer stores allowed-by-rule events only for flagged rules | no | 0 until a rule is flagged |
| R8 | Hourly ranker Lambda (EventBridge) building top-blocked and top-allowed snapshots for 1, 7, 30 and 90 day windows | no | About $0.10/month at 1k, $6 at 100k |
| R9 (optional) | Firehose: GZIP, `dt=/hour=` prefix, larger buffers, S3 lifecycle, one record per upload; Glue table with partition projection and an Athena workgroup with a scan limit | no | see below |

## Event store options

Assumptions: 1,000 events per Mac per day, about 1.5 KB each, blocks 0.5% of events.

| Option | 1k Macs/month | 100k Macs/month | Notes |
|---|---|---|---|
| A. Firehose to S3 (GZIP), Athena | about $3 to $6 | about $250 to $560 | Full history; 1 to 5 s per query; a time window is required |
| B. DynamoDB table of all events | about $75 | about $7,400 | Rejected |
| C. CloudWatch Logs Insights | about $22 | about $2,250 | No page tokens; fine for ad hoc use only |
| D. DynamoDB table of blocked and file access events, with counters and a ranker | about $0.90 | about $20 to $86 | Real time, one query per page; no allowed binaries |

Decided for v1 (owner, 2026-11-05): **blocked and allowed binaries**, stored as D extended to every uploaded execution event ("D+"). File access, audit and bundle events wait for v2.

What Santa uploads decides the cost, not how often binaries run. By default (sync_v1.proto, `enable_all_event_upload` and `disable_unknown_event_upload`) Santa uploads only executions that were blocked or would have been blocked in lockdown: every BLOCK_* decision, plus ALLOW_UNKNOWN on Macs in monitor mode. Executions allowed by a rule are uploaded only when `EnableAllEventUpload` is set, and even then only decisions Santa did not answer from its cache. So by default, "allowed binaries" means the unknown binaries monitor-mode Macs ran, which is the list an admin works through before moving to lockdown.

D+ costs, assuming about 1 KB per stored event (one write unit; the item keeps only the fields the pages show) and GSIs projecting a few small attributes:

| Upload setting | Events per Mac per day (assumed) | Write units per event | 1k Macs/month | 100k Macs/month |
|---|---|---|---|---|
| Default (blocks and ALLOW_UNKNOWN), monitor mode | about 50 | about 6 (item, 3 GSIs, day counter, machine marker) | about $6 plus $2 storage | about $560 plus $200 |
| Default, lockdown mode | about 5 | about 6 | under $1 | about $60 |
| `EnableAllEventUpload` on | about 500 | about 6 | about $60 plus $20 | about $5,600 plus $2,000 |

The per-Mac rate is a guess to be replaced by the real figure from the smoke test (count `events` per upload in the Lambda logs). The config CLI's help for `--enable-all-event-upload` should warn that it multiplies event storage cost about tenfold.

D+ in detail: the event item is keyed `PK = Exec#<m>`, `SK = <20-digit unix µs>#<sha256>#<pid>`, written with `attribute_not_exists(PK)` so Santa's retries are harmless. Only a new event bumps the per-day counter (`BinaryDay#<UTC day>` / `<sha>`, with separate `Blocked` and `Allowed` counts) and the per-binary machine marker (`BinarySeen#<sha>` / `<m>`), both with a 100-day TTL; events of one upload for the same binary are combined into one counter update. Event items expire after 90 days. A failed write answers 5xx like the other sinks.

### Collecting allowed events per rule (owner request, 2026-11-05)

Use case: Google's team ID is allowed fleet-wide; to find the signing IDs of Gemini (to block them with a signing ID rule, which outranks the team ID allow), the owner wants to see what the Google team ID allows for a while, then stop, without paying to store every allowed execution all the time.

Santa has no per-rule upload switch: the Rule message (sync_v1.proto) has no such field, and `enable_all_event_upload` applies to the whole Mac. So the choice is made by Rudolph when it stores events:

- Rule rows get an optional `CollectAllowedEvents` (bool, omitempty). The CLI sets and clears it (`rudolph rule update ... --collect-allowed-events=true|false`, and `rule list` shows it). Rudolph already records rule changes in the feed, but this flag is not sent to Santa, so setting it must not touch the rules feed or `rules_hash`.
- The event writer stores an allowed-by-rule event (ALLOW_BINARY, ALLOW_CERTIFICATE, ALLOW_TEAMID, ALLOW_SIGNINGID, ALLOW_CDHASH) only if the rule that matched has the flag set. The rule is found from the decision and the event's own fields: `team_id`, `signing_id`, `cdhash`, `file_sha256`, or the leaf certificate's SHA-256. Every event carries `signing_id`, so collecting on the Google team ID lists the signing IDs it allows. Machine rules are looked up as well as global ones, so one Mac can be watched on its own.
- BLOCK_* and ALLOW_UNKNOWN are stored as before. ALLOW_SCOPE and ALLOW_PLATFORM are not stored in v1.
- The flagged rules are read from one small index item (`EventCollection` / `Rules`, rewritten by the CLI when a flag changes), cached for 60 s in the Lambda, so the writer makes no extra read per event.
- Santa only uploads allowed-by-rule events when `EnableAllEventUpload` is on. The CLI warns when a rule is flagged and the setting is off for every Mac, and the owner can turn it on for some Macs only (machine config). With the setting on but no rule flagged, the cost is the extra uploads (Lambda requests, and the Kinesis or Firehose sink if one is enabled), not DynamoDB: about $0.30/month at 1k Macs for the Lambda.
- Turning collection off leaves the events already stored until they expire (90 days).

This makes the `EnableAllEventUpload` row of the cost table apply only to events of flagged rules, not to every allowed execution.

A (Athena over Firehose) stays an optional later addition for full history beyond 90 days; v1 does not need Firehose.

## Contract mismatches to handle

1. RuleType, Policy and ClientMode are numbers. Decode them, render unknown values as `UNKNOWN(n)`, add ALLOWLIST_TRANSITIVE (6). Show the configured client mode (machine config) apart from the reported one (sensor data). Rudolph skips a machine config attribute it cannot decode and applies the global value, so a mistyped ClientMode on the Config row shows as not configured rather than an error.
2. SIPStatus is the raw CSR flags: 0 means every protection is on, 127 means SIP is disabled. Rudolph's comment calling 0 "SIP disabled" is wrong. Santa's `sip_status` is a plain proto3 uint32, so a 0 is left out of the preflight and Rudolph stores no attribute: a missing SIPStatus also means enabled.
3. Certificate sort keys are `Cert#`: correct the contract README.
4. LastSyncType is stored lower case.
5. Rule state (in force, removing, expired), and the legacy `SHA256` attribute read as Identifier.
6. Machines are ordered by serial; hostname search is case-insensitive; machine IDs stay case-sensitive.
7. Decisions: add ALLOW_PLATFORM and pass unknown values through; map file access decisions; event times are float seconds.
8. Events come from the new table, not from Rudolph's table; Athena search is a separate method that needs a time window.
9. The top-blocked filter becomes a fixed window (1, 7, 30 or 90 days), not a minute-precise "since", and a top-allowed list is added alongside it. Done: the lists are the ranker's stored snapshots, shown in stored order (Count, highest first). `Count` goes by execution date and `Machines` by upload date and is present on the first 100 rows only, so the web view shows "—" below them. Snapshot IDs are opaque strings. A missing pointer means the list has not been computed yet; the page shows the generation time and warns when it is more than 3 hours old.
10. Machine config: `TemporaryClientMode` (number, as ClientMode) and `TemporaryClientModeExpiresAt` (number, Unix seconds) are read together; if either is missing or mistyped, the mode is unknown or the expiry is 0 or less, there is no temporary mode, as in Rudolph. Ended values stay on the row, so the web layer compares the expiry with its own clock (and, like Rudolph, ignores an expiry more than 30 days and 10 minutes ahead). `CleanSync` (BOOL) is a pending clean sync request; any other type reads as none. None of these fail the machine page.
11. Client versions: `IdentityUnverified` (BOOL) on an event row is stored only when true; absent or mistyped reads as false and does not fail the page. The web view parses SantaVersion as year.month (`2025.12.0` is 2025.12) and marks versions below 2026.8 as a legacy client; an empty or unparseable version is not marked.

## Implementation order (tests first)

1. web: contract decoding over `rows.json` (pure Go).
2. web: DynamoDB Local harness with a vendored, checksum-tested copy of Rudolph's table definition and a client that fails on Scan, extra queries or a wrong Limit.
3. web: machine detail, machine rules, global rules (all and by type), DynamoDB cursors. Needs no Rudolph change.
4. Rudolph: R1, R2 (and R3 if chosen); contract fixture regenerated; COST2 unchanged.
5. Rudolph: R4, one GSI per change, tested for 0 index writes per steady-state sync.
6. web: machine list on both indexes.
7. Rudolph: R5, then web: policy filter.
8. Rudolph: R6, R7 (second table in ddblocal), then R8.
9. web: execution events, top blocked and top allowed readers, plus IAM. File access events in v2.
10. Optional: R9 and Athena search.

## Decisions

- 2026-11-05, owner: v1 shows blocked and allowed binaries (every uploaded execution event, D+). File access, audit and bundle events, and full history beyond what Santa uploads, wait for v2.
- 2026-11-05, owner: allowed-by-rule events are collected per rule, switched on and off from the CLI (see "Collecting allowed events per rule"); unknown and blocked executions are always stored.
- Taken as defaults until the owner says otherwise: day-level last seen (R3); events kept 90 days; R9 and Athena left out of v1, so whether Firehose or a SIEM is in use no longer blocks anything.

Defaults proposed for the rest: case-insensitive hostname search; serial order; a sparse policy attribute with backfill; day-aligned top-blocked windows refreshed hourly with exact machine counts; combined event filters refused in v1; the events table in Rudolph's Terraform; an events-table failure answers 5xx; file access AUDIT_ONLY events stored once file access events arrive in v2.
