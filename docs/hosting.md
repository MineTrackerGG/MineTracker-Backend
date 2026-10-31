# Automatic hosting player total

The hosting monitor discovers candidates through the Pterodactyl Application API, chooses each server's own primary public allocation and starts protocol collection automatically. It does not create editorial pages or require the user to configure roles, groups or links. Pterodactyl supplies inventory, not player counts; counts come from Java SLP or Bedrock RakNet responses.

The integration only sends GET requests to Pterodactyl. It does not change servers, API keys, allocations, firewall rules, files or game configuration. Operators supply an existing Application token with read access to servers and allocations. `egg` metadata is optional and can be null when its read permission is absent.

## Configuration and bounds

Backend-only environment remains `PTERODACTYL_URL` (HTTPS), `PTERODACTYL_APPLICATION_TOKEN` and `PTERODACTYL_SYNC_INTERVAL` (60-3600 seconds, default300). There are no per-server setup variables. Without URL/token, hosting reports unavailable and existing editorial monitoring continues. Incomplete configuration fails startup. HTTPS certificate validation is mandatory; redirects and environment HTTP proxies are disabled. Every panel A/AAAA answer must be public before the literal-IP dial, using the existing SSRF protections.

The adapter requests `/api/application/servers?include=allocations,egg&per_page=100&page=N`, capped at ten pages/1,000 servers, 2MiB/page, 64 allocations/server, 15 seconds/request and 45 seconds/sync. It never follows supplied pagination links. Missing allocation permission, malformed pages and duplicate UUIDs abort the refresh. It reads primary allocation ID and matches that exact ID in the server's own allocations. If the primary is missing or private, no secondary allocation or environment IP/port substitutes for it. Query uses a canonical public literal IP, the primary port and `srv:false`.

