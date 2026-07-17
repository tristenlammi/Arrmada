-- 0052_buffer_cause: record the diagnosed likely cause of each buffer spell, so the
-- Reliability view can explain WHY a stream stalled (transcode overload vs bandwidth).

ALTER TABLE buffer_events ADD COLUMN cause TEXT NOT NULL DEFAULT '';
ALTER TABLE buffer_events ADD COLUMN detail TEXT NOT NULL DEFAULT '';
