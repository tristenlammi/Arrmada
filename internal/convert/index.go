package convert

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tristenlammi/arrmada/internal/safego"
)

// libraryIndex is the persisted answer to "what's in the library and what codec is it".
// Listing reads this table instead of walking the library and probing per request — see
// migration 0058 for why.
type libraryIndex struct {
	db *sql.DB
	// gen counts writes, so the cached views built over the index (libcache.go) know
	// when they're stale.
	gen atomic.Uint64
}

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
	OrigLang  string // the title's original language (TMDB), for keep-original-language audio
	Info      *MediaInfo
}

// rowMeta is what the indexer needs to know about an indexed file to decide whether it's
// up to date without touching the disk.
type rowMeta struct {
	size     int64
	codec    string
	ver      int
	origLang string
}

// current reports whether an indexed row still describes the file: same size, a probe that
// succeeded, written by the current analysis. An empty codec marks a probe that FAILED
// (spun-down array, transient I/O) — that must be retried, not latched forever.
func (m rowMeta) current(size int64) bool {
	return m.size == size && size > 0 && m.codec != "" && m.ver == probeSchemaVersion
}

// metaFor returns path → rowMeta for a scope.
func (ix *libraryIndex) metaFor(ctx context.Context, mediaType string, seriesID int64) map[string]rowMeta {
	q := `SELECT path, size_bytes, video_codec, info_ver, orig_lang FROM convert_library WHERE media_type = ?`
	args := []any{mediaType}
	if seriesID > 0 {
		q += ` AND series_id = ?`
		args = append(args, seriesID)
	}
	out := map[string]rowMeta{}
	rows, err := ix.db.QueryContext(ctx, q, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var m rowMeta
		if rows.Scan(&p, &m.size, &m.codec, &m.ver, &m.origLang) == nil {
			out[p] = m
		}
	}
	return out
}

