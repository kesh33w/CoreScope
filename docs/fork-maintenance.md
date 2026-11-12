# Fork maintenance

This fork retains upstream CoreScope history. Release `v3.17.0+sacmesh.1` integrates the unified SacMesh changes with the upstream `v3.17.0` tag. The previously deployed `v3.15.0+sacmesh.1` remains a separate tested release, not a second fork. It keeps upstream licensing and attribution. It is not an upstream release.

## Included changes

- A packet's ingestor writes commit together. The database accepts an explicit `NORMAL` or `FULL` synchronous setting, with `FULL` as the default.
- Scoped observer-radio records keep their observation identity while node lookups use the underlying public key.
- Historical packet queries fall back to SQLite when the requested interval is no longer fully cached.
- Cache eviction releases secondary references and clears removed slice entries. Hash migration does not restore observations already evicted from memory.
- Optional private connection telemetry reports session status and verified proxy provenance. Browser page reports contain only allowlisted page names, never URLs or query strings. Detailed telemetry is not part of the public API.
- Live-map path inference anchors observations to the receiver and preserves unknown-hop gaps. The removed inferred-hop toggle is no longer included in settings sync.

## Observer comparisons

The comparison page distinguishes stored receptions, distinct messages and alternate relay paths. It pairs signal measurements only when reception evidence matches, and labels truncated samples. Local experiment notes stay in the browser and do not alter measurement history. See [Observer comparison](observer-comparison.md).

## One source, separate instances

Build the web server, ingestor and frontend assets from the same reviewed source revision. Use one release version and record the source commit and artifact hashes. Keep each instance's configuration and databases separate.

A release must not replace site-specific broker subscriptions, region filters, blacklists, retention settings, memory limits, private telemetry paths or trusted-proxy lists. Changing a binary is not permission to change those settings.

The web server keeps the analyzer database read-only. The ingestor remains the only writer of measurements and schema changes. The SacMesh changes add no analyzer schema migration of their own. The newer upstream base adds observer radio history and neighbor-report tables. With accounts enabled, it also upgrades the separate users database from schema v6 to v8; older binaries cannot open that upgraded users database. Back up both databases before deployment and test rollback on copies. There is no further upstream database migration from v3.16 to v3.17. Merging source does not upgrade a running instance.

## Release checks

Run `make test`, `make vet` and `sh test-all.sh`. Run focused race tests for changed concurrent code. Validate the frontend in a browser before deployment.

Build with CGO enabled. A build with CGO disabled can link a SQLite stub and still appear successful. Confirm the packaged binary runs against a real test database.

Check the running executable and frontend hashes after deployment. A container tag alone does not prove which files are running. Health/version checks must account for `CORESCOPE_VERSION` and `.image-version`, which override the baked version.

Keep the previous tested artifact for rollback. For rollback from the v3.16-or-newer account schema, restore the matching pre-upgrade account database as well as application files. Test the analyzer rollback on a copy; do not assume compatibility from additive tables alone. Upgrade one instance at a time and check ingestion, API behavior and client reconnection before continuing.

## Single-broker setup

Use exactly one MQTT source for a broker-only instance. Subscribe to that broker's `meshcore/+/+/packets` and `meshcore/+/+/status` topics. Leave `iataFilter` and `observerIATAWhitelist` empty only when every region from that broker is wanted. Broker selection controls source isolation; geographic filtering does not.

Keep separate container names, ports, configuration, data directories and databases for each instance. Never point a new instance at another instance's writable database. The `regions` object supplies display labels, not an ingestion allowlist. Keep credentials, channel secrets, runtime configuration and databases outside Git and the image build context.
