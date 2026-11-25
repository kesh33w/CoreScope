#!/usr/bin/env bash
# sacmesh-deploy.sh: build the tagged fork commit and deploy it to SacMesh's
# CoreScope instances (WAN and scope) on Borguntu.
#
#   git checkout v3.19.0+sacmesh.5
#   scripts/sacmesh-deploy.sh [--dry-run] wan|scope|both
#
# For each instance it builds a derived image FROM the image the instance
# runs now (only the ingestor, server and public/ replaced), backs up
# compose.yaml (and scope's bind-mounted server), switches compose.yaml to
# the new image, restarts only the CoreScope services, and waits for
# /api/health to report the new commit. If that does not happen it puts the
# backups back and restarts again. Nothing touches the databases or
# config.json, so a deploy that needs a schema change or config edit still
# needs its own plan. --dry-run builds everything, including the image, then
# removes the staging folder and changes no running instance.
#
# Needs: a clean checkout at an exact v*+sacmesh.N tag, go, zig (static musl
# cross-build, see Makefile), docker (through sudo unless DOCKER is set),
# curl and python3. Paths are SacMesh's; WAN_DIR, SCOPE_DIR, WAN_HEALTH and
# SCOPE_HEALTH override them. See docs/sacmesh-deploy.md.
set -euo pipefail

DRY_RUN=0
if [[ "${1:-}" == "--dry-run" ]]; then DRY_RUN=1; shift; fi
TARGET="${1:-}"
case "$TARGET" in
  wan) INSTANCES=(wan) ;;
  scope) INSTANCES=(scope) ;;
  both) INSTANCES=(wan scope) ;;
  *) echo "usage: $0 [--dry-run] wan|scope|both" >&2; exit 2 ;;
esac

WAN_DIR="${WAN_DIR:-$HOME/code/corescope-mqtt}"
WAN_HEALTH="${WAN_HEALTH:-http://10.20.0.10:18082/api/health}"
SCOPE_DIR="${SCOPE_DIR:-$HOME/services/sacscope}"
SCOPE_HEALTH="${SCOPE_HEALTH:-http://127.0.0.1:18083/api/health}"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-300}"
read -r -a DOCKER <<<"${DOCKER:-sudo docker}"

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# --- preflight ---------------------------------------------------------------
REPO="$(git rev-parse --show-toplevel)"
cd "$REPO"
[[ -z "$(git status --porcelain)" ]] || die "the checkout has uncommitted changes"
VERSION="$(git describe --tags --exact-match HEAD 2>/dev/null)" ||
  die "HEAD is not at a tag; check out the v*+sacmesh.N tag to deploy"
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+\+sacmesh\.[0-9]+$ ]] ||
  die "tag $VERSION is not a v*+sacmesh.N release tag"
COMMIT="$(git rev-parse --short=8 HEAD)"
SLUG="${VERSION/+/-}-$COMMIT"            # v3.19.0-sacmesh.5-1234abcd
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
for tool in go zig curl python3; do
  command -v "$tool" >/dev/null || die "$tool not found on PATH"
done
"${DOCKER[@]}" version --format '{{.Server.Version}}' >/dev/null || die "docker is not usable as: ${DOCKER[*]}"
log "deploying $VERSION ($COMMIT) to ${INSTANCES[*]}$([[ $DRY_RUN == 1 ]] && echo ' (dry run)')"

# --- build -------------------------------------------------------------------
make crossbuild-ingestor-linux-amd64 crossbuild-server-linux-amd64 VERSION="$VERSION" >/dev/null
INGESTOR="$REPO/dist/corescope-ingestor-linux-amd64"
SERVER="$REPO/dist/corescope-server-linux-amd64"
file "$SERVER" | grep -q 'statically linked' || die "server binary is not static"
log "built ingestor $(sha256sum "$INGESTOR" | cut -c1-12), server $(sha256sum "$SERVER" | cut -c1-12)"

# --- helpers -----------------------------------------------------------------
# compose_image DIR SERVICE: the image a compose service is configured with.
compose_image() {
  (cd "$1" && "${DOCKER[@]}" compose config --format json) |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["services"][sys.argv[1]]["image"])' "$2"
}

# swap_image FILE OLD NEW: replace exactly "image: OLD" lines with NEW.
swap_image() {
  python3 - "$1" "$2" "$3" <<'PY'
import re, sys
path, old, new = sys.argv[1:]
text = open(path).read()
pat = re.compile(r'^(\s*image:\s*)' + re.escape(old) + r'\s*$', re.M)
out, n = pat.subn(lambda m: m.group(1) + new, text)
if n == 0:
    sys.exit("no 'image: %s' line in %s" % (old, path))
open(path, 'w').write(out)
print(n)
PY
}

# health_commit URL: the commit /api/health reports, or "" when it is down.
health_commit() {
  curl -fsS --max-time 5 "$1" 2>/dev/null |
    python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("commit","") if d.get("status")=="ok" else "")' 2>/dev/null || true
}

# wait_health URL COMMIT: poll until /api/health reports COMMIT.
wait_health() {
  local deadline=$((SECONDS + HEALTH_TIMEOUT)) got=""
  while ((SECONDS < deadline)); do
    got="$(health_commit "$1")"
    [[ "$got" == "$2" ]] && return 0
    sleep 5
  done
  log "health at $1 reports '${got:-down}', wanted $2"
  return 1
}

