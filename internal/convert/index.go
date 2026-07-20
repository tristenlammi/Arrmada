package convert

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// libraryIndex is the persisted answer to "what's in the library and what codec is it".
// Listing reads this table instead of walking the library and probing per request — see
// migration 0058 for why.
type libraryIndex struct{ db *sql.DB }

// indexRow is one indexed file.
type indexRow struct {
	Path      string
	MediaType string // "movie" | "episode"
	MovieID   int64
	SeriesID  int64
	Season    int
	Episode   int
	Title     string
	Year      int
	PosterURL string
	SizeBytes int64
	Codec     string
	Info      *MediaInfo
}

// sizesFor returns path → indexed size for a scope, so the indexer can spot unchanged
// files without touching the filesystem. seriesID 0 with movies=false means everything.
func (ix *libraryIndex) sizesFor(ctx context.Context, mediaType string, seriesID int64) map[string]int64 {
	q := `SELECT path, size_bytes FROM convert_library WHERE media_type = ?`
	args := []any{mediaType}
	if seriesID > 0 {
		q += ` AND series_id = ?`
		args = append(args, seriesID)
	}
	rows, err := ix.db.QueryContext(ctx, q, args...)
	if err != nil {
		return map[string]int64{}
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var p string
		var n int64
		if rows.Scan(&p, &n) == nil {
			out[p] = n
		}
	}
	return out
}

// codecsFor returns path → recorded codec for a scope. An empty codec marks a row whose
// probe failed, which must be retried rather than treated as up to date.
func (ix *libraryIndex) codecsFor(ctx context.Context, mediaType string, seriesID int64) map[string]string {
	q := `SELECT path, video_codec FROM convert_library WHERE media_type = ?`
	args := []any{mediaType}
	if seriesID > 0 {
		q += ` AND series_id = ?`
		args = append(args, seriesID)
	}
	rows, err := ix.db.QueryContext(ctx, q, args...)
	if err != nil {
		return map[string]string{}
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, c string
		if rows.Scan(&p, &c) == nil {
			out[p] = c
		}
	}
	return out
}

func (ix *libraryIndex) upsert(ctx context.Context, r indexRow) error {
	var infoJSON string
	if r.Info != nil {
		if b, err := json.Marshal(r.Info); err == nil {
			infoJSON = string(b)
		}
	}
	_, err := ix.db.ExecContext(ctx,
		`INSERT INTO convert_library
		   (path, media_type, movie_id, series_id, season, episode, title, year, poster_url,
		    size_bytes, video_codec, info_json, indexed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(path) DO UPDATE SET
		   media_type = excluded.media_type, movie_id = excluded.movie_id,
		   series_id = excluded.series_id, season = excluded.season, episode = excluded.episode,
		   title = excluded.title, year = excluded.year, poster_url = excluded.poster_url,
		   size_bytes = excluded.size_bytes, video_codec = excluded.video_codec,
		   info_json = excluded.info_json, indexed_at = datetime('now')`,
		r.Path, r.MediaType, r.MovieID, r.SeriesID, r.Season, r.Episode, r.Title, r.Year,
		r.PosterURL, r.SizeBytes, r.Codec, infoJSON)
	return err
}

// prune drops indexed rows for files no longer in the library (deleted, or replaced by a
// different path), scoped the same way as sizesFor.
func (ix *libraryIndex) prune(ctx context.Context, mediaType string, seriesID int64, keep map[string]bool) {
	for path := range ix.sizesFor(ctx, mediaType, seriesID) {
		if keep[path] {
			continue
		}
		_, _ = ix.db.ExecContext(ctx, `DELETE FROM convert_library WHERE path = ?`, path)
	}
}

