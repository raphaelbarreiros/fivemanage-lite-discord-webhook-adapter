# Discord Webhook Adapter for Fivemanage Lite

A self-hosted receiver for FiveM scripts that send Discord-style JSON logs. Replace the webhook URL for supported payloads without changing their contents.

**HTTP receiver → local SQLite queue → single delivery worker → Fivemanage Lite API**

One Go binary, one adapter instance, no message broker and no separate database server for the adapter. SQLite stores pending messages only; searchable history belongs to Lite/ClickHouse. No LLM, attachment fetching, HTML rendering, or additional moderation UI.

> **HTTP 204 means committed to the local queue—not delivered to Lite.** This is a bounded, best-effort buffer. Events can be lost before admission, through configured discard policies, or if the volume is lost. Delivery can be duplicated when Lite succeeds but local completion does not. This is not high availability or exactly-once delivery.

## Requirements and tested versions

- Docker Engine with Docker Compose, Git, and Python 3.10+ for the isolated development stack.
- Go **1.27.2** for native builds and tests; the Go toolchain can download the version declared in `go.mod`.
- A persistent **local** filesystem for SQLite. Do not use NFS or SMB for WAL files.
- `modernc.org/sqlite v1.60.1`, embedding **SQLite 3.53.4**, including the applicable WAL-reset corruption fix. The adapter verifies the engine version and connection settings at startup.
- Fivemanage Lite commit [`74ba491368d88d2747d9013cfd3fc4bcd78d1e34`](https://github.com/fivemanage/lite/tree/74ba491368d88d2747d9013cfd3fc4bcd78d1e34), with the separate ingestion confirmation patch in [`patches/lite/`](patches/lite/).

Container base images are pinned by version and digest. The adapter has been exercised on macOS/arm64 and Linux/arm64. A successful local test is not a production-readiness certification.

## Quick start: isolated local stack

Run these commands from the repository root:

```sh
python3 integration/prepare.py
# PostgreSQL and ClickHouse belong to Lite, not to the adapter queue.
docker compose up -d --build --wait lite
python3 integration/bootstrap.py
docker compose up -d --build --wait adapter
python3 integration/compose_smoke.py
```

`prepare.py` fetches the pinned Lite source and generates private, disposable credentials without printing or replacing existing secret values. It refuses a different or modified tracked Lite checkout. `bootstrap.py` creates a synthetic organization, dataset, and API token through Lite's HTTP APIs, then writes `.local/compose.config.json`.

**Run bootstrap only when provisioning the test environment.** Running it again creates another organization/token and requires recreating the adapter. It is not a production provisioning tool.

| Service | Local address |
|---|---|
| Lite dashboard | `http://127.0.0.1:18080` |
| Webhook receiver | `127.0.0.1:18081` |
| Adapter health | `http://127.0.0.1:19090/healthz` |
| Adapter readiness | `http://127.0.0.1:19090/readyz` |
| Adapter metrics | `http://127.0.0.1:19090/metrics` |

Lite login: username `admin`; the generated password is in `.local/secrets/admin_password`. Do not publish that file or paste its contents into issues.

The test configuration uses a 3-second outbound timeout, a 4-second maximum retry delay, and a 1-second maintenance interval to exercise failures quickly. The binary defaults are 10 seconds, 5 minutes, and 30 seconds respectively.

### What the Compose stack includes

- The adapter and the patched Lite backend with its official frontend.
- PostgreSQL and ClickHouse on a dedicated Compose network, with **no host-published database ports**.
- A named volume mounting the **entire `/data` directory**, including the database, WAL, SHM, and instance lock. The adapter runs as UID/GID 10001 with a read-only root filesystem and dropped capabilities. Its data directory is mode 0700 and database file mode 0600.
- Individual read-only secret mounts. The host secret directory is mode 0700; files are mode 0444 so UID 10001 can read the mounts. Host root and Docker access remain trusted.

The official Lite manifest was the reference for its dependencies. MinIO and Jaeger are not required for this logging path and are not started. Media uploads are not configured or tested. No Kafka, Redis, RabbitMQ, or separate queue database is added.

To stop or resume without deleting data:

```sh
docker compose stop
docker compose up -d --wait
```

`docker compose down` preserves named volumes. **Do not use `down -v` if you need to keep pending messages.**

## Organizations, datasets, and routing

### Which organization receives an event?

**Each source's Lite API token selects its organization.** The adapter loads `sources[].lite_token_file` and sends that source's token in the `Authorization` header, without a `Bearer` prefix. Lite resolves the token to its `OrganizationID`. The source's `dataset` is then looked up **within that organization** using `X-Fivemanage-Dataset`.

| Setting | Purpose |
|---|---|
| `lite_url` | Selects the Lite server |
| `sources[].lite_token_file` | Supplies the API token belonging to this source's target organization |
| `sources[].dataset` | Selects a dataset by name within that token's organization |
| `sources[].resource` | Sets the trusted resource label; it does not select an organization |
| `sources[].secret_file` | Authenticates incoming webhooks; unrelated to the Lite API token |

There is no `organization_id` configuration option for ingestion. Adding an organization ID to the incoming JSON does not change the destination. The authenticated organization and dataset are server-controlled, not client-controlled.

For an existing organization:

1. Open the intended organization in Lite and create an API token for it.
2. Create the target dataset in that same organization.
3. Store the token in a private file and mount it into the adapter.
4. Point that source's `lite_token_file` to the mounted path and set its `dataset` and `resource`.
5. Restart the adapter and verify delivery in the intended organization before switching real scripts.

**Multiple organizations are supported in one instance.** Each source must declare its own `lite_token_file`. Sources targeting the same organization may reference the same token file; sources targeting different organizations reference their respective organization tokens. Identical dataset names in different organizations are supported. Incoming JSON cannot override the selected credentials.

The instance still has one Lite server URL, one shared HTTP transport, one worker, and one SQLite queue. Capacity, expiration, pacing, and transient-failure backoff are shared—not per-organization quotas or independently scheduled workers. A busy source can consume shared capacity; a failing destination can delay other organizations. Credentials are never stored in SQLite or copied into log metadata.

Do not change a source's token to another organization while it has pending events: routing is resolved at delivery time. Drain before reassigning a source, or keep the existing source/token mapping for its pending events and add a new source ID for the new organization. Rotating a token within the same organization does not change the intended destination.

### Two-organization configuration

[`config.example.json`](config.example.json) contains two sources, `alpha-inventory` and `beta-inventory`, using the same dataset name `fivem` but separate organization token files. Create `fivem` in each organization, then mount all four secrets into the **same adapter container**. For example, edit the existing Compose secret declarations and the adapter's secret list:

```yaml
services:
  adapter:
    secrets: [alpha_webhook, alpha_lite_token, beta_webhook, beta_lite_token]
secrets:
  alpha_webhook:
    file: .local/secrets/alpha_webhook
  alpha_lite_token:
    file: .local/secrets/alpha_lite_token
  beta_webhook:
    file: .local/secrets/beta_webhook
  beta_lite_token:
    file: .local/secrets/beta_lite_token
```

This is a configuration fragment, not a standalone stack. Keep the existing Lite/database secrets and service settings. Store each organization's Lite API token in its matching private file; generate independent random inbound webhook secrets. Update the mounted `.local/compose.config.json` with the desired sources and restart the adapter. The example's native `admin_listen` is loopback; retain `:9090` inside Compose when host-loopback metrics access is required.

### Migrating from the global token setting

The old top-level `lite_token_file` is no longer accepted. Move its path into **every existing source**, preserving each source ID and its current destination. Remove the top-level key, keep the secret mount, and recreate the adapter with the rebuilt image. There is no fallback credential or silent default organization. Missing/unreadable per-source token files fail startup. SQLite schema and existing pending IDs do not change.

## Configuration

Use [`config.example.json`](config.example.json) as the complete configuration reference. The adapter reads strict JSON and rejects unknown configuration options. Configuration and secrets are loaded at startup; changes require a controlled restart.

```sh
go build -trimpath -o .local/adapter ./cmd/adapter
.local/adapter -config /private/path/config.json
```

`lite_url` accepts a scheme, host, and optional port—not embedded credentials, query parameters, or a subpath. HTTPS is required unless `allow_http=true` is explicitly set for a trusted local network. The client does not follow redirects or use environment proxy settings.

The verified ingestion endpoint is `POST /api/logs`, accepting an array of `{level,message,resource,metadata}` objects.

### Source configuration

The following is a source entry to place in the configuration's `sources` array:

```json
{
  "id": "inventory",
  "secret_file": "/run/secrets/inventory_token",
  "lite_token_file": "/run/secrets/organization_lite_token",
  "dataset": "fivem",
  "resource": "inventory",
  "rule": {
    "event_type": "inventory_change",
    "fields": {
      "player_session_id": "Session",
      "player_character_id": "Character",
      "player_account_id": "Account",
      "player_discord_id": "Discord",
      "item": "Item",
      "quantity": "Quantity"
    }
  }
}
```

Use an independent random adapter secret for each source, preferably 32 random bytes encoded as hexadecimal. **Never reuse actual Discord webhook tokens.** Mount each secret file and reference its container path. Secrets must contain 24–4096 non-whitespace bytes; a trailing newline is accepted and placeholders are rejected. Use URL-safe values for webhook secrets.

Source IDs, dataset names, and resource names are restricted to 1–64 alphanumeric, underscore, or hyphen characters. The dataset must exist in the token's organization. `resource` is a log label, not a separately provisioned dataset. Rules are optional; without them, events still receive a readable generic interpretation and preserve the original JSON.

The resulting webhook URL has this form:

```text
https://logs.example.invalid/api/webhooks/inventory/ADAPTER_TOKEN_PLACEHOLDER
```

The Compose receiver is published on loopback only. A FiveM server on another machine cannot reach it through its own `127.0.0.1`. For remote access, use a trusted deployment with persistent local storage and an HTTPS reverse proxy. Expose only the webhook receiver, keep administration and databases private, and disable access logs that would record authenticated URL paths or queries.

Treat the full webhook URL as a credential. Do not include it in shell history, issue reports, metrics labels, or commits.

### Deterministic interpretation

`rule.event_type` is a trusted fixed value. `rule.fields` maps normalized fields to **exact embed field names**. The first matching value wins, traversing embeds and fields in order. Match names to the actual language and spelling used by your scripts; the English example above does not translate incoming field names.

Supported normalized fields:

- `player_session_id`, `player_character_id`, `player_account_id`, `player_discord_id`;
- the corresponding four `target_*` identifiers;
- `item`, `quantity`, `value`, and `event_time`.

Without an explicit rule, the adapter does not infer business fields. Numbers use `json.Number` and normalized values are strings, avoiding indiscriminate float conversion. Session, character, account, and Discord identifiers remain distinct.

Configuration and rule changes affect pending events when they are next processed. Drain or consistently back up the queue before changing routing or interpretation. Removing a source with pending events causes an explicit `source_removed` discard when the worker reaches them.

## Compatibility

| Input or feature | Behavior |
|---|---|
| `POST /api/webhooks/{id}/{token}`, UTF-8 `application/json` | Supported |
| Nonempty string `content`, an `embeds` array containing objects, or both | Supported |
| Multiple embeds, ordered/repeated fields, Unicode, large integers | Preserved in the original payload; generic text retains embed/field order |
| Titles, descriptions, colors, URLs, timestamps, unknown properties | Original preserved in full; recognized text fields contribute to the readable message |
| Unknown business format or future embed properties | Accepted if the message structure is valid; readable fallback when necessary |
| `username`, `avatar_url`, `allowed_mentions`, `tts=false` | Preserved as data; no impersonation, mentions, or audio playback |
| No query, `wait=false`, or `wait=0` | 204 after local commit |
| `wait=true`, unknown/duplicate query parameters, threads/forum tags | 400; unsupported |
| Multipart uploads, JSON `attachments`/`files`, including empty arrays | 415 for unsupported MIME; 400 for unsupported JSON features |
| `components`, `poll`, `flags`, `tts=true`, `payload_json` | 400; not simulated or silently ignored |
| Message editing/deletion/retrieval, `/messages` subroutes, other methods | 404/405 |
| Gzip or other request-body content encoding | 415 |
| Invalid JSON, incorrect content/embeds types, missing message | 400 |
| Invalid source or token | 401 without reflecting credentials |
| Oversized body | 413 |

Image and attachment URLs are **never fetched**. HTML is plain data; the adapter does not execute or render it. Downstream applications must continue treating it as untrusted data.

Duplicate JSON object keys remain in the original bytes; the readable projection follows Go's decoder resolution of duplicate properties. Repeated elements in embed field arrays retain their order.

This is not universal Discord API compatibility. Scripts that validate Discord domains, use different versioned paths, require returned message IDs, upload files, or depend on unsupported features need changes beyond replacing the URL.

## Payload preservation

The tested Lite version collapses whitespace, flattens metadata objects, drops boolean/null values, and can round JSON numbers. The worker therefore sends `discord_payload_b64`, containing the **exact original request bytes**. Decode it to recover all properties, numbers, ordering, and whitespace. Base64 is not encryption or anonymization.

Diagnostic metadata includes:

| Field | Meaning |
|---|---|
| `adapter_event_id` | Random 128-bit identifier, stable across delivery attempts |
| `adapter_source` | Authenticated source ID |
| `adapter_received_at` | Original receipt time in UTC, at the queue's millisecond precision |
| `adapter_interpreter` | Interpreter version, currently `discord-generic/1` |
| `discord_embed_N_timestamp` | Timestamp declared by embed N, separate from receipt time |

`event_time` is added only by an explicit rule. Lite also assigns its own ingestion timestamp. Readable text and normalized metadata may still be transformed by Lite; the reversible representation is the original base64 payload.

## Required Lite ingestion patch

The pinned, unpatched Lite version can return HTTP 200 after failing to store a log. **The adapter does not accept that response as delivery confirmation.** It requires all of:

- HTTP 200;
- response JSON with `status=ok` and `data=ok`;
- `X-Fivemanage-Ingest: clickhouse-sync-v1`.

The [separate patch](patches/lite/README.md) emits confirmation only after synchronous ClickHouse `batch.Send()` succeeds. The Compose build applies it to the pinned source. There is no unsafe switch to bypass confirmation, and proxies must not manufacture the header.

This confirms the tested ingestion operation, not replication, backups, survival of remote disk loss, or a distributed transaction with SQLite. A lost response can still cause a duplicate.

Known upstream behavior retained by the patch:

- Rate limiting of 20 requests/second per IP.
- A five-minute API-token cache; token revocation may not be immediate.
- Log retention hardcoded to 30 days in the ingestion service, even if the dataset specifies another retention value.

The worker's default interval is 100 ms **plus HTTP request duration**, limiting it to approximately ten sends/second at most. This is a pacing setting, not a throughput guarantee.

## Queue and storage policy

The `pending` table contains only `id`, `source`, one original payload BLOB, `received_ms`, `attempts`, and `next_ms`. Its primary key supports completion, the due-time index supports scheduling, and the receipt-time index supports expiration and overflow. Converted payloads, rendered HTML, delivered-event records, and a separate event archive are not stored.

A dedicated `database/sql` connection and bounded admission coordinate short SQLite operations. No database transaction or I/O gate is held across HTTP delivery. Each request waits for its own commit. An exclusive file lock prevents another instance from owning the same data directory. **Do not scale multiple replicas over one volume.**

Startup verifies **WAL**, **synchronous=FULL**, **auto_vacuum=INCREMENTAL**, lock timeout, and the effective SQLite engine version. Incremental vacuum is enabled before creating tables. Existing databases without the required setting are rejected rather than silently rebuilt.

| Setting | Default | Effect |
|---|---:|---|
| `max_body_bytes` | 262,144 (256 KiB) | Per-request JSON limit |
| `max_inflight` | 32 | Concurrent admissions; excess requests receive 503 |
| `queue.max_messages` | 10,000 | Pending-message quota |
| `queue.max_bytes` | 134,217,728 (128 MiB) | Original payload bytes, not physical disk size |
| `queue.max_age` | 24h | Lifetime measured from original receipt |
| `queue.overflow` | `reject_new` | Return 429 when the logical quota cannot admit an event |
| `queue.min_free_bytes` | 268,435,456 (256 MiB) | Filesystem reserve; insufficient space returns 507 |
| `queue.max_disk_bytes` | 536,870,912 (512 MiB) | Admission threshold for DB+WAL+SHM, not a hard disk quota |
| `queue.busy_timeout` | 500ms | SQLite lock wait |
| `queue.maintenance_interval` | 30s | Expiration, checkpoint, and incremental reclamation |
| `queue.vacuum_pages` | 256 | Pages requested per maintenance pass |
| `retry_min` / `retry_max` | 1s / 5min | Exponential retry delay; Retry-After capped at retry_max |
| `max_attempts` | 100 | Explicit discard after the attempt limit |
| `send_interval` | 100ms | Minimum pause between worker iterations |
| `http_timeout` / `admission_timeout` | 10s / 5s | Outbound HTTP / admission operation timeout |
| `shutdown_timeout` | 20s | Handler shutdown deadline; Compose allows 30s |

These defaults are conservative buffer choices, **not measured production sizing**. Smaller quotas/TTLs and `drop_oldest` favor bounded storage over event retention.

### Retry, expiration, and discard

- Before HTTP delivery, the worker persists the attempt number and next eligible time, covering the request timeout and backoff. There is no persistent `processing` state. A crash before sending may consume an attempt.
- The in-memory active marker is released atomically, independently of the database I/O gate and cancellation. Database contention cannot permanently pin it.
- Connection failures, 429, 5xx, 401/403, and missing/invalid confirmation are retried within the attempt and age limits. Transient failures pause the single worker to avoid hammering an unavailable destination.
- Lite responses 400/404/405/413/415/422 are permanent failures and trigger explicit discard. An invalid dataset therefore loses the affected event after an alert; configure destinations before switching real scripts.
- Confirmed delivery triggers immediate DELETE of that row. A failed DELETE preserves the row and may cause redelivery with the same event ID. Repeated calls from scripts receive new IDs; there is no universal input deduplication.
- `drop_oldest` protects the active event and removes up to 128 old pending rows per admission. If that does not free enough capacity, the transaction rolls back and the new event is rejected.
- Expiration runs in batches of up to 128 and opportunistically during admission. Retries never reset receipt time. Expired events are not newly sent, but removal can lag maintenance cycles, in-flight operations, and cleanup batches. Disk failures can further delay removal.
- Discards emit bounded-label counters and operational warnings. No indefinitely retained dead-letter archive is created.

### Physical space management

Admission checks filesystem free space and DB/WAL/SHM size, with an estimated temporary margin of `4 × payload + 64 KiB`. Indexes, free pages, WAL, maintenance, and other processes can consume additional space. **Logical quotas and admission thresholds do not enforce an exact disk ceiling.** Provision and monitor the filesystem separately.

DELETE makes pages reusable; it does not immediately shrink the database. Incremental vacuum reclaims excess pages in batches. Regular `wal_checkpoint(PASSIVE)` and opportunistic zero-wait `TRUNCATE` handle busy readers without indefinitely stalling admission. The adapter does not run a full VACUUM per event or manually delete WAL/SHM files. An empty queue retains a nonzero database file.

Logical deletion is not forensic erasure. Free pages, snapshots, and backups may retain previous bytes. Protect the data volume and its backups appropriately.

## Health, metrics, and security

The native administrative listener defaults to loopback. Compose binds it inside its private network and publishes it only on host loopback. Do not expose it through the public webhook proxy.

| Endpoint | Meaning |
|---|---|
| `/healthz` | HTTP process is alive |
| `/readyz` | Not shutting down; queue gate and configured space/capacity checks permit admission |
| `/metrics` | Prometheus text metrics without player or credential labels |

Readiness does not certify Lite availability or guarantee that the next write will succeed. Only an individual request's committed enqueue warrants a 204.

Counters cover received, accepted, rejected, delivered, failed, retried, and discarded events, local errors, and busy checkpoints. Discard reasons are `expired`, `overflow`, `permanent`, and `attempts`. Latency metrics expose sum/count values in seconds. Gauges expose pending messages, logical payload bytes, separate DB/WAL/SHM sizes, free disk space, page/freelist statistics, and oldest-message age. Counters reset on restart; pending state does not. Metrics collection can return 503 under contention. No monitoring server or alerting service is bundled.

Recommended alerts: increasing discards/rejections/local errors, oldest pending age approaching TTL, sustained WAL growth, low free space, missing confirmation, and authentication/dataset failures at Lite.

Structured logs contain fixed messages and diagnostic fields such as reason, HTTP status, attempt, and event ID—not payloads, Authorization values, or token-bearing paths. Compose rotates the adapter's operational logs across three files of up to 10 MiB each, separately from SQLite quotas. For native execution, configure supervisor/journald retention; the binary writes only to stdout. Lite service retention remains a separate operational concern.

### Troubleshooting

| Symptom | Check |
|---|---|
| Receiver 401 | Source ID, secret file, and configured webhook token |
| Receiver 400/413/415 | Compatibility matrix, payload structure, size, MIME, and unsupported features |
| Receiver 429 | Logical buffer capacity under `reject_new` |
| Receiver 507 | DB/WAL/SHM usage, filesystem reserve, and other disk consumers |
| Receiver 503 | Admission limit, SQLite contention/write failure, shutdown, or unavailable volume |
| Queue not draining | Patched Lite contract, organization token, dataset, connectivity, rate limits, and metrics |

Never delete SQLite auxiliary files to unblock the queue. Never attach real credentials or player payloads to public reports.

On SIGTERM/SIGINT, the adapter stops new admissions, drains handlers, cancels the worker and outbound requests, waits for worker exit, shuts down administration, and closes SQLite. It does not delete unconfirmed events to accelerate shutdown. Fire-and-forget scripts that ignore HTTP errors/timeouts can lose events when the receiver, disk, or volume is unavailable before commit.

## Backup, recovery, and upgrades

The queue is temporary, not a history store. If a consistent backup is needed:

1. Stop the adapter and wait for shutdown. Copy the complete SQLite directory/volume, including auxiliary files still present. Do not copy only the `.db` while writes are active in WAL mode.
2. Alternatively, use SQLite's Online Backup API or `VACUUM INTO` with a compatible engine and a consistent connection. No backup HTTP endpoint is implemented. The system SQLite CLI may be older than the embedded engine.
3. Restore while the adapter is stopped, on local storage, with the required ownership and permissions. Run `integrity_check` on a copy using a corrected SQLite engine. Start only one owner of the restored directory.
4. Restoring a snapshot can resend events delivered after that snapshot. Expiration still uses original receipt times.
5. If corruption occurs, preserve a stopped/consistent copy for diagnosis. Do not manually remove WAL/SHM. Abandoning a queue is an explicit loss decision, not recovery.

Before upgrading, drain or consistently back up the queue. Pin dependencies and image digests, review the embedded engine's integrity fixes, preserve third-party notices, and revalidate the exact engine-version check. Recheck Lite's ingestion contract, metadata transformation, and license on every new base. Do not switch to floating branches or `latest`. Unknown queue schema versions are not automatically migrated.

## Testing

```sh
gofmt -w cmd internal
go vet ./...
go test -race -count=1 -v ./...
go build -o .local/adapter ./cmd/adapter
# Native integration tests use the same adapter ports as Compose.
docker compose stop adapter
python3 integration/e2e.py
python3 integration/multi_org.py
python3 integration/load.py
python3 integration/scan_secrets.py
# Tests provision their own synthetic destinations; re-provision the demo before resuming.
python3 integration/bootstrap.py
docker compose up -d --force-recreate --wait adapter
python3 integration/compose_smoke.py
```

The synthetic fixtures deliberately include Unicode, multilingual field names, repeated fields, unknown properties, and large identifiers. E2E tests query the **real Lite HTTP API**, stop/start only this Compose project's Lite/ClickHouse services, and terminate only adapter subprocesses they created. A disposable loopback proxy synchronizes crash windows before and after remote storage. SQLite tests use persistent files; `SQLITE_FULL` is induced through a page limit rather than filling the host disk.

Coverage includes commit-before-response, authentication and compatibility boundaries, concurrent admission/cleanup, restart recovery, bounded retries, overflow/expiration, failed writes and deletion, busy checkpoints, page reuse, and physical reclamation. The active-marker regression holds the I/O gate beyond a simulated admission timeout and verifies that release and subsequent draining still succeed.

`integration/multi_org.py` creates two real Lite organizations with identically named datasets, queues interleaved events in one adapter while Lite is unavailable, kills/restarts that adapter, and checks each organization's API results. It verifies stable event IDs, exact payload recovery, and that payload-supplied organization/dataset/credential fields do not override trusted routing. It leaves synthetic credentials in the ignored secrets directory and requires the native adapter ports to be available, like `e2e.py`.

Local evidence includes an exact 1,063-byte payload round-trip, expected duplicate delivery after a remote-success crash window, and a bounded 200-event load with 200 accepted requests and 200 records found in Lite. These are local observations, not production throughput claims. Power-loss resilience, operational backup restoration, long soak testing, and Linux/amd64 runtime have not been verified.

Generated secrets and evidence stay in ignored `.local/` and `artifacts/` directories. They are synthetic test artifacts, not an adapter event archive. Secret scanning checks known disposable credentials and real Discord webhook URL patterns; it is not a comprehensive security audit. Validation was performed by the implementer, not an independent reviewer.

## License

Original adapter code is distributed under the [MIT License](LICENSE). Third-party notices are preserved in [`licenses/`](licenses/).

**Fivemanage Lite is not MIT at the tested commit.** Its authoritative `LICENSE.md` specifies **FSL-1.1-ALv2**, explicitly permitting internal use, restricting competing commercial uses, and providing an Apache-2.0 future license on the applicable second anniversary. The upstream README's MIT claim does not match that license file. The separate patch retains Lite's terms in [`patches/lite/LICENSE.md`](patches/lite/LICENSE.md); the adapter's MIT license does not relicense Lite.

Review the applicable licenses and security updates before redistribution or deployment. Pinned images reproduce tested inputs; they do not certify that all upstream components are vulnerability-free.
