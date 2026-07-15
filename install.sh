#!/usr/bin/env sh
# Arrmada installer — one-shot first-run setup. Idempotent; safe to re-run.
#
#   git clone https://github.com/tristenlammi/Arrmada && cd Arrmada
#   ./install.sh                  # core stack (app + qBittorrent + FlareSolverr)
#   ./install.sh --with-prowlarr  # also start the optional Prowlarr indexer manager
#
# It creates .env (with a random BitTorrent port, and prompts for your TMDB key),
# then builds and starts everything. To update later:  ./update.sh
set -eu
cd "$(dirname "$0")"

say() { printf '%s\n' "$*"; }

# ── prerequisites ──────────────────────────────────────────────────────────────
if ! command -v docker >/dev/null 2>&1; then
  say "✗ Docker isn't installed (or not on PATH). Install Docker, then re-run ./install.sh" >&2
  exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  say "✗ 'docker compose' (v2) isn't available. Update Docker Engine, then re-run." >&2
  exit 1
fi

# ── .env (created once, never overwritten) ──────────────────────────────────────
if [ ! -f .env ]; then
  say "First run — creating .env…"

  # A random high port, avoiding low/ephemeral ranges. This is both the Docker-published
  # port and qBittorrent's incoming port (Arrmada keeps them in sync), so you forward
  # just this one on your router.
  PORT=$(awk 'BEGIN { srand(); print int(20000 + rand() * 40000) }')

  # TMDB key: take it from the environment if provided, else prompt when interactive.
  KEY="${ARRMADA_TMDB_API_KEY:-}"
  if [ -z "$KEY" ] && [ -t 0 ]; then
    printf 'TMDB API key (free from https://www.themoviedb.org/settings/api — needed to add/scan Movies & TV)\n  Paste it now, or press Enter to add it later: '
    read -r KEY || KEY=""
  fi

  cat > .env <<EOF
# ─── Arrmada configuration ─── edit, then run ./update.sh to apply ───

# Movie/TV metadata (required to add or scan Movies & TV).
#   Free v3 key: https://www.themoviedb.org/settings/api
ARRMADA_TMDB_API_KEY=$KEY
# Optional external ratings (IMDb / Rotten Tomatoes / Metacritic).
#   Free key: https://www.omdbapi.com/apikey.aspx
ARRMADA_OMDB_API_KEY=

# Web UI port.
ARRMADA_PORT=7878

# Incoming BitTorrent port (published + auto-applied inside qBittorrent).
# Forward this on your router (TCP + UDP) for good seeding.
ARRMADA_QBIT_PORT=$PORT

# Require login (recommended once this is reachable on your network). First visit
# creates the admin account. Leave false for a quick local trial.
ARRMADA_AUTH_ENABLED=false

# ─── Using an EXISTING library ───────────────────────────────────────────────
# By default Arrmada keeps its own managed library in a Docker volume. To point it
# at library folders you already have, copy docker-compose.override.example.yml to
# docker-compose.override.yml, edit the paths, then set these to the in-container
# mount points (leave blank for the managed library):
ARRMADA_MOVIES_DIR=
ARRMADA_TV_DIR=
ARRMADA_EBOOKS_DIR=
ARRMADA_AUDIOBOOKS_DIR=
EOF

  say "✓ Wrote .env  (BitTorrent port: $PORT)"
  [ -z "$KEY" ] && say "  ! No TMDB key set yet — add ARRMADA_TMDB_API_KEY to .env to enable Movies/TV, then ./update.sh"
  say "  → Forward TCP+UDP $PORT on your router to this machine for best seeding."
else
  say ".env already exists — keeping your settings."
fi

# ── build + start ──────────────────────────────────────────────────────────────
PROFILES=""
[ "${1:-}" = "--with-prowlarr" ] && PROFILES="--profile prowlarr"

say "Building and starting Arrmada… (the first build downloads + compiles — a few minutes)"
# shellcheck disable=SC2086
docker compose $PROFILES up -d --build

WEBPORT=$(grep -E '^ARRMADA_PORT=' .env | cut -d= -f2)
say ""
say "✓ Arrmada is up.  Open http://localhost:${WEBPORT:-7878}"
say "  Update anytime with:  ./update.sh"
