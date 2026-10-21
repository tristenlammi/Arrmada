# Developing Arrmada

The backend is Go 1.26 with SQLite; the web UI is React, TypeScript and Tailwind (Vite),
embedded into the Go binary.

```sh
go run ./cmd/arrmada                  # backend (Go 1.26+) on :7878
cd web && npm install && npm run dev  # UI (Node 24) on :5173, proxies /api
```

For a single binary with the UI inside it:

```sh
cd web && npm run build && cd .. && go build -o arrmada ./cmd/arrmada
```

To build the Docker image from your checkout, put `ARRMADA_APP_IMAGE=arrmada:dev` in `.env`
(otherwise the build is tagged with the published image's name) and run
`docker compose up -d --build`. Without `--build`, `docker compose up -d` runs the published
image. A cold image build needs about 2 GB of free memory (4 GB is comfortable): the heavy
compiles take turns and size their parallel jobs to free memory (`docker/build-heavy`; set the
build argument `ARRMADA_BUILD_JOBS` to fix the count). A cold build takes roughly 20 minutes on
four cores; rebuilds after a Go or web change take under a minute.

## Checks

CI runs these on every push:

```sh
gofmt -l $(git ls-files '*.go')        # must print nothing
go vet ./... && go test -race -timeout 40m ./...
shellcheck -s sh update.sh install.sh docker/entrypoint.sh
cd web && npm run lint && npm test && npx tsc --noEmit -p . && npm run build && npm run e2e
```

`npm run e2e` runs the Playwright tests against the built UI with every API call answered
from fixtures in `web/e2e`, so no backend is needed.

### Container verification in GitHub Actions

The **Image** workflow (`.github/workflows/image.yml`) builds plain and all-in-one
images natively on amd64 and arm64 on pull requests to `main`, pushes to `main`,
and manual dispatch. It never logs into a registry or publishes an image. Each
build loads its image only into that disposable runner and runs
`scripts/test-postgres-image.sh` against a new PostgreSQL 18 container.

The smoke test provisions a restricted role with database `CREATE`, starts Arrmada
through the image's real entrypoint as UID/GID 1000, checks native clients and
health after automatic schema creation, revokes database `CREATE`, makes a native
backup, stops the app, restores into a different pre-provisioned empty
schema, and starts the recovered installation. It verifies the transferred
marker, an unrelated schema's sentinel, no SQLite database creation, and clean
shutdown. Bundled companion services are off: this validates the PostgreSQL app
path in the all-in-one image, not the companions themselves. All test containers,
network and data volume are disposable and removed on exit.

With Docker locally, the same check can run against an already-built image:

```sh
sh scripts/test-postgres-image.sh arrmada:dev
```

A workflow definition or a passing Go build is not proof that a container works;
record the actual Image run results before calling container verification passed.

The separate **Publish test image** workflow is manual-only. Supply the full
source commit SHA: its latest CI and Image validation runs must both have passed.
It builds the plain image natively on amd64 and arm64, repeats the PostgreSQL
smoke test before pushing each image, then publishes
`ghcr.io/<repository-owner>/arrmada:test-<full-source-sha>`. It does not change
`stable`/`latest` tags, create a GitHub release, or deploy anything. The run summary
and `test-image-reference` artifact record the multi-platform digest to pin in a
deployment. New GHCR packages may be private by default; check package visibility
or supply registry credentials before expecting an unauthenticated cluster pull.

### PostgreSQL integration tests

The PostgreSQL connection boundary is tested against a real server, not a SQL mock.
It is groundwork for the optional backend; the application still uses SQLite until
the repository queries, schema lifecycle and administration paths are ported.

Use a **disposable local database**, never an existing installation or a shared server:

```sh
export ARRMADA_TEST_POSTGRES_ADMIN_URL='postgres://postgres:test-password@127.0.0.1:5432/arrmada_test?sslmode=disable'
go test -race -count=1 ./internal/store -run 'Test(Postgres|Bind|DatabaseSQLiteBoundary)'
```

The administrator URL is for test setup only: the fixture creates randomly named schemas
and restricted login roles, runs the application connection as those roles, then removes
only its own objects. The roles cannot create databases or roles and are not superusers.
The tests check per-connection schema isolation, ownership checks, parameter binding,
prepared queries and transaction rollback while another schema holds unrelated data.
They also exercise the settings service's persistence, concurrent writes and failed-write
behavior against PostgreSQL; migration locking, checksums, backup-required upgrades,
transactional failure recovery, schema/column/type/index/foreign-key parity, and paired
SQLite/PostgreSQL tests for book edits and viewing-group triggers. Concurrent viewing
inserts are checked against an independent window-function calculation.
Authentication, sessions, personal API keys, account merging, configuration
repositories and persisted jobs have real PostgreSQL integration coverage too.
Repository coverage also checks duplicate episode refreshes preserving library state,
book freshness and alias inserts, acquisition expiry, recent imports, outbox retry
deduplication, and audiobook rename identity (paired with SQLite).
Paired JSON tests cover concurrent movie/show metadata backfills, preservation of
unknown fields and large integers, per-season notification state, folder-path/cache
rewrites and rollback, plus ASCII-only matching. PostgreSQL JSON mutations read and
write text documents under `WithTx` rather than forcing them through `jsonb` or a
lossy struct round-trip; scalar reads use guarded native `json` expressions.
Insights tests compare watch-time clamping, grouped history, imports/overlap repair,
person/title totals and graph aggregates on both engines. PostgreSQL graph buckets
use the application's local timezone (including custom fixed zones and repeated DST
hours), not the remote server's timezone. Book edit/reset stamps and repeated offline
listening uploads have paired high-water-mark tests as well.
Calendar/list/preset tests cover undated and malformed provider dates, integer
monitoring flags, acquisition summaries and literal/case-insensitive searches.
Subtitle queue ids are checked across pruning and service reconstruction, with
an upgrade test for the explicit-id sequence high-water migration.

