# Fork maintenance

This fork retains upstream CoreScope history. Release `v3.18.0+sacmesh.1` merges upstream `master` after `v3.18.0` (through `6c78a1d2`) into the SacMesh changes; `v3.17.0+sacmesh.1` and `.2` were built on the upstream `v3.17.0` tag. The previously deployed `v3.15.0+sacmesh.1` remains a separate tested release, not a second fork. It keeps upstream licensing and attribution. It is not an upstream release.

## Included changes

- A packet's ingestor writes commit together. The database accepts an explicit `NORMAL` or `FULL` synchronous setting, with `FULL` as the default.
- Scoped observer-radio records keep their observation identity while node lookups use the underlying public key.
- Historical packet queries fall back to SQLite when the requested interval is no longer fully cached.
- Cache eviction releases secondary references and clears removed slice entries. Hash migration does not restore observations already evicted from memory.
- Optional private connection telemetry reports session status and verified proxy provenance. Browser page reports contain only allowlisted page names, never URLs or query strings. Detailed telemetry is not part of the public API.
- Live-map path inference anchors observations to the receiver and preserves unknown-hop gaps. The removed inferred-hop toggle is no longer included in settings sync.
- **Hop alias names** (`hopAliases`, off by default). Trusted observers can name path-hop identities that never advertise, such as HydraHop's per-antenna aliases. See [Hop alias names](#hop-alias-names).

## Observer comparisons

The comparison page distinguishes stored receptions, distinct messages and alternate relay paths. It pairs signal measurements only when reception evidence matches, and labels truncated samples. Local experiment notes stay in the browser and do not alter measurement history. See [Observer comparison](observer-comparison.md).

## One source, separate instances

Build the web server, ingestor and frontend assets from the same reviewed source revision. Use one release version and record the source commit and artifact hashes. Keep each instance's configuration and databases separate.

A release must not replace site-specific broker subscriptions, region filters, blacklists, retention settings, memory limits, private telemetry paths or trusted-proxy lists. Changing a binary is not permission to change those settings.

The web server keeps the analyzer database read-only. The ingestor remains the only writer of measurements and schema changes. The only analyzer schema change of the SacMesh fork is the additive `hop_aliases` table (created empty on start; older binaries ignore it). The newer upstream base adds observer radio history and neighbor-report tables. With accounts enabled, it also upgrades the separate users database from schema v6 to v8; older binaries cannot open that upgraded users database. Back up both databases before deployment and test rollback on copies. There is no further upstream database migration from v3.16 to v3.17. Merging source does not upgrade a running instance.

