#!/usr/bin/env sh
# Arrmada first-run setup: creates .env (with a random BitTorrent port) and
# starts the stack. Safe to re-run — it won't overwrite an existing .env.
#
#   ./install.sh                 # core stack
#   ./install.sh --with-prowlarr # also start the optional Prowlarr companion
set -eu

cd "$(dirname "$0")"

if [ ! -f .env ]; then
	# A random high port, avoiding the low/ephemeral ranges. This becomes both the
	# Docker-published port and qBittorrent's incoming port (Arrmada keeps them in
	# sync), so you only ever forward this one port on your router.
	PORT=$(awk 'BEGIN { srand(); print int(20000 + rand() * 40000) }')
	printf 'ARRMADA_TMDB_API_KEY=%s\nARRMADA_QBIT_PORT=%s\n' "${ARRMADA_TMDB_API_KEY:-}" "$PORT" > .env
	echo "Created .env with a random BitTorrent port: $PORT"
	echo "→ Forward TCP+UDP $PORT on your router to this machine for best seeding."
else
	echo ".env already exists — leaving it as-is."
fi

PROFILES=""
[ "${1:-}" = "--with-prowlarr" ] && PROFILES="--profile prowlarr"

echo "Starting Arrmada…"
# shellcheck disable=SC2086
docker compose $PROFILES up -d --build

echo "Done. Open http://localhost:7878"
