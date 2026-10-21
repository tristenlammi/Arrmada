#!/bin/sh
# Disposable image smoke test. No ports, databases or volumes from a deployment
# are reused; callers supply only the image built in this Actions job.
set -eu

IMAGE=${1:?usage: test-postgres-image.sh IMAGE}
WORK=$(mktemp -d)
ID="arrmada-pg-smoke-$(basename "$WORK" | tr '[:upper:].' '[:lower:]-')"
NETWORK="$ID-net"
DB="$ID-db"
APP="$ID-app"
VOLUME="$ID-data"
URL="postgres://arrmada_app:ci-only-password@$DB/arrmada_test?sslmode=disable"

cleanup() {
  status=$?
  trap - 0
  if [ "$status" -ne 0 ]; then
    docker logs "$APP" || true
    docker logs "$DB" || true
  fi
  docker rm -f "$APP" "$DB" >/dev/null 2>&1 || true
  docker volume rm "$VOLUME" >/dev/null 2>&1 || true
  docker network rm "$NETWORK" >/dev/null 2>&1 || true
  rmdir "$WORK" || true
  exit "$status"
}
trap cleanup 0
trap 'exit 1' INT TERM

docker network create "$NETWORK" >/dev/null
docker volume create "$VOLUME" >/dev/null
docker run -d --name "$DB" --network "$NETWORK" \
  -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD=ci-admin-only \
  -e POSTGRES_DB=arrmada_test postgres:18 >/dev/null
ready=false
for _ in $(seq 1 60); do
  if docker exec "$DB" pg_isready -h 127.0.0.1 -U postgres -d arrmada_test >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
[ "$ready" = true ] || { echo 'Disposable PostgreSQL did not become ready' >&2; exit 1; }
docker exec -i "$DB" psql -v ON_ERROR_STOP=1 -U postgres -d arrmada_test <<'SQL'
CREATE ROLE arrmada_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT PASSWORD 'ci-only-password';
GRANT CREATE ON DATABASE arrmada_test TO arrmada_app;
CREATE SCHEMA arrmada_recovered AUTHORIZATION arrmada_app;
CREATE SCHEMA unrelated;
CREATE TABLE unrelated.sentinel (value text NOT NULL);
INSERT INTO unrelated.sentinel VALUES ('preserved');
SQL

start_app() {
  docker run -d --name "$APP" --network "$NETWORK" \
    -p 127.0.0.1::7878 -v "$VOLUME:/data" \
    -e PUID=1000 -e PGID=1000 -e ARRMADA_DATA_DIR=/data \
    -e ARRMADA_HOST=0.0.0.0 -e ARRMADA_PORT=7878 \
    -e ARRMADA_LIBRARY_DIR=/data/library -e ARRMADA_DOWNLOADS_DIR=/data/downloads \
    -e ARRMADA_DATABASE_BACKEND=postgres -e "ARRMADA_POSTGRES_URL=$URL" \
    -e "ARRMADA_POSTGRES_SCHEMA=$1" \
    -e ARRMADA_BUNDLE_QBITTORRENT=false -e ARRMADA_BUNDLE_FLARESOLVERR=false \
    -e ARRMADA_BUNDLE_PROWLARR=false -e ARRMADA_BUNDLE_SABNZBD=false \
    "$IMAGE" >/dev/null
  address=$(docker port "$APP" 7878/tcp)
  ready=false
  for _ in $(seq 1 90); do
    if curl --fail --silent --max-time 2 "http://$address/api/health" >/dev/null; then
      ready=true
      break
    fi
    sleep 1
  done
  [ "$ready" = true ] || { echo 'PostgreSQL-backed image did not become healthy' >&2; exit 1; }
  docker exec -u 1000:1000 "$APP" sh -ec '
    "$ARRMADA_POSTGRES_BIN/pg_dump" --version
    "$ARRMADA_POSTGRES_BIN/pg_restore" --version
    test ! -e /data/arrmada.db
  '
}

start_app arrmada
# Subsequent starts and maintenance need schema ownership, not database CREATE.
docker exec "$DB" psql -v ON_ERROR_STOP=1 -U postgres -d arrmada_test \
  -c 'REVOKE CREATE ON DATABASE arrmada_test FROM arrmada_app'
docker exec -e PGPASSWORD=ci-only-password "$DB" psql -v ON_ERROR_STOP=1 \
  -h 127.0.0.1 -U arrmada_app -d arrmada_test \
  -c "INSERT INTO arrmada.settings (key,value) VALUES ('image-smoke','preserved')"
archive=$(docker exec -u 1000:1000 "$APP" /usr/local/bin/arrmada backup --kind pre-update)
case "$archive" in /data/backups/*.pgdump) ;; *) echo 'Native backup CLI returned an unexpected path' >&2; exit 1 ;; esac
docker stop --time 15 "$APP" >/dev/null
[ "$(docker inspect --format '{{.State.ExitCode}}' "$APP")" = 0 ] || { echo 'Source image did not shut down cleanly' >&2; exit 1; }
docker run --rm --network "$NETWORK" -v "$VOLUME:/data" --user 1000:1000 \
  --entrypoint /usr/local/bin/arrmada -e ARRMADA_DATA_DIR=/data \
  -e ARRMADA_DATABASE_BACKEND=postgres -e "ARRMADA_POSTGRES_URL=$URL" \
  -e ARRMADA_POSTGRES_SCHEMA=arrmada \
  "$IMAGE" restore-postgres --schema arrmada_recovered "$archive"
docker rm "$APP" >/dev/null
start_app arrmada_recovered
value=$(docker exec "$DB" psql -v ON_ERROR_STOP=1 -At -U postgres -d arrmada_test \
  -c "SELECT value FROM arrmada_recovered.settings WHERE key='image-smoke'")
[ "$value" = preserved ] || { echo 'Recovered image lost the database marker' >&2; exit 1; }
value=$(docker exec "$DB" psql -v ON_ERROR_STOP=1 -At -U postgres -d arrmada_test \
  -c 'SELECT value FROM unrelated.sentinel')
[ "$value" = preserved ] || { echo 'Image touched an unrelated database schema' >&2; exit 1; }
docker stop --time 15 "$APP" >/dev/null
[ "$(docker inspect --format '{{.State.ExitCode}}' "$APP")" = 0 ] || { echo 'Image did not shut down cleanly' >&2; exit 1; }
echo 'Image PostgreSQL startup, native backup, offline restore, isolation and shutdown passed.'
