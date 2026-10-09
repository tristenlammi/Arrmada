#!/usr/bin/env sh
# Arrmada updater — pull the latest code and rebuild in place.
#
#   ./update.sh                       back up the database, pull, rebuild and restart the app
#   ./update.sh --force-local         skip the pull and rebuild the code that's here
#   ./update.sh --rollback            go back to the build that was running before the last update
#   ./update.sh --rollback --with-db  ...and put back the database from before that update
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
  say "Usage: ./update.sh [--force-local] [--no-backup] [-y]"
  say "       ./update.sh --rollback [--with-db] [-y]"
  say ""
  say "  (no options)    back up the database, pull the latest code, rebuild the app and restart it"
  say "  --force-local   don't pull; rebuild the code that's in this folder"
  say "  --no-backup     don't take the pre-update database backup"
  say "  --rollback      start the build that ran before the last update again"
  say "  --with-db       with --rollback: also put back the database backup taken before that update"
  say "  -y, --yes       don't ask for confirmation (also: ARRMADA_UPDATE_FORCE=1)"
}

PULLED=""
FORCE_LOCAL=""
ROLLBACK=""
WITH_DB=""
NO_BACKUP=""
YES=""
for _arg in "$@"; do
  case "$_arg" in
    --pulled) PULLED=1 ;;
    --force-local) FORCE_LOCAL=1 ;;
    --rollback) ROLLBACK=1 ;;
    --with-db) WITH_DB=1 ;;
    --no-backup) NO_BACKUP=1 ;;
    -y | --yes) YES=1 ;;
    -h | --help) usage; exit 0 ;;
    *) say "✗ Unknown option: $_arg" >&2; usage >&2; exit 2 ;;
  esac
done
# For unattended runs that pass no options: the same as -y.
if [ "${ARRMADA_UPDATE_FORCE:-}" = 1 ]; then
  YES=1
fi
if [ -n "$WITH_DB" ] && [ -z "$ROLLBACK" ]; then
  say "✗ --with-db only goes with --rollback." >&2; usage >&2; exit 2
fi

# The user the app runs as. Commands run inside the container use it too, so any file
# they write in the data folder is the app's, not root's.
RUN_UID=$(grep -E '^ARRMADA_PUID=' .env 2>/dev/null | cut -d= -f2 | tr -d '\r" ')
RUN_GID=$(grep -E '^ARRMADA_PGID=' .env 2>/dev/null | cut -d= -f2 | tr -d '\r" ')
RUN_AS="${RUN_UID:-1000}:${RUN_GID:-1000}"

# Where update.sh notes the name of the database backup it took before the last update,
# which is the one --rollback --with-db puts back.
PREVIOUS_DB_FILE=.arrmada-previous-db

# app_answers -> 0 if the app answers its health check right now. It asks from inside
# the container, so it works whatever host port was picked and needs no curl on the host.
app_answers() {
  docker exec Arrmada-app curl -fsS -o /dev/null "http://127.0.0.1:7878/api/health" >/dev/null 2>&1
}

# health_json prints the app's health answer (one line of JSON), or nothing.
health_json() {
  docker exec Arrmada-app curl -fsS "http://127.0.0.1:7878/api/health" 2>/dev/null || true
}

# json_str KEY reads a string field from one line of JSON on stdin (no jq on Unraid).
json_str() {
  sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p"
}

# json_num KEY reads a whole-number field from one line of JSON on stdin.
json_num() {
  sed -n "s/.*\"$1\":\([0-9][0-9]*\).*/\1/p"
}

# image_id REF -> the image's ID, or nothing if there's no such image.
image_id() {
  docker image inspect -f '{{.Id}}' "$1" 2>/dev/null || true
}

# image_label REF LABEL -> the label's value on an image, or nothing.
image_label() {
  docker image inspect -f "{{index .Config.Labels \"$2\"}}" "$1" 2>/dev/null || true
}

