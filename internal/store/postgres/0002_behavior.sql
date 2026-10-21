-- Preserve the behavioral guarantees of SQLite's book and viewing triggers.
-- All functions are invoker-security and confined to the connection's dedicated
-- application schema. No extensions, superuser access or trigger disabling.

CREATE FUNCTION books_preserve_edits_and_metadata() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    fields text[][] := ARRAY[
        ['title','title'], ['subtitle','subtitle'], ['author','author'],
        ['series_name','series_name'], ['series_position','series_position'],
        ['description','description'], ['year','year'], ['language','language'],
        ['isbn','isbn'], ['narrator','audiobook_narrator'],
        ['publisher','publisher'], ['subjects','subjects_json']
    ];
    field text[];
    previous jsonb := to_jsonb(OLD);
    incoming jsonb := to_jsonb(NEW);
    edits jsonb;
    catalogue jsonb;
    preserved boolean := false;
BEGIN
    IF NEW.edited_at IS NOT DISTINCT FROM OLD.edited_at THEN
        FOREACH field SLICE 1 IN ARRAY fields LOOP
            IF incoming -> field[2] IS DISTINCT FROM previous -> field[2]
               AND OLD.edits_json::jsonb ? field[1] THEN
                IF NOT preserved THEN
                    edits := NEW.edits_json::jsonb;
                    preserved := true;
                END IF;
                catalogue := incoming -> field[2];
                IF field[1] = 'subjects' THEN
                    catalogue := CASE WHEN NEW.subjects_json = '' THEN '[]'::jsonb
                                      ELSE NEW.subjects_json::jsonb END;
                END IF;
                edits := jsonb_set(edits, ARRAY[field[1]], catalogue);
                incoming := jsonb_set(incoming, ARRAY[field[2]], previous -> field[2]);
            END IF;
        END LOOP;
        IF preserved THEN
            NEW := jsonb_populate_record(NEW, incoming);
            NEW.edits_json := edits::text;
        END IF;
    END IF;

    -- Compare displayed values after preserving edits. A catalogue refresh
    -- underneath an edit must not invalidate the audiobook catalogue.
    IF ROW(NEW.title, NEW.author, NEW.year, NEW.cover_url, NEW.description,
           NEW.subjects_json, NEW.series_name, NEW.series_position,
           NEW.audiobook_path, NEW.ebook_path, NEW.audiobook_narrator,
           NEW.subtitle, NEW.language, NEW.isbn, NEW.publisher)
       IS DISTINCT FROM
       ROW(OLD.title, OLD.author, OLD.year, OLD.cover_url, OLD.description,
           OLD.subjects_json, OLD.series_name, OLD.series_position,
           OLD.audiobook_path, OLD.ebook_path, OLD.audiobook_narrator,
           OLD.subtitle, OLD.language, OLD.isbn, OLD.publisher) THEN
        NEW.meta_updated_at := GREATEST(
            floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint,
            NEW.meta_updated_at + 1);
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER books_meta_updated BEFORE UPDATE ON books
FOR EACH ROW EXECUTE FUNCTION books_preserve_edits_and_metadata();

CREATE FUNCTION book_audio_versions_touch_metadata() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE books SET meta_updated_at = GREATEST(
        floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint,
        meta_updated_at + 1)
    WHERE id = NEW.book_id;
    RETURN NEW;
END
$$;

CREATE TRIGGER book_audio_versions_meta_updated
AFTER UPDATE OF label, path, narrator ON book_audio_versions
FOR EACH ROW
WHEN (ROW(NEW.label, NEW.path, NEW.narrator) IS DISTINCT FROM ROW(OLD.label, OLD.path, OLD.narrator))
EXECUTE FUNCTION book_audio_versions_touch_metadata();

-- SQLite serializes writers. PostgreSQL must serialize viewing maintenance
-- before row locks are acquired, not just lock the row being marked afterwards.
-- A statement trigger takes a schema-scoped transaction advisory lock. This
-- protects predecessor/successor calculations even for concurrent inserts or
-- out-of-order imports. It does not lock another application's schema.
CREATE FUNCTION stream_sessions_lock_viewing_writes() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        'arrmada/viewing/' || current_database() || '/' || current_schema(), 0));
    RETURN NULL;
