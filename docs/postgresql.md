# PostgreSQL (opt-in)

SQLite remains the default. PostgreSQL is an alternative for **one Arrmada
instance**, not a multi-replica mode. Supported servers are PostgreSQL **16, 17
and 18**, in an existing **UTF8** database. Arrmada uses a dedicated schema; it
never creates a database, installs extensions, or administers unrelated schemas.

Backend selection never copies data automatically. Use the explicit offline
conversion below to move an existing installation; keep the original database
and filesystem data intact until the converted installation has been checked.

## Provisioning and privileges

Ask the database operator to provision a login and access to the existing
database. **Normal startup creates the configured schema if it is missing**,
provided the role owns the database or has `CREATE` on that database. It never
creates a database or changes another schema's ownership. For example, run by
the operator (not Arrmada):

```sql
CREATE ROLE arrmada_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT
  PASSWORD 'a-secret-set-by-the-operator';
GRANT CONNECT, CREATE ON DATABASE existing_db TO arrmada_app;
```

`CREATE ON DATABASE` permits creating schemas inside that database; it is not
the role-level `CREATEDB` privilege. A dedicated Arrmada database owned by the
application role already grants the required authority. On startup Arrmada takes
its configured schema's lifecycle locks, verifies native backup clients and local
backup publication, then creates and migrates the schema. Concurrent startups
cannot initialize it twice. A missing schema with insufficient privileges fails
with instructions to grant database `CREATE` or pre-provision the schema; there
is no fallback to another schema, `public`, or SQLite.

For **restricted/shared-database deployments**, database `CREATE` is unnecessary:
grant `CONNECT` only and have the operator pre-provision the schema instead:

```sql
-- Connected to existing_db as its operator:
CREATE SCHEMA arrmada AUTHORIZATION arrmada_app;
```

An explicit `CREATE` grant can also be revoked after the initial creation;
subsequent startups only need schema ownership. An application that owns the
database inherently retains database-owner privileges. Keep the configured
schema name stable: changing it to a new name can create a new empty installation,
not move the old installation's data.

Use a lowercase schema identifier, up to 63 characters. `public`,
`information_schema`, and `pg_*` are refused. The application role must own the
schema (and objects Arrmada creates within it), with USAGE and CREATE. Ownership
also permits its trusted migrations and recovery to alter its own tables,
sequences, functions and user triggers. No superuser, database-creation,
replication, extension, or other application's object privileges are needed.
The operator remains responsible for the shared database's role/public-schema
policies; Arrmada does not change those policies.

An existing schema must be owned by the application role and either empty or an
initialized compatible Arrmada installation. Arbitrary tables/functions are not
adopted. A second Arrmada instance targeting the same database/schema is refused.
Different designated schemas are independent. Runtime connection sessions hold
schema-scoped locks so recovery/upgrades cannot overlap surviving old sessions
after a coordinator connection is lost. Arrmada shuts down rather than silently
rejoining as another instance.
After a network partition, server-side sessions can retain locks until PostgreSQL
detects their disconnection. Startup/recovery refuses to overlap them. Configure
appropriate connection/TCP timeouts with the database operator; if manual session
cleanup is necessary, first establish that the old process cannot resume. Arrmada
never terminates another session automatically to force ownership.

## Configuration

Set these in the app's private environment or Compose `.env` (mode `0600`):

```dotenv
ARRMADA_DATABASE_BACKEND=postgres
ARRMADA_POSTGRES_URL=postgres://arrmada_app:URL_ENCODED_PASSWORD@db.example/existing_db?sslmode=verify-full&sslrootcert=/certs/ca.pem
ARRMADA_POSTGRES_SCHEMA=arrmada
ARRMADA_POSTGRES_MAX_CONNECTIONS=4
```

URL-encode credentials. Never pass the URL as a command-line argument, publish
`docker compose config` output containing it, or put it in logs. For remote
connections use certificate-verified TLS (`sslmode=verify-full`), with the CA
and any client certificate/key files readable by the application user and mounted
at the same paths for Go and native clients. libpq's `prefer` default can fall
back to plaintext; do not rely on it for an untrusted network.

The backend defaults to `sqlite`; a URL alone never changes it. Invalid backend
names, connection/ownership failures and failed migrations stop startup, never
fall back to SQLite. `arrmada.db` is neither opened nor automatically converted
when PostgreSQL is selected. Resolve any previously staged SQLite restore
explicitly before switching backends. Revert the selector to `sqlite` to use the
old file again, understanding that changes made in PostgreSQL are not in it.

