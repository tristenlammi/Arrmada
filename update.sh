#!/usr/bin/env sh
# Arrmada updater — pull the latest code and rebuild in place.
#
#   ./update.sh                  # core stack
#   ./update.sh --with-prowlarr  # keep the optional Prowlarr companion running too
#
# Your .env, database, and media volumes are preserved. Run it any time to upgrade
# or to apply changes you made to .env.
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

PROFILES=""
[ "${1:-}" = "--with-prowlarr" ] && PROFILES="--profile prowlarr"

say "Rebuilding and restarting…"
# shellcheck disable=SC2086
docker compose $PROFILES up -d --build

# Reclaim space from the previous image build (harmless if nothing to prune).
docker image prune -f >/dev/null 2>&1 || true

WEBPORT=$(grep -E '^ARRMADA_PORT=' .env | cut -d= -f2)
say ""
say "✓ Arrmada updated.  Open http://localhost:${WEBPORT:-7878}"
