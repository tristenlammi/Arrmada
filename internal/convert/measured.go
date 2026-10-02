package convert

import (
	"context"
	"database/sql"
	"time"
)

// Measured sizes. The Library's "Saves" figure is an estimate from each file's own bitrate,
// cautious by design. Once a file has actually been test-encoded — by the rehearsal before
// a conversion, or by Compare — there is a real number: the clips' size scaled to the
// runtime, at the real settings. From then on that's what the file is judged and shown by.

// measurement is one test encode's projection for a file in one format.
type measurement struct {
	Size       int64 // the file's size when measured; a replaced file is measured afresh
	CRF        int
	VideoBytes int64 // the whole file's video at that setting, projected
	SSIM       float64
	Source     string
}

// measureStore persists measurements (convert_measured).
type measureStore struct{ db *sql.DB }

func measureKey(path, codec string) string { return codec + "|" + path }

// all loads every measurement, keyed by measureKey. The table holds one row per tested
// file and format — small enough to read whole wherever the library is evaluated.
func (m *measureStore) all(ctx context.Context) map[string]measurement {
	out := map[string]measurement{}
	if m == nil || m.db == nil {
		return out
	}
	rows, err := m.db.QueryContext(ctx, `SELECT path, codec, size_bytes, crf, video_bytes, ssim, source FROM convert_measured`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var path, codec string
		var r measurement
		if rows.Scan(&path, &codec, &r.Size, &r.CRF, &r.VideoBytes, &r.SSIM, &r.Source) == nil {
			out[measureKey(path, codec)] = r
		}
	}
	return out
}

func (m *measureStore) put(ctx context.Context, path, codec string, r measurement) {
	if m == nil || m.db == nil || path == "" || r.VideoBytes <= 0 {
		return
	}
	_, _ = m.db.ExecContext(ctx,
		`INSERT INTO convert_measured (path, codec, size_bytes, crf, video_bytes, ssim, source, measured_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(path, codec) DO UPDATE SET size_bytes = excluded.size_bytes, crf = excluded.crf,
		   video_bytes = excluded.video_bytes, ssim = excluded.ssim, source = excluded.source,
		   measured_at = excluded.measured_at`,
		path, codec, r.Size, r.CRF, r.VideoBytes, r.SSIM, r.Source, time.Now().Unix())
}

// forget drops a file's measurements — it has been converted, so they describe a file
// that no longer exists.
func (m *measureStore) forget(ctx context.Context, path string) {
	if m == nil || m.db == nil {
		return
	}
	_, _ = m.db.ExecContext(ctx, `DELETE FROM convert_measured WHERE path = ?`, path)
}

// recordMeasurement stores a projection and refreshes the cached library views.
func (s *Service) recordMeasurement(ctx context.Context, path, codec string, r measurement) {
	s.measured.put(ctx, path, codec, r)
	s.invalidateLibraryCache()
}

// measuredFor returns the measurement that applies to this file under this plan: the same
// file (path and size), the same format, and taken at the plan's quality target or a
// tighter one (a tighter setting only makes the file bigger, so it never over-promises).
// A measurement at a looser setting than today's — the target was raised since — doesn't
// count.
func (p prefs) measuredFor(mi *MediaInfo, path string, plan Plan) (measurement, bool) {
	if plan.VideoCodec == "" || path == "" {
		return measurement{}, false
	}
	m, ok := p.measured[measureKey(path, plan.VideoCodec)]
	if !ok || m.Size != mi.SizeBytes || m.CRF > plan.Quality {
		return measurement{}, false
	}
	return m, true
}

// sizeAfter is the expected size of a file after its plan: measured when a test encode
// has measured it, else the estimate.
func (p prefs) sizeAfter(mi *MediaInfo, path string, plan Plan) (est int64, measured bool) {
	if m, ok := p.measuredFor(mi, path, plan); ok {
		return m.VideoBytes + keptAudioBytes(mi, plan), true
	}
	return estimatePlanSize(mi, plan), false
}