The pool limit is 1–100 (default 4). Allow additional connections for ownership,
pre-upgrade inspection, native dumping and maintenance commands; typically the
configured application pool plus three, with additional headroom for CLI work.
The session uses Read Committed, UTC, UTF8 and ISO/YMD dates independently of
shared-server defaults. Application-local graph timezones and schedules still
use Arrmada's configured `TZ`.

The image bundles PostgreSQL 18 native clients on both architectures, including
the all-in-one variant. For a bare Go binary, install `pg_dump` and `pg_restore`
from versions 16–18, **no older than the server**, on PATH. Optionally set
`ARRMADA_POSTGRES_BIN` to their directory. Startup checks the clients before a
new installation can accumulate data. Current native backup support requires a
single-host URL, no libpq service file, and no encrypted-key `sslpassword`
option. Unsupported settings fail explicitly. The server and schema are not
added to Compose; provision and back them up according to your database policy.

## Backups and upgrades

The same Backups card, nightly schedule, manual backup, download, safety copies
and per-kind retention apply. PostgreSQL copies are **native custom archives**
named `arrmada-<kind>-<timestamp>.pgdump`, kept under `<data>/backups` on the
Arrmada server, not necessarily the database server's disk. Downloads are raw
archives, not `.db.gz`. They contain credentials and all user state: protect
them, download an off-server copy, and test recovery. Database-server backups
and point-in-time recovery remain the operator's responsibility.

Native dumps are schema-scoped, consistent, decoded completely before publication,
private on disk, and never overwrite an existing backup. If schema protection
is lost during dumping, no finished backup is published. Retention never prunes
the other backend's copies after a switch. Native archive metadata is decoded
without connecting to a database or executing archive SQL.
The backup directory must support hard links for atomic non-overwrite publication
(checked before startup). Allow room for the uncompressed archive **plus at least
17 MiB** of application headroom. COPY output can greatly exceed PostgreSQL's
compressed relation size; streaming free-space checks stop a growing dump rather
than allowing that estimate to exhaust the application's disk.

`arrmada backup`, `schema`, `reset-password` and `reset-2fa` use the selected
backend without creating/migrating it. Native backups require an initialized
Arrmada schema. Existing PostgreSQL schemas are upgraded only after a successful
native pre-migration backup. Checksums and unknown/newer migrations are checked.
The SQLite escape hatches `ARRMADA_SKIP_MIGRATION_SNAPSHOT` and
`ARRMADA_ALLOW_NEWER_SCHEMA` are deliberately **not supported** for PostgreSQL.

`update.sh` takes its normal native pre-update backup. A code-only rollback is
permitted only when the old build supports PostgreSQL and accepts the current
schema. An older SQLite-only image is refused before it can open `arrmada.db`.
`--rollback --with-db` is refused for PostgreSQL **before** containers/images or
schemas change: database recovery is an explicit offline operation below.
Switching images does not undo migrations. Keep the matching old image/binary
and archive for rollback.

## Offline restore / database rollback

There is **no in-app PostgreSQL restore or upload/staged replacement**. Use the
offline command with a build whose PostgreSQL migration history exactly matches
the archive. Recover an old backup with that matching old build first; upgrade
after recovery, not while interpreting a different schema version.
Unlike runtime startup, maintenance commands, offline restore and conversion
**never create a missing schema**. Recovery destinations must still be explicitly
pre-provisioned and empty; they are not implicitly initialized by a URL typo.

1. Stop Arrmada and keep the old database schema and filesystem data intact.
2. Have the operator provision a **new empty schema**, owned by the same
   application role. Existing/occupied destinations, even another Arrmada
   installation, are refused; they are never dropped or truncated.
3. Supply `ARRMADA_POSTGRES_URL` securely through the environment and run as the
   filesystem/application user (use the matching image for a container install):

   ```sh
   arrmada restore-postgres --schema arrmada_recovered /safe/backup.pgdump
   ```

4. After success, set `ARRMADA_POSTGRES_SCHEMA=arrmada_recovered` and start the
   matching build. For a code rollback, `update.sh --rollback` without `--with-db`
   can then select the matching PostgreSQL-capable previous image.

Arrmada preserves the source archive. `pg_restore` only decodes it into a private
temporary file: no archive DDL, functions, role/session commands or sequence SQL
are executed. Trusted embedded migrations, validated COPY data, all-table row
counts/fingerprints, deferred relationship checks, user-trigger restoration and
identity sequence state commit together. Failed transfers roll back destination
initialization as well as data; a failed destination does not have usable
migration metadata. Temporary decoded data is removed (a hard process/machine
crash can leave a private hidden temporary file for manual cleanup).
Allow room in the data directory for decoded COPY data as well as any local
source archive; streaming free-space guards retain the same application reserve
used for backups, including when a compressed native archive expands greatly.

