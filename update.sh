#!/usr/bin/env sh
# Arrmada updater — pull the latest code and rebuild in place.
#
#   ./update.sh
#
# Your .env, database, and media volumes are preserved. Run it any time to upgrade
# or to apply changes you made to .env. Only the app image is rebuilt; the companions
# (qBittorrent, FlareSolverr, and Prowlarr if you opted in) keep running untouched.
set -eu
cd "$(dirname "$0")"

say() { printf '%s\n' "$*"; }

# wait_healthy -> 0 once the app answers its health check, 1 if it crashed or never came up.
# It asks from inside the container, so it works whatever host port was picked and needs no
# curl/wget on the host.
wait_healthy() {
  _base=$(grep -E '^ARRMADA_BASE_URL=' .env 2>/dev/null | cut -d= -f2)
  _i=0
  printf 'Waiting for Arrmada to start' >&2
  while [ "$_i" -lt 90 ]; do
    _state=$(docker inspect -f '{{.State.Status}}' Arrmada-app 2>/dev/null || echo missing)
    case "$_state" in
      exited|dead) printf '\n' >&2; return 1 ;;
    esac
    if docker exec Arrmada-app curl -fsS -o /dev/null "http://127.0.0.1:7878${_base%/}/api/health" 2>/dev/null; then
      printf ' ready\n' >&2; return 0
    fi
    printf '.' >&2
    _i=$((_i + 1)); sleep 2
  done
  printf '\n' >&2
  return 1
}

# fail_start prints why the app didn't come up and exits non-zero.
fail_start() {
  say ""
  say "✗ Arrmada didn't start. The last lines of its log:" >&2
  docker compose logs --tail 40 arrmada-app >&2 || true
  say "" >&2
  say "  Fix what the log says, then run the script again. Full log: docker compose logs arrmada-app" >&2
  exit 1
}

if ! docker compose version >/dev/null 2>&1; then
  say "✗ 'docker compose' (v2) isn't available." >&2
  exit 1
fi
if [ ! -f .env ]; then
  say "✗ No .env found — run ./install.sh first." >&2
  exit 1
fi

# port_in_use PORT -> 0 if something already listens on PORT on this host.
port_in_use() {
  if command -v ss >/dev/null 2>&1; then
    ss -ltnH 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${1}\$"
  elif command -v netstat >/dev/null 2>&1; then
    netstat -ltn 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${1}\$"
  else
    return 1
  fi
}

# Settings added since this install: give each a free value once, never overwrite.
if ! grep -qE '^ARRMADA_AUDIOBOOK_PORT=' .env; then
  AUDIOPORT=""
  for p in 13379 13380 13381 13382 18379; do
    if ! port_in_use "$p"; then AUDIOPORT=$p; break; fi
  done
  [ -z "$AUDIOPORT" ] && AUDIOPORT=18379
  {
    say ""
    say "# Audiobook server for listening apps (off until switched on in Books → Audiobook server)."
    say "ARRMADA_AUDIOBOOK_PORT=$AUDIOPORT"
  } >> .env
  say "Added ARRMADA_AUDIOBOOK_PORT=$AUDIOPORT to .env (the audiobook server's port)."
fi

# ── pull latest (skip cleanly if this isn't a git checkout) ─────────────────────
if [ -d .git ] && command -v git >/dev/null 2>&1; then
  say "Pulling latest changes…"
  git pull --ff-only || say "  ! git pull skipped (local changes or detached HEAD) — continuing with current code."
else
  say "Not a git checkout — building the code that's here."
fi

say "Rebuilding and restarting Arrmada…"
# Rebuild + recreate ONLY the app. --no-deps leaves the companions (qBittorrent,
# Prowlarr, FlareSolverr) running and, crucially, does NOT re-run the one-shot media /
# qBittorrent init containers — those only need to run once, at install. An update
# only changes the app image, so nothing else needs touching.
docker compose up -d --build --no-deps arrmada-app

wait_healthy || fail_start

# Reclaim space from the previous image build (harmless if nothing to prune).
docker image prune -f >/dev/null 2>&1 || true

WEBPORT=$(grep -E '^ARRMADA_PORT=' .env | cut -d= -f2)
HOSTIP=$(hostname -I 2>/dev/null | awk '{print $1}')
say ""
say "✓ Arrmada updated.  Open http://${HOSTIP:-localhost}:${WEBPORT:-7878}"
