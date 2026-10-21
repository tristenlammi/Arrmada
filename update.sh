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
# (qBittorrent, FlareSolverr, and Prowlarr or SABnzbd if you opted in) keep running
# untouched, and which ones run (COMPOSE_PROFILES in .env) is never changed here.
# The build that was running before is kept as the image arrmada:previous, so
# --rollback has something to go back to — and if the new build doesn't start, the
# script goes back to it by itself.
#
# An install made by the one-line installer without git (it has .arrmada-source) gets its
# newer files as a download instead of a git pull; everything else is the same.
#
# Which Arrmada an update brings is ARRMADA_CHANNEL in .env:
#   ARRMADA_CHANNEL=stable   the published releases (the default): the image ARRMADA_IMAGE
#                            names, ghcr.io/tristenlammi/arrmada:stable when it's unset, is
#                            downloaded, which takes minutes. It never goes backwards: a
#                            server running newer code than the latest release keeps it
#                            until a release includes it.
#   ARRMADA_CHANNEL=main     the newest code on main, compiled here on every update
#                            (developers; the better part of an hour each time).
# Everything else (the backup, arrmada:previous, --rollback) works the same either way.
# --force-local still compiles the code in this folder. An install from before the line
# existed gets it on its next update, without going backwards (see choose_channel).
set -eu
cd "$(dirname "$0")"

say() { printf '%s\n' "$*"; }