// IndexSeries reindexes one series' episodes. Called after an import so the Convert
// library reflects new files immediately, without re-walking the whole library.
func (s *Service) IndexSeries(ctx context.Context, seriesID int64) error {
	if s.series == nil || s.index == nil {
		return nil
	}
	full, err := s.series.Get(ctx, seriesID)
	if err != nil {
		return err
	}
	known := s.index.sizesFor(ctx, "episode", seriesID)
	codecs := s.index.codecsFor(ctx, "episode", seriesID)
	keep := map[string]bool{}
	for _, sn := range full.Seasons {
		for _, e := range sn.Episodes {
			if !e.HasFile || e.FilePath == "" {
				continue
			}
			keep[e.FilePath] = true
			// The episode row already carries the file size, so an unchanged file needs
			// no stat and no probe — the whole point of the index on a spun-down array.
			// An empty recorded codec means a PREVIOUS probe failed (spun-down array,
			// transient I/O); skipping on size alone latched that forever, so the file
			// never became a candidate, never appeared in any list, and was never retried.
			if size, ok := known[e.FilePath]; ok && size == e.SizeBytes && e.SizeBytes > 0 && codecs[e.FilePath] != "" {
				continue
			}
			row := indexRow{
				Path: e.FilePath, MediaType: "episode", SeriesID: full.ID,
				Season: e.SeasonNumber, Episode: e.EpisodeNumber,
				Title:     fmt.Sprintf("%s - S%02dE%02d", full.Title, e.SeasonNumber, e.EpisodeNumber),
				Year:      full.Year,
				PosterURL: full.PosterURL,
				SizeBytes: e.SizeBytes,
			}
			if mi, err := s.probeCached(ctx, e.FilePath); err == nil {
				row.Info, row.Codec = mi, mi.VideoCodec
				if row.SizeBytes == 0 {
					row.SizeBytes = mi.SizeBytes
				}
			}
			if err := s.index.upsert(ctx, row); err != nil {
				s.log.Warn("convert: index episode failed", "path", e.FilePath, "err", err)
			}
		}
	}
	s.index.prune(ctx, "episode", seriesID, keep)
	return nil
}

// IndexMovie reindexes one movie.
func (s *Service) IndexMovie(ctx context.Context, movieID int64) error {
	if s.movies == nil || s.index == nil {
		return nil
	}
	m, err := s.movies.Get(ctx, movieID)
	if err != nil {
		return err
	}
	if !m.HasFile || m.MovieFilePath == "" {
		return nil
	}
	row := indexRow{
		Path: m.MovieFilePath, MediaType: "movie", MovieID: m.ID,
		Title: m.Title, Year: m.Year, PosterURL: m.PosterURL,
	}
	if mi, err := s.probeCached(ctx, m.MovieFilePath); err == nil {
		row.Info, row.Codec, row.SizeBytes = mi, mi.VideoCodec, mi.SizeBytes
	}
	if err := s.index.upsert(ctx, row); err != nil {
		return err
	}
	// A convert can change the container (MKV → MP4), so drop any row still pointing at this
	// movie's previous path — otherwise the old codec lingers and it stays "convertible".
	_, _ = s.index.db.ExecContext(ctx,
		`DELETE FROM convert_library WHERE media_type = 'movie' AND movie_id = ? AND path <> ?`,
		m.ID, m.MovieFilePath)
	return nil
}

// IndexAll refreshes the whole index — the daily sweep. Incremental: files whose
// recorded size still matches the index are skipped without a stat or a probe, so a
// steady library costs almost nothing and the array stays asleep.
func (s *Service) IndexAll(ctx context.Context) {
	if s.index == nil {
		return
	}
	started := time.Now()

	if s.movies != nil {
		if list, err := s.movies.List(ctx); err == nil {
			known := s.index.sizesFor(ctx, "movie", 0)
			codecs := s.index.codecsFor(ctx, "movie", 0)
			keep := map[string]bool{}
			for _, m := range list {
				if ctx.Err() != nil {
					return
				}
				if !m.HasFile || m.MovieFilePath == "" {
					continue
				}
				keep[m.MovieFilePath] = true
				// Re-probe when the recorded codec is empty (a previous probe failed), so a
				// transient error doesn't hide the file from Convert permanently.
				if _, ok := known[m.MovieFilePath]; ok && codecs[m.MovieFilePath] != "" {
					continue
				}
				if err := s.IndexMovie(ctx, m.ID); err != nil {
					s.log.Warn("convert: index movie failed", "movie", m.Title, "err", err)
				}
			}
			s.index.prune(ctx, "movie", 0, keep)
		}
	}

	if s.series != nil {
		if list, err := s.series.List(ctx); err == nil {
			for _, sm := range list {
				if ctx.Err() != nil {
					return
				}
				if err := s.IndexSeries(ctx, sm.ID); err != nil {
					s.log.Warn("convert: index series failed", "series", sm.Title, "err", err)
				}
			}
		}
	}
	s.log.Info("convert: library index refreshed", "took", time.Since(started).Round(time.Second).String())
}

