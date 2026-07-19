#!/usr/bin/env sh
# Arrmada installer — one command, everything set up. Idempotent; safe to re-run.
#
#   git clone https://github.com/tristenlammi/Arrmada && cd Arrmada
#   ./install.sh                  # core stack (app + qBittorrent + FlareSolverr)
#   ./install.sh --with-prowlarr  # also start the optional Prowlarr indexer manager
#
# It asks two things — where your media lives and where to transcode — then handles the
# rest automatically: free host ports (never clashes with Radarr/Sonarr/qBit), the run
# user, the database location, and GPU pass-through if a GPU is present.
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

# ── prerequisites ──────────────────────────────────────────────────────────────
if ! command -v docker >/dev/null 2>&1; then
  say "✗ Docker isn't installed (or not on PATH). Install Docker, then re-run ./install.sh" >&2; exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  say "✗ 'docker compose' (v2) isn't available. Update Docker Engine, then re-run." >&2; exit 1
fi

# ── .env (created once, never overwritten) ──────────────────────────────────────
if [ ! -f .env ]; then
  say "First run — let's set up Arrmada. Two questions, then it's automatic."
  say ""

  KEY="${ARRMADA_TMDB_API_KEY:-}"
  [ -z "$KEY" ] && KEY=$(ask "TMDB API key (free: themoviedb.org — needed for Movies & TV; Enter to skip): " "")

  say ""
  say "1/2  Where does your media live? Give the folder that CONTAINS your libraries +"
  say "     downloads (e.g. /mnt/user/masterdirectory). Enter to use Arrmada's own managed storage."
  MEDIA=$(ask "     Media folder [blank = managed]: " "")

  say ""
  say "2/2  Where should Convert transcode? A fast SSD/NVMe pool, NOT the array (e.g."
  say "     /mnt/cache/transcode). Enter to skip (transcoding uses container storage)."
  TRANSCODE=$(ask "     Transcode folder [blank = skip]: " "")

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
    say ""
    say "# Host ports (auto-picked free at install so nothing clashes with your other apps)."
    say "ARRMADA_PORT=$WEBPORT"
    say "ARRMADA_QBIT_WEBUI_PORT=$QBWEB"
    say "ARRMADA_PROWLARR_PORT=$PROWPORT"
    say "ARRMADA_QBIT_PORT=$BTPORT"
    say ""
    say "# Run as this user/group (auto-detected from your media folder / platform)."
    say "ARRMADA_PUID=$PUID"
    say "ARRMADA_PGID=$PGID"
    say ""
    say "# Database + config live here."
    say "ARRMADA_DATA_HOST=$DATA"
    if [ -n "$MEDIA" ]; then
      say ""
      say "# Your media folder is mounted at /storage inside the app. Point each library at"
      say "# its subfolder (adjust to your actual folder names, or set them in the app later)."
      say "ARRMADA_STORAGE_HOST=$MEDIA"
      say "ARRMADA_MOVIES_DIR=/storage/media/movies"
      say "ARRMADA_TV_DIR=/storage/media/tvshows"
      say "ARRMADA_EBOOKS_DIR=/storage/media/ebooks"
      say "ARRMADA_AUDIOBOOKS_DIR=/storage/media/audiobooks"
      say "ARRMADA_DOWNLOADS_DIR=/storage/torrents"
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
        [ -n "$MEDIA" ] && say "      - $MEDIA:/storage"
        [ -n "$TRANSCODE" ] && say "      - $TRANSCODE:/transcode"
      fi
      if [ -n "$GPU" ]; then
        say "    devices:"
        say "      - /dev/dri:/dev/dri"
      fi
      if [ -n "$MEDIA" ]; then
        say "  arrmada-qbittorrent:"
        say "    volumes:"
        say "      - $MEDIA:/storage"
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
  say "   • Run as:        $PUID:$PGID"
  [ -n "$GPU" ] && say "   • GPU:           /dev/dri detected → hardware transcode enabled" || say "   • GPU:           none detected → Convert will use the CPU"
  [ -z "$KEY" ] && say "   ! No TMDB key — add ARRMADA_TMDB_API_KEY to .env for Movies/TV, then ./update.sh"
else
  say ".env already exists — keeping your settings."
fi

# ── build + start ──────────────────────────────────────────────────────────────
PROFILES=""
if [ "${1:-}" = "--with-prowlarr" ]; then
  PROFILES="--profile prowlarr"
  say "Including the optional Prowlarr indexer manager."
fi

say ""
say "Building and starting Arrmada… (the first build compiles everything — a few minutes)"
# shellcheck disable=SC2086
docker compose $PROFILES up -d --build

WEBPORT=$(grep -E '^ARRMADA_PORT=' .env | cut -d= -f2)
say ""
say "✓ Arrmada is up.  Open http://localhost:${WEBPORT:-7878}"
say "  Update anytime with:  ./update.sh"
if [ -z "$PROFILES" ]; then
  say ""
  say "  Prowlarr is optional and was NOT installed. Want it? Run:  ./install.sh --with-prowlarr"
fi
