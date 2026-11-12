# Fork maintenance

This release uses upstream CoreScope v3.15.0 as its base. It keeps upstream licensing and attribution. It is not an upstream release.

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

The web server keeps the analyzer database read-only. The ingestor remains the only writer of measurements and schema changes. This release adds no analyzer schema migration.

## Release checks

Run `make test`, `make vet` and `sh test-all.sh`. Run focused race tests for changed concurrent code. Validate the frontend in a browser before deployment.

Build with CGO enabled. A build with CGO disabled can link a SQLite stub and still appear successful. Confirm the packaged binary runs against a real test database.

Check the running executable and frontend hashes after deployment. A container tag alone does not prove which files are running. Health/version checks must account for `CORESCOPE_VERSION` and `.image-version`, which override the baked version.

Keep the previous tested artifact for rollback. Rollback changes application files, not measurement history. Upgrade one instance at a time and check ingestion, API behavior and client reconnection before continuing.