# --- per instance ------------------------------------------------------------
deploy_instance() {
  local name="$1" dir health services image_repo stage chown_flag bind=""
  case "$name" in
    wan)
      dir="$WAN_DIR" health="$WAN_HEALTH" services=(ingestor server)
      image_repo="corescope:wan-$SLUG" stage="$dir/deploy/wan-$VERSION-$STAMP"
      chown_flag="--chown=1000:1000" ;;
    scope)
      dir="$SCOPE_DIR" health="$SCOPE_HEALTH" services=(corescope)
      image_repo="sacmesh-scope-picker:$SLUG" stage="$dir/build-$SLUG"
      chown_flag="" bind="$dir/caddy/operator-release/corescope-server" ;;
  esac
  [[ -f "$dir/compose.yaml" ]] || die "$name: no compose.yaml in $dir"
  exec 9>"$dir/.sacmesh-deploy.lock"
  flock -n 9 || die "$name: another deploy holds $dir/.sacmesh-deploy.lock"

  local base svc
  base="$(compose_image "$dir" "${services[0]}")"
  for svc in "${services[@]:1}"; do
    [[ "$(compose_image "$dir" "$svc")" == "$base" ]] || die "$name: services do not share one image"
  done
  local prev_commit base_user
  prev_commit="$(health_commit "$health")"
  [[ -n "$prev_commit" ]] || die "$name: $health is not healthy before the deploy; fix that first"
  [[ "$prev_commit" != "$COMMIT" ]] || { log "$name: already runs $COMMIT, skipping"; return 0; }
  base_user="$("${DOCKER[@]}" image inspect -f '{{.Config.User}}' "$base")"

  log "$name: staging $stage (from $base, now $prev_commit)"
  [[ ! -e "$stage" ]] || die "$name: $stage already exists"
  mkdir -p "$stage"
  cp "$INGESTOR" "$stage/corescope-ingestor"
  cp "$SERVER" "$stage/corescope-server"
  cp -r "$REPO/public" "$stage/public"
  printf '%s\n' "$COMMIT" >"$stage/.git-commit"
  printf '%s\n' "$VERSION" >"$stage/.image-version"
  cat >"$stage/Dockerfile" <<EOF
# CoreScope $VERSION (kesh33w/CoreScope $COMMIT), built by scripts/sacmesh-deploy.sh.
# The image it replaced, $base, with the ingestor, server and public/ swapped.
FROM $base
USER root
RUN rm -rf /app/public
COPY $chown_flag corescope-ingestor corescope-server .git-commit .image-version /app/
COPY $chown_flag public/ /app/public/
RUN chmod 755 /app/corescope-ingestor /app/corescope-server
ENV CORESCOPE_VERSION=$VERSION
USER ${base_user:-root}
LABEL org.sacmesh.corescope.source="kesh33w/CoreScope $COMMIT ($VERSION)"
EOF
  "${DOCKER[@]}" build -q -t "$image_repo" "$stage" >/dev/null
  log "$name: built image $image_repo"

  local compose_bak="$dir/compose.yaml.before-$VERSION-$STAMP" bind_bak=""
  [[ -n "$bind" ]] && bind_bak="$bind.before-$VERSION-$STAMP"
  {
    echo "Rollback for $name from $VERSION ($COMMIT) to $prev_commit:"
    echo "  cp '$compose_bak' '$dir/compose.yaml'"
    [[ -n "$bind" ]] && echo "  sudo install -o 1000 -g 1000 -m 500 '$bind_bak' '$bind'"
    echo "  (cd '$dir' && sudo docker compose up -d --no-deps ${services[*]})"
    echo "  then check $health reports $prev_commit"
  } >"$stage/ROLLBACK.txt"

  if [[ $DRY_RUN == 1 ]]; then
    # Keep the image; drop the folder so the real run can stage it again.
    rm -rf "$stage"
    log "$name: dry run, leaving the instance on $prev_commit"
    return 0
  fi

  cp -p "$dir/compose.yaml" "$compose_bak"
  [[ -n "$bind" ]] && cp -p "$bind" "$bind_bak"
  rollback() {
    log "$name: rolling back to $prev_commit"
    cp -p "$compose_bak" "$dir/compose.yaml"
    [[ -n "$bind" ]] && sudo install -o 1000 -g 1000 -m 500 "$bind_bak" "$bind"
    (cd "$dir" && "${DOCKER[@]}" compose up -d --no-deps "${services[@]}")
    if wait_health "$health" "$prev_commit"; then log "$name: back on $prev_commit"
    else log "$name: STILL UNHEALTHY after rollback; see $stage/ROLLBACK.txt"; fi
  }

  swap_image "$dir/compose.yaml" "$base" "$image_repo" >/dev/null
  if [[ -n "$bind" ]]; then
    # The container runs the server as uid 1000; a root-owned mode-500
    # binary fails to start (502). Keep 1000:1000.
    sudo install -o 1000 -g 1000 -m 500 "$SERVER" "$bind"
    [[ "$(stat -c %u:%g "$bind")" == "1000:1000" ]] || { rollback; die "$name: bind-mounted server owner is wrong"; }
  fi
  (cd "$dir" && "${DOCKER[@]}" compose up -d --no-deps "${services[@]}") || { rollback; die "$name: compose up failed"; }
  if ! wait_health "$health" "$COMMIT"; then rollback; die "$name: new build never became healthy"; fi
  local base_url="${health%/api/health}"
  curl -fsS --max-time 10 "$base_url/api/packets?limit=1" >/dev/null ||
    { rollback; die "$name: /api/packets fails on the new build"; }
  log "$name: live on $VERSION ($COMMIT); rollback steps in $stage/ROLLBACK.txt"
}

for inst in "${INSTANCES[@]}"; do deploy_instance "$inst"; done
log "done"
