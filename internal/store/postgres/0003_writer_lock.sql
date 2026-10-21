-- Keep SQLite's established single-writer transaction semantics while using
-- PostgreSQL's native pooling, reads, constraints and durability. WithTx takes
-- this lock before its first read; statement triggers include autocommit writes.
-- The lock is scoped to this application's schema, not the shared database.
-- Callbacks are never replayed automatically: several also update in-memory
-- results, and retrying them could duplicate those changes.
CREATE FUNCTION arrmada_lock_writes() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'arrmada/write/' || current_database() || '/' || current_schema(), 0));
    RETURN NULL;
END
$$;

DO $$
DECLARE
    target record;
BEGIN
    FOR target IN
        SELECT c.relname, n.nspname FROM pg_catalog.pg_class c
        JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = current_schema() AND c.relkind = 'r'
          AND c.relname != 'schema_migrations'
        ORDER BY c.relname
    LOOP
        EXECUTE format(
            'CREATE TRIGGER arrmada_serialize_writes BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON %I.%I FOR EACH STATEMENT EXECUTE FUNCTION arrmada_lock_writes()',
            target.nspname, target.relname);
    END LOOP;
END
$$;
