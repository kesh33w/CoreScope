# Deploying a fork release to WAN and scope

`scripts/sacmesh-deploy.sh` deploys a tagged fork release to SacMesh's two CoreScope instances on Borguntu:

| Instance | Directory | Services restarted | Health check |
|---|---|---|---|
| WAN (wan.sacmesh.org) | `~/code/corescope-mqtt` | `ingestor`, `server` | `http://10.20.0.10:18082/api/health` |
| scope (scope.sacmesh.org) | `~/services/sacscope` | `corescope` | `http://127.0.0.1:18083/api/health` |

## Use

```bash
git fetch kesh --tags && git checkout v3.19.0+sacmesh.5
scripts/sacmesh-deploy.sh --dry-run both   # build the images only
scripts/sacmesh-deploy.sh both             # wan first, then scope
```

It calls `sudo` for docker and for scope's server binary, so run it from a terminal. Without one (an agent), give sudo an askpass helper: put a `sudo` shim that runs `/usr/bin/sudo -A "$@"` first on `PATH` and set `SUDO_ASKPASS`.

`go`, `zig` (for the static musl build; `pip install ziglang` in a venv plus a `zig` wrapper on `PATH` works), `docker` through `sudo`, `curl` and `python3` must be available. `WAN_DIR`, `SCOPE_DIR`, `WAN_HEALTH`, `SCOPE_HEALTH`, `HEALTH_TIMEOUT` (seconds, default 300) and `DOCKER` (default `sudo docker`) override the defaults.

## What it does

1. Refuses a dirty checkout, a HEAD that is not exactly on a `v*+sacmesh.N` tag, or an instance that is unhealthy before it starts. Skips an instance already on the commit.
2. Builds the static ingestor and server once (`make crossbuild-*-linux-amd64`).
3. Per instance: stages the binaries, `public/`, `.git-commit`, `.image-version` and a Dockerfile `FROM` the image the instance runs now (WAN: `deploy/wan-<version>-<time>/`; scope: `build-<version>-<commit>/`), and builds `corescope:wan-<version>-<commit>` or `sacmesh-scope-picker:<version>-<commit>`.
4. Backs up `compose.yaml` (and scope's bind-mounted `caddy/operator-release/corescope-server`) as `*.before-<version>-<time>`, points compose at the new image, installs scope's server as `1000:1000` mode 500 (root-owned, the container cannot run it), and restarts only the CoreScope services with `--no-deps`.
5. Waits for `/api/health` to report the new commit and checks `/api/packets`. On any failure it restores the backups, restarts, and waits for the old commit.
6. Writes the rollback steps to `ROLLBACK.txt` in the staging folder.

## What it does not do

- Database migrations, `config.json` edits or new compose settings. A release that needs any of these gets its own plan, a DB backup and a tested rollback first; the automatic rollback only restores binaries and the image.
- The instance READMEs and the NAS `AGENTS.md` approval note: add the release section by hand after the deploy.
- Clean up images: each release is a layer on the previous one. Prune old `corescope:wan-*` and `sacmesh-scope-picker:*` tags once the release has run for a while.
