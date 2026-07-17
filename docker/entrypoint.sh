#!/bin/sh
# Run Arrmada as a configurable user (PUID/PGID) — the standard container pattern so the
# app owns its appdata and can read your media, whatever uid your files use. Defaults to
# 1000:1000. The container starts as root, fixes ownership of the data dir, then drops
# privileges to PUID:PGID via su-exec.
set -e

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"
DATA="${ARRMADA_DATA_DIR:-/data}"

mkdir -p "$DATA"
# Make the app's own data dir (DB, config, scratch, recycle) writable by the runtime user.
# Best-effort: never fail boot over a chown (e.g. a read-only or odd filesystem).
chown -R "$PUID:$PGID" "$DATA" 2>/dev/null || true

# Hardware transcode: if a GPU render node was passed in (compose `devices: /dev/dri`),
# open its permissions so the dropped-privilege PUID:PGID user can use VAAPI. The device
# is already cgroup-whitelisted by Docker; this just avoids having to match the host's
# render group. Best-effort — no GPU, or a read-only /dev, is fine (falls back to CPU).
if [ -e /dev/dri ]; then
  chmod -R a+rw /dev/dri 2>/dev/null || true
fi

exec su-exec "$PUID:$PGID" /usr/local/bin/arrmada "$@"
