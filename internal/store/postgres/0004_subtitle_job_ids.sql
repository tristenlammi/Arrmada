-- The subtitle queue assigns its own numeric ids. Explicit identity inserts do
-- not advance a PostgreSQL sequence, unlike SQLite AUTOINCREMENT. Track that
-- high-water mark so pruning the newest job cannot make a later restart reuse
-- an id that a caller already holds. Sequence advances may survive rollback,
-- as with native PostgreSQL identity inserts; gaps are safe, reuse is not.
CREATE FUNCTION arrmada_track_subtitle_job_id() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    seq regclass;
    previous bigint;
BEGIN
    seq := pg_catalog.pg_get_serial_sequence(
        format('%I.%I', TG_TABLE_SCHEMA, TG_TABLE_NAME), 'id')::regclass;
    previous := pg_catalog.pg_sequence_last_value(seq);
    IF NEW.id >= 1 AND (previous IS NULL OR NEW.id > previous) THEN
        PERFORM pg_catalog.setval(seq, NEW.id, true);
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER arrmada_subtitle_job_id_high_water
AFTER INSERT ON subtitle_jobs FOR EACH ROW
EXECUTE FUNCTION arrmada_track_subtitle_job_id();

-- Also cover an existing schema whose queue predates this migration, without
-- lowering a sequence whose newest rows were already pruned.
DO $$
DECLARE
    seq regclass := pg_catalog.pg_get_serial_sequence('subtitle_jobs', 'id')::regclass;
    high_water bigint;
BEGIN
    SELECT GREATEST(COALESCE(MAX(id), 0), COALESCE(pg_catalog.pg_sequence_last_value(seq), 0))
        INTO high_water FROM subtitle_jobs;
    IF high_water > 0 THEN
        PERFORM pg_catalog.setval(seq, high_water, true);
    END IF;
END
$$;