usage() {
  say "Usage: ./update.sh [--force-local] [--no-backup] [-y]"
  say "       ./update.sh --rollback [--with-db] [-y]"
  say ""
  say "  (no options)    back up the database, get the newest version and restart the app with it:"
  say "                  the latest release (ARRMADA_CHANNEL=stable in .env, the default), or main"
  say "                  compiled here (ARRMADA_CHANNEL=main)"
  say "  --force-local   don't pull; rebuild the code that's in this folder (even with ARRMADA_IMAGE set)"
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
# Set while the script goes back on its own after an update that didn't start.
AUTO_ROLLBACK=""
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

# Where an install downloaded without git (install.sh writes .arrmada-source then) gets
# its newer files. Overridable for forks and the installer's own tests.
REPO="tristenlammi/Arrmada"
API_URL="${ARRMADA_API_URL:-https://api.github.com/repos/$REPO}"
TARBALL_BASE="${ARRMADA_TARBALL_BASE:-https://codeload.github.com/$REPO/tar.gz}"

# fetch URL -> the body on stdout (curl or wget, whichever is here).
fetch() {
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1"; else wget -qO- "$1"; fi
}

# source_get KEY -> a value from .arrmada-source.
source_get() {
  sed -n "s/^$1=//p" .arrmada-source 2>/dev/null | head -n 1
}

# under PATH NAME -> 0 if host folder PATH is this folder's NAME or inside it.
under() {
  case "$1" in
    "") return 1 ;;
    /*) _abs="$1" ;;
    *) _abs="$(pwd)/${1#./}" ;;
  esac
  case "${_abs%/}/" in "$(pwd)/$2/"*) return 0 ;; esac
  return 1
}

# refresh_download: an install downloaded without git gets the newer files the same way —
# the branch's newest download, unpacked over this folder. Only what the download
# contains is replaced: .env, the override, the data and transcode folders and anything
# else of yours are never touched.
refresh_download() {
  _branch=$(source_get branch)
  _branch=${_branch:-main}
  _json=$(fetch "$API_URL/commits/$_branch" 2>/dev/null || true)
  _sha=$(printf '%s\n' "$_json" | sed -n 's/^  "sha": "\([0-9a-f]\{40\}\)".*/\1/p' | head -n 1)
  _date=$(printf '%s\n' "$_json" | sed -n 's/.*"date": *"\([0-9]\{4\}\)-\([0-9]\{2\}\)-\([0-9]\{2\}\)T.*/\1.\2.\3/p' | sed -n '2p')
  _short=$(printf '%s' "$_sha" | cut -c1-7)
  if [ -n "$_short" ] && [ "$_short" = "$(source_get commit)" ]; then
    say "The files here are already the newest ($_short)."
    return 0
  fi
  say "Downloading the latest files…"
  # Unpacked next to the install (same disk, so each swap is a rename), under a name the
  # loop below never touches.
  _tmp=$(mktemp -d ./.arrmada-download.XXXXXX)
  _url="$TARBALL_BASE/${_sha:-refs/heads/$_branch}"
  if ! fetch "$_url" | tar -xz -C "$_tmp"; then
    rm -rf "$_tmp"
    say "" >&2
    say "✗ Couldn't download $_url. Nothing was changed; Arrmada is still running the version it was." >&2
    say "  Check this server can reach github.com, then run update.sh again." >&2
    exit 1
  fi
  _top=$(find "$_tmp" -mindepth 1 -maxdepth 1 -type d | head -n 1)
  if [ -z "$_top" ] || [ ! -f "$_top/docker-compose.yml" ] || [ ! -f "$_top/update.sh" ]; then
    rm -rf "$_tmp"
    say "✗ The download from $_url didn't contain Arrmada. Nothing was changed." >&2
    exit 1
  fi
  _data=$(grep -E '^ARRMADA_DATA_HOST=' .env | tail -n 1 | cut -d= -f2- | tr -d '\r"'"'"' ')
  _media=$(grep -E '^ARRMADA_STORAGE_HOST=' .env | tail -n 1 | cut -d= -f2- | tr -d '\r"'"'"' ')
  _mhost=$(grep -E '^ARRMADA_MEDIA_HOST=' .env | tail -n 1 | cut -d= -f2- | tr -d '\r"'"'"' ')
  for _src in "$_top"/* "$_top"/.[!.]*; do
    [ -e "$_src" ] || continue
    _name=$(basename "$_src")
    case "$_name" in
      .env | .env.* | docker-compose.override.yml* | data | transcode | .arrmada-* | .git) continue ;;
    esac
    if under "$_data" "$_name" || under "$_media" "$_name" || under "$_mhost" "$_name"; then
      continue
    fi
    rm -rf "./$_name"
    mv "$_src" "./$_name"
  done
  rm -rf "$_tmp"
  {
    say "# Written by install.sh: this install was downloaded, not cloned with git."
    say "# update.sh downloads the newer files from here."
    say "branch=$_branch"
    say "commit=$_short"
    say "date=$_date"
  } > .arrmada-source
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

# wait_healthy -> 0 once the app answers its health check, 1 if it crashed, keeps
# restarting (Docker's restart policy brings a crashing build straight back, so it never
# reads as stopped), or never came up.
wait_healthy() {
  _i=0
  _restarts0=""
  printf 'Waiting for Arrmada to start' >&2
  while [ "$_i" -lt 90 ]; do
    _state=$(docker inspect -f '{{.State.Status}} {{.RestartCount}}' Arrmada-app 2>/dev/null || echo "missing 0")
    case "$_state" in
      exited* | dead*) printf '\n' >&2; return 1 ;;
    esac
    # Counted from here: a container that wasn't replaced keeps its count, and every
    # restart from the app's own Settings adds one.
    _restarts="${_state#* }"
    case "$_restarts" in "" | *[!0-9]*) _restarts=0 ;; esac
    [ -z "$_restarts0" ] && _restarts0="$_restarts"
    if [ "$((_restarts - _restarts0))" -ge 3 ]; then
      printf '\n' >&2; return 1
    fi
    if app_answers; then
      printf ' ready\n' >&2; return 0
    fi
    printf '.' >&2
    _i=$((_i + 1)); sleep 2
  done
  printf '\n' >&2
  # Still running, just not answering yet (a big database upgrade can take a while):
  # told apart from a crash, so nothing is stopped in the middle of it.
  return 2
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
  say "  (Started with a plain 'docker compose up -d'? Update with: git pull && docker compose pull && docker compose up -d)" >&2
  exit 1
fi

# env_get KEY -> KEY's value in .env, or nothing.
env_get() {
  grep -E "^$1=" .env 2>/dev/null | tail -n 1 | cut -d= -f2- | tr -d '\r"'"'"' ' || true
}

# An older image ignores unknown database environment variables and would open
# SQLite. Refuse it before stopping/replacing the running PostgreSQL app.
database_backend() {
  _requested_backend=$(printf '%s' "${ARRMADA_DATABASE_BACKEND:-$(env_get ARRMADA_DATABASE_BACKEND)}" | tr '[:upper:]' '[:lower:]')
  if [ "$_requested_backend" = postgres ]; then printf postgres; return; fi
  # Also honor a compose override/Portainer-style environment rather than
  # assuming .env is the only source. Only print this non-secret field; the
  # rendered config (which contains credentials) is never logged or saved.
  _compose_backend=$(docker compose config --format json 2>/dev/null |
    sed -n 's/.*"ARRMADA_DATABASE_BACKEND":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1 | tr '[:upper:]' '[:lower:]')
  printf '%s' "${_compose_backend:-${_requested_backend:-sqlite}}"
}

require_database_backend_image() {
  _db_backend=$(database_backend)
  case "$_db_backend" in
    sqlite|postgres) ;;
    *) say "✗ Unknown database backend; nothing was changed. Use sqlite or postgres." >&2; exit 1 ;;
  esac
  if [ "$_db_backend" = postgres ] && [ "$(image_label "$1" org.arrmada.postgres)" != 1 ]; then
    say "✗ The selected build does not support PostgreSQL; nothing was changed." >&2
    say "  Select a PostgreSQL-capable build, or convert offline to SQLite before using this build." >&2
    exit 1
  fi
}

# A relative ARRMADA_MEDIA_HOST would put the media inside this folder, where a download of
# newer files, or a build, would meet it. Refused before anything happens.
MEDIA_HOST=$(env_get ARRMADA_MEDIA_HOST)
case "$MEDIA_HOST" in
  . | ./* | .. | ../*)
    say "✗ ARRMADA_MEDIA_HOST=$MEDIA_HOST in .env is a relative path. Give the folder's full path" >&2
    say "  (e.g. /mnt/user/arrmada-media), then run ./update.sh again. Nothing was changed." >&2
    exit 1 ;;
esac

# media_source CONTAINER -> what CONTAINER has at /media: a host folder, or a volume's name.
media_source() {
  docker inspect -f '{{range .Mounts}}{{if eq .Destination "/media"}}{{if .Name}}{{.Name}}{{else}}{{.Source}}{{end}}{{end}}{{end}}' "$1" 2>/dev/null || true
}
# media_name SOURCE -> how to say it: the folder, or "the Docker volume NAME".
media_name() {
  case "$1" in
    /*) say "$1" ;;
    *) say "the Docker volume $1" ;;
  esac
}

# ── which image compose runs ────────────────────────────────────────────────────
# docker-compose.yml runs ARRMADA_APP_IMAGE, and without it the published release, so a
# plain `docker compose up -d` in a fresh checkout downloads rather than compiles. This
# script works differently: every build or download it makes is the local image
# arrmada:dev (the one before kept as arrmada:previous, for --rollback), so compose has to
# run that name. It's exported for every compose call below, whatever .env says, and
# written to .env once so a hand-run `docker compose up -d` agrees.
#
# An install from before the published default gets the line now. One that built or
# downloaded through these scripts has an arrmada:dev, and carries on exactly as it did
# (compiling here, unless ARRMADA_IMAGE says otherwise). One with no arrmada:dev and no
# ARRMADA_IMAGE has only ever run `docker compose up -d` here, which means the published
# image, so its updates download that too rather than start compiling.
DEFAULT_IMAGE="ghcr.io/tristenlammi/arrmada:stable"
_app_image=$(env_get ARRMADA_APP_IMAGE)
if [ -z "$_app_image" ]; then
  if [ -z "$(env_get ARRMADA_IMAGE)" ] && [ -z "$FORCE_LOCAL" ] && [ -z "$ROLLBACK" ] &&
    ! docker image inspect arrmada:dev >/dev/null 2>&1; then
    {
      say ""
      say "# The published image update.sh downloads (delete the line to compile here instead)."
      say "ARRMADA_IMAGE=$DEFAULT_IMAGE"
    } >> .env
    say "Added ARRMADA_IMAGE=$DEFAULT_IMAGE to .env: this install runs the published image, so updates download it."
  fi
  {
    say ""
    say "# The image docker compose runs: update.sh's own build or download (keep this line)."
    say "ARRMADA_APP_IMAGE=arrmada:dev"
  } >> .env
  say "Added ARRMADA_APP_IMAGE=arrmada:dev to .env (the image update.sh builds or downloads)."
elif [ "$_app_image" != arrmada:dev ]; then
  say "✗ ARRMADA_APP_IMAGE=$_app_image in .env, but update.sh only runs its own arrmada:dev image." >&2
  say "  To have updates download a published image, set ARRMADA_IMAGE=$_app_image and" >&2
  say "  ARRMADA_APP_IMAGE=arrmada:dev, then run ./update.sh again. Or leave .env as it is" >&2
  say "  and update with: docker compose pull && docker compose up -d" >&2
  exit 2
fi
ARRMADA_APP_IMAGE=arrmada:dev
export ARRMADA_APP_IMAGE

# ── which Arrmada this server follows ───────────────────────────────────────────
# ARRMADA_CHANNEL: stable downloads the published image (ARRMADA_IMAGE, or the :stable
# release when that's unset); main compiles the code here, whatever ARRMADA_IMAGE says.
# Without the line (an install from before it existed) ARRMADA_IMAGE decides, as it always
# did, until choose_channel below writes one.
CHANNEL=$(env_get ARRMADA_CHANNEL)
case "$CHANNEL" in
  "" | stable | main) ;;
  *)
    say "✗ ARRMADA_CHANNEL=$CHANNEL in .env: it's stable (the published releases) or main (compile the newest code here)." >&2
    exit 2 ;;
esac
ENV_IMAGE=$(env_get ARRMADA_IMAGE)

# Pull mode: the published image to download, or nothing to compile here. --force-local
# means "build what's here", so it turns pull mode off for this run. The script's own
# local tags can't be the source: they're what it manages. --rollback never pulls, so it
# doesn't read it.
PULL_IMAGE=""
if [ -z "$FORCE_LOCAL" ] && [ -z "$ROLLBACK" ]; then
  case "$CHANNEL" in
    stable) PULL_IMAGE=${ENV_IMAGE:-$DEFAULT_IMAGE} ;;
    main)
      if [ -n "$ENV_IMAGE" ]; then
        say "ARRMADA_CHANNEL=main: compiling main here (ARRMADA_IMAGE in .env is for the stable channel, so it isn't used)."
      fi ;;
    *) PULL_IMAGE=$ENV_IMAGE ;;
  esac
fi
case "$PULL_IMAGE" in
  arrmada | arrmada:*)
    say "✗ ARRMADA_IMAGE=$PULL_IMAGE in .env names one of this script's own local images." >&2
    say "  Set it to a published image (e.g. ghcr.io/tristenlammi/arrmada:stable), or remove the line to build here." >&2
    exit 2 ;;
esac

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

# reopen_bundled_qbit: a database put back with --with-db can be from before Arrmada gave
# the bundled qBittorrent its password (qbittorrent-login.json in the data folder), so the
# client in the restored database signs in without one, and qBittorrent would turn it
# away. Its login-free access goes back on for the build being started, for the Docker
# network Arrmada and qBittorrent share and nothing else (docker/qbittorrent/reopen.sh: never
# the private ranges a first config from before had, which covered every home network); an
# Arrmada with the password switch gives it its password again when it starts. Nothing
# happens without that file (no password was ever given) or without the bundle.
reopen_bundled_qbit() {
  docker compose config --services 2>/dev/null | grep -qx arrmada-qbittorrent-init || return 0
  _made=$(docker compose run --rm --no-deps -T --entrypoint sh arrmada-app \
    -c 'if [ -f /data/qbittorrent-login.json ]; then echo yes; else echo no; fi' 2>/dev/null </dev/null | tr -d '\r' | tail -n 1)
  [ "$_made" = no ] && return 0
  # The marker first: it tells the next Arrmada with the switch that this access was put
  # back on purpose (it's Arrmada's to take away again), not set up by you since.
  if [ "$_made" = yes ] && [ -f docker/qbittorrent/reopen.sh ] &&
    docker compose run --rm --no-deps -T --entrypoint sh arrmada-app -c 'touch /data/qbittorrent-reopened' >/dev/null 2>&1 </dev/null &&
    docker compose stop arrmada-qbittorrent >/dev/null 2>&1 &&
    docker compose run --rm --no-deps -T arrmada-qbittorrent-init sh /seed/reopen.sh >/dev/null 2>&1 </dev/null &&
    docker compose start arrmada-qbittorrent >/dev/null 2>&1; then
    say "  qBittorrent: the database put back doesn't have the password Arrmada gave it, so its"
    say "  login-free access from the Docker network (that network only) is back on for this"
    say "  version. Your next update gives it its password again."
    return 0
  fi
  docker compose start arrmada-qbittorrent >/dev/null 2>&1 || true
  say "! qBittorrent may turn the version you went back to away: the database put back doesn't" >&2
  say "  have the password Arrmada gave it, and it couldn't be let in without one. Either enter" >&2
  say "  the login on qBittorrent's card in Settings → Connections (Edit); it's in the data folder:" >&2
  say "    docker exec Arrmada-app cat /data/qbittorrent-login.json" >&2
  say "  or switch its login-free access from the Docker network back on:" >&2
  say "    docker compose run --rm --no-deps --entrypoint sh arrmada-app -c 'touch /data/qbittorrent-reopened'" >&2
  say "    docker compose stop arrmada-qbittorrent" >&2
  say "    docker compose run --rm --no-deps arrmada-qbittorrent-init sh /seed/reopen.sh" >&2
  say "    docker compose start arrmada-qbittorrent" >&2
  say "    docker compose restart arrmada-app" >&2
}

# tighten_env: .env can hold a key or token (ARRMADA_PLEX_TOKEN), and new installs make it
# readable by its owner only. An older one is tightened the same way when it's yours and
# you're not root: the owner keeps reading and writing it (so docker compose and this
# script still can), and its owner never changes. A root-owned one is left as it is, since
# other users on the server may be running docker compose with it.
tighten_env() {
  [ -f .env ] || return 0
  _uid=$(id -u)
  [ "$_uid" != 0 ] || return 0
  _owner=$(stat -c %u .env 2>/dev/null) || return 0
  _mode=$(stat -c %a .env 2>/dev/null) || return 0
  [ "$_owner" = "$_uid" ] || return 0
  case "$_mode" in *00) return 0 ;; esac
  if chmod go-rwx .env 2>/dev/null; then
    say "  .env is now readable by you only (it can hold a key or token)."
  fi
}

# qbit_page_notice: installs from before 2026-10-18 publish qBittorrent's own page on every
# interface; docker-compose.yml now publishes it on this server only unless .env says
# otherwise (ARRMADA_QBIT_WEBUI_BIND). Updates only recreate the app, so a running
# qBittorrent keeps what it had until its container is next recreated: say so, and how to
# choose, until .env says which (or it has been recreated).
qbit_page_notice() {
  grep -q '^ARRMADA_QBIT_WEBUI_BIND=' .env 2>/dev/null && return 0
  _pub=$(docker port Arrmada-qbittorrent 8080/tcp 2>/dev/null | head -n 1)
  case "$_pub" in
    0.0.0.0:* | "[::]:"* | :::*) ;;
    *) return 0 ;;
  esac
  say ""
  say "  ! qBittorrent's own page (port ${_pub##*:}) is open to your whole network. It asks for a"
  say "    password, but nothing outside this server needs it, so new installs publish it on this"
  say "    server only, as this one will once its container is recreated. To do that now:"
  say "      docker compose up -d arrmada-qbittorrent   (restarts qBittorrent once)"
  say "    To keep it open to your network instead, add ARRMADA_QBIT_WEBUI_BIND=0.0.0.0 to .env."
}

# Puts the build that ran before the last update back: arrmada:previous becomes
# arrmada:dev again, and the build being replaced is kept as arrmada:rolled-back so
# going forward again is one update away. Nothing is pulled or rebuilt.
rollback() {
  _db_backend=$(database_backend)
  if [ -n "$WITH_DB" ]; then
    case "$_db_backend:$(cat "$PREVIOUS_DB_FILE" 2>/dev/null || true)" in
      postgres:*|*.pgdump)
        say "✗ PostgreSQL database rollback is offline only; no containers, images or schemas were changed." >&2
        say "  Stop Arrmada, recover the pre-update archive with the matching build's restore-postgres" >&2
        say "  into a NEW empty schema, update ARRMADA_POSTGRES_SCHEMA, then roll back without --with-db." >&2
        say "  See docs/postgresql.md. Never copy a .pgdump archive over arrmada.db." >&2
        exit 1 ;;
    esac
  fi
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
  require_database_backend_image arrmada:previous

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

  # A build from before this script could ask it about the database (no org.arrmada.cli
  # label: every build before the backup/rollback tooling) starts on whatever database it
  # finds. It has no newer-schema check, so on a database the update upgraded it runs old
  # code against tables it doesn't know. Say so before anything changes: the database has
  # to be put back by hand, since such a build can't restore one either.
  _legacy=""
  if [ -z "$_backup" ] && [ "$(image_label arrmada:previous org.arrmada.cli)" != "1" ]; then
    # Going back on its own after a failed update, the script can't ask, so it doesn't.
    if [ -n "$AUTO_ROLLBACK" ]; then
      say "✗ The previous build${_prev_commit:+ (commit $_prev_commit)} is too old to go back to automatically" >&2
      say "  (it can't check the database). To go back by hand:" >&2
      manual_db_steps "arrmada-pre-migrate-<the newest one>.db"
      exit 1
    fi
    _legacy=1
    say "! The previous build${_prev_commit:+ (commit $_prev_commit)} is older than update.sh's database checks: it starts"
    say "  on the database as it is now, even if the update upgraded it, and can damage an upgraded one."
    say "  If the update upgraded the database (<data>/backups has an arrmada-pre-migrate-* copy from it)"
    say "  and you haven't put that copy back yet, answer no and do this instead:"
    manual_db_steps "arrmada-pre-migrate-<the newest one>.db"
    if ! confirm "Start the previous build on the database as it is now?"; then
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
    _check=$(docker compose run --rm --no-deps -T --entrypoint /usr/local/bin/arrmada arrmada-app schema 2>&1 </dev/null) || _rc=$?
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
      # An automatic go-back (a failed update) tells its caller why, to offer exactly that.
      if [ -n "$AUTO_ROLLBACK" ]; then exit 3; fi
      exit 1
    fi
  fi

  if [ -n "$_backup" ]; then
    docker compose stop arrmada-app
    docker image tag arrmada:previous arrmada:dev
    # Staged with the build that will boot on it: the swap happens as it starts.
    if ! docker compose run --rm --no-deps -T arrmada-app restore "$_backup" </dev/null; then
      say "✗ Restoring $_backup failed (see above)." >&2
      if [ -n "$_cur" ]; then
        docker image tag arrmada:rolled-back arrmada:dev
      fi
      say "  Starting the build that was running again…" >&2
      docker compose up -d --no-build --no-deps --force-recreate arrmada-app
      wait_healthy || say "  ✗ It didn't come back. Full log: docker compose logs arrmada-app" >&2
      exit 1
    fi
    reopen_bundled_qbit
  else
    docker image tag arrmada:previous arrmada:dev
  fi
  start_app_or_go_back "$_cur"

  _commit=$(health_json | json_str commit)
  say ""
  say "✓ Rolled back. Arrmada is running commit ${_commit:-unknown} again."
  if [ -n "$_backup" ]; then
    say "  The database is back to $_backup; everything recorded after it is gone."
  elif [ -n "$_legacy" ]; then
    # --rollback --with-db and System → Backups can't help a build this old (see above).
    say "  If the update upgraded the database and you haven't put it back yet: docker compose stop arrmada-app,"
    say "  copy <data>/backups/arrmada-pre-migrate-<the newest one>.db over <data>/arrmada.db, delete"
    say "  arrmada.db-wal and arrmada.db-shm, then docker compose up -d --no-build --no-deps arrmada-app."
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
# Pull mode pulls too: nothing is compiled from it, but it keeps this script and
# docker-compose.yml current.
#
# PRE_PULL is the commit the files were at before the pull. A build from before builds
# were stamped with their commit is that code, or older, so it's what choose_channel goes
# by for one. It's handed to the copy run after the pull (and by the install line, which
# may pull first); an older copy of this script hands nothing over, but its pull leaves
# the commit before it in git's ORIG_HEAD.
PRE_PULL="${ARRMADA_PRE_PULL:-}"
if [ -z "$PULLED" ]; then
  if [ -n "$FORCE_LOCAL" ]; then
    say "Not pulling (--force-local) — building the code that's here."
  elif [ -d .git ] && command -v git >/dev/null 2>&1; then
    say "Pulling latest changes…"
    [ -z "$PRE_PULL" ] && PRE_PULL=$(git rev-parse HEAD 2>/dev/null || true)
    # The commit this checkout was on before the pull: the code that's running now, if the
    # last update went through. Kept for reference (git checkout it to rebuild that code).
    git rev-parse --short "${PRE_PULL:-HEAD}" > .arrmada-previous 2>/dev/null || true
    _before=$(cksum < update.sh)
    if ! git pull --ff-only; then
      say "" >&2
      say "✗ git pull failed (see above). Nothing was rebuilt; Arrmada is still running the version it was." >&2
      say "  Fix the checkout ('git status' shows local changes or a diverged branch), then run ./update.sh again," >&2
      say "  or run ./update.sh --force-local to rebuild the code that's here without pulling." >&2
      exit 1
    fi
    if [ "$(cksum < update.sh)" != "$_before" ]; then
      ARRMADA_PRE_PULL=$PRE_PULL
      export ARRMADA_PRE_PULL
      exec sh ./update.sh --pulled "$@"
    fi
  elif [ -f .arrmada-source ]; then
    [ -z "$PRE_PULL" ] && PRE_PULL=$(source_get commit)
    _before=$(cksum < update.sh)
    refresh_download
    if [ "$(cksum < update.sh)" != "$_before" ]; then
      ARRMADA_PRE_PULL=$PRE_PULL
      export ARRMADA_PRE_PULL
      exec sh ./update.sh --pulled "$@"
    fi
  elif [ -n "$PULL_IMAGE" ]; then
    say "Not a git checkout — the scripts here stay as they are."
  else
    say "Not a git checkout — building the code that's here."
  fi
elif [ -z "$PRE_PULL" ] && [ -d .git ] && command -v git >/dev/null 2>&1; then
  PRE_PULL=$(git rev-parse -q --verify ORIG_HEAD 2>/dev/null || true)
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

# ── the release this update would run ──────────────────────────────────────────
# is_sha S -> 0 if S is a commit, as builds are stamped with one.
is_sha() {
  case "$1" in "" | *[!0-9a-f]*) return 1 ;; esac
  [ "${#1}" -ge 7 ] && [ "${#1}" -le 40 ]
}

# is_release V -> 0 for a release version as the release build stamps it (v0.2.0,
# v0.3.0-rc.1). A local build of a tag with edits in it (-dirty) isn't one.
is_release() {
  case "$1" in *-dirty) return 1 ;; esac
  printf '%s\n' "$1" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'
}

# semver_cmp A B -> -1, 0 or 1 as release A is older than, the same as or newer than B.
# A pre-release comes before its release; two pre-releases of one version go by text.
semver_cmp() {
  awk -v a="${1#v}" -v b="${2#v}" 'BEGIN {
    ca = a; pa = ""; i = index(a, "-"); if (i) { ca = substr(a, 1, i - 1); pa = substr(a, i + 1) }
    cb = b; pb = ""; i = index(b, "-"); if (i) { cb = substr(b, 1, i - 1); pb = substr(b, i + 1) }
    split(ca, x, "."); split(cb, y, ".")
    for (k = 1; k <= 3; k++) {
      if (x[k] + 0 < y[k] + 0) { print -1; exit }
      if (x[k] + 0 > y[k] + 0) { print 1; exit }
    }
    if (pa == pb) print 0
    else if (pa == "") print 1
    else if (pb == "") print -1
    else if (pa < pb) print -1
    else print 1
  }'
}

# env_set KEY VALUE COMMENT -> KEY=VALUE in .env: the KEY line replaced, or added with
# COMMENT above it. The .env from before this run's first change is kept as .env.bak-<time>.
ENV_BACKUP=""
env_set() {
  if [ -z "$ENV_BACKUP" ]; then
    ENV_BACKUP=".env.bak-$(date +%Y%m%d-%H%M%S)"
    cp -p .env "$ENV_BACKUP"
  fi
  if grep -qE "^$1=" .env; then
    _tmp=$(mktemp ./.env.tmp.XXXXXX)
    sed "s|^$1=.*|$1=$2|" .env > "$_tmp"
    # Written back over the same file, so its owner and permissions stay as they are.
    cat "$_tmp" > .env
    rm -f "$_tmp"
  else
    { say ""; say "$3"; say "$1=$2"; } >> .env
  fi
}
CHANNEL_COMMENT="# Which Arrmada updates bring: stable = the published releases, main = the newest code compiled here."

# running_build sets RUN_VERSION and RUN_COMMIT to the build that's running (or, with the
# app stopped, the one compose starts): as stamped on its image, else as the app reports
# them. A build from before builds were stamped says neither; it was built from the files
# as they were before this update's pull (PRE_PULL) or from older ones, and "this commit
# or older" is all the checks below need.
RUN_VERSION=""
RUN_COMMIT=""
running_build() {
  _img=${RUNNING_IMAGE:-$(image_id arrmada:dev)}
  if [ -n "$_img" ]; then
    RUN_VERSION=$(image_label "$_img" org.arrmada.version)
    RUN_COMMIT=$(image_label "$_img" org.arrmada.commit)
  fi
  if ! is_sha "$RUN_COMMIT" && [ -n "$RUNNING_IMAGE" ]; then
    _health=$(health_json)
    RUN_COMMIT=$(printf '%s\n' "$_health" | json_str commit)
    RUN_VERSION=$(printf '%s\n' "$_health" | json_str version)
  fi
  if ! is_sha "$RUN_COMMIT" && is_sha "$PRE_PULL"; then
    RUN_COMMIT=$PRE_PULL
    RUN_VERSION=""
  fi
  return 0
}

# have_git -> 0 in a git checkout this server can run git in.
have_git() { [ -d .git ] && command -v git >/dev/null 2>&1; }

# contains RELEASE_COMMIT RELEASE_VERSION BUILD_COMMIT -> 0 if the release includes the
# build's code (built from that commit or a later one), 1 if the build has code the
# release doesn't (newer, or on a branch of its own), 2 if it can't tell. The checkout
# answers when it has both commits (it fetches the release's tag if need be); else GitHub.
contains() {
  if have_git; then
    if ! git cat-file -e "$1^{commit}" 2>/dev/null && is_release "$2"; then
      GIT_TERMINAL_PROMPT=0 git fetch -q origin "refs/tags/$2:refs/tags/$2" </dev/null >/dev/null 2>&1 || true
    fi
    if git cat-file -e "$1^{commit}" 2>/dev/null && git cat-file -e "$3^{commit}" 2>/dev/null; then
      if git merge-base --is-ancestor "$3" "$1" 2>/dev/null; then return 0; fi
      return 1
    fi
  fi
  _status=$(fetch "$API_URL/compare/$3...$1?per_page=1" 2>/dev/null | sed -n 's/^  "status": "\([a-z]*\)".*/\1/p' | head -n 1)
  case "$_status" in
    ahead | identical) return 0 ;;
    behind | diverged) return 1 ;;
  esac
  return 2
}

