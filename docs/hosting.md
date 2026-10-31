# Hosting player total

The optional hosting integration reads the Pterodactyl Application API. It never modifies panel servers, allocation assignments, firewall rules, files or API keys. An operator must supply an existing Application token with read access to servers and allocations. No live panel access is implied by local adapter tests.

Configure backend-only secrets `PTERODACTYL_URL=https://painel.verone.cloud` and `PTERODACTYL_APPLICATION_TOKEN`. HTTPS is mandatory, with valid certificate verification, no redirects and no HTTP proxy environment fallback. `PTERODACTYL_SYNC_INTERVAL` defaults to 300 seconds and accepts 60-3600. Without URL/token the feature reports unavailable; existing editorial monitoring continues. Only a DEV deployment is authorized for this iteration; this document does not authorize deployment or panel changes.

The adapter calls `GET /api/application/servers?include=allocations&per_page=100&page=N`. It checks every page, never follows pagination URLs, caps at ten pages/1,000 servers, 2 MiB per page, 64 allocations per server, 15 seconds per request and 45 seconds per full sync. It retains only UUID, name, suspension/status and public allocation IP/port. It does not retain owner, environment, startup, credentials or player identities. Missing allocation permission, malformed responses or incomplete pagination abort synchronization. All panel DNS A/AAAA answers must be public before dialing a literal IP, using the same resolver validation as game probes.

Adapter fields follow the official [ServerTransformer](https://github.com/pterodactyl/panel/blob/1.0-develop/app/Transformers/Api/Application/ServerTransformer.php) and [AllocationTransformer](https://github.com/pterodactyl/panel/blob/1.0-develop/app/Transformers/Api/Application/AllocationTransformer.php). Server metadata does not provide player counts. Any non-null server status is treated conservatively as unavailable for aggregation.

## Identity and participation

Mongo `minetracker.verone_hosting` stores one atomic inventory document, including successful sync time, panel identity, tombstones and participation. It must be included in existing Mongo backups. Keep one monitor replica: sync, participation updates, editorial capacity admission and sample publication use the same process mutex. No additional database service or migration is required.

An inventory row `id` is the panel UUID. Its `monitorId` is a deterministic UUID derived from the configured panel URL and panel UUID, independent of IP/port and editorial IDs. Repeated syncs retain this identity. Switching the panel URL on an existing inventory fails closed and requires an explicit data migration or new database. Removed or suspended rows preserve their settings and existing Mongo/Influx history. They stop generating hosting probes; suspension does not fabricate a zero. Editorial probes remain independently governed by editorial configuration.

New rows are enabled but `pending`, with no query, so they cannot trigger probes until classified. All server types initially require classification; eggs, names and startup commands are not trusted as topology or Minecraft proof. Mark non-Minecraft or unwanted sources `excluded`, or disable their participation. Inventory tombstones and editorial configurations share a conservative combined budget of 1,000 records.

Select Java and/or Bedrock endpoints only from the server's public allocation IP/port. SRV is disabled for inventory queries; private addresses are never accepted. Domains and aliases require a separate verified mapping and are not enabled by this version. The ordinary public game probe still resolves and validates DNS for all other editorial destinations. No private collector or firewall exception is installed.

Optional `editorialId` reuses an existing editorial monitor only when its Java/Bedrock endpoints exactly equal the selected query and belong to this same inventory row's allocations. Another row cannot claim that editorial ID. The link is revalidated on reads; a changed editorial endpoint stops contributing until corrected. No editorial listing is created. Without a link, the technical monitor is separate and is absent from public `/v1/servers` and public history routes.

## API

All routes retain existing token authentication, body limits, global request budget and no browser CORS.

- `GET /v1/hosting/summary`, read/admin token: `{players:number|null,state:"complete"|"partial"|"unavailable",updatedAt:ISO|null,coverage:{eligible,responding,pending}}`. Contains no server IDs, client names or addresses.
- `GET /v1/admin/hosting/servers`, admin token: `{servers:HostingRow[],syncedAt:ISO|null,state:"current"|"stale"|"unavailable"}`.
- `PUT /v1/admin/hosting/servers/:id`, admin token: complete participation body `{revision,role,group,mode,authority,enabled,editorialId,query:{java,bedrock}}`. Returns the row. Revision must increase; identical retry at the same revision is idempotent, stale/changed equal revision returns 409, invalid query/topology/link returns 400, unknown inventory UUID returns 404. Sync changes to presence, suspension, name or allocations also increment revision, so the admin must refresh on conflict.

`HostingRow` has `id,monitorId,name,revision,role,group,mode,authority,enabled,editorialId,query,present,suspended,allocations:[{host,port}],snapshot`. Snapshot is nullable and retains the existing snapshot schema. Role is `standalone|backend|proxy|excluded|pending`; group is null or 1-64 ASCII letters/digits/underscore/hyphen, starting with a letter or digit. Modes are `backends|proxy`. `authority=true` is valid only for a proxy in proxy mode. Standalone has no group; backend and proxy require one.

## Aggregation and freshness

Standalone counts once. In a group with mode `backends`, only backends count; proxy 10 plus backends 4+6 totals 10. In mode `proxy`, exactly one proxy must be explicitly authoritative, and backends do not count. Disagreeing group modes or multiple authoritative proxies are pending coverage; no arbitrary proxy wins. Operators must verify that a proxy's reported population covers hosted members only. This cannot be inferred from inventory metadata.

Java/Bedrock use the existing summary selection (Java when responding, otherwise Bedrock), never addition. Overlapping selected monitor IDs or identical endpoint host/port pairs across separate rows are rejected from the total. Java and Bedrock sharing one endpoint in the same row still count once. The total represents simultaneous reported populations, not globally unique people; no individual player identifiers are collected.

Only online samples no older than 180 seconds, within three configured probe intervals, and matching the current configuration revision contribute. Zero requires a real valid response with zero players. `updatedAt` is the oldest contributing sample. Sources without valid readings reduce coverage; pending classification or inconsistent groups makes an otherwise readable subtotal partial. With no valid sources, players/updatedAt are null and state is unavailable. Removed rows are outside the current inventory scope; suspended selected rows remain missing coverage.

Inventory expires after three configured sync intervals. Then hosting probes stop and the public total becomes unavailable, even when old snapshots exist. A fetch failure retains previous inventory until that explicit expiry and logs `hosting_inventory_sync_failed` without secrets. Existing editorial collection continues. Restore/removal never erases retained history automatically. Mongo/Influx writes retain the existing non-atomic cross-database limitation.