# confirm QUESTION -> 0 to go ahead. Only asks on a terminal: -y, and unattended runs
# (cron, a pipe), go ahead without asking.
confirm() {
  if [ -n "$YES" ] || [ ! -t 0 ]; then
    return 0
  fi
  printf '%s [y/N] ' "$1" >&2
  read -r _answer || _answer=""
  case "$_answer" in
    y | Y | yes | YES | Yes) return 0 ;;
  esac
  return 1
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
# start_app_or_go_back CURRENT_ID -> starts whatever arrmada:dev is now and waits for
# it. If it doesn't come up, the build it replaced (kept as arrmada:rolled-back) is put
# back and started, so a failed rollback never leaves Arrmada down; then it exits 1.
start_app_or_go_back() {
  if docker compose up -d --no-build --no-deps --force-recreate arrmada-app && wait_healthy; then
    return 0
  fi
  say "" >&2
  say "✗ The previous build didn't start. The last lines of its log:" >&2
  show_log
  if [ -n "$1" ]; then
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
}

# manual_db_steps BACKUP prints how to put a backup back by hand.
manual_db_steps() {
  say "  To put the database back by hand:" >&2
  say "    1. docker compose stop arrmada-app" >&2
  say "    2. In the data folder (ARRMADA_DATA_HOST in .env, or the arrmada-data volume), copy" >&2
  say "       backups/$1 over arrmada.db, and delete arrmada.db-wal and arrmada.db-shm." >&2
  say "    3. ./update.sh --rollback" >&2
}

# Puts the build that ran before the last update back: arrmada:previous becomes
# arrmada:dev again, and the build being replaced is kept as arrmada:rolled-back so
# going forward again is one update away. Nothing is pulled or rebuilt.
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
  _prev_commit=$(image_label arrmada:previous org.arrmada.commit)

  _backup=""
  if [ -n "$WITH_DB" ]; then
    _backup=$(cat "$PREVIOUS_DB_FILE" 2>/dev/null || true)
    if [ -z "$_backup" ]; then
      say "✗ No database backup was recorded for the previous build (the last update didn't take one)." >&2
      say "  The automatic copies taken before each schema change are in <data>/backups (arrmada-pre-migrate-*)." >&2
      manual_db_steps "arrmada-pre-migrate-<the one from before the update>.db"
      exit 1
    fi
    # The build being rolled back to is the one that boots on the restored database,
    # so it has to be the one that understands `arrmada restore`.
    if [ "$(image_label arrmada:previous org.arrmada.restore)" != "1" ]; then
      say "✗ The previous build can't restore a database itself, so nothing was changed." >&2
      manual_db_steps "$_backup"
      exit 1
    fi
    say "This goes back to the previous build${_prev_commit:+ (commit $_prev_commit)} AND puts back the"
    say "database as it was before the last update ($_backup)."
    say "Everything Arrmada recorded since then is lost: requests, watch and listening history,"
    say "settings changes. Torrents grabbed since keep running in qBittorrent, but Arrmada won't know them."
    if ! confirm "Roll back the build and the database?"; then
      say "Nothing was changed."
      exit 0
    fi
  fi

  say "Rolling back to the build that ran before the last update…"
  # Tagged first, so the build being left can never be deleted as untagged meanwhile.
  if [ -n "$_cur" ]; then
    docker image tag "$_cur" arrmada:rolled-back
  fi

  # Without --with-db, ask the previous build itself, before anything is stopped,
  # whether it can run on the database as it is now: if this update upgraded it, that
  # build refuses to start on it, so stop here instead. Only a build labelled with the
  # CLI is asked (an older one would start a server); one too old to know the command
  # answers with a usage error, and then the start itself is the check. The one-off
  # container runs whatever arrmada:dev names, hence the brief retag.
  _schema_ok=""
  if [ -z "$_backup" ] && [ "$(image_label arrmada:previous org.arrmada.cli)" = "1" ]; then
    docker image tag arrmada:previous arrmada:dev
    _rc=0
    _check=$(docker compose run --rm --no-deps -T --entrypoint /usr/local/bin/arrmada arrmada-app schema 2>&1) || _rc=$?
    if [ -n "$_cur" ]; then
      docker image tag "$_cur" arrmada:dev
    fi
    [ "$_rc" = 0 ] && _schema_ok=1
    if [ "$_rc" = 3 ]; then
      docker image rm arrmada:rolled-back >/dev/null 2>&1 || true
      say "" >&2
      say "$_check" | sed 's/^/  /' >&2
      say "" >&2
      say "✗ The previous build can't run on the database as it is now: the update upgraded it." >&2
      say "  Nothing was changed. To go back together with the database from before the update" >&2
      say "  (everything recorded since is lost): ./update.sh --rollback --with-db" >&2
      exit 1
    fi
  fi

  if [ -n "$_backup" ]; then
    docker compose stop arrmada-app
    docker image tag arrmada:previous arrmada:dev
    # Staged with the build that will boot on it: the swap happens as it starts.
    if ! docker compose run --rm --no-deps arrmada-app restore "$_backup"; then
      say "✗ Restoring $_backup failed (see above)." >&2
      if [ -n "$_cur" ]; then
        docker image tag arrmada:rolled-back arrmada:dev
      fi
      say "  Starting the build that was running again…" >&2
      docker compose up -d --no-build --no-deps --force-recreate arrmada-app
      wait_healthy || say "  ✗ It didn't come back. Full log: docker compose logs arrmada-app" >&2
      exit 1
    fi
  else
    docker image tag arrmada:previous arrmada:dev
  fi
  start_app_or_go_back "$_cur"

  _commit=$(health_json | json_str commit)
  say ""
  say "✓ Rolled back. Arrmada is running commit ${_commit:-unknown} again."
  if [ -n "$_backup" ]; then
    say "  The database is back to $_backup; everything recorded after it is gone."
  elif [ -z "$_schema_ok" ]; then
    say "  If this update ran a database migration, also restore the pre-update backup"
    say "  (./update.sh --rollback --with-db, or System → Backups)."
  fi
  say "  The build you left is kept as arrmada:rolled-back; ./update.sh goes forward again."
}

