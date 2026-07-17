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

if ! docker compose version >/dev/null 2>&1; then
  say "✗ 'docker compose' (v2) isn't available." >&2
  exit 1
fi
if [ ! -f .env ]; then
  say "✗ No .env found — run ./install.sh first." >&2
  exit 1
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

# Reclaim space from the previous image build (harmless if nothing to prune).
docker image prune -f >/dev/null 2>&1 || true

WEBPORT=$(grep -E '^ARRMADA_PORT=' .env | cut -d= -f2)
say ""
say "✓ Arrmada updated.  Open http://localhost:${WEBPORT:-7878}"
