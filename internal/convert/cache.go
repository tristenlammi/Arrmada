package convert

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
)

// probeCache persists ffprobe results so the library scan doesn't re-analyze
// every file on each restart. An entry is valid only while the file's size and
// mtime are unchanged; any change (or a missing file) forces a fresh probe.
// Backed by the convert_probe_cache table.
type probeCache struct{ db *sql.DB }

// get returns the cached MediaInfo for path when the stored size + mtime match
// the current file (i.e. the file hasn't changed since it was probed).
func (c *probeCache) get(ctx context.Context, path string, size, mtime int64) (*MediaInfo, bool) {
	var infoJSON string
	err := c.db.QueryRowContext(ctx,
		`SELECT info_json FROM convert_probe_cache WHERE path = ? AND size_bytes = ? AND mtime_unix = ?`,
		path, size, mtime).Scan(&infoJSON)
	if err != nil {
		return nil, false
	}
	var mi MediaInfo
	if json.Unmarshal([]byte(infoJSON), &mi) != nil {
		return nil, false
	}
	return &mi, true
}

// put upserts a probe result for path, stamped with the file's size + mtime.
func (c *probeCache) put(ctx context.Context, path string, size, mtime int64, mi *MediaInfo) {
	b, err := json.Marshal(mi)
	if err != nil {
		return
	}
	_, _ = c.db.ExecContext(ctx,
		`INSERT INTO convert_probe_cache (path, size_bytes, mtime_unix, info_json, probed_at)
		 VALUES (?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(path) DO UPDATE SET
		   size_bytes = excluded.size_bytes,
		   mtime_unix = excluded.mtime_unix,
		   info_json  = excluded.info_json,
		   probed_at  = excluded.probed_at`,
		path, size, mtime, string(b))
}

// probeCached returns a file's MediaInfo from the cache when the file is
// unchanged, otherwise runs ffprobe once and stores the result. This is what the
// read-only library scan uses so it only ever probes new or changed files.
func (s *Service) probeCached(ctx context.Context, path string) (*MediaInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	size, mtime := fi.Size(), fi.ModTime().Unix()
	if s.cache != nil {
		if mi, ok := s.cache.get(ctx, path, size, mtime); ok {
			return mi, nil
		}
	}
	mi, err := probe(ctx, s.ffprobe, path)
	if err != nil {
		return nil, err
	}
	if s.cache != nil {
		s.cache.put(ctx, path, size, mtime, mi)
	}
	return mi, nil
}

// WarmCache probes any library file not already cached, off the request path, so
// the first Convert page load after a restart is instant instead of triggering a
// full ffprobe sweep. Safe to run in a background goroutine; respects ctx.
func (s *Service) WarmCache(ctx context.Context) {
	if s.cache == nil {
		return
	}
	list, err := s.movies.List(ctx)
	if err != nil {
		return
	}
	warmed := 0
	for _, m := range list {
		if ctx.Err() != nil {
			return
		}
		if !m.HasFile || m.MovieFilePath == "" {
			continue
		}
		fi, err := os.Stat(m.MovieFilePath)
		if err != nil {
			continue
		}
		if _, ok := s.cache.get(ctx, m.MovieFilePath, fi.Size(), fi.ModTime().Unix()); ok {
			continue // already cached and unchanged
		}
		if mi, err := probe(ctx, s.ffprobe, m.MovieFilePath); err == nil {
			s.cache.put(ctx, m.MovieFilePath, fi.Size(), fi.ModTime().Unix(), mi)
			warmed++
		}
	}
	if warmed > 0 {
		s.log.Info("convert: probe cache warmed", "files", warmed)
	}
}