END
$$;

CREATE TRIGGER stream_sessions_serialize_writes
BEFORE INSERT OR UPDATE OR DELETE ON stream_sessions
FOR EACH STATEMENT EXECUTE FUNCTION stream_sessions_lock_viewing_writes();

CREATE FUNCTION stream_sessions_maintain_viewings() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    changed_id bigint;
    next_new bigint;
    next_old bigint;
    new_key text;
    old_key text;
BEGIN
    IF TG_OP != 'DELETE' THEN
        changed_id := NEW.id;
        new_key := CASE WHEN NEW.rating_key <> '' THEN NEW.rating_key
            ELSE NEW.media_type || '|' || NEW.title || '|' || NEW.grandparent_title || '|' || NEW.parent_index || '|' || NEW.media_index END;
        SELECT n.id INTO next_new FROM stream_sessions n
        WHERE n.user_id = NEW.user_id
          AND (CASE WHEN n.rating_key <> '' THEN n.rating_key
               ELSE n.media_type || '|' || n.title || '|' || n.grandparent_title || '|' || n.parent_index || '|' || n.media_index END) = new_key
          AND (n.started_at, n.id) > (NEW.started_at, NEW.id)
        ORDER BY n.started_at, n.id LIMIT 1;
    END IF;
    IF TG_OP != 'INSERT' THEN
        old_key := CASE WHEN OLD.rating_key <> '' THEN OLD.rating_key
            ELSE OLD.media_type || '|' || OLD.title || '|' || OLD.grandparent_title || '|' || OLD.parent_index || '|' || OLD.media_index END;
        SELECT n.id INTO next_old FROM stream_sessions n
        WHERE n.user_id = OLD.user_id
          AND (CASE WHEN n.rating_key <> '' THEN n.rating_key
               ELSE n.media_type || '|' || n.title || '|' || n.grandparent_title || '|' || n.parent_index || '|' || n.media_index END) = old_key
          AND (n.started_at, n.id) > (OLD.started_at, OLD.id)
        ORDER BY n.started_at, n.id LIMIT 1;
    END IF;
    UPDATE stream_sessions AS s SET viewing_start = COALESCE((
        SELECT CASE WHEN s.started_at - p.stopped_at > 10800 THEN 1 ELSE 0 END
        FROM stream_sessions p
        WHERE p.user_id = s.user_id
          AND (CASE WHEN p.rating_key <> '' THEN p.rating_key
               ELSE p.media_type || '|' || p.title || '|' || p.grandparent_title || '|' || p.parent_index || '|' || p.media_index END)
            = (CASE WHEN s.rating_key <> '' THEN s.rating_key
               ELSE s.media_type || '|' || s.title || '|' || s.grandparent_title || '|' || s.parent_index || '|' || s.media_index END)
          AND (p.started_at, p.id) < (s.started_at, s.id)
        ORDER BY p.started_at DESC, p.id DESC LIMIT 1), 1)
    WHERE s.id = ANY(ARRAY[changed_id, next_new, next_old]);
    RETURN NULL;
END
$$;

CREATE TRIGGER stream_sessions_viewing_insert AFTER INSERT ON stream_sessions
FOR EACH ROW EXECUTE FUNCTION stream_sessions_maintain_viewings();

CREATE TRIGGER stream_sessions_viewing_delete AFTER DELETE ON stream_sessions
FOR EACH ROW EXECUTE FUNCTION stream_sessions_maintain_viewings();

CREATE TRIGGER stream_sessions_viewing_update
AFTER UPDATE OF user_id, rating_key, media_type, title, grandparent_title, parent_index, media_index, started_at, stopped_at ON stream_sessions
FOR EACH ROW
WHEN (ROW(NEW.user_id, NEW.rating_key, NEW.media_type, NEW.title, NEW.grandparent_title, NEW.parent_index, NEW.media_index, NEW.started_at, NEW.stopped_at)
      IS DISTINCT FROM ROW(OLD.user_id, OLD.rating_key, OLD.media_type, OLD.title, OLD.grandparent_title, OLD.parent_index, OLD.media_index, OLD.started_at, OLD.stopped_at))
EXECUTE FUNCTION stream_sessions_maintain_viewings();
