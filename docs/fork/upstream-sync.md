# Syncing with upstream

The fork keeps upstream CoreScope's full history and merges upstream releases into `master`; it is never rebased. The current base is upstream `v3.19.0`. What the fork adds is listed in the [feature reference](README.md).

## Merging an upstream release

1. Fetch upstream (`git fetch origin --tags`, where `origin` is `Kpa-clawbot/CoreScope`) and branch from `master`: `sacmesh/upstream-vX.Y.Z`.
2. Merge the upstream tag. Resolve conflicts by keeping both sides: the fork's changes are mostly separate files, plus hooks in `routes.go`, `store.go`, `config.go`, the ingestor's `db.go`/`main.go` and `public/region-filter.js`.
3. Read upstream's release notes (`docs/release-notes/`) for **migrations**. Each one is a deploy decision: note whether it is one-way and what a rollback needs (v3.19's `observers` rebuild, for example, needs a database restore).
4. Run the [fork-specific checks](#fork-specific-checks) below, then `make test`, `make vet` and `sh test-all.sh`.
5. Test on copies of both instances' databases, offline (`--network none`): start the ingestor (migrations), then compare the server's main endpoints old vs new (`/api/nodes`, `/api/packets`, `/api/channels`, `/api/observers`, analytics) for All and a few regions or frequencies.
6. Open a PR to `master`, merge with a merge commit, tag `vX.Y.Z+sacmesh.1`, and [deploy](deploy.md) one instance at a time.
7. Update the upstream base in the [README](../../README.md), the CHANGELOG and these docs.

## Fork-specific checks

- **Region SQL:** every SQL filter that matches `obs.iata` against a `?region=` value must go through `regionObsMatcher`, or frequency mode breaks. `grep -n "iata)) IN" cmd/server` should list only `region_frequency.go` and `scope_audit.go` (`observerRegionClause`).
- **Region parameter:** handlers must read the region from the `region` query parameter, or presets are not expanded (`regionPresetMiddleware`).
- **Observer joins:** since v3.19 `observers` has an explicit `idx` key; fork queries join on it.
- **Batched writer:** with `db.batchCommitMs` set, an upstream migration that opens its own transaction must still run (`batch_commit.go` passes a caller's `BEGIN` through). Start the ingestor on a database copy with batching on, as WAN runs it.
- **Observation cleanup archives** copy rows by column name, so a column upstream adds to `observations`, `transmissions` or `observers` is picked up; check the restore tests still pass.
- **meshfeed publisher:** if upstream changed `cmd/ingestor/decoder.go` or `util.go`, copy them into `cmd/meshfeed-publisher/` again.
- **Frontend:** Live, Packets and Observers must still filter client-side with `RegionFilter.keyMatches`, and the Live map must not regain the interpolated-hop toggle.

## Releases and instances

- One source revision builds the ingestor, server and frontend for both instances. Record the commit and binary hashes. Health reports `CORESCOPE_VERSION`/`.image-version`, so check the commit too.
- A release never changes an instance's configuration: broker subscriptions, region filter, blacklists, retention, memory limits, telemetry paths and trusted proxies stay as they are. Changing a binary is not permission to change those.
- The server keeps the analyzer database read-only; the ingestor is the only writer of measurements and schema. The fork's own schema changes are additive (`hop_aliases`, observation-cleanup archive tables); older binaries ignore them.
- Build with CGO enabled. A CGO-disabled build can link a SQLite stub and still look successful; confirm the packaged binary runs against a real test database.
- Keep the previous image (and scope's previous server binary) for rollback. For a release with a one-way migration, back up each database first and test the rollback on a copy.

## Running a broker-only instance

Use exactly one MQTT source and subscribe to `meshcore/+/+/packets` and `meshcore/+/+/status`. Leave `iataFilter` and `observerIATAWhitelist` empty only when every region from that broker is wanted: the broker choice isolates sources, geography does not. Keep separate container names, ports, configuration, data directories and databases per instance, and never point a new instance at another instance's writable database. `regions` only labels codes; it is not an ingestion allowlist. Keep credentials, channel secrets, runtime configuration and databases out of Git and the image build context.

## Release history

| Tag | Upstream base | Highlights |
|---|---|---|
| `v3.18.0+sacmesh.1` | v3.18.0 + upstream `master` to `6c78a1d2` | Upstream v3.18 with the SacMesh changes so far; hop alias names |
| `v3.18.0+sacmesh.2` | same | Region filter frequency mode |
| `v3.18.0+sacmesh.3` | same | Region dropdown beside the Frequency one |
| `v3.19.0+sacmesh.1` | v3.19.0 | Upstream v3.19 (one-way `observers` rebuild) |
| `v3.19.0+sacmesh.2` | v3.19.0 | Cleanup commits each chunk under batched writes |
| `v3.19.0+sacmesh.3`, `.4` | v3.19.0 | Region-filtered neighbor graph fix (`.3`'s hop tie-breaker removed again in `.4`) |
| `v3.19.0+sacmesh.5` | v3.19.0 | Region presets; deploy script |

Earlier, `v3.15.0+sacmesh.1` and `v3.17.0+sacmesh.1`/`.2` were built on those upstream tags. Details: [CHANGELOG](../../CHANGELOG.md).