Only Arrmada native COPY custom archives are accepted, not SQL scripts, directory
archives, SQLite files or database-wide dumps containing other schemas. Source
and destination schema names can differ. Connections lost during COMMIT have
the normal remote-transaction ambiguity: inspect the destination before retrying.
An occupied destination must never be cleared automatically. No recovery uses
`DROP ... CASCADE`, trigger-constraint bypass, or superuser privileges.

## Offline SQLite ↔ PostgreSQL conversion

Use the same build for both engines. This build's reviewed compatibility pair is
SQLite `0347_quality_multi_counts_language` and PostgreSQL
`0004_subtitle_job_ids`. Older/newer histories, changed SQLite schema objects,
unexpected tables/columns/types/identities, and incompatible values are refused;
conversion does not upgrade the source. Upgrade with its existing backend first.

1. Stop Arrmada and all maintenance commands and external database writers. Take
   an independent backup. Keep the source and filesystem paths intact.
2. Set `ARRMADA_POSTGRES_URL` and `ARRMADA_POSTGRES_SCHEMA` in the private
   environment. For SQLite → PostgreSQL, the operator must provision a **new
   empty owned schema**. For PostgreSQL → SQLite, choose a **new nonexistent
   file**, with no journal/WAL/SHM or staged restore at that destination.
3. Run exactly one direction as the application/filesystem user:

   ```sh
   arrmada convert-database --from sqlite --to postgres --sqlite-file /data/arrmada.db
   # Or, from the configured PostgreSQL schema into a new file:
   arrmada convert-database --from postgres --to sqlite --sqlite-file /new-data/arrmada.db
   ```

4. Only after success, select the destination backend/schema/data directory.
   Preserve media/download/cover paths and other persistent filesystem data.
   Start Arrmada and verify login and library state before retiring the source.
   Simply selecting the old backend again does **not** replay later changes.

The command does not change runtime configuration. SQLite conversion requires
Linux/macOS/BSD file locking; its stable `.arrmada-sqlite-use.lock` sidecar must
not be deleted or replaced. Shared runtime/maintenance leases exclude conversion;
PostgreSQL uses the same exclusive lifecycle fencing as offline restore. These
locks coordinate Arrmada, not arbitrary external SQL clients or older SQLite
builds: stopping **all** writers remains mandatory. Source symlinks resolve to
their real path; hard-linked SQLite source aliases are refused.

SQLite source database/WAL are read-only, without upgrading or checkpointing;
committed crashed-WAL changes are included in the snapshot. PostgreSQL sources
use a read-only repeatable-read transaction. Destinations use trusted embedded
migrations with independently generated migration metadata. Transfer verifies
typed all-table multiset fingerprints, relationships and identity high-water
marks (including deleted/rolled-back IDs). Normal SQLite ID allocation stays
unchanged; extra `sqlite_sequence` bridge entries retain PostgreSQL sequence gaps
for a future conversion back. Unknown JSON fields and large integers are copied
as text, not parsed through floating point.

Conversion refuses NUL/invalid-UTF8 text, non-integer INTEGER values, non-finite
floats, lossy numeric coercions, negative-zero floats when targeting SQLite, NULL
primary keys, timestamps outside years 1–9999 or with sub-microsecond precision.
Timestamps are normalized to UTC. Repair unsupported values deliberately in the
source using a backed-up installation; the converter never silently repairs them.

PostgreSQL initialization/data/verification commit together. SQLite is built in a
private staging directory, checked, closed and synced, then published with an
exclusive hard link; the destination filesystem must support hard links. Reserve
space for a fresh reference schema plus the destination and at least 17 MiB of
headroom. SQLite → PostgreSQL uses `<data>/conversion-work` for the reference;
PostgreSQL → SQLite stages beside the new file. Ordinary failures remove private
staging files and leave the destination empty/unpublished, permitting retry.
Never overwrite an occupied destination, including one created concurrently.
A process/machine crash can leave private `.arrmada-conversion-*` staging files;
inspect them offline before manual cleanup. A connection loss during PostgreSQL
COMMIT has an uncertain outcome: inspect the destination before retrying, never
clear it automatically. The preserved source is not a live failover replica.

## What still lives on disk

Keep the persistent data directory and existing media/download mounts. PostgreSQL
does not move covers, media, logs, local model files, certificates, conversion
scratch/holds, bundled companion configuration or native backup archives into
the database. Back up those files separately and preserve paths/permissions.
Database recovery alone cannot recreate or undo media filesystem operations.