// MaybeIndexSweep is the scheduler entry point. It ticks often but only sweeps once a day,
// at the admin-configured time (Settings → Convert, default 03:00), so the array isn't
// woken at an arbitrary hour. Imports keep the index fresh in between — this only catches
// changes made outside Arrmada.
func (s *Service) MaybeIndexSweep(ctx context.Context) {
	if s.index == nil {
		return
	}
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	now := time.Now()
	if !s.lastSweep.IsZero() && now.Sub(s.lastSweep) < 20*time.Hour {
		return
	}
	at := strings.TrimSpace(s.settings.Get(ctx, keyScanAt, defaultScanAt))
	hh, mm, ok := parseHHMM(at)
	if !ok {
		hh, mm, _ = parseHHMM(defaultScanAt)
	}
	// Run once we're inside the configured hour. Startup does its own IndexAll, so this
	// is purely the recurring daily pass — there's no need to catch up on boot.
	if now.Hour() != hh || now.Minute() < mm {
		return
	}
	s.lastSweep = now
	s.IndexAll(ctx)
}

// parseHHMM parses "HH:MM".
func parseHHMM(v string) (hour, minute int, ok bool) {
	parts := strings.SplitN(v, ":", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// SeriesRollup is one row of the Convert → TV Shows list: a whole show summarized, so the
// tab renders a few dozen rows instead of thousands of episodes.
type SeriesRollup struct {
	SeriesID    int64  `json:"series_id"`
	Title       string `json:"title"`
	Year        int    `json:"year,omitempty"`
	PosterURL   string `json:"poster_url,omitempty"`
	Files       int    `json:"files"`
	Convertible int    `json:"convertible"`
	TotalBytes  int64  `json:"total_bytes"`
	EstBytes    int64  `json:"est_bytes"` // estimated size of the convertible files after conversion
}

// LibraryTVSeries returns the per-series roll-up for the TV tab — one grouped query over
// the index, no per-episode work.
func (s *Service) LibraryTVSeries(ctx context.Context) ([]SeriesRollup, error) {
	if s.index == nil {
		return nil, nil
	}
	dp := s.defaultPlan(ctx)
	target := s.targetCodec(ctx)
	recode := s.av1RecodesHEVC(ctx)

	// One sequential scan of the index, aggregated in Go. Aggregating here rather than
	// in SQL is what lets the estimated saving use the same estimatePlanSize the detail
	// view does — and it's still one query with no filesystem access, which was the
	// actual cost. Only the aggregate crosses the wire.
	rows, err := s.index.db.QueryContext(ctx,
		`SELECT series_id, size_bytes, video_codec, info_json
		   FROM convert_library WHERE media_type = 'episode'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	agg := map[int64]*SeriesRollup{}
	for rows.Next() {
		var id, size int64
		var codec, infoJSON string
		if err := rows.Scan(&id, &size, &codec, &infoJSON); err != nil {
			return nil, err
		}
		r := agg[id]
		if r == nil {
			r = &SeriesRollup{SeriesID: id}
			agg[id] = r
		}
		r.Files++
		r.TotalBytes += size
		if isCandidateCodec(codec, target, recode) {
			r.Convertible++
			if infoJSON != "" {
				var mi MediaInfo
				if json.Unmarshal([]byte(infoJSON), &mi) == nil {
					r.EstBytes += estimatePlanSize(&mi, dp)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The index stores per-episode display titles ("Show - S01E02"), so take show names
	// from one series.List rather than parsing them back out or fetching per series.
	out := []SeriesRollup{}
	if s.series != nil {
		list, err := s.series.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, sm := range list {
			r := agg[sm.ID]
			if r == nil || r.Files == 0 {
				continue
			}
			r.Title, r.Year, r.PosterURL = sm.Title, sm.Year, sm.PosterURL
			out = append(out, *r)
		}
	}
	return out, nil
}

// MediaStats summarizes one media type's slice of the library.
type MediaStats struct {
	Files            int   `json:"files"`
	Convertible      int   `json:"convertible"`
	TotalBytes       int64 `json:"total_bytes"`
	EstBytes         int64 `json:"est_bytes"`         // convertible files' estimated size after conversion
	ConvertibleBytes int64 `json:"convertible_bytes"` // convertible files' CURRENT size on disk
	Reclaimable      int64 `json:"reclaimable"`       // total_bytes of convertible files minus est_bytes
	// HDR counts, over CONVERTIBLE files only — the question the format cards answer is
	// "what would this format leave behind", not "what do I own".
	HDR10       int `json:"hdr10"`
	HDR10Plus   int `json:"hdr10_plus"`
	DolbyVision int `json:"dolby_vision"`
	HLG         int `json:"hlg"`

	H264  int `json:"h264"`
	HEVC  int `json:"hevc"`
	AV1   int `json:"av1"`
	Other int `json:"other"`
}

// LibraryStats is the Overview tab's numbers, across the WHOLE library. The Overview used to
// derive these from the movie list it fetched on page load, so it silently ignored TV — with
// thousands of episodes indexed that made the codec breakdown and "reclaimable" figure wrong.
// One pass over the index instead, and the page no longer fetches every movie just to render.
type LibraryStats struct {
	Movies MediaStats `json:"movies"`
	TV     MediaStats `json:"tv"`
	Total  MediaStats `json:"total"`
}

// addHDR counts a file's HDR format. These drive the format cards in setup: they turn
// "AV1 can't carry Dolby Vision" into "AV1 would leave 47 of your files alone", which is
// the same fact in terms the user can act on.
func (m *MediaStats) addHDR(hdr string) {
	switch hdr {
	case "HDR10":
		m.HDR10++
	case "HDR10+":
		m.HDR10Plus++
	case "Dolby Vision":
		m.DolbyVision++
	case "HLG":
		m.HLG++
	}
}

func (m *MediaStats) add(codec string, size, est int64, convertible bool) {
	m.Files++
	m.TotalBytes += size
	switch codecClass(codec) {
	case "h264":
		m.H264++
	case "hevc":
		m.HEVC++
	case "av1":
		m.AV1++
	default:
		m.Other++
	}
	if convertible {
		m.Convertible++
		m.EstBytes += est
		m.ConvertibleBytes += size
		if d := size - est; d > 0 {
			m.Reclaimable += d
		}
	}
}

// codecClass buckets a codec name for the Overview's breakdown bar.
func codecClass(c string) string {
	switch strings.ToLower(c) {
	case "h264", "avc", "avc1":
		return "h264"
	case "hevc", "h265", "hev1", "hvc1":
		return "hevc"
	case "av1", "av01":
		return "av1"
	default:
		return "other"
	}
}

// LibraryStats aggregates the whole index in one pass.
func (s *Service) LibraryStats(ctx context.Context) (*LibraryStats, error) {
	out := &LibraryStats{}
	if s.index == nil {
		return out, nil
	}
	dp := s.defaultPlan(ctx)
	target := s.targetCodec(ctx)
	recode := s.av1RecodesHEVC(ctx)
	rows, err := s.index.db.QueryContext(ctx,
		`SELECT media_type, size_bytes, video_codec, info_json FROM convert_library`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var mediaType, codec, infoJSON string
		var size int64
		if err := rows.Scan(&mediaType, &size, &codec, &infoJSON); err != nil {
			return nil, err
		}
		convertible := isCandidateCodec(codec, target, recode)
		var est int64
		hdr := ""
		if infoJSON != "" {
			var mi MediaInfo
			if json.Unmarshal([]byte(infoJSON), &mi) == nil {
				hdr = mi.HDR
				if convertible {
					est = estimatePlanSize(&mi, dp)
				}
			}
		}
		per := &out.Movies
		if mediaType == "episode" {
			per = &out.TV
		}
		per.add(codec, size, est, convertible)
		out.Total.add(codec, size, est, convertible)
		if convertible {
			per.addHDR(hdr)
			out.Total.addHDR(hdr)
		}
	}
	return out, rows.Err()
}

// QueueSeries enqueues every convertible episode of a series — or of one season when
// season >= 0 — and returns how many were queued. Episodes already in the target codec
// are skipped, so re-running it is harmless.
func (s *Service) QueueSeries(ctx context.Context, seriesID int64, season int) (int, error) {
	eps, err := s.indexedCandidates(ctx, "episode", seriesID)
	if err != nil {
		return 0, err
	}
	plan := s.defaultPlan(ctx)
	maxFail := s.maxFailures(ctx)
	queued := 0
	for _, c := range eps {
		if !c.Candidate {
			continue
		}
		if season >= 0 && c.Season != season {
			continue
		}
		if s.failures.blocklisted(ctx, episodeKey(c.SeriesID, c.Season, c.Episode), maxFail) {
			continue // keeps failing — don't re-queue it every time
		}
		if _, err := s.enqueueEpisodeIndexed(ctx, c, plan); err != nil {
			s.log.Warn("convert: queue episode failed",
				"series", seriesID, "season", c.Season, "episode", c.Episode, "err", err)
			continue
		}
		queued++
	}
	return queued, nil
}

// indexedCandidates reads the index and shapes it into the list the UI consumes.
// One query — no filesystem access, no probing. seriesID > 0 narrows to a single show.
func (s *Service) indexedCandidates(ctx context.Context, mediaType string, seriesID int64) ([]Candidate, error) {
	if s.index == nil {
		return nil, nil
	}
	q := `SELECT path, media_type, movie_id, series_id, season, episode, title, year,
	             poster_url, size_bytes, video_codec, info_json
	      FROM convert_library WHERE media_type = ?`
	args := []any{mediaType}
	if seriesID > 0 {
		q += ` AND series_id = ?`
		args = append(args, seriesID)
	}
	q += ` ORDER BY title, season, episode`
	rows, err := s.index.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	dp := s.defaultPlan(ctx)
	target := s.targetCodec(ctx)
	recode := s.av1RecodesHEVC(ctx)
	var out []Candidate
	for rows.Next() {
		var r indexRow
		var infoJSON string
		if err := rows.Scan(&r.Path, &r.MediaType, &r.MovieID, &r.SeriesID, &r.Season, &r.Episode,
			&r.Title, &r.Year, &r.PosterURL, &r.SizeBytes, &r.Codec, &infoJSON); err != nil {
			return nil, err
		}
		c := Candidate{
			Kind: r.MediaType, MovieID: r.MovieID, SeriesID: r.SeriesID,
			Season: r.Season, Episode: r.Episode, Title: r.Title, Year: r.Year,
			PosterURL: r.PosterURL, Path: r.Path,
		}
		if infoJSON != "" {
			var mi MediaInfo
			if json.Unmarshal([]byte(infoJSON), &mi) == nil {
				c.Info = &mi
				// Candidacy is derived here, not stored — changing the target codec
				// takes effect immediately with no reindex.
				c.Candidate = isCandidateCodec(mi.VideoCodec, target, recode)
				if c.Candidate {
					c.EstBytes = estimatePlanSize(&mi, dp)
				}
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
