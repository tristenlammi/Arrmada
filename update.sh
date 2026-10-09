#!/usr/bin/env sh
# Arrmada updater — pull the latest code and rebuild in place.
#
#   ./update.sh                 pull, rebuild and restart the app
#   ./update.sh --force-local   skip the pull and rebuild the code that's here
#   ./update.sh --rollback      go back to the build that was running before the last update
#
# Your .env, database, and media volumes are preserved. Run it any time to upgrade
# or to apply changes you made to .env. Only the app image is rebuilt; the companions
# (qBittorrent, FlareSolverr, and Prowlarr if you opted in) keep running untouched.
# The build that was running before is kept as the image arrmada:previous, so
# --rollback has something to go back to.
set -eu
cd "$(dirname "$0")"

say() { printf '%s\n' "$*"; }

usage() {
  say "Usage: ./update.sh [--force-local] [--rollback]"
  say ""
  say "  (no options)    pull the latest code, rebuild the app and restart it"
  say "  --force-local   don't pull; rebuild the code that's in this folder"
  say "  --rollback      start the build that ran before the last update again"
}

PULLED=""
FORCE_LOCAL=""
ROLLBACK=""
for _arg in "$@"; do
  case "$_arg" in
    --pulled) PULLED=1 ;;
    --force-local) FORCE_LOCAL=1 ;;
    --rollback) ROLLBACK=1 ;;
    -h | --help) usage; exit 0 ;;
    *) say "✗ Unknown option: $_arg" >&2; usage >&2; exit 2 ;;
  esac
done

BASE=$(grep -E '^ARRMADA_BASE_URL=' .env 2>/dev/null | cut -d= -f2)

# app_answers -> 0 if the app answers its health check right now. It asks from inside
# the container, so it works whatever host port was picked and needs no curl on the host.
app_answers() {
  docker exec Arrmada-app curl -fsS -o /dev/null "http://127.0.0.1:7878${BASE%/}/api/health" >/dev/null 2>&1
}

# health_json prints the app's health answer (one line of JSON), or nothing.
health_json() {
  docker exec Arrmada-app curl -fsS "http://127.0.0.1:7878${BASE%/}/api/health" 2>/dev/null || true
}

# json_str KEY reads a string field from one line of JSON on stdin (no jq on Unraid).
json_str() {
  sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p"
}

# image_id REF -> the image's ID, or nothing if there's no such image.
image_id() {
  docker image inspect -f '{{.Id}}' "$1" 2>/dev/null || true
}

# wait_healthy -> 0 once the app answers its health check, 1 if it crashed or never came up.
wait_healthy() {
  _i=0
  printf 'Waiting for Arrmada to start' >&2
  while [ "$_i" -lt 90 ]; do
    _state=$(docker inspect -f '{{.State.Status}}' Arrmada-app 2>/dev/null || echo missing)
    case "$_state" in
      exited|dead) printf '\n' >&2; return 1 ;;
    esac
    if app_answers; then
      printf ' ready\n' >&2; return 0
    fi
    printf '.' >&2
    _i=$((_i + 1)); sleep 2
  done
  printf '\n' >&2
  return 1
}

# show_log prints the last lines of the app's log.
show_log() {
  docker compose logs --tail 40 arrmada-app >&2 || true
}