if [ -n "$ROLLBACK" ]; then
  rollback
  exit 0
fi

# ── long conversion? ───────────────────────────────────────────────────────────
# Restarting the app abandons a conversion in flight, and a 4K encode can take a day.
# Asked before anything changes (the pull included), and only once: a copy of this
# script re-run after the pull (--pulled) has already been through it. Only asks on a
# terminal; -y, ARRMADA_UPDATE_FORCE=1 and unattended runs print the warning and go on.
# The app reports this only to callers inside its own container.
if [ -z "$PULLED" ]; then
  _health=$(health_json)
  _sec=$(printf '%s\n' "$_health" | json_num convert_running_sec)
  if [ -n "$_sec" ] && [ "$_sec" -gt 7200 ]; then
    _pct=$(printf '%s\n' "$_health" | json_num convert_progress)
    say "! A conversion has been running for $((_sec / 3600))h $(((_sec % 3600) / 60))m (${_pct:-0}%). Updating now restarts it from the beginning."
    if ! confirm "Update anyway?"; then
      say "Not updating; nothing was changed. Run ./update.sh again once it's done (or with -y to go ahead)."
      exit 0
    fi
  fi
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

# The build that's running now, if it's answering: the rollback target once the new
# build replaces it. A crash-looping build is no rollback target, so then the last good
# one stays arrmada:previous.
RUNNING_IMAGE=""
if app_answers; then
  RUNNING_IMAGE=$(docker inspect -f '{{.Image}}' Arrmada-app 2>/dev/null || true)
fi

# ── pre-update database backup ─────────────────────────────────────────────────
# Taken by the running app's own binary (`arrmada backup`), the same checked copy the
# app makes nightly, while it keeps running. Only an image labelled org.arrmada.cli is
# ever given arguments: an older binary ignores them and starts a second full server.
PRE_UPDATE_BACKUP=""
if [ -n "$NO_BACKUP" ]; then
  say "  ! Not backing up the database first (--no-backup)."
elif [ -z "$RUNNING_IMAGE" ]; then
  say "  ! Arrmada isn't running, so there's no pre-update backup (it still snapshots the database before any schema change)."
