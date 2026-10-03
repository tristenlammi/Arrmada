-- 0088_quality_ideal_file: what a profile's ideal FILE looks like — codec, HDR, audio and a
-- bitrate window per resolution — so the library can show which files don't fit. Report
-- only: nothing here changes what is downloaded. One JSON document ('' = not set up).
ALTER TABLE quality_profiles ADD COLUMN ideal TEXT NOT NULL DEFAULT '';

-- Required formats ("must have Atmos") were edited in the builder and used when choosing
-- releases, but there was no column for them: every save silently dropped them.
ALTER TABLE quality_profiles ADD COLUMN required_formats TEXT NOT NULL DEFAULT '[]';