# fail_start prints why the app didn't come up and exits non-zero.
fail_start() {
  say ""
  say "✗ Arrmada didn't start. The last lines of its log:" >&2
  show_log
  say "" >&2
  say "  Fix what the log says, then run the script again. Full log: docker compose logs arrmada-app" >&2
  if docker image inspect arrmada:previous >/dev/null 2>&1; then
    say "  To go back to the build that was running before: ./update.sh --rollback" >&2
  fi
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

# ── rollback ───────────────────────────────────────────────────────────────────
# Puts the build that ran before the last update back: arrmada:previous becomes
# arrmada:dev again, and the build being replaced is kept as arrmada:rolled-back so
# going forward again is one retag away. Nothing is pulled or rebuilt. If the older
# build doesn't come up, the one it replaced is started again, so a failed rollback
# never leaves Arrmada down.
rollback() {
  _prev=$(image_id arrmada:previous)
  if [ -z "$_prev" ]; then
    say "✗ There's no earlier build to go back to (no arrmada:previous image)." >&2
    say "  One is kept from the first update made with this version of update.sh." >&2
    exit 1
  fi
  _cur=$(image_id arrmada:dev)
  if [ "$_prev" = "$_cur" ]; then
    say "Arrmada is already running the previous build. To go forward again, run ./update.sh"
    exit 0
  fi

  say "Rolling back to the build that ran before the last update…"
  if [ -n "$_cur" ]; then
    docker image tag arrmada:dev arrmada:rolled-back
  fi
  docker image tag arrmada:previous arrmada:dev
  if ! { docker compose up -d --no-build --no-deps --force-recreate arrmada-app && wait_healthy; }; then
    say "" >&2
    say "✗ The previous build didn't start. The last lines of its log:" >&2
    show_log
    if [ -n "$_cur" ]; then
      say "" >&2
      say "  Starting the build you rolled back from again…" >&2
      docker image tag arrmada:rolled-back arrmada:dev
      if docker compose up -d --no-build --no-deps --force-recreate arrmada-app && wait_healthy; then
        say "  Arrmada is back on the build it was running." >&2
      else
        say "  ✗ That didn't start either. Full log: docker compose logs arrmada-app" >&2
      fi
    fi
    exit 1
  fi

  _commit=$(health_json | json_str commit)
  say ""
  say "✓ Rolled back. Arrmada is running commit ${_commit:-unknown} again."
  say "  The build you left is kept as arrmada:rolled-back; ./update.sh goes forward again."
  say "  If this update ran a database migration, also restore the pre-update backup"
  say "  (./update.sh --rollback --with-db, or System → Backups)."
}

if [ -n "$ROLLBACK" ]; then
  rollback
  exit 0
fi

# ── pull latest (skip cleanly if this isn't a git checkout) ─────────────────────
# If the pull changed this script, run the new one: it may know about settings or steps
# this older copy doesn't, and they should apply now rather than on the next update.
if [ -z "$PULLED" ]; then
  if [ -n "$FORCE_LOCAL" ]; then
    say "Not pulling (--force-local) — building the code that's here."
  elif [ -d .git ] && command -v git >/dev/null 2>&1; then
    say "Pulling latest changes…"
    # The commit this checkout was on before the pull: the code that's running now, if the
    # last update went through. Kept for reference (git checkout it to rebuild that code).
    git rev-parse --short HEAD > .arrmada-previous 2>/dev/null || true
    _before=$(cksum < update.sh)
    if ! git pull --ff-only; then
      say "" >&2
      say "✗ git pull failed (see above). Nothing was rebuilt; Arrmada is still running the version it was." >&2
      say "  Fix the checkout ('git status' shows local changes or a diverged branch), then run ./update.sh again," >&2
      say "  or run ./update.sh --force-local to rebuild the code that's here without pulling." >&2
      exit 1
    fi
    if [ "$(cksum < update.sh)" != "$_before" ]; then
      exec sh ./update.sh --pulled "$@"
    fi
  else
    say "Not a git checkout — building the code that's here."
  fi
fi

# Settings added since this install: give each a free value once, never overwrite.
if ! grep -qE '^ARRMADA_AUDIOBOOK_PORT=' .env; then
  AUDIOPORT=""
  for p in 13379 13380 13381 13382 18379; do
    if ! port_in_use "$p"; then AUDIOPORT=$p; break; fi
  done
  [ -z "$AUDIOPORT" ] && AUDIOPORT=18379
  {
    say ""
    say "# Audiobook server for listening apps (off until switched on in Services → Audiobooks)."
    say "ARRMADA_AUDIOBOOK_PORT=$AUDIOPORT"
  } >> .env
  say "Added ARRMADA_AUDIOBOOK_PORT=$AUDIOPORT to .env (the audiobook server's port)."
fi

# Stamp the build with what it was built from, so the Dashboard and /api/health say
# which code is running (docker-compose.yml passes these as build args).
ARRMADA_VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev-docker)
ARRMADA_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
export ARRMADA_VERSION ARRMADA_COMMIT

# Keep the build that's running now as arrmada:previous, for --rollback. It's the image
# the running container was started from, and only while it's answering: a crash-looping
# build is no rollback target, so then the last good one stays. A tagged image isn't
# dangling, so the prune below leaves it; the one it replaces is what gets pruned.
RUNNING_IMAGE=""
OLD_PREVIOUS=$(image_id arrmada:previous)
if app_answers; then
  RUNNING_IMAGE=$(docker inspect -f '{{.Image}}' Arrmada-app 2>/dev/null || true)
fi
if [ -n "$RUNNING_IMAGE" ]; then
  docker image tag "$RUNNING_IMAGE" arrmada:previous
elif [ -z "$OLD_PREVIOUS" ] && [ -n "$(image_id arrmada:dev)" ]; then
  docker image tag arrmada:dev arrmada:previous
elif [ -n "$OLD_PREVIOUS" ]; then
  say "  ! Arrmada isn't answering right now, so arrmada:previous stays the build that ran before."
fi

say "Rebuilding and restarting Arrmada…"
# Rebuild + recreate ONLY the app. --no-deps leaves the companions (qBittorrent,
# Prowlarr, FlareSolverr) running and, crucially, does NOT re-run the one-shot media /
# qBittorrent init containers — those only need to run once, at install. An update
# only changes the app image, so nothing else needs touching.
docker compose up -d --build --no-deps arrmada-app

wait_healthy || fail_start

# Nothing changed (the build came out identical to what was running): put the older
# rollback target back rather than losing it to the prune below.
if [ -n "$RUNNING_IMAGE" ] && [ -n "$OLD_PREVIOUS" ] && [ "$(image_id arrmada:dev)" = "$RUNNING_IMAGE" ]; then
  docker image tag "$OLD_PREVIOUS" arrmada:previous
fi
# A build kept by an earlier --rollback is superseded by this one.
docker image rm arrmada:rolled-back >/dev/null 2>&1 || true
# Reclaim space from older builds (harmless if nothing to prune). arrmada:previous is
# tagged, so it stays.
docker image prune -f >/dev/null 2>&1 || true

WEBPORT=$(grep -E '^ARRMADA_PORT=' .env | cut -d= -f2)
HOSTIP=$(hostname -I 2>/dev/null | awk '{print $1}')
say ""
say "✓ Arrmada updated to ${ARRMADA_VERSION}.  Open http://${HOSTIP:-localhost}:${WEBPORT:-7878}"
say "  A database snapshot is taken automatically before any schema change (<data>/backups, newest 5 kept)."
say "  The build that ran before is kept as arrmada:previous: ./update.sh --rollback goes back to it."