elif [ "$(docker inspect -f '{{index .Config.Labels "org.arrmada.cli"}}' Arrmada-app 2>/dev/null || true)" != "1" ]; then
  say "  ! This build can't take a pre-update backup; continuing (it still snapshots the database before any schema change)."
else
  say "Backing up the database…"
  if _path=$(docker exec -u "$RUN_AS" Arrmada-app arrmada backup --kind pre-update); then
    PRE_UPDATE_BACKUP=$(basename "$_path")
    say "  Saved $_path"
  else
    say "" >&2
    say "✗ The pre-update backup failed (see above), so nothing was changed." >&2
    say "  Free some space in the data folder and run ./update.sh again," >&2
    say "  or run ./update.sh --no-backup to update without one." >&2
    exit 1
  fi
fi

# Stamp the build with what it was built from, so the Dashboard and /api/health say
# which code is running (docker-compose.yml passes these as build args).
ARRMADA_VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev-docker)
ARRMADA_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
export ARRMADA_VERSION ARRMADA_COMMIT

# Keep the build that's running as arrmada:previous, for --rollback. It has to be
# tagged before the build: once the new build takes arrmada:dev and the old container
# is replaced, an untagged image can be deleted on the spot (Docker's containerd image
# store does). The rollback target it replaces is held as arrmada:previous-old until
# it's clear the build really replaced the running one; a failed or identical build
# puts it back. Tagged images are never pruned.
OLD_PREVIOUS=$(image_id arrmada:previous)
if [ -n "$RUNNING_IMAGE" ]; then
  if [ -n "$OLD_PREVIOUS" ] && [ "$OLD_PREVIOUS" != "$RUNNING_IMAGE" ]; then
    docker image tag "$OLD_PREVIOUS" arrmada:previous-old
  fi
  docker image tag "$RUNNING_IMAGE" arrmada:previous
fi

# Settings that were removed: the app never sees them now, so say so here.
if grep -qE '^ARRMADA_BASE_URL=.*[^/[:space:]"'"'"']' .env; then
  say "  ! ARRMADA_BASE_URL in .env is no longer supported and is ignored — Arrmada is served"
  say "    at the root of its own hostname. Point your reverse proxy at the root, then delete the line."
fi

say "Rebuilding and restarting Arrmada…"
# Rebuild + recreate ONLY the app. --no-deps leaves the companions (qBittorrent,
# Prowlarr, FlareSolverr) running and, crucially, does NOT re-run the one-shot media /
# qBittorrent init containers — those only need to run once, at install. An update
# only changes the app image, so nothing else needs touching.
UP_OK=1
docker compose up -d --build --no-deps arrmada-app || UP_OK=""

if [ -n "$RUNNING_IMAGE" ]; then
  if [ "$(image_id arrmada:dev)" != "$RUNNING_IMAGE" ]; then
    # Replaced (whether or not the new build starts: one that won't is exactly when
    # --rollback is needed). The backup just taken belongs with it.
    if [ -n "$PRE_UPDATE_BACKUP" ]; then
      printf '%s\n' "$PRE_UPDATE_BACKUP" > "$PREVIOUS_DB_FILE"
    else
      rm -f "$PREVIOUS_DB_FILE"
    fi
  elif [ -n "$OLD_PREVIOUS" ]; then
    # Nothing was replaced: the older rollback target stays the rollback target.
    docker image tag "$OLD_PREVIOUS" arrmada:previous
  else
    docker image rm arrmada:previous >/dev/null 2>&1 || true
  fi
  docker image rm arrmada:previous-old >/dev/null 2>&1 || true
fi

if [ -z "$UP_OK" ]; then
  if [ -n "$RUNNING_IMAGE" ] && [ "$(image_id arrmada:dev)" = "$RUNNING_IMAGE" ] && app_answers; then
    say "" >&2
    say "✗ The build failed (see above). Nothing was replaced; Arrmada is still running the version it was." >&2
    exit 1
  fi
  fail_start
fi
wait_healthy || fail_start

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
if docker image inspect arrmada:previous >/dev/null 2>&1; then
  say "  The build that ran before is kept as arrmada:previous: ./update.sh --rollback goes back to it."
fi