# newest_tag -> the newest release tag the checkout knows once it has fetched tags (never
# a pre-release: those don't become stable), or nothing.
newest_tag() {
  GIT_TERMINAL_PROMPT=0 git fetch -q --tags origin </dev/null >/dev/null 2>&1 || true
  _best=""
  for _t in $(git tag -l 'v[0-9]*' 2>/dev/null); do
    case "$_t" in *-*) continue ;; esac
    is_release "$_t" || continue
    if [ -z "$_best" ] || [ "$(semver_cmp "$_t" "$_best")" = 1 ]; then _best=$_t; fi
  done
  printf '%s' "$_best"
}

# stay_put WHY: this update leaves Arrmada on the build it's running, says why, and ends
# the script. Nothing has been changed by then (bar the .env lines added above), and the
# next update checks again.
REL_VERSION=""
REL_COMMIT=""
stay_put() {
  _build="${RUN_VERSION:+$RUN_VERSION, }commit $(printf '%.9s' "${RUN_COMMIT:-unknown}")"
  _release=${REL_VERSION:-the published release}
  say ""
  case "$1" in
    newer)
      say "Arrmada stays on the build it's running ($_build): it has code the latest"
      say "release ($_release) doesn't, so moving to that would be a step back. Nothing was changed." ;;
    edits)
      say "Arrmada stays on the build it's running ($_build): it was built with local"
      say "edits no release has, so moving to a release would be a step back. Nothing was changed." ;;
    unknown)
      say "Arrmada stays on the build it's running: it doesn't say which code it was built from, so"
      say "there's no telling whether the latest release would be a step back. Nothing was changed."
      say "  To move it to the published releases anyway, add ARRMADA_CHANNEL=stable to .env and run"
      say "  ./update.sh again (a release still never starts on a database newer than it)." ;;
    schema)
      say "✗ The database is newer than $_release: a newer build of Arrmada has already upgraded it," >&2
      say "  and the release can't run on it. Nothing was changed; Arrmada keeps the build it's running." >&2 ;;
  esac
  if [ "$1" = schema ]; then
    say "  Every ./update.sh checks again, and moves to the published releases once one can run on this database." >&2
  else
    say "  Every ./update.sh checks again, and moves to the published releases once one includes this build."
  fi
  say "  To follow main instead (compiled here on every update): add ARRMADA_CHANNEL=main to .env, then ./update.sh."
  media_waits
  if [ "$1" = schema ]; then exit 1; fi
  exit 0
}

