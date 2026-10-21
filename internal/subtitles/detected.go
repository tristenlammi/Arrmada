package subtitles

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/tristenlammi/arrmada/internal/langs"
	"github.com/tristenlammi/arrmada/internal/store"
)

// detectedQueryBatch is how many paths one IN query names (SQLite's bound-parameter limit
// is far above it).
const detectedQueryBatch = 200

// DetectedAudio is, for each file in paths (path → its size in bytes as the library
// records it, 0 = unknown), the language detection heard on each untagged audio track, by
// the track's 0:a:N index — the same tracks and threshold the AI rung itself trusts
// (withDetected). It reads the probe cache alone and never probes or stats a file: an
// entry whose recorded size isn't the file's is a different file and is skipped. Files
// nothing was heard on are absent.
//
// Convert asks for it (wired in main; convert must not import subtitles) so the language
// badges and the quality profile's language rules read an untagged track as what it is.
func (s *Service) DetectedAudio(ctx context.Context, paths map[string]int64) map[string]map[int]string {
	out := map[string]map[int]string{}
	if s == nil || s.cache == nil || s.cache.db == nil || len(paths) == 0 {
		return out
	}
	read := func(q string, args ...any) {
		rows, err := s.cache.db.QueryContext(ctx, q, args...)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var path, infoJSON string
			var size int64
			if rows.Scan(&path, &size, &infoJSON) != nil {
				continue
			}
			want, ok := paths[path]
			if !ok || (want > 0 && size != want) {
				continue
			}
			var mi mediaInfo
			if json.Unmarshal([]byte(infoJSON), &mi) != nil {
				continue
			}
			if heard := heardOn(&mi); len(heard) > 0 {
				out[path] = heard
			}
		}
	}
	// The paths are looked up by key, a batch at a time, so a whole library's dry run never
	// scans the cache; only rows detection wrote to are worth decoding.
	base := `SELECT path, size_bytes, info_json FROM subtitle_probe_cache WHERE ` + store.LikeASCII(s.cache.db, "info_json", `'%"detected"%'`, false) + ` AND path IN (`
	batch := make([]any, 0, detectedQueryBatch)
	flush := func() {
		if len(batch) > 0 {
			read(base+strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")+`)`, batch...)
			batch = batch[:0]
		}
	}
	for p := range paths {
		if ctx.Err() != nil {
			return out
		}
		batch = append(batch, p)
		if len(batch) == detectedQueryBatch {
			flush()
		}
	}
	flush()
	return out
}

// heardOn is a probe's confident detections on its untagged tracks, as canonical codes.
func heardOn(mi *mediaInfo) map[int]string {
	out := map[int]string{}
	for _, t := range mi.Audio {
		d, ok := mi.Detected[t.Index]
		if !ok || !t.untagged() || d.Prob < detectMinProb {
			continue
		}
		if c := langs.Canonical(d.Lang); c != "" {
			out[t.Index] = c
		}
	}
	return out
}