From v3.17 to v3.18 upstream adds two migrations. The ingestor runs the content-hash migration `content_hash_payload_bits_v1` once (#2409): it scans every packet by id. It rewrites stale hashes and merges any duplicates the rewrite creates; the fork's server-side copy of this migration is gone. Its status is a row in `_async_migrations`, which older binaries ignore; a merge is not undone by a rollback. With user accounts on, `users.db` moves to schema v9 (#2415), which v3.17 binaries refuse; neither SacMesh instance enables accounts. The UI's live feed now connects to `/ws` (#2383), so a proxy must forward WebSocket upgrades on `/ws` as well as `/`.

## Release checks

Run `make test`, `make vet` and `sh test-all.sh`. Run focused race tests for changed concurrent code. Validate the frontend in a browser before deployment.

Build with CGO enabled. A build with CGO disabled can link a SQLite stub and still appear successful. Confirm the packaged binary runs against a real test database.

Check the running executable and frontend hashes after deployment. A container tag alone does not prove which files are running. Health/version checks must account for `CORESCOPE_VERSION` and `.image-version`, which override the baked version.

Keep the previous tested artifact for rollback. For rollback from the v3.16-or-newer account schema, restore the matching pre-upgrade account database as well as application files. Test the analyzer rollback on a copy; do not assume compatibility from additive tables alone. Upgrade one instance at a time and check ingestion, API behavior and client reconnection before continuing.

## Single-broker setup

Use exactly one MQTT source for a broker-only instance. Subscribe to that broker's `meshcore/+/+/packets` and `meshcore/+/+/status` topics. Leave `iataFilter` and `observerIATAWhitelist` empty only when every region from that broker is wanted. Broker selection controls source isolation; geographic filtering does not.

Keep separate container names, ports, configuration, data directories and databases for each instance. Never point a new instance at another instance's writable database. The `regions` object supplies display labels, not an ingestion allowlist. Keep credentials, channel secrets, runtime configuration and databases outside Git and the image build context.

## Map tile provider follow-up

The fork includes upstream change `a0b4e6cf` (part of v3.18.0) for consistent light/dark tile selection, late configuration and node-map refresh. Map and Live also refresh the existing layer's maximum zoom and attribution when switching providers, including label overlays. Re-registering the layer lets Leaflet recalculate its zoom limits; it does not add packet-processing work.

Explicit operator tile URL overrides now take precedence consistently on these maps. Without an override, the existing customizer provider selection and server defaults apply. No configuration keys or defaults are added by this follow-up. Test fresh loads, provider/theme changes and zoom limits before deploying to an instance with custom tile URLs. This source change alone does not update running instances.

## Hop alias names

HydraHop replaces its own path hop with a per-antenna alias that records which antenna heard a packet and which one sent it. Aliases never advertise, so a stock CoreScope shows them as unknown hops. HydraHop lists them in its MQTT status message:

```json
"hop_aliases": [{"key": "<64 hex>", "label": "BD6E g3910→rak910"}]
```

With `hopAliases` configured, the ingestor accepts that list only from the named observer keys, and only via the named MQTT sources (use a source whose broker authenticates observers). Each alias becomes a `nodes` row with role `repeater`, no location and its label as the name. Path resolution, `/api/nodes` and the browser hop resolver then name it with no server or frontend change.

```json
"hopAliases": {
  "observers": ["<observer public key>", "..."],
  "sources": ["SACMesh"]
}
```

- A scoped observer ID (`<key>@<radio>@<IATA>`) matches by its key. Only live status messages apply; a retained replay does not.
- The table `hop_aliases` records which `nodes` rows are aliases and which observer owns each. A later status that drops an alias deletes its row. An alias key that already belongs to a real node, or to another trusted observer, is skipped.
- At most 64 aliases per message; labels are trimmed to 48 characters with control characters removed.
- Alias rows age out with `retention.nodeDays` like any node if the observer stops reporting.
- Alias rows appear in node lists as repeaters without a location, and count toward node totals.

## Region filter by frequency

`regionFilter.mode: "frequency"` (default `"iata"`) makes the region filter group observers by the frequency their radio reports as well as by IATA code. WAN uses it: a Frequency dropdown picks a network (910.525, 909.875, 927.875 MHz, ...) and a Region dropdown beside it narrows to regions on that network; scope keeps IATA mode. Both run the same build.

- `?region=` takes both kinds of token in this mode: numbers are frequencies, the rest IATA codes. An observer matches when its frequency is one of the selected ones (if any) AND its code is one of the selected ones (if any): `910.525,SJC,OAK` is SJC and OAK on 910.525.
- Each observer's region key is `"<frequency>|<IATA>"` (`observerRegionKey`), either part possibly empty; `regionSelection.matchesKey` / `regionKeyMatches` do the matching, and `RegionFilter.observerKey` / `RegionFilter.keyMatches` mirror them in the frontend.
- Node home-region placements are computed twice in this mode and merged (`mergeFrequencyPlacements`): over the pair keys, and over the frequency alone (`frequencyOnlyKeys`). A frequency-only selection matches only the bare frequencies, so its results are exactly those from before regions could be combined (a node heard by several cities on one frequency would otherwise have its plurality split between them). Analytics home regions do the same. `region_placement.regions` in `/api/nodes` therefore lists both kinds, e.g. `["910.525", "910.525|SJC"]`.

- One switch for the whole deployment, in `cmd/server/region_frequency.go`. Every region path keys on one observer → region-key mapping: `observerRegionKeys` (node home regions, `observerIATAs`), `regionObsMatcher`/`regionObsCond` (every SQL `?region=` filter), `GetObserverIdsForRegion` (in-memory region caches). In IATA mode `regionObsMatcher` emits the original `UPPER(TRIM(iata)) IN (...)`.
- An observer's frequency is its current `observers.radio`, read like the ingestor's `parseObserverFrequency` (unit suffix or magnitude-scaled Hz/kHz) and rounded to kHz. A retuned observer takes its old packets with it. `observer_radios` history is not used.
- `/api/config/regions` lists the frequencies and IATA codes of observers seen in the last 7 days; `regions` may label either. `/api/config/region-quick-picks` sends `mode: "frequency"`, the configured picks (offered in the Region dropdown) and `frequencyRegions` (codes per frequency, which narrow the Region dropdown); the frontend reads them there (`RegionFilter.isFrequencyMode`) to show the two dropdowns, and matches observers client-side on Live, Packets and Observers with `RegionFilter.keyMatches`.
- On upstream merges, any new SQL that matches `obs.iata` against a `?region=` value must go through `regionObsMatcher`; `grep -n "iata)) IN" cmd/server` should list only `region_frequency.go` and `scope_audit.go` (`observerRegionClause`, which matches frequencies on its caller's transaction).