// setOrigLang updates just the original language of an otherwise-current row.
func (ix *libraryIndex) setOrigLang(ctx context.Context, path, lang string) {
	if _, err := ix.db.ExecContext(ctx, `UPDATE convert_library SET orig_lang = ? WHERE path = ?`, lang, path); err == nil {
		ix.gen.Add(1)
	}
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
		    size_bytes, video_codec, info_json, orig_lang, info_ver, indexed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(path) DO UPDATE SET
		   media_type = excluded.media_type, movie_id = excluded.movie_id,
		   series_id = excluded.series_id, season = excluded.season, episode = excluded.episode,
		   title = excluded.title, year = excluded.year, poster_url = excluded.poster_url,
		   size_bytes = excluded.size_bytes, video_codec = excluded.video_codec,
		   info_json = excluded.info_json, orig_lang = excluded.orig_lang,
		   info_ver = excluded.info_ver, indexed_at = datetime('now')`,
		r.Path, r.MediaType, r.MovieID, r.SeriesID, r.Season, r.Episode, r.Title, r.Year,
		r.PosterURL, r.SizeBytes, r.Codec, infoJSON, r.OrigLang, probeSchemaVersion)
	if err == nil {
		ix.gen.Add(1)
	}
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
		ix.gen.Add(1)
	}
}

// forget drops a job's item from the index — the library no longer records a file for it,
// so it can't be converted until an import reindexes it.
func (ix *libraryIndex) forget(ctx context.Context, job *Job) {
	var err error
	if job.Kind == "episode" {
		_, err = ix.db.ExecContext(ctx,
			`DELETE FROM convert_library WHERE media_type = 'episode' AND series_id = ? AND season = ? AND episode = ?`,
			job.SeriesID, job.Season, job.Episode)
	} else {
		_, err = ix.db.ExecContext(ctx,
			`DELETE FROM convert_library WHERE media_type = 'movie' AND movie_id = ?`, job.MovieID)
	}
	if err == nil {
		ix.gen.Add(1)
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
	known := s.index.metaFor(ctx, "episode", seriesID)
	origLang := ""
	if full.Extra != nil {
		origLang = full.Extra.OriginalLanguage
	}
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
			if m, ok := known[e.FilePath]; ok && m.current(e.SizeBytes) {
				if m.origLang != origLang {
					s.index.setOrigLang(ctx, e.FilePath, origLang)
				}
				continue
			}
			row := indexRow{
				Path: e.FilePath, MediaType: "episode", SeriesID: full.ID,
				Season: e.SeasonNumber, Episode: e.EpisodeNumber,
				Title:     fmt.Sprintf("%s - S%02dE%02d", full.Title, e.SeasonNumber, e.EpisodeNumber),
				Year:      full.Year,
				PosterURL: full.PosterURL,
				SizeBytes: e.SizeBytes,
				OrigLang:  origLang,
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
		Title: m.Title, Year: m.Year, PosterURL: m.PosterURL, OrigLang: movieOrigLang(m),
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
	s.index.gen.Add(1)
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
			known := s.index.metaFor(ctx, "movie", 0)
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
				// Movie rows carry no size to compare against without a stat, so an indexed
				// row with a successful, current probe is taken as up to date (a convert or
				// re-import reindexes the movie directly).
				if meta, ok := known[m.MovieFilePath]; ok && meta.codec != "" && meta.ver == probeSchemaVersion {
					if meta.origLang != movieOrigLang(m) {
						s.index.setOrigLang(ctx, m.MovieFilePath, movieOrigLang(m))
					}
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
	// Run once we're past today's scheduled time and haven't yet swept since it.
	// The old check required the hourly tick to land INSIDE the scheduled hour at or
	// after the minute — a 03:55 schedule had a ~1-in-12 chance of ever firing.
	sched := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	if s.lastSweep.IsZero() {
		// Startup does its own IndexAll; treat boot as the baseline rather than
		// re-sweeping on the first tick.
		s.lastSweep = now
		return
	}
	if now.Before(sched) || !s.lastSweep.Before(sched) {
		return
	}
	s.lastSweep = now
	s.history.prune(ctx) // once a day is plenty for a 90-day horizon
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
	Convertible int    `json:"convertible"` // worth converting
	Reencode    int    `json:"reencode"`    // of those, re-encodes
	TidyOnly    int    `json:"tidy_only"`   // tracks differ, but too little to gain to do automatically
	TotalBytes  int64  `json:"total_bytes"`
	EstBytes    int64  `json:"est_bytes"`  // estimated size of the worthwhile files after conversion
	SaveBytes   int64  `json:"save_bytes"` // estimated space freed by them
}

// computeLibraryTVSeries returns the per-series roll-up for the TV tab — one grouped query
// over the index, no per-episode work. LibraryTVSeries (libcache.go) is the cached front.
func (s *Service) computeLibraryTVSeries(ctx context.Context) ([]SeriesRollup, error) {
	if s.index == nil {
		return nil, nil
	}
	p := s.prefs(ctx)

	// One sequential scan of the index, aggregated in Go. Aggregating here rather than
	// in SQL is what lets the estimated saving use the same estimatePlanSize the detail
	// view does — and it's still one query with no filesystem access, which was the
	// actual cost. Only the aggregate crosses the wire.
	rows, err := s.index.db.QueryContext(ctx,
		`SELECT series_id, size_bytes, info_json, path, orig_lang
		   FROM convert_library WHERE media_type = 'episode'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	agg := map[int64]*SeriesRollup{}
	dirCache := map[string][]string{} // one ReadDir per season folder, not per episode
	for rows.Next() {
		var id, size int64
		var infoJSON, path, orig string
		if err := rows.Scan(&id, &size, &infoJSON, &path, &orig); err != nil {
			return nil, err
		}
		r := agg[id]
		if r == nil {
			r = &SeriesRollup{SeriesID: id}
			agg[id] = r
		}
		r.Files++
		r.TotalBytes += size
		// The roll-up has to ask the SAME question the episode list asks, or a show
		// reads "all efficient" while every one of its episodes is listed as needing
		// work. That is exactly what happened when this counted codecs alone.
		var mi MediaInfo
		if infoJSON == "" || json.Unmarshal([]byte(infoJSON), &mi) != nil {
			continue
		}
		_, needs := p.planFor(&mi, path, orig, dirCache)
		switch {
		case needs.Worth:
			r.Convertible++
			if needs.Video {
				r.Reencode++
			}
			r.SaveBytes += needs.Save
			r.EstBytes += mi.SizeBytes - needs.Save
		case needs.Any():
			r.TidyOnly++
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
	// Skipped counts files that CAN'T convert (Dolby Vision into AV1, still seeding, …).
	// They're excluded from Reclaimable so the headline stops promising space that will
	// never arrive, but still counted here so the UI can explain the shortfall.
	Skipped int `json:"skipped"`

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
	AsOf   int64      `json:"as_of"` // unix seconds the figures were computed (they're cached)
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

// computeLibraryStats aggregates the whole index in one pass. LibraryStats (libcache.go)
// is the cached front for it.
func (s *Service) computeLibraryStats(ctx context.Context) (*LibraryStats, error) {
	out := &LibraryStats{}
	if s.index == nil {
		return out, nil
	}
	p := s.prefs(ctx)
	// Files whose skip won't resolve on its own aren't reclaimable space, however
	// convertible they look.
	skipped := s.skips.permanentKeys(ctx)
	rows, err := s.index.db.QueryContext(ctx,
		`SELECT media_type, movie_id, series_id, season, episode, size_bytes, video_codec, info_json, path, orig_lang
		   FROM convert_library`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dirCache := map[string][]string{}
	for rows.Next() {
		var mediaType, codec, infoJSON, path, orig string
		var movieID, seriesID, size int64
		var season, episode int
		if err := rows.Scan(&mediaType, &movieID, &seriesID, &season, &episode, &size, &codec, &infoJSON, &path, &orig); err != nil {
			return nil, err
		}
		key := movieKey(movieID)
		if mediaType == "episode" {
			key = episodeKey(seriesID, season, episode)
		}
		var mi MediaInfo
		probed := infoJSON != "" && json.Unmarshal([]byte(infoJSON), &mi) == nil
		hdr := ""
		var est int64
		convertible := false
		if probed {
			hdr = mi.HDR
			_, needs := p.planFor(&mi, path, orig, dirCache)
			convertible = needs.Worth
			if convertible {
				est = size - needs.Save
			}
		}
		permaSkipped := convertible && skipped[key]
		per := &out.Movies
		if mediaType == "episode" {
			per = &out.TV
		}
		per.add(codec, size, est, convertible && !permaSkipped)
		out.Total.add(codec, size, est, convertible && !permaSkipped)
		if permaSkipped {
			per.Skipped++
			out.Total.Skipped++
		} else if convertible {
			per.addHDR(hdr)
			out.Total.addHDR(hdr)
		}
	}
	return out, rows.Err()
}

// indexedCandidates reads the index and shapes it into the list the UI consumes.
// One query — no filesystem access, no probing. seriesID > 0 narrows to a single show.
func (s *Service) indexedCandidates(ctx context.Context, mediaType string, seriesID int64) ([]Candidate, error) {
	if s.index == nil {
		return nil, nil
	}
	q := `SELECT path, media_type, movie_id, series_id, season, episode, title, year,
	             poster_url, size_bytes, video_codec, info_json, orig_lang
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

	p := s.prefs(ctx)
	dirCache := map[string][]string{}
	var out []Candidate
	for rows.Next() {
		var r indexRow
		var infoJSON string
		if err := rows.Scan(&r.Path, &r.MediaType, &r.MovieID, &r.SeriesID, &r.Season, &r.Episode,
			&r.Title, &r.Year, &r.PosterURL, &r.SizeBytes, &r.Codec, &infoJSON, &r.OrigLang); err != nil {
			return nil, err
		}
		c := Candidate{
			Kind: r.MediaType, Key: ItemKey(r.MediaType, r.MovieID, r.SeriesID, r.Season, r.Episode),
			MovieID: r.MovieID, SeriesID: r.SeriesID,
			Season: r.Season, Episode: r.Episode, Title: r.Title, Year: r.Year,
			PosterURL: r.PosterURL, Path: r.Path,
		}
		if infoJSON != "" {
			var mi MediaInfo
			if json.Unmarshal([]byte(infoJSON), &mi) == nil {
				c.Info = &mi
				// The gap is derived here, not stored — changing a setting takes effect
				// immediately with no reindex.
				plan, needs := p.planFor(&mi, r.Path, r.OrigLang, dirCache)
				c.Needs, c.Candidate, c.Worth, c.SaveBytes = needs, needs.Any(), needs.Worth, needs.Save
				if c.Candidate {
					c.EstBytes = mi.SizeBytes - needs.Save
					c.Tracks = trackSummary(&mi, plan)
				}
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RefreshIndex runs a full index pass now, on demand.
//
// Imports keep the index current on their own, and the scheduled sweep catches whatever
// changed outside Arrmada — but only once a day. Anything that slipped both (a hook that
// errored, a file moved in by hand, a run of imports from before the hooks existed) was
// invisible to Convert until 03:00 with no way to hurry it.
//
// Returns false when a sweep is already running: IndexAll walks the whole library, and
// two at once would just contend on the same probes.
func (s *Service) RefreshIndex(ctx context.Context) bool {
	if s.index == nil {
		return false
	}
	if !s.indexMu.TryLock() {
		return false
	}
	s.indexScanning.Store(true)
	safego.Go(s.log, "convert: index refresh", func() {
		// Deliberately NOT the request's context: a full pass outlives the HTTP call
		// that asked for it, and cancelling on response would leave it half done.
		s.RunRefreshIndex(context.WithoutCancel(ctx))
	})
	return true
}

// BeginRefreshIndex claims a full rescan for a caller that runs it itself (the Rescan
// button, as a job): false when one is already running. Follow with RunRefreshIndex, or
// AbandonRefreshIndex if it can't be started. Claimed up front so the status endpoint
// says "running" from the first poll.
func (s *Service) BeginRefreshIndex() bool {
	if s.index == nil || !s.indexMu.TryLock() {
		return false
	}
	s.indexScanning.Store(true)
	return true
}

// AbandonRefreshIndex releases a claim that never ran.
func (s *Service) AbandonRefreshIndex() {
	s.indexScanning.Store(false)
	s.indexMu.Unlock()
}

// RunRefreshIndex runs a claimed rescan and releases the claim.
func (s *Service) RunRefreshIndex(ctx context.Context) {
	defer s.indexMu.Unlock()
	defer s.indexScanning.Store(false)
	s.IndexAll(ctx)
	s.lastSweep = time.Now()
}

// IndexScanning reports whether a rescan is in progress, so the UI can reload the list
// when it finishes rather than asking the user to guess.
func (s *Service) IndexScanning() bool { return s.indexScanning.Load() }
