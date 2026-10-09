#!/usr/bin/env sh
# Arrmada installer — one command, everything set up. Idempotent; safe to re-run.
#
#   git clone https://github.com/tristenlammi/Arrmada && cd Arrmada
#   ./install.sh                  # core stack (app + qBittorrent + FlareSolverr)
#   ./install.sh --with-prowlarr  # also start the optional Prowlarr indexer manager
#
# It asks three things — where your media lives, where to transcode, and your timezone
# (pre-filled from this machine) — then handles the rest automatically: free host ports
# (never clashes with Radarr/Sonarr/qBit), the run user, the database location, and GPU
# pass-through if a GPU is present. The TMDB key and
# each library folder are set in the app's first-run setup, with a folder picker.
# Update later with:  ./update.sh
set -eu
cd "$(dirname "$0")"

say() { printf '%s\n' "$*"; }
ask() { # ask "prompt" "default" -> echoes the answer (default if non-interactive/blank)
  _p="$1"; _d="${2:-}"; _a=""
  if [ -t 0 ]; then printf '%s' "$_p" >&2; read -r _a || _a=""; fi
  [ -z "$_a" ] && _a="$_d"
  printf '%s' "$_a"
}

# detect_tz -> this machine's timezone name (e.g. Australia/Brisbane), or nothing.
detect_tz() {
  _t="${TZ:-}"
  [ -z "$_t" ] && [ -r /etc/timezone ] && _t=$(head -n1 /etc/timezone 2>/dev/null)
  # Unraid keeps it in its flash config (Settings → Date and Time).
  [ -z "$_t" ] && [ -r /boot/config/ident.cfg ] && _t=$(sed -n 's/^timeZone="*\([^"]*\)"*.*/\1/p' /boot/config/ident.cfg 2>/dev/null | head -n1)
  [ -z "$_t" ] && [ -L /etc/localtime ] && _t=$(readlink /etc/localtime 2>/dev/null | sed 's#.*/zoneinfo/##')
  if valid_tz "$_t"; then printf '%s' "$_t"; fi
}
# valid_tz NAME -> 0 if NAME is a timezone this machine knows (any name, if it ships no
# zone files to check against — the app carries its own copy of the database).
valid_tz() {
  case "$1" in "" | /* | *..* | *" "*) return 1 ;; esac
  [ -d /usr/share/zoneinfo ] || return 0
  [ -f "/usr/share/zoneinfo/$1" ]
}
# tz_fix_case NAME -> the correctly-capitalised zone name ("australia/brisbane" →
# "Australia/Brisbane"), or nothing if there's no such zone.
tz_fix_case() {
  [ -d /usr/share/zoneinfo ] || return 0
  find /usr/share/zoneinfo -type f -ipath "/usr/share/zoneinfo/$1" 2>/dev/null | grep -v '/right/\|/posix/' | head -n1 | sed 's#^/usr/share/zoneinfo/##'
}

# port_in_use PORT -> 0 (true) if something is already listening on PORT on this host.
port_in_use() {
  _p="$1"
  if command -v ss >/dev/null 2>&1; then
    ss -ltnH 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${_p}\$"
  elif command -v netstat >/dev/null 2>&1; then
    netstat -ltn 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${_p}\$"
  else
    return 1 # can't check — assume free
  fi
}
# free_port PREFERRED... -> first preferred port that's free, else scan upward from 18000.
free_port() {
  for _p in "$@"; do port_in_use "$_p" || { printf '%s' "$_p"; return; }; done
  _p=18000
  while port_in_use "$_p"; do _p=$((_p + 1)); done
  printf '%s' "$_p"
}
# rand_port -> a random high port (for BitTorrent) that isn't currently in use.
rand_port() {
  _p=$(awk 'BEGIN { srand(); print int(20000 + rand() * 40000) }')
  while port_in_use "$_p"; do _p=$((_p + 1)); done
  printf '%s' "$_p"
}

# wait_healthy -> 0 once the app answers its health check, 1 if it crashed or never came up.
# It asks from inside the container, so it works whatever host port was picked and needs no
# curl/wget on the host.
wait_healthy() {
  _i=0
  printf 'Waiting for Arrmada to start' >&2
  while [ "$_i" -lt 90 ]; do
    _state=$(docker inspect -f '{{.State.Status}}' Arrmada-app 2>/dev/null || echo missing)
    case "$_state" in
      exited|dead) printf '\n' >&2; return 1 ;;
    esac
    if docker exec Arrmada-app curl -fsS -o /dev/null "http://127.0.0.1:7878/api/health" 2>/dev/null; then
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

# ── prerequisites ──────────────────────────────────────────────────────────────
if ! command -v docker >/dev/null 2>&1; then
  say "✗ Docker isn't installed (or not on PATH). Install Docker, then re-run ./install.sh" >&2; exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  say "✗ 'docker compose' (v2) isn't available. Update Docker Engine, then re-run." >&2; exit 1
fi

# ── .env (created once, never overwritten) ──────────────────────────────────────
if [ ! -f .env ]; then
  say "First run — let's set up Arrmada. Three questions, then it's automatic."
  say ""

  # The TMDB key is entered in the app's first-run setup. An exported value is still
  # honoured, for scripted installs.
  KEY="${ARRMADA_TMDB_API_KEY:-}"

  say "1/3  Where does your media live? Give the folder that CONTAINS your libraries +"
  say "     downloads (e.g. /mnt/user/masterdirectory). Enter to use Arrmada's own managed storage."
  MEDIA=$(ask "     Media folder [blank = managed]: " "")
  # On Unraid, "managed storage" is a Docker volume — inside docker.img (20 GB by
  # default), which downloads fill in no time. Say so once before accepting it.
  if [ -z "$MEDIA" ] && [ -d /mnt/user ] && [ -t 0 ]; then
    say "     ! On Unraid, managed storage lives inside docker.img, and downloads will fill it."
    say "       Give a share instead (e.g. /mnt/user/data), or press Enter again to use it anyway."
    MEDIA=$(ask "     Media folder [blank = managed]: " "")
  fi
  # A mistyped folder would be created empty (and owned by root) by Docker, and the app
  # would find no media. Ask again rather than guess.
  while [ -n "$MEDIA" ] && [ ! -d "$MEDIA" ]; do
    say "     ! $MEDIA doesn't exist. Check the path (it's case-sensitive)."
    MEDIA=$(ask "     Media folder [blank = managed]: " "")
  done

  say ""
  say "2/3  Where should Convert transcode? A fast SSD/NVMe pool, NOT the array (e.g."
  say "     /mnt/cache/transcode). Enter to skip (transcoding uses container storage)."
  TRANSCODE=$(ask "     Transcode folder [blank = skip]: " "")

  # Timezone. Schedules — the overnight encode window, nightly sweeps, listening per
  # day — run on this clock; without it the container runs on UTC and "overnight"
  # quietly means overnight in London.
  DETECTED_TZ=$(detect_tz)
  say ""
  say "3/3  What's your timezone? Schedules like overnight encoding run on this clock."
  say "     Use the Region/City name — e.g. Australia/Brisbane, America/New_York, Europe/London."
  say "     Find yours in the \"TZ identifier\" column at:"
  say "     https://en.wikipedia.org/wiki/List_of_tz_database_time_zones"
  if [ -n "$DETECTED_TZ" ]; then
    TZONE=$(ask "     Timezone [Enter = $DETECTED_TZ, detected from this machine]: " "$DETECTED_TZ")
  else
    TZONE=$(ask "     Timezone [Enter = Etc/UTC]: " "Etc/UTC")
  fi
  while ! valid_tz "$TZONE"; do
    _fixed=$(tz_fix_case "$TZONE")
    if [ -n "$_fixed" ]; then TZONE=$_fixed; break; fi
    if [ ! -t 0 ]; then TZONE="Etc/UTC"; break; fi
    say "     ! \"$TZONE\" isn't a timezone name. Use Region/City from the list above (e.g. Australia/Brisbane)."
    TZONE=$(ask "     Timezone: " "${DETECTED_TZ:-Etc/UTC}")
  done

  # Auto: run user. Match the media folder's owner so the app can read/write it; fall back to
  # Unraid's nobody/users (99/100) or a generic 1000/1000.
  OWNER=""
  [ -n "$MEDIA" ] && [ -e "$MEDIA" ] && OWNER=$(stat -c '%u:%g' "$MEDIA" 2>/dev/null || echo "")
  case "$OWNER" in
    "" | "0:0") if [ -d /mnt/user ]; then OWNER="99:100"; else OWNER="1000:1000"; fi ;;
  esac
  PUID=${OWNER%:*}; PGID=${OWNER#*:}

  # Auto: free host ports — so it never collides with Radarr (7878), an existing qBit (8080), etc.
  WEBPORT=$(free_port 7878 7979 8790 8385)
  QBWEB=$(free_port 8080 8081 8082)
  PROWPORT=$(free_port 9696 9697 9698)
  AUDIOPORT=$(free_port 13379 13380 13381 13382)
  BTPORT=$(rand_port)

  # Auto: database/config location. Unraid → appdata; otherwise ./data inside this folder.
  if [ -d /mnt/user/appdata ]; then DATA="/mnt/user/appdata/arrmada"; else DATA="./data"; fi

  # Auto: GPU pass-through when a render node exists on the host.
  GPU=""
  [ -e /dev/dri ] && GPU=1

  {
    say "# ─── Arrmada configuration ─── edit, then ./update.sh to apply ───"
    say ""
    say "# Movie/TV metadata (required for Movies & TV). Free: https://www.themoviedb.org/settings/api"
    say "ARRMADA_TMDB_API_KEY=$KEY"
    say "ARRMADA_OMDB_API_KEY="
    say "# Books: optional Hardcover key (hardcover.app → Settings → Hardcover API). Without it Open Library is used."
    say "ARRMADA_HARDCOVER_API_KEY="
    say ""
    say "# Host ports (auto-picked free at install so nothing clashes with your other apps)."
    say "ARRMADA_PORT=$WEBPORT"
    say "ARRMADA_QBIT_WEBUI_PORT=$QBWEB"
    say "ARRMADA_PROWLARR_PORT=$PROWPORT"
    say "# Audiobook server for listening apps (off until switched on in Services → Audiobooks)."
    say "ARRMADA_AUDIOBOOK_PORT=$AUDIOPORT"
    say "ARRMADA_QBIT_PORT=$BTPORT"
    say ""
    say "# Timezone (auto-detected from this host). Schedules — the encode window especially —"
    say "# run in this zone. Full list: en.wikipedia.org/wiki/List_of_tz_database_time_zones"
    say "TZ=$TZONE"
    say ""
    say "# Run as this user/group (auto-detected from your media folder / platform)."
    say "ARRMADA_PUID=$PUID"
    say "ARRMADA_PGID=$PGID"
    say ""
    say "# Database + config live here."
    say "ARRMADA_DATA_HOST=$DATA"
    if [ -n "$MEDIA" ]; then
      say ""
      say "# Your media folder, mounted at /storage inside the app. Each library's folder under"
      say "# it is picked in the app's first-run setup (Settings → Library later)."
      say "ARRMADA_STORAGE_HOST=$MEDIA"
    fi
  } > .env

  # Generate the compose override: media mount, transcode mount, and GPU devices — only the
  # pieces that apply, so the file is always valid YAML.
  if [ -n "$MEDIA" ] || [ -n "$TRANSCODE" ] || [ -n "$GPU" ]; then
    {
      say "# Auto-generated by install.sh — merged into docker-compose.yml. Edit freely + ./update.sh."
      say "services:"
      say "  arrmada-app:"
      if [ -n "$MEDIA" ] || [ -n "$TRANSCODE" ]; then
        say "    volumes:"
        [ -n "$MEDIA" ] && say "      - \"$MEDIA:/storage\""
        [ -n "$TRANSCODE" ] && say "      - \"$TRANSCODE:/transcode\""
      fi
      if [ -n "$GPU" ]; then
        say "    devices:"
        say "      - /dev/dri:/dev/dri"
      fi
      if [ -n "$MEDIA" ]; then
        say "  arrmada-qbittorrent:"
        say "    volumes:"
        say "      - \"$MEDIA:/storage\""
      fi
    } > docker-compose.override.yml
    say "✓ Wrote docker-compose.override.yml"
  fi

  [ "$DATA" = "./data" ] && mkdir -p ./data
  [ -n "$TRANSCODE" ] && mkdir -p "$TRANSCODE" 2>/dev/null || true
  say ""
  say "✓ Wrote .env"
  say "   • Web UI port:   $WEBPORT"
  say "   • qBit WebUI:    $QBWEB"
  say "   • BitTorrent:    $BTPORT   (forward this on your router, TCP+UDP)"
  say "   • Audiobooks:    $AUDIOPORT   (listening apps; switch on in Services → Audiobooks)"
  say "   • Timezone:      $TZONE   (change later: TZ in .env, then ./update.sh)"
  say "   • Run as:        $PUID:$PGID"
  if [ -n "$GPU" ]; then
    say "   • GPU:           /dev/dri detected → hardware transcode enabled"
  else
    say "   • GPU:           none detected → Convert will use the CPU"
  fi
  [ -n "$MEDIA" ] && say "   • Media folder:  $MEDIA → /storage inside the app"
else
  say ".env already exists — keeping your settings."
fi

# ── build + start ──────────────────────────────────────────────────────────────
PROFILES=""
if [ "${1:-}" = "--with-prowlarr" ]; then
  PROFILES="--profile prowlarr"
  say "Including the optional Prowlarr indexer manager."
fi

# Stamp the build with what it was built from, so the Dashboard says which code is running:
# a release tag, or else the commit's date with the short commit beside it (same as update.sh).
ARRMADA_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
ARRMADA_VERSION=$(git describe --tags --exact-match 2>/dev/null || git log -1 --format=%cd --date=format:%Y.%m.%d 2>/dev/null || echo dev)
if [ "$ARRMADA_COMMIT" != unknown ] && ! git diff --quiet HEAD -- 2>/dev/null; then
  ARRMADA_VERSION="$ARRMADA_VERSION-dirty"
fi
export ARRMADA_VERSION ARRMADA_COMMIT

say ""
say "Building and starting Arrmada… (the first build compiles everything — a few minutes)"
# shellcheck disable=SC2086
docker compose $PROFILES up -d --build

wait_healthy || fail_start

WEBPORT=$(grep -E '^ARRMADA_PORT=' .env | cut -d= -f2)
HOSTIP=$(hostname -I 2>/dev/null | awk '{print $1}')
say ""
say "✓ Arrmada is running.  Open http://${HOSTIP:-localhost}:${WEBPORT:-7878}"
say "  Create your admin account, then the setup walks you through the TMDB key and your folders."
say "  Update anytime with:  ./update.sh"
if [ -z "$PROFILES" ]; then
  say ""
  say "  Prowlarr is optional and was NOT installed. Want it? Run:  ./install.sh --with-prowlarr"
fi