Repository constructors accept `store.Pool`; multi-step writes use
`store.WithTx` and its dialect-aware `store.Transaction`. Architecture tests
reject concrete driver pools/transactions and `LastInsertId` in production
repositories. Use `store.InsertID` for inserts or explicit `RETURNING` for an
upsert that must return the updated row. The initial PostgreSQL writer policy
is documented in [the migration notes](../internal/store/postgres/README.md).
SQL date expressions distinguish UTC text columns (`store.UTCNowText`) from native
timestamp columns (`store.UTCNowTimestamp`); interval modifiers are bound values.
The parameter boundary handles bare `?` and SQLite's repeated numbered `?N`
markers, including prepared statements; it does not translate SQL dialects.

Without the environment variable the real-server tests explicitly skip; CI supplies
disposable PostgreSQL 16, 17 and 18 services so they must run there. The connector
rejects server versions outside that range, requires a UTF8 database and pins
UTF8 client encoding, UTC, ISO/YMD dates and Read
Committed isolation independently of shared-server defaults or URL options.
Runtime startup now selects an explicit backend, and the backup service/HTTP API
exercise native snapshots, metadata, downloads and offline-only restore
capabilities with PostgreSQL. See [PostgreSQL operations](postgresql.md).
Conversion is still under development; full application parity remains a
completion claim to establish, not something inferred from repository coverage.

Native archive tests additionally require `pg_dump` and `pg_restore` from
PostgreSQL 16–18, no older than the server. Set `ARRMADA_TEST_POSTGRES_BIN` to
their directory if they are not on PATH. An explicitly enabled PostgreSQL test
run fails rather than silently skipping missing native clients. The current
private archive prototype checks restricted-role schema isolation, lookalike
schema names, complete archive decoding, private file permissions, occupied
backup filenames and failure cleanup. Offline recovery tests additionally check
trusted-migration initialization, all-table row-count/fingerprint verification,
relationship checks, numeric precision, source/archive preservation, sequence
high-water marks, restored triggers, and rollback of initialization after data,
compatibility and cancellation failures. Conversion remains unimplemented.
Set `ARRMADA_TEST_POSTGRES_RESTORE_ADMIN_URL` to a second disposable server to run
the successful native-recovery test across server majors instead of within one
server; source and destination both still use restricted application roles.
Locally built clients without TLS/compression support establish
only socket/uncompressed-archive coverage, not TLS or compressed-archive claims.

Private lifecycle tests now cover single-owner admission, exclusion of upgrades
while application connections exist, schema isolation, coordinator termination,
in-flight transaction fencing, rejected replacement-session cleanup and heartbeat
shutdown. Every application connection holds a shared schema lock; recovery needs
exclusive ownership, so safety does not depend on the heartbeat winning a race.
Application startup now uses these primitives. Native backup
currently requires a single-host URL, no libpq service file and no encrypted-key
`sslpassword` option; unsupported cases fail explicitly rather than silently
dumping another target or weakening TLS settings.

### Offline PostgreSQL recovery

The maintenance command below accepts native custom-format COPY archives, not SQL scripts,
SQLite files, directory archives or database-wide dumps. Recovery requires a
build with **exactly matching PostgreSQL migration history**. To roll back, use
the matching old build and its pre-upgrade archive; recover first, then upgrade.

1. Stop Arrmada. Keep the old schema and filesystem data intact.
2. Have the database operator provision a **new, empty, dedicated schema** in the
   existing UTF8 database, owned by the application role. Do not use `public` or
   a `pg_*` schema. No database-create or superuser privilege is needed by Arrmada.
3. Supply the destination connection URL securely through the environment (never
   as a command argument), install supported native clients, and run as the
   filesystem/application user:

   ```sh
   # ARRMADA_POSTGRES_URL is already set securely to the existing database.
   arrmada restore-postgres --schema arrmada_recovered /safe/backup.pgdump
   ```

The source archive is untouched. `pg_restore` only decodes data to a private
temporary file; Arrmada never executes its DDL, functions, session commands or
sequence SQL. Trusted embedded migrations, validated COPY data, deferred foreign
key checks, trigger restoration and sequence state commit in one transaction.
Failures roll back all destination objects; decoded temporary files are removed.
Transferred tables must have the exact expected columns and migration checksums,
and every table is fingerprinted after import. Recovery of an existing/occupied
schema is refused, including an existing Arrmada installation. There is no
`DROP ... CASCADE`, privilege escalation or in-app PostgreSQL restore.

After success, update backend configuration to the recovered schema before
starting Arrmada. A lost connection during COMMIT has the usual remote-transaction
ambiguity: inspect the destination before retrying. An occupied schema must never
be cleared automatically. Database recovery does not restore media, covers,
downloads, models, certificates or other filesystem data under the data directory.

## Screenshots

The pictures in the README and on the website (`docs/images/*.png`) are taken from those same
fixtures — never a real server:

```sh
cd web && npm run build
README_SHOTS=1 E2E_NO_BUILD=1 npx playwright test --project=readme-shots --project=readme-shots-phone
```

`web/e2e/readme-shots.spec.ts` lists the pages; posters are made-up gradient landscapes.

## Testing the installer

`install.sh` and `update.sh` take `ARRMADA_REPO_URL`, `ARRMADA_BRANCH`, `ARRMADA_API_URL` and
`ARRMADA_TARBALL_BASE` so a fork (or a test) can point them at its own copy, and `--image` to
run any image. Test them in a throwaway `docker:dind` container, never on a real server.