# media_waits: when Arrmada stays on its build, nothing is restarted, so a media folder
# changed in .env (ARRMADA_MEDIA_HOST) isn't in use yet. Say so, and how to apply it now.
media_waits() {
  _now=$(media_source Arrmada-app)
  [ -n "$_now" ] || return 0
  _want=${MEDIA_HOST%/}
  case "$_want" in \~/*) _want="${HOME:-/root}/${_want#\~/}" ;; esac
  case "$_want" in
    "") case "$_now" in /*) ;; *) return 0 ;; esac ;;
    "$_now") return 0 ;;
    /*) ;;
    *) case "$_now" in *"_$_want") return 0 ;; esac ;; # a named volume, project_name
  esac
  say ""
  say "! The media folder in .env (ARRMADA_MEDIA_HOST) isn't in use yet: Arrmada still has"
  say "  $(media_name "$_now"). It applies on the next update that restarts Arrmada, or now, on the"
  say "  build it's running, with: docker compose up -d"
}

# choose_channel: an install without ARRMADA_CHANNEL that compiles here (made before the
# line existed) moves to the published releases, but only if that isn't a step back:
# when the latest release includes the code it's running. One running newer code (main
# since the last release, or local work) keeps the build it has, and the next update
# looks again. Nothing ever puts an install on main by itself: that's for its owner to
# choose. A first look at the checkout's release tags spares the download of a release
# that clearly can't be used; the release itself is checked once it's here.
MIGRATE=""
choose_channel() {
  running_build
  _why=""
  case "$RUN_VERSION" in *-dirty) _why=edits ;; esac
  if [ -z "$_why" ] && ! is_sha "$RUN_COMMIT"; then _why=unknown; fi
  if [ -z "$_why" ] && have_git; then
    _tag=$(newest_tag)
    if [ -n "$_tag" ] && git cat-file -e "$_tag^{commit}" 2>/dev/null && git cat-file -e "$RUN_COMMIT^{commit}" 2>/dev/null &&
      ! git merge-base --is-ancestor "$RUN_COMMIT" "$_tag" 2>/dev/null; then
      REL_VERSION=$_tag
      _why=newer
    fi
  fi
  if [ -n "$_why" ]; then stay_put "$_why"; fi
  PULL_IMAGE=$DEFAULT_IMAGE
}

# release_check: the downloaded release replaces the running build only if that isn't a
# step back. Its code has to include the running build's (or, both being releases, be the
# same version or newer), and the database mustn't be newer than the release, which would
# stop it from starting. An image that says nothing about itself (a fork's, say) is taken
# on trust on the stable channel, as ARRMADA_IMAGE always was; the database check still
# applies to it.
release_check() {
  REL_VERSION=$(image_label "$PULL_IMAGE" org.arrmada.version)
  REL_COMMIT=$(image_label "$PULL_IMAGE" org.arrmada.commit)
  require_database_backend_image "$PULL_IMAGE"
  running_build
  _rc=2
  case "$RUN_VERSION" in *-dirty) stay_put edits ;; esac
  if is_release "$RUN_VERSION" && is_release "$REL_VERSION"; then
    _rc=0
    if [ "$(semver_cmp "$REL_VERSION" "$RUN_VERSION")" = -1 ]; then _rc=1; fi
  elif is_sha "$RUN_COMMIT" && is_sha "$REL_COMMIT"; then
    _rc=0
    contains "$REL_COMMIT" "$REL_VERSION" "$RUN_COMMIT" || _rc=$?
  fi
  if [ "$_rc" = 1 ]; then stay_put newer; fi
  if [ "$_rc" = 2 ] && [ -n "$MIGRATE" ]; then stay_put unknown; fi

  # Asked of the release's own binary, on the database as it is, before anything stops.
  # Only an image labelled with the CLI is given arguments (an older one starts a server).
  if [ "$(image_label "$PULL_IMAGE" org.arrmada.cli)" = 1 ]; then
    _rc=0
    _check=$(ARRMADA_APP_IMAGE="$PULL_IMAGE" docker compose run --rm --no-deps -T --user "$RUN_AS" \
      --entrypoint /usr/local/bin/arrmada arrmada-app schema 2>&1 </dev/null) || _rc=$?
    case "$_rc" in
      0) ;;
      3)
        say "$_check" | grep -E '^(database|this build|A newer)' | sed 's/^/  /' >&2
        stay_put schema ;;
      *)
        say "  ! Couldn't compare the database with ${REL_VERSION:-the new version} ($(printf '%s\n' "$_check" | tail -n 1))."
        say "    Going ahead: a release won't start on a database newer than it, and the update then goes back by itself." ;;
    esac
  fi
}

if [ -z "$CHANNEL" ] && [ -z "$FORCE_LOCAL" ]; then
  if [ -n "$PULL_IMAGE" ]; then
    # It already downloads a published image, which is what the stable channel is.
    env_set ARRMADA_CHANNEL stable "$CHANNEL_COMMENT"
    CHANNEL=stable
    say "Added ARRMADA_CHANNEL=stable to .env: this install already downloads the published releases."
  else
    MIGRATE=1
    choose_channel
  fi
fi

# Pull mode: the image is downloaded before anything else changes, and checked.
if [ -n "$PULL_IMAGE" ]; then
  say "Downloading $PULL_IMAGE…"
  if ! docker pull "$PULL_IMAGE"; then
    say "" >&2
    say "✗ Couldn't download $PULL_IMAGE (see above). Nothing was changed; Arrmada is still running the version it was." >&2
    if [ -n "$MIGRATE" ]; then
      say "  (This update would have moved it from compiling here to the published releases. The next one tries again.)" >&2
    fi
    say "  Check the name in ARRMADA_IMAGE and that this server can reach ghcr.io. If the package" >&2
    say "  is private, sign this server in once with 'docker login ghcr.io' and a token of its own" >&2
    say "  that can only read packages. To compile here instead: ./update.sh --force-local" >&2
    exit 1
  fi
  if [ "$(image_id "$PULL_IMAGE")" != "$RUNNING_IMAGE" ]; then
    release_check
  fi
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
# which code is running (docker-compose.yml passes these as build args). A tagged
# release reads as its tag; anything else as the date of its commit, with the short
# commit beside it — a bare hash says nothing about how old the build is. Every git
# call is guarded so a copy that isn't a git checkout still builds. A published image
# was stamped by the release build, so pull mode reads its labels afterwards instead.
if [ -z "$PULL_IMAGE" ]; then
  if [ ! -d .git ] && [ -f .arrmada-source ]; then
    # Downloaded without git: the download recorded which commit it is.
    ARRMADA_COMMIT=$(source_get commit)
    ARRMADA_VERSION=$(source_get date)
    ARRMADA_COMMIT=${ARRMADA_COMMIT:-unknown}
    ARRMADA_VERSION=${ARRMADA_VERSION:-dev}
  else
    ARRMADA_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
    ARRMADA_VERSION=$(git describe --tags --exact-match 2>/dev/null || git log -1 --format=%cd --date=format:%Y.%m.%d 2>/dev/null || echo dev)
    if [ "$ARRMADA_COMMIT" != unknown ] && ! git diff --quiet HEAD -- 2>/dev/null; then
      ARRMADA_VERSION="$ARRMADA_VERSION-dirty"
    fi
  fi
  export ARRMADA_VERSION ARRMADA_COMMIT
fi

# With ARRMADA_MEDIA_HOST in .env (media somewhere other than the managed volume), the
# media init runs before anything is replaced. It only makes the downloads and library
# folders that aren't there yet, so a folder newly set in .env has them before the app
# starts on it, and it refuses a folder that overlaps the database's. When it refuses,
# nothing has changed yet, the rollback target included.
if [ -n "$MEDIA_HOST" ]; then
  if ! docker compose run --rm --no-deps -T arrmada-media-init </dev/null; then
    say "" >&2
    say "✗ Couldn't set up the media folder (ARRMADA_MEDIA_HOST in .env; see above). Nothing was" >&2
    say "  replaced: Arrmada is still running the version it was. Fix the line, then run ./update.sh again." >&2
    exit 1
  fi
fi

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

tighten_env

# Settings that were removed: the app never sees them now, so say so here.
if grep -qE '^ARRMADA_BASE_URL=.*[^/[:space:]"'"'"']' .env; then
  say "  ! ARRMADA_BASE_URL in .env is no longer supported and is ignored — Arrmada is served"
  say "    at the root of its own hostname. Point your reverse proxy at the root, then delete the line."
fi

# Rebuild + recreate ONLY the app. --no-deps leaves the companions (qBittorrent,
# Prowlarr, FlareSolverr) running and, crucially, does NOT re-run the one-shot media /
# qBittorrent init containers — those only need to run once, at install. An update
# only changes the app image, so nothing else needs touching.
#
# In pull mode the published image (downloaded and checked above) is tagged arrmada:dev,
# the name docker-compose.yml runs, so the rollback tags and every step after this one
# work on it exactly as they do on a local build. A switch to the releases runs as the
# stable channel straight away; its .env lines are written once it's up.
UP_OK=1
if [ -n "$MIGRATE" ]; then
  ARRMADA_CHANNEL=stable
  export ARRMADA_CHANNEL
fi
if [ -n "$PULL_IMAGE" ]; then
  say "Restarting Arrmada on $PULL_IMAGE…"
  if ! docker image tag "$PULL_IMAGE" arrmada:dev || ! docker compose up -d --no-build --no-deps arrmada-app; then
    UP_OK=""
  fi
else
  say "Rebuilding and restarting Arrmada…"
  docker compose up -d --build --no-deps arrmada-app || UP_OK=""
fi
# Whether the pull brought nothing new (already on the newest image).
SAME_IMAGE=""
if [ -n "$RUNNING_IMAGE" ] && [ "$(image_id arrmada:dev)" = "$RUNNING_IMAGE" ]; then
  SAME_IMAGE=1
fi

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

# update_failed: the new version replaced the running one and didn't start. Go back to
# the one that was running (kept as arrmada:previous a moment ago) rather than leave
# Arrmada down. The rollback checks the database first: if the new version already
# upgraded it, going back also needs the backup taken at the start of this update, which
# loses whatever happened in the last few minutes, so that is only ever done when someone
# at the terminal says yes.
update_failed() {
  say "" >&2
  say "✗ The new version of Arrmada didn't start. The last lines of its log:" >&2
  show_log
  if [ -z "$RUNNING_IMAGE" ] || [ "$(image_id arrmada:previous)" != "$RUNNING_IMAGE" ] || [ "$(image_id arrmada:dev)" = "$RUNNING_IMAGE" ]; then
    say "" >&2
    say "  There's no earlier running version to go back to. Fix what the log says, then run the" >&2
    say "  script again. Full log: docker compose logs arrmada-app" >&2
    exit 1
  fi
  say "" >&2
  say "Going back to the version that was running before the update…" >&2
  AUTO_ROLLBACK=1
  _rc=0
  ( rollback ) || _rc=$?
  if [ "$_rc" = 3 ] && [ -f "$PREVIOUS_DB_FILE" ]; then
    _answer=""
    if [ -z "$YES" ] && [ -t 0 ]; then
      say "" >&2
      say "The new version had already upgraded the database. Going back needs the backup taken at" >&2
      say "the start of this update ($(cat "$PREVIOUS_DB_FILE")); anything Arrmada recorded since then is lost." >&2
      printf 'Put it back and start the previous version? [y/N] ' >&2
      read -r _answer || _answer=""
    fi
    case "$_answer" in
      y | Y | yes | YES | Yes)
        _rc=0
        ( WITH_DB=1; YES=1; rollback ) || _rc=$? ;;
      *)
        say "  Arrmada is down. Either go back with the database: ./update.sh --rollback --with-db" >&2
        say "  or fix what the log says and run ./update.sh again." >&2
        exit 1 ;;
    esac
  fi
  say "" >&2
  if [ "$_rc" = 0 ]; then
    say "✗ The update didn't go through, so Arrmada is back on the version it was running." >&2
    say "  Nothing else was changed. The new version's log is above; please report it at" >&2
    say "  https://github.com/$REPO/issues" >&2
  else
    say "✗ The update didn't go through, and going back didn't work either (see above)." >&2
    say "  Full log: docker compose logs arrmada-app" >&2
  fi
  exit 1
}

if [ -z "$UP_OK" ]; then
  if [ -n "$RUNNING_IMAGE" ] && [ "$(image_id arrmada:dev)" = "$RUNNING_IMAGE" ] && app_answers; then
    say "" >&2
    say "✗ The build failed (see above). Nothing was replaced; Arrmada is still running the version it was." >&2
    exit 1
  fi
  update_failed
fi
_waited=0
wait_healthy || _waited=$?
if [ "$_waited" = 2 ]; then
  # Not crashed, only slow: going back now would stop it in the middle of whatever it's
  # doing (an upgrade of the database's tables, say), so leave it running and say how to
  # follow it.
  say "" >&2
  say "! The new version is running but hasn't answered for 3 minutes. It may still be upgrading" >&2
  say "  the database. Follow it with: docker compose logs -f arrmada-app" >&2
  say "  If it doesn't come up, go back with: ./update.sh --rollback" >&2
  exit 1
elif [ "$_waited" != 0 ]; then
  update_failed
fi

# A switch to the published releases is recorded now it has worked. (Had it failed, .env
# would still say what it did before, and the next update would look again.)
if [ -n "$MIGRATE" ]; then
  env_set ARRMADA_CHANNEL stable "$CHANNEL_COMMENT"
  if [ -z "$ENV_IMAGE" ]; then
    env_set ARRMADA_IMAGE "$DEFAULT_IMAGE" "# The published image update.sh downloads on the stable channel."
  fi
  say ""
  say "This install now follows the published releases: ARRMADA_CHANNEL=stable is in .env (the"
  say "  .env from before is $ENV_BACKUP). Updates download each new release instead of compiling."
  say "  To follow main instead (compiled here on every update): ARRMADA_CHANNEL=main in .env."
fi

# The companions that share /media with the app (qBittorrent, and SABnzbd when it's on) have
# to see the same folder it does, or downloads land where the app never looks. Only the app
# was restarted above, so after ARRMADA_MEDIA_HOST changes in .env each running companion
# still on the old one is recreated on the new one (that one container, nothing else). One
# that's stopped is left stopped: `docker compose up -d` gives it the new folder when it
# next starts it.
_app_media=$(media_source Arrmada-app)
if [ -n "$_app_media" ]; then
  for _c in arrmada-qbittorrent:Arrmada-qbittorrent arrmada-sabnzbd:Arrmada-sabnzbd; do
    _svc=${_c%%:*}
    _was=$(media_source "${_c#*:}")
    if [ -z "$_was" ] || [ "$_was" = "$_app_media" ] ||
      [ "$(docker inspect -f '{{.State.Running}}' "${_c#*:}" 2>/dev/null)" != true ]; then
      continue
    fi
    say "Moving $_svc to the media Arrmada now uses ($(media_name "$_app_media"))…"
    if docker compose up -d --no-deps "$_svc" </dev/null; then
      if [ "$(media_source "${_c#*:}")" = "$_was" ]; then
        say "  It still has $(media_name "$_was"): docker-compose.override.yml mounts that for it, so it stays."
      else
        say "  What was in its old media ($(media_name "$_was")) is still there; move it across if you want it."
      fi
    else
      say "! $_svc couldn't be restarted on the new media folder. Run: docker compose up -d $_svc" >&2
    fi
  done
fi

# A build kept by an earlier --rollback is superseded by this one.
docker image rm arrmada:rolled-back >/dev/null 2>&1 || true
# Reclaim space from older builds (harmless if nothing to prune). arrmada:previous is
# tagged, so it stays.
docker image prune -f >/dev/null 2>&1 || true

WEBPORT=$(grep -E '^ARRMADA_PORT=' .env | cut -d= -f2)
HOSTIP=$(hostname -I 2>/dev/null | awk '{print $1}')
# Not every hostname knows -I (Unraid's, busybox's); the route to the internet names it too.
if [ -z "$HOSTIP" ] && command -v ip >/dev/null 2>&1; then
  HOSTIP=$(ip route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") { print $(i + 1); exit }}')
fi
say ""
if [ -n "$PULL_IMAGE" ]; then
  # What the release build stamped on the image (the same labels --rollback reads).
  ARRMADA_VERSION=$(image_label arrmada:dev org.arrmada.version)
  ARRMADA_COMMIT=$(image_label arrmada:dev org.arrmada.commit)
  if [ -n "$SAME_IMAGE" ]; then
    say "✓ Arrmada is already on the newest $PULL_IMAGE: ${ARRMADA_VERSION:-unknown} (${ARRMADA_COMMIT:-unknown}).  Open http://${HOSTIP:-localhost}:${WEBPORT:-7878}"
  else
    say "✓ Arrmada updated to ${ARRMADA_VERSION:-unknown} (${ARRMADA_COMMIT:-unknown}) from $PULL_IMAGE.  Open http://${HOSTIP:-localhost}:${WEBPORT:-7878}"
  fi
else
  say "✓ Arrmada updated to ${ARRMADA_VERSION} (${ARRMADA_COMMIT}).  Open http://${HOSTIP:-localhost}:${WEBPORT:-7878}"
fi
say "  A database snapshot is taken automatically before any schema change (<data>/backups, newest 5 kept)."
if docker image inspect arrmada:previous >/dev/null 2>&1; then
  say "  The build that ran before is kept as arrmada:previous: ./update.sh --rollback goes back to it."
fi
qbit_page_notice
