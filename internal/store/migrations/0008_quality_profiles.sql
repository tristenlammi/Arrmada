-- 0008_quality_profiles: user-defined quality profiles + custom formats, per media type.

CREATE TABLE quality_profiles (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    media_type          TEXT NOT NULL DEFAULT 'movie',   -- movie | series | book | music
    name                TEXT NOT NULL,
    base                TEXT NOT NULL DEFAULT '',          -- preset it was cloned from
    allowed_resolutions TEXT NOT NULL DEFAULT '[]',        -- JSON array of resolution strings
    min_source          TEXT NOT NULL DEFAULT '',
    size_cap_gb         REAL NOT NULL DEFAULT 0,
    small_bias          REAL NOT NULL DEFAULT 0,
    min_format_score    INTEGER NOT NULL DEFAULT 0,
    format_scores       TEXT NOT NULL DEFAULT '{}',        -- JSON map name -> score
    custom_formats      TEXT NOT NULL DEFAULT '[]',        -- JSON array of {name, conditions}
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_quality_profiles_media ON quality_profiles(media_type);
