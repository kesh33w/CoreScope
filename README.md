# CoreScope: SacMesh fork

This is SacMesh's fork of [CoreScope](https://github.com/Kpa-clawbot/CoreScope), the self-hosted MeshCore packet analyzer: an MQTT ingestor, a Go web server and API, and a browser UI with live map, packets, channels and analytics. The fork runs two SacMesh instances from one build:

| Instance | What it shows | Region filter |
|---|---|---|
| [scope.sacmesh.org](https://scope.sacmesh.org) | SacMesh's own observers (SMF, SCK, OAK, SFO), with a 909.875 MHz policy for SMF and HydraHop hop names | IATA codes |
| [wan.sacmesh.org](https://wan.sacmesh.org) | An aggregate of several West Coast networks, fed by [meshfeed](cmd/meshfeed-publisher/README.md) | Frequency + region, with presets |

Everything the fork adds is **opt-in**. With none of its configuration keys or flags set, CoreScope behaves like upstream, apart from the bug fixes listed in the [feature reference](docs/fork/README.md#fixes-to-inherited-behaviour).

| | |
|---|---|
| **Main line** | [`master`](https://github.com/kesh33w/CoreScope/tree/master): what runs on both instances |
| **Releases** | `v<upstream>+sacmesh.N` tags, e.g. `v3.19.0+sacmesh.5`. Changes: [CHANGELOG.md](CHANGELOG.md) |
| **Upstream base** | `Kpa-clawbot/CoreScope` `v3.19.0` (24 Nov 2026): see [syncing with upstream](docs/fork/upstream-sync.md) |
| **Upstream docs** | [UPSTREAM-README.md](UPSTREAM-README.md): features, performance, Docker install, configuration |

## What this fork changes

Full reference, with every config key, flag and default: **[docs/fork/README.md](docs/fork/README.md)**.

| Area | Change | Default |
|---|---|---|
| **Region filter** | [Frequency mode](docs/fork/README.md#region-filter-frequency-mode): a Frequency dropdown beside the Region one, so a multi-network instance can be viewed one network, and one region of it, at a time. | Off (IATA) |
| | [Region presets](docs/fork/README.md#region-presets): one code for a frequency plus a set of regions (`BAY910`), for apps such as NodeScope that send one region at a time. | None |
| **Ingest** | [Observer reception frequency policy](docs/fork/README.md#observer-reception-frequency-policy): drop receptions an observer heard on the wrong frequency for its region, before they reach analytics. | Off |
| | [Hop alias names](docs/fork/README.md#hop-alias-names): trusted observers (HydraHop) name path hops that never advertise. | Off |
| | [Per-radio observer feeds](docs/fork/README.md#per-radio-observer-feeds): a multi-radio observer's radios are stored as separate observers. | On when the uploader sends `observer_radio_id` |
| | [Channel-hash prefilter](docs/fork/README.md#channel-hash-prefilter) for large public hashtag lists. | Always on |
| **Database** | [Grouped writer commits](docs/fork/README.md#grouped-writer-commits) and WAL checkpoint tuning, for SSD wear. | Off |
| | [Recoverable observation cleanup](docs/fork/README.md#recoverable-observation-cleanup): archive and remove one observer's bad time range, with restore. | Admin-only |
| **Analysis** | [Observer comparison](docs/fork/README.md#observer-comparison): two receivers side by side, before and after a hardware change. | Always on |
| | [Live path inference](docs/fork/README.md#live-path-inference) anchored to each receiver, with gaps for unknown hops. | Always on |
| **Operations** | [Private connection telemetry and operator control](docs/fork/README.md#private-telemetry-and-operator-control) over local Unix sockets. | Off |
| | [meshfeed publisher](docs/fork/README.md#meshfeed-publisher): derived MQTT topics (decoded, merged, node directory). | Separate binary |
| | [Deploy script](docs/fork/deploy.md) for scope and WAN, with automatic rollback. | n/a |

## Build, test and deploy

```sh
make build                   # host binaries into dist/
make test && make vet        # Go tests
sh test-all.sh               # frontend unit suites (needs Node)
```

- **Deploying to scope or WAN:** check out the release tag and run `scripts/sacmesh-deploy.sh [--dry-run] wan|scope|both`. See [docs/fork/deploy.md](docs/fork/deploy.md).
- **Contributing:** work on a `sacmesh/<topic>` branch and open a PR to `master`; PRs are merged with a merge commit. Upstream's GitHub workflows do not run on this fork, so the Go and frontend suites above are the gate. Tag a release on `master` after merging.
- **Upstream merges:** see [docs/fork/upstream-sync.md](docs/fork/upstream-sync.md) for the checks the fork's changes need.

Fixes that make sense upstream are sent there too, e.g. [Kpa-clawbot/CoreScope#2478](https://github.com/Kpa-clawbot/CoreScope/pull/2478). Licensing and attribution are upstream's; this is not an upstream release.