Adapter fields follow the official [ServerTransformer](https://github.com/pterodactyl/panel/blob/1.0-develop/app/Transformers/Api/Application/ServerTransformer.php) and [AllocationTransformer](https://github.com/pterodactyl/panel/blob/1.0-develop/app/Transformers/Api/Application/AllocationTransformer.php). API metadata does not contain online-player counts.

## Automatic discovery

The adapter temporarily examines the effective Java/Bedrock launch, the JAR referenced after `-jar`, selected version/loader variables and optional egg information. It supports `SERVER_JARFILE` templates, Java `@args` launches such as NeoForge, Java generic `server.jar`, and Bedrock startup. Non-string environment values do not invalidate inventory. It never executes or fetches a startup command or download URL.

Only a fixed derived kind (`java|bedrock|proxy|other|unknown`), an uncertainty flag and a fingerprint of limited technical hints survive the response. Raw command, environment, egg name and secrets are not saved or logged. Changing a recognized version/loader hint updates the fingerprint and advances the monitor revision; an unchanged sync does not churn revisions.

Explicitly launched Velocity/Bungee/Waterfall/Geyser-standalone artifacts and coherent proxy version/download hints are excluded. A generic launched Java JAR with `VELOCITY_VERSION` is a proxy hint. An explicit backend JAR takes precedence over residual proxy download/version variables. A Geyser plugin on a Java backend does not make that backend a proxy. Explicit non-Minecraft launches such as node/python/Terraria are excluded when no Minecraft launch is present; mixed prep+Java commands remain Java candidates. There are no hardcoded egg IDs, customer names or service-specific profiles, and egg name alone does not decide participation.

Java candidates query SLP; Bedrock candidates query RakNet. Unknown wrappers are automatically tried with both protocols on the same primary public endpoint, within the existing eight-second collection budget; the existing summary selection counts one responding population rather than adding editions. Generic Java or unrecognized wrapper metadata remains uncertain even if it responds; those measured counts can contribute to a partial subtotal. No manual resolution is required to start collecting them.

SLP processing also derives a private boolean from explicitly advertised proxy software. It does not store the software text. A fresh matching proxy hint excludes that technical source from the hosting total and clears its technical live counters before history insertion. Editorial proxy monitoring keeps its original online count and status. The proxy hint is BSON-only and absent from JSON. Plain Geyser-plugin branding is not a proxy signal.

These are bounded metadata/protocol heuristics, not binary attestation. Arbitrary launch wrappers or proxy software that masks its identity can remain uncertain or escape software recognition. The total is the sum of reported populations selected by these rules, not globally unique people. It does not collect player names, addresses or UUIDs. A partial state remains honest when software/topology is uncertain.

## Migration, refresh and history

Mongo `minetracker.verone_hosting` stores inventory, successful sync time, stable UUIDs, derived discovery and participation. Its first successful sync after this version migrates existing participation, including pending rows, automatically to the derived primary-allocation configuration and clears editorial links/groups. UUIDs and stored Mongo/Influx histories are retained. No separate script or manual PUT is needed. Automatic rows refresh whenever hints, primary allocation, name, presence or suspension change. Revision increases once per effective change, so an in-flight older collection cannot publish after a refresh. Identical refreshes are idempotent.

The technical `monitorId` remains derived from the panel identity and Pterodactyl UUID, independent of IP/port. Removal and suspension retain settings/history and stop hosting probes. They never fabricate zero. Reappearance resumes collection using the same technical identity. Switching a configured panel on an existing inventory requires an explicit migration or a new database.

The private admin API remains compatible for operator overrides; a changed participation PUT marks that row nonautomatic, and later refreshes retain the override while still validating its query against current own allocations. This is an exception for operators, not a user setup requirement. The initial migration applies the newly authorized automatic policy to legacy rows. An operator cannot enable proxy-authority counting in the total: roles `proxy` and `excluded` are always omitted; standalone/backend sources count with duplicate protection. Old group mode/authority fields remain accepted for compatibility but do not override the backend-only policy.

Keep exactly one replica. Sync, operator PUT, capacity admission and sample publication share the existing process mutex. Inventory tombstones plus editorial configurations retain the combined cap of1,000. Eight concurrent collections, fair scheduling, per-edition timeouts, DNS/SRV protection and retention remain unchanged. All hosting records stay out of public editorial `/v1/servers` and public UUID history routes.

## API and freshness

- `GET /v1/hosting/summary`, read/admin token: `{players:number|null,state:"complete"|"partial"|"unavailable",updatedAt:ISO|null,coverage:{eligible,responding,pending}}`. No names, UUIDs, addresses, commands or hints.
- `GET /v1/admin/hosting/servers`, admin token: existing `{servers,syncedAt,state}`; each private row additionally exposes `automatic` and `discovery:{kind,uncertain}`. The technical fingerprint and SLP proxy boolean are not JSON fields.
- `PUT /v1/admin/hosting/servers/:id`, admin token: existing full participation/revision contract retained for exceptional operator overrides, with idempotent retry and stale revision rejection. It is not part of the normal collection flow.

Only online samples matching current revision, no older than180 seconds and within three configured probe intervals contribute. Crossplay and duplicate endpoint/monitor sources do not add twice. Suspended sources and failed probes leave missing coverage. Zero requires a real valid response with zero players. `updatedAt` is the oldest contributing sample. With no valid count, players/time are null and state is unavailable; missing or uncertain sources make a readable subtotal partial. `coverage.pending` now also includes uncertain automatically queried candidates, not a demand for manual classification.

Inventory expires after three sync intervals (default15 minutes). Hosting probes stop and the aggregate becomes unavailable until a successful sync. Fetch failure keeps the previous inventory only until this expiry and logs `hosting_inventory_sync_failed` without secrets. Editorial probes remain independently configured.

Include the inventory collection in existing Mongo backups; Influx's initial retention remains90 days. Mongo publication and Influx insertion retain the existing non-atomic cross-database limitation. No production deployment, backup restore or live panel validation is implied by local tests. DEV activation and independent gates are coordinated separately.
