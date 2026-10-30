# Verone Minecraft monitor

This service is a separate executable (`./cmd/verone`). The upstream executable and its routes remain available only when explicitly started instead. The Verone executable exposes `/healthz` and the `/v1` routes below; it has no debug, legacy, websocket or browser CORS routes.

## Deployment in Coolify

Use this repository branch as a Docker Compose resource, with `deploy/compose.yml` as the compose file. Set the build context to the repository root. Generate independent random values for every variable in `deploy/.env.example` (for example `openssl rand -hex 32`); tokens must be distinct and at least 32 characters. Use URL-safe hexadecimal for `MONGO_PASSWORD` because it is interpolated into a connection URI. Keep secrets in Coolify's secret environment, never in Git or `NEXT_PUBLIC_*` variables.

Run exactly **one monitor replica per database**. Configuration writes and completed collections share a process mutex, making revision validation, capacity admission and sample publication serial within that replica. Multiple replicas or direct database writers bypass this invariant and are unsupported. There is no distributed lease. Restart the instance when replacing it; do not use overlapping rolling deployments.

Only the monitor port binds to host loopback (default `127.0.0.1:18080`); Mongo and Influx have no published ports. To reach the monitor from the landing application's server container, attach it to the private Docker network and address `http://monitor:8080`, or configure a private authenticated reverse proxy with an explicit source allowlist. Do not expose the database ports or make this API a public browser endpoint. Configure the Next server's proxy with the read token, and its administrative synchronization job with the admin token. The browser receives neither token. TLS is required across hosts. `/healthz` is unauthenticated, returns 204 when both databases answer and 503 otherwise, with a three-second deadline.

The monitor runs as a non-root user, with a read-only filesystem, dropped capabilities, bounded logs, 384 MiB memory, one CPU and 100 PIDs. Mongo and Influx have 512/768 MiB memory limits. Allow roughly 2 GiB RAM plus host overhead. Database images are pinned to major/minor families; freeze image digests in the deployment before activation and retain those digests with backups.

## API contract

All data requests require `Authorization: Bearer TOKEN`. The admin token can read and write; the read token can only read. Missing or invalid credentials return 401. A global authenticated request budget of 600 per minute returns 429 with `Retry-After: 60`. Configuration bodies are limited to 8 KiB.

| Request | Behavior |
| --- | --- |
| `PUT /v1/admin/servers/:uuid` | `{id,revision,enabled,deleted,interval,java,bedrock}`; canonical lowercase UUID, positive monotonic revision, interval 30-3600 seconds. Same revision and identical body is idempotent; stale or changed equal revisions return 409. |
| `GET /v1/servers` | Array of snapshots, excluding deleted configurations. |
| `GET /v1/servers/:uuid/history?period=24h\|7d\|30d` | `{points,period,aggregation:"maximum"}`; points contain Unix-second timestamp, nullable players, responding and samples. Unknown/deleted UUID returns 404. |

Endpoints are `null` or `{host,port,srv}`. Ports are 1-65535; SRV is supported only for Java with default port 25565. Each query resolves DNS again, validates every A/AAAA and every SRV target, and dials a literal public IP. Mixed public/private answers fail closed. Local, private, shared, documentation and transition address ranges are rejected. SRV answers are capped at 32. No HTTP requests or redirects are involved. Add a network egress firewall as deployment defense in depth.

Snapshots contain `id,revision,status,players,maxPlayers,latencyMs,updatedAt,peak,popularity,java,bedrock`. Status is `online`, `no_response`, `unknown`, `stale` or `disabled`; no response never proves a server offline. Current player counts are nullable, including malformed/missing counts. When both editions respond, Java supplies the summary (counts are not added because networks can share players). Edition readings retain their individual counts. Latency measures the status exchange after connection, excluding DNS and TCP connect. The peak is the highest observed summary count, preserved across failures and configuration revisions. Popularity is an exponentially weighted observed-player count with a one-day time constant, not a percentage or ranking. No fabricated defaults are used.

After three configured intervals without a fresh sample the response is stale and clears live numbers and edition readings; the historical peak/popularity remain historical observations. Disabled configurations expose no live numbers. A changed revision is unknown until its own sample completes. Disable/delete does not erase stored snapshots or history. Re-enabling preserves previous peak/popularity. Deleted history remains stored but is hidden by the API until the UUID is restored with a higher revision.

## Scheduling and storage limits

At most 1,000 UUIDs, including tombstones, may be registered. Admission and writes are serialized. Reuse the same UUID to restore deleted records; automatic tombstone removal would break revision ordering and is intentionally absent. The scheduler runs every five seconds, chooses the least recently started configurations first, and allows eight concurrent collections, with a shared eight-second collection timeout and four seconds per edition. Intervals are targets; a large or slow fleet will sample less often and may become stale. Capacity-plan based on measured probe duration. Java payloads are capped at 1 MiB; Bedrock advertisements at a 4 KiB datagram and player counts at one million.

Mongo stores configuration and latest snapshot in `minetracker.verone_configs` / `verone_snapshots`; Influx stores `verone_samples` tagged by immutable UUID. History uses maximum player count per 5-minute / 1-hour / 3-hour window and sums response/sample counts. Failed probes record a sample with no players, rather than zero players. Mongo publication and Influx insertion are not an atomic cross-database transaction: an Influx outage can leave a newer latest snapshot without its history sample, logged as `monitor_record_failed`. No synthetic backfill or automatic retry claims exactly-once delivery.

Influx's initial bucket retention is 90 days; latest peak/popularity and configurations in Mongo persist until manually migrated or restored. The retention initialization variable affects only a new Influx volume. Verify the live bucket retention after restoring or reusing a volume. Mongo history is bounded by the UUID cap, while Influx disk use depends on sample frequency. Monitor disk space, health and `monitor_*_failed` logs.

## Backup, restore and rollback

Keep encrypted off-host backups of Mongo, Influx, image digests and secret references. Backups contain hostnames and monitoring data. Use a backup operator with access to this compose project, and keep all backup files outside the Git checkout.

1. Stop **this monitor instance only** to avoid cross-database skew; keep both databases running.
2. Run `mongodump --archive --gzip` inside this project's Mongo container with authenticated credentials and stream the archive to your encrypted backup destination. Supply credentials through the operator's secure environment, not command history or logs.
3. Run `influx backup /backup/<timestamp> --host http://localhost:8086 --token "$INFLUXDB_TOKEN"` in this project's Influx container using a temporary backup mount. Copy the resulting directory to the encrypted destination. Save bucket/org metadata and verify the backup command succeeds.
4. Restart this monitor and verify health, an authenticated list and a history request. Record backup time and digests.

For a restore drill, use a new isolated compose project and new volumes, unpublished database ports and a loopback monitor port. Restore Mongo with `mongorestore --archive --gzip --drop` **only into the isolated database**; restore the Influx backup using the matching Influx 2.7 CLI and its restore token requirements. Do not use destructive restore flags on the running deployment. Configure matching organization/bucket/token and validate health, UUID/revision counts, peaks and a known history window before promotion. A backup is not proven until this drill succeeds.

Rollback: stop the monitor, restore its previous image digest and compatible environment, then restart one instance and verify the API. Configuration revisions must remain monotonic; roll an endpoint back with a higher revision. If data/schema rollback is necessary, restore both backups into replacement volumes and switch the deployment after verification; preserve the old volumes for recovery. Never delete volumes as part of a routine rollback.

No production deployment, backup drill or runtime capacity claim is implied by the repository tests.
