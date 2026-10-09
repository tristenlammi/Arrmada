-- 0148_download_client_priority: an order for download clients, and which row is the
-- packaged qBittorrent.
--
-- New downloads always went to the lowest id, so the order couldn't be changed, and the
-- bundled client was recognised only by its URL: startup re-added a row for that URL
-- whenever none had it, so it could neither be removed nor kept off. priority orders the
-- clients (lowest first, like indexers); bundled marks the packaged one, set at startup.
ALTER TABLE download_clients ADD COLUMN priority INTEGER NOT NULL DEFAULT 25;
ALTER TABLE download_clients ADD COLUMN bundled INTEGER NOT NULL DEFAULT 0;
