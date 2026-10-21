# PostgreSQL migrations

This is a separate migration history from `../migrations` (SQLite). Never edit or
remove a migration after it ships. PostgreSQL migrations run in transactions and
must not contain `BEGIN` or `COMMIT`.

`0001_baseline.sql` is generated from a new SQLite installation at
`0347_quality_multi_counts_language`, not by replaying SQLite DDL on PostgreSQL:

```sh
go run ./scripts/postgres-baseline -postgres
```

The generated baseline preserves columns, defaults, primary/unique keys, foreign
keys, check constraints, indexes and seed data. Integers remain signed 64-bit;
floating values remain double precision. Flags and JSON retain their existing
integer/text representations. Foreign keys are initially immediate but deferrable,
so offline conversion can validate a complete dataset with cyclic relationships.
Text uses the `C` collation to retain SQLite's byte ordering and equality. The ASCII
fold helper preserves SQLite's case folding without requiring extensions.

Behavioral triggers are reviewed and maintained separately in `0002_behavior.sql`.
They must preserve book edits, audiobook metadata invalidation and viewing-group
maintenance. Table/column parity alone does not prove behavioral parity.

`0003_writer_lock.sql` retains the existing single-writer contract while repositories
are ported. `WithTx` takes a schema-scoped transaction advisory lock before its first
read; statement triggers take the same lock for autocommit writes. Reads remain
concurrent, unrelated schemas are not locked, and callbacks are never replayed.
Connections explicitly use Read Committed isolation, so a waiting transaction's
first repository read sees the preceding writer's committed state even if the
shared server's default isolation is different.
New application tables must install the same `arrmada_serialize_writes` trigger;
an integration test checks that every application table participates. Increasing
write concurrency later requires auditing the read-then-write invariants, not
simply removing this lock.

`0004_subtitle_job_ids.sql` advances the subtitle job identity sequence for the
queue's explicitly assigned ids and backfills existing rows. This prevents a
restart after pruning from reusing an id, matching SQLite AUTOINCREMENT's safety
property. As with native PostgreSQL identities, a rolled-back insert can leave a
sequence gap; it must never move the high-water mark backwards.

Offline conversion has a separately reviewed pair in `../conversion.go`:
SQLite `0347_quality_multi_counts_language` ↔ PostgreSQL
`0004_subtitle_job_ids`. New migrations must review/update that pair and its
typed round-trip tests explicitly; histories/checksums are never copied between
engines. Unsupported histories fail closed rather than inferring compatibility
from matching column names. Keep `TestPostgresBidirectionalOfflineConversion`,
failure cleanup, crashed-WAL, source-value and real CLI authentication tests in
the PostgreSQL 16–18 matrix when updating this contract.
