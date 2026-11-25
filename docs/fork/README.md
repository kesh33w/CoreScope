# Fork feature reference

Everything the SacMesh fork adds to upstream CoreScope, in one place. Each feature lists:
- what it does;
- how to turn it on;
- its default;
- where the detailed doc and the code live.

Config keys go in `config.json` (shared by the server and ingestor). Unless noted, a change needs a restart of the process that reads it.

- [Region filter](#region-filter)
  - [Frequency mode](#region-filter-frequency-mode)
  - [Region presets](#region-presets)
- [Ingest](#ingest)
  - [Observer reception frequency policy](#observer-reception-frequency-policy)
  - [Hop alias names](#hop-alias-names)
  - [Per-radio observer feeds](#per-radio-observer-feeds)
  - [Channel-hash prefilter](#channel-hash-prefilter)
- [Database](#database)
  - [Grouped writer commits](#grouped-writer-commits)
  - [Recoverable observation cleanup](#recoverable-observation-cleanup)
- [Analysis](#analysis)
  - [Observer comparison](#observer-comparison)
  - [Live path inference](#live-path-inference)
- [Operations](#operations)
  - [Private telemetry and operator control](#private-telemetry-and-operator-control)
  - [meshfeed publisher](#meshfeed-publisher)
  - [Deploying](#deploying)
- [Fixes to inherited behaviour](#fixes-to-inherited-behaviour)
- [Code map](#code-map)
- [Deploying](deploy.md) · [Syncing with upstream](upstream-sync.md)

---

## Region filter

### Region filter frequency mode

By default the region filter (`?region=`, the dropdown on every page) groups observers by IATA code. With frequency mode it groups them by the frequency their radio reports **and** by IATA code, so an instance that aggregates several networks can show one network, or one region of one network.

```json
"regionFilter": { "mode": "frequency" }
```

- The UI shows a **Frequency** dropdown (frequencies of observers seen in the last 7 days, e.g. `910.525 MHz`) beside a **Region** dropdown, which lists only the regions on the selected frequencies. Switching frequency drops selected regions that are not on it.
- `?region=` takes both kinds of token: numbers are frequencies in MHz, anything else an IATA code. Matching is OR within a kind and AND across kinds: `910.525,SJC,OAK` is SJC and OAK observers on 910.525.
- `regions` can label a frequency (`"910.525": "US 910"`) as well as a code. `regionQuickPicks` appear in the Region dropdown.
- An observer's frequency is its **current** `observers.radio`, read with the ingestor's unit rules and rounded to kHz. An observer that retunes takes its older packets with it; one that never reported a radio setting matches only selections without a frequency.
- A frequency on its own gives exactly the results of a frequency-only selection before regions could be combined (node home regions are computed per frequency as well as per frequency+region, then merged).
- Default `"iata"`: upstream behaviour and SQL, unchanged. WAN uses frequency mode; scope does not.
- Code: `cmd/server/region_frequency.go` (keys, matching, SQL, `/api/config/regions`), `node_home_regions.go`, `region_nodes.go`, `store.go`, `scope_audit.go`; frontend `public/region-filter.js` (`RegionFilter.observerKey`, `RegionFilter.keyMatches`, used by Live, Packets and Observers).

### Region presets

One code for a fixed selection of frequencies and regions. NodeScope (the iOS app) sends one `?region=` value at a time, so this is how its users can pick "910.525 MHz in the Bay Area"; the web Region dropdown shows the presets too.

```json
"regionFilter": {
  "mode": "frequency",
  "presets": [
    { "code": "BAY910", "label": "Bay Area 910", "regions": ["910.525", "OAK", "SFO", "SJC"] }
  ]
}
```

- Frequency mode only; ignored (with a startup log line) otherwise. Default: none.
- Codes are upper-cased and must not be a number, `All`, or contain `,` or `|`. Duplicates, empty presets and presets naming other presets are skipped and logged at startup.
- A middleware expands preset codes in `?region=` before any handler reads it, so every endpoint, cache and SQL filter sees plain tokens. `/api/config/regions` lists each code with its label; `/api/config/region-quick-picks` returns `presets` for the web UI, which expands a `?region=BAY910` link client-side as well.
- Several presets combine like any selection (OR within a kind, AND across kinds), so two presets on different frequencies also match each other's regions.
- WAN's presets: `BAY910`, `CV910`, `NV910`, `SOCAL927`.
- Code: `cmd/server/region_presets.go`; `public/region-filter.js` (`presetPicks`, `expandSelectedPresets`).

## Ingest

### Observer reception frequency policy

Per IATA region, the frequency observers there are expected to receive on. The ingestor drops receptions heard on another frequency **before** decoding, observation writes and node updates, so a misconfigured or retuned observer does not pollute that region's analytics.

```json
"observerFrequencyRules": {
  "SMF": { "mhz": 909.875, "toleranceKHz": 0.5, "maxStatusAgeSec": 1800, "dropUnknown": false }
}
```

- Default: no rules, nothing dropped. Up to 64 rules; edit them in **Customize → Frequency** (admin session or strong API key), then restart the ingestor.
- Uses the reception's own `radio` metadata, else the observer's fresh `/status` radio (per radio for multi-radio observers). Unknown or stale evidence is accepted unless `dropUnknown` is true. Nothing already stored is removed.
- Doc: [OBSERVER_FREQUENCY_FILTER.md](../../OBSERVER_FREQUENCY_FILTER.md). Code: `cmd/ingestor/observer_frequency.go`, `cmd/server/observer_frequency_config.go`, `public/customize-v2.js`.

### Hop alias names

HydraHop replaces its own path hop with a per-antenna alias that never advertises, so stock CoreScope shows those hops as unknown. HydraHop lists them in its MQTT status (`"hop_aliases": [{"key": "<64 hex>", "label": "BD6E g3910→rak910"}]`); with this on, the ingestor turns each into a named repeater row.

```json
"hopAliases": { "observers": ["<observer public key>"], "sources": ["SACMesh"] }
```

- Default: off. Accepted only from the listed observer keys, only via the listed MQTT sources (use one whose broker authenticates observers), and only from live status messages, not retained replays.
- The `hop_aliases` table records which `nodes` rows are aliases. A later status that drops an alias deletes it; a key that belongs to a real node or another observer is skipped. At most 64 per message, labels trimmed to 48 characters. Alias rows age out with `retention.nodeDays`.
- Additive schema: the empty `hop_aliases` table is created on start; older binaries ignore it.
- Code: `cmd/ingestor/hop_aliases.go`.

### Per-radio observer feeds

A multi-radio uploader (HydraHop) that sends `observer_radio_id` in its MQTT messages gets one observer per radio, with ID `<public key>@<radio>@<IATA>`, so one radio's region, frequency and metrics never overwrite another's.

- Applies only when the topic key is 64 hex characters and the radio and region are short safe tokens; every other observer keeps its plain ID.
- Node lookups, RF topology and the frequency policy use the underlying public key; observation filters use the full ID.
- Code: `cmd/ingestor/observer_radio_feed.go`, `cmd/server/observer_radio_feed.go`.

### Channel-hash prefilter

With a long `hashChannels` list (thousands of public hashtags), the ingestor checks a group message's one-byte channel hash before trying each key's HMAC and AES. A match is still fully validated. On a 2,703-name list this took one decode from about 4.1 ms to 0.6 ms.

- Always on; no setting. Doc: [docs/sacmesh/channel-decryption.md](../sacmesh/channel-decryption.md). Code: `cmd/ingestor/decoder.go`.

## Database

### Grouped writer commits

Each observation used to cost about three SQLite commits, each rewriting the same index pages into the WAL. Grouping commits cuts SSD writes several-fold on a busy feed (WAN: about 509 → 42 MB/min for the ingestor).

```json
"db": { "batchCommitMs": 4000, "walAutocheckpointPages": 50000, "synchronous": "NORMAL" }
```

- `batchCommitMs` (1–4000; default 0 = one commit per write): top-level writes share one transaction, committed once it is that old, before any PRAGMA/VACUUM/DDL and on close. An application transaction becomes a savepoint inside it, and a caller's own `BEGIN` passes through. **A crash or power loss can drop up to one interval of writes**; the database stays consistent, and the server sees rows up to one interval later.
- `walAutocheckpointPages` (default 0 = SQLite's 1000): a larger WAL so a page rewritten by many batches is copied to the database once per checkpoint.
- `synchronous`: `FULL` (default) or `NORMAL` for the ingestor.
- Observation cleanup commits each chunk at once under batch mode, so its progress is durable.
- WAN uses `batchCommitMs: 4000` and `walAutocheckpointPages: 50000`; scope uses neither.
- Code: `cmd/ingestor/batch_commit.go`, `internal/dbconfig/dbconfig.go`, `internal/dbschema/writer_dsn.go`.

### Recoverable observation cleanup

**Customize → Cleanup** lets an administrator archive and remove one observer's observations over a time range (for example, a period it was on the wrong frequency), and restore them later.

- Preview counts observations, distinct packets and packets only that selection supports; confirm queues the whole selection.
- The server stays read-only: it hands the request to the ingestor through a private filesystem queue. The ingestor works in 500-row transactions and archives complete rows in the same database before deleting; packets left with no observations go too. Archives are kept indefinitely.
- **Restart every server reading the database after a cleanup or restore**; in-memory analytics do not refresh otherwise.
- Doc: [observation-cleanup.md](../observation-cleanup.md). Code: `cmd/ingestor/observation_cleanup.go`, `cmd/server/observation_cleanup.go`, `internal/prunequeue/`, `public/observation-cleanup.js`.

## Analysis

### Observer comparison

**Observers → Compare** puts two receivers on the same RF settings side by side over 15 minutes to 24 hours: stored receptions, distinct messages, extra relay paths, and SNR paired on identical wire bytes. A before/after mode compares equal windows around a hardware change, with B as the reference. Private notes stay in the browser.

- Always on; on-demand reads with bounded rows, time and concurrency. Doc: [observer-comparison.md](../observer-comparison.md). Code: `cmd/server/observer_comparison.go`, `public/compare.js`.

### Live path inference

The Live map resolves each observation's path with the same whole-path resolver as packet details, anchored to that observation's receiver and sender. Unknown hops keep their place in the path but get no coordinates; animation and clickable paths stop at each gap. Upstream's interpolated-hop toggle is removed (and dropped from settings sync).

- Always on. Doc: [live-path-inference.md](../live-path-inference.md). Code: `public/live.js`, `cmd/server/settings_allowlist.go`.

## Operations

### Private telemetry and operator control

Two local Unix sockets for the SacMesh connection monitor; both off unless their flag is set, and neither is part of the public API.

| Flag (server) | Purpose |
|---|---|
| `-telemetry-socket PATH` | Read-only connection telemetry: WebSocket sessions, verified proxy chain, allowlisted page names (never URLs or query strings). Never touches the database. |
| `-telemetry-source NAME` | Label for this instance in the telemetry (default `corescope`). |
| `-telemetry-trusted-proxies CIDRs` | Proxies whose `X-Forwarded-For` the telemetry trusts. |
| `-operator-control-socket PATH`, `-operator-control-policy PATH`, `-operator-control-uid UID` | IP blocking for WebSocket admission, persisted in the policy file. Only a local peer with the given OS UID can use the socket. |

- Code: `cmd/server/telemetry.go`, `cmd/server/operator_control.go`, `cmd/server/websocket.go`.

### meshfeed publisher

`cmd/meshfeed-publisher` is a separate binary for [feed.sacmesh.org](https://feed.sacmesh.org): it subscribes to meshfeed's raw stream and publishes decoded packets, merged per-packet reports, a retained node directory and observer roster, and health. Its `decoder.go` and `util.go` are copies of the ingestor's; re-copy them when the ingestor decoder changes.

- Doc: [cmd/meshfeed-publisher/README.md](../../cmd/meshfeed-publisher/README.md). Build: `make build-meshfeed-publisher`.

### Deploying

`scripts/sacmesh-deploy.sh` deploys a release tag to scope and WAN with backups and automatic rollback. See [deploy.md](deploy.md).

## Fixes to inherited behaviour

Changes to upstream behaviour that are on whether or not any fork feature is configured:

- **Ingest durability:** a packet's writes commit together. `synchronous` defaults to `FULL`.
- **Bounded legacy advert backfill:** the `from_pubkey` backfill pages through 256 rows at a time and releases the writer between pages, instead of an upfront COUNT that could hold the writer for minutes. See [BOUNDED_BACKFILL.md](../../BOUNDED_BACKFILL.md).
- **Historical packet queries** fall back to SQLite when the requested interval is no longer fully in memory.
- **Cache eviction** releases secondary references and clears removed slice entries.
- **Region-filtered neighbor graph** was always empty because of an observer-ID case mismatch (sent upstream as Kpa-clawbot/CoreScope#2478).
- **v3.19 observers rebuild:** fork code joins observers on the new `idx` key, the batched writer accepts the rebuild's own `BEGIN`, and cleanup archives copy rows by column name.

## Code map

| Area | Files |
|---|---|
| Region filter | `cmd/server/region_frequency.go`, `region_presets.go`, `node_home_regions.go`, `region_nodes.go`, `scope_audit.go`; `public/region-filter.js` |
| Ingest | `cmd/ingestor/observer_frequency.go`, `hop_aliases.go`, `observer_radio_feed.go`, `decoder.go`, `advert_route_evidence.go` |
| Database writer | `cmd/ingestor/batch_commit.go`, `db.go`, `maintenance.go`, `observation_cleanup.go`; `internal/dbconfig`, `internal/dbschema`, `internal/prunequeue` |
| Server | `cmd/server/observer_comparison.go`, `observation_cleanup.go`, `observer_frequency_config.go`, `observer_radio_feed.go`, `telemetry.go`, `operator_control.go`, `neighbor_api.go`, `store.go` |
| Frontend | `public/region-filter.js`, `live.js`, `compare.js`, `observation-cleanup.js`, `customize-v2.js`, `observers.js`, `packets.js` |
| Tooling | `scripts/sacmesh-deploy.sh`, `cmd/meshfeed-publisher/` |
