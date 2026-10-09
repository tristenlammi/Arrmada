package movies

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/safego"
)

// Every track caches its file's media info (movies.media_json for the default track,
// movie_versions.media_json for extras). The file is read with ffprobe once when it's
// imported, renamed into a changed file, refreshed, attached by a scan or backfilled; the
// detail page only stats each file and lists each folder once, and a cache entry whose
// size or mtime no longer matches the file is read again in the background. A page never
// waits on ffprobe, so the 3-second reloads during a download cost a stat per track.

// probeTrack reads a file's media facts from the file itself: a stat, an ffprobe and a
// sidecar listing, with the filename's facts where the probe has nothing. Nil for no path;
// Missing when the file can't be stat'd. Only the import, rename/repoint, refresh, scan
// attach and backfill paths call it — never a page view or a sweep.
func (s *Service) probeTrack(path string) *MovieFile {
	if path == "" {
		return nil
	}
	f := nameFacts(path)
	fi, err := s.statFile(path)
	if err != nil {
		f.Missing = true
		return f
	}
	f.SizeBytes = fi.Size()
	f.MtimeUnix = fi.ModTime().Unix()
	f.Subtitles = sidecarSubtitles(path)
	f.OrphanSubtitles = orphanSubtitles(path)
	// Prefer real media info from the file over the (fallible) filename.
	if mi, err := s.probeFile(path); err == nil {
		applyProbe(f, mi, parser.Parse(filepath.Base(path)))
	}
	return f
}

// nameFacts is what a file's name says about it — the answer while its real facts are
// still being read.
func nameFacts(path string) *MovieFile {
	rel := parser.Parse(filepath.Base(path))
	f := &MovieFile{
		Path:         path,
		Filename:     filepath.Base(path),
		Quality:      qualityLabel(path),
		Codec:        string(rel.Codec),
		Audio:        rel.Audio,
		HDR:          rel.HDR,
		Group:        rel.Group,
		MediaVersion: MediaVersion,
	}
	for _, a := range rel.Audio {
		if strings.EqualFold(a, "Atmos") {
			f.Atmos = true
		}
	}
	return f
}

// applyProbe lays the probed facts over the filename's.
func applyProbe(f *MovieFile, mi mediainfo.Info, rel parser.Release) {
	f.Probed = true
	if mi.VideoCodec != "" {
		f.Codec = mi.VideoCodec
	}
	if mi.Resolution != "" {
		f.Resolution = mi.Resolution
		// Show the badge from the REAL resolution + the parsed source, so the detail
		// page and table present the same true facts.
		f.Quality = mi.Resolution
		if rel.Source != "" {
			f.Quality += " " + string(rel.Source)
		}
	}
	if mi.DurationSec > 0 {
		f.DurationMin = mi.DurationSec / 60
	}
	if len(mi.HDR) > 0 {
		f.HDR = mi.HDR
	}
	if len(f.Audio) == 0 && mi.AudioCodec != "" {
		label := mi.AudioCodec
		if mi.Channels > 0 {
			label += " " + audioChannels(mi.Channels)
		}
		f.Audio = []string{label}
	}
	// Atmos lives in the stream's profile, which most filenames don't mention; the file
	// is the authority.
	if mi.Atmos {
		f.Atmos = true
	}
}

// trackResolution is a file's resolution for routing it to a track: the probed one when
// the file was read, else its name's.
func trackResolution(f *MovieFile, path string) string {
	if f != nil && f.Probed && f.Resolution != "" {
		return f.Resolution
	}
	return string(parser.Parse(filepath.Base(path)).Resolution)
}

// cacheCurrent reports whether a cached entry still describes the file stat'd as fi: built
// by the current probe, the same size, and the same mtime. An entry cached before mtimes
// were recorded (0) is judged on its size alone, so upgrading doesn't re-read the whole
// library.
func cacheCurrent(c *MovieFile, fi os.FileInfo) bool {
	if c == nil || c.Missing || c.MediaVersion < MediaVersion || c.SizeBytes != fi.Size() {
		return false
	}
	return c.MtimeUnix == 0 || c.MtimeUnix == fi.ModTime().Unix()
}

// mediaJSONOf encodes a probed entry for the cache ("" for none, or a file that was gone:
// the cache only describes files that were there).
func mediaJSONOf(f *MovieFile) string {
	if f == nil || f.Missing {
		return ""
	}
	b, err := json.Marshal(f)
	if err != nil {
		return ""
	}
	return string(b)
}

// movedMedia is the cache entry for a track's file after it moved to newPath (a rename or
// Convert's swap). When the file there is the same one the cache describes — same size and
// mtime, as a rename keeps them — the entry follows it without reading the file. Otherwise
// the content changed (Convert re-encoded it) and the new file is read once now, so the
// upgrade sweep costs the converted file at its real size.
func (s *Service) movedMedia(cached *MovieFile, newPath string) *MovieFile {
	if cached != nil && cached.MtimeUnix != 0 {
		if fi, err := s.statFile(newPath); err == nil && cacheCurrent(cached, fi) {
			f := *cached
			f.Path, f.Filename, f.Missing = newPath, filepath.Base(newPath), false
			f.Subtitles = sidecarSubtitles(newPath)
			f.OrphanSubtitles = orphanSubtitles(newPath)
			return &f
		}
	}
	return s.probeTrack(newPath)
}

// VersionsLive returns all tracks for a movie with their files as they are on disk now:
// the cached media info, one stat per track (missing, current size) and one folder
// listing per folder for the sidecar subtitles. It never runs ffprobe. A track whose cache
// is empty, built by an older probe, or no longer matches the file's size or mtime is
// answered from the filename and read again in the background (EnsureTrackMedia).
//
// Detail page only — periodic jobs use VersionRows, which doesn't touch the disk at all.
func (s *Service) VersionsLive(ctx context.Context, id int64) ([]Version, error) {
	rows, err := s.VersionRows(ctx, id)
	if err != nil {
		return nil, err
	}
	listings := map[string][]os.DirEntry{}
	listing := func(dir string) []os.DirEntry {
		if e, ok := listings[dir]; ok {
			return e
		}
		e, _ := s.listDir(dir)
		listings[dir] = e
		return e
	}
	for i := range rows {
		v := &rows[i]
		if !v.HasFile || v.FilePath == "" {
			v.File = nil
			continue
		}
		cached := v.File
		fi, statErr := s.statFile(v.FilePath)
		if statErr != nil {
			// Tracked but gone: say so, with whatever the cache knew about the file.
			f := nameFacts(v.FilePath)
			if cached != nil {
				c := *cached
				f = &c
			}
			f.Path, f.Filename = v.FilePath, filepath.Base(v.FilePath)
			f.Missing, f.Subtitles, f.OrphanSubtitles = true, nil, nil
			v.File = f
			continue
		}
		var f *MovieFile
		switch {
		case cacheCurrent(cached, fi):
			c := *cached
			f = &c
		case cached != nil && !cached.Missing && cached.SizeBytes == fi.Size():
			// Same content, older probe or touched: the cached facts beat the filename's
			// while the fresh read runs.
			c := *cached
			f = &c
			s.refreshTrackLater(id, v.ID)
		default:
			f = nameFacts(v.FilePath)
			s.refreshTrackLater(id, v.ID)
		}
		f.Path, f.Filename, f.Missing = v.FilePath, filepath.Base(v.FilePath), false
		f.SizeBytes = fi.Size()
		dir := filepath.Dir(v.FilePath)
		entries := listing(dir)
		f.Subtitles = baseNames(library.PairedSidecarsIn(v.FilePath, entries))
		f.OrphanSubtitles = baseNames(library.OrphanSidecarsIn(dir, entries))
		v.File = f
		v.SizeBytes = fi.Size()
	}
	return rows, nil
}

// listDir lists a folder through the swappable function (tests count the calls).
func (s *Service) listDir(dir string) ([]os.DirEntry, error) {
	if s.readDir == nil {
		return os.ReadDir(dir)
	}
	return s.readDir(dir)
}

// trackKey names one track for the in-flight probe set.
func trackKey(movieID, versionID int64) string { return fmt.Sprintf("%d:%d", movieID, versionID) }

// refreshTrackLater reads a track's file in the background, unless a read of it is
// already running. Detached from the request: the page that asked has its answer.
func (s *Service) refreshTrackLater(movieID, versionID int64) {
	if _, busy := s.probing.Load(trackKey(movieID, versionID)); busy {
		return
	}
	s.bg.Add(1)
	safego.Go(s.log, "movies: read track media", func() {
		defer s.bg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s.EnsureTrackMedia(ctx, movieID, versionID)
	})
}

// EnsureTrackMedia reads one track's file and caches its media info, unless the cache
// already describes it (versionID 0 = the default track). Reads of the same track never
// overlap, at most cap(probeSem) run at once, and a read that finishes after the track's
// file changed writes nothing.
func (s *Service) EnsureTrackMedia(ctx context.Context, movieID, versionID int64) {
	key := trackKey(movieID, versionID)
	if _, busy := s.probing.LoadOrStore(key, struct{}{}); busy {
		return
	}
	defer s.probing.Delete(key)
	// Give up waiting for a probe slot when ctx ends (shutdown, or the caller's budget): a
	// big library queues many of these, and none may outlive the run context.
	if s.probeSem != nil {
		select {
		case s.probeSem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-s.probeSem }()
	}

	var path string
	var cached *MovieFile
	if versionID == 0 {
		m, err := s.repo.Get(ctx, movieID)
		if err != nil || !m.HasFile {
			return
		}
		path, cached = m.MovieFilePath, m.File
	} else {
		v, owner, err := s.repo.GetVersion(ctx, versionID)
		if err != nil || owner != movieID || !v.HasFile {
			return
		}
		path, cached = v.FilePath, v.File
	}
	if path == "" {
		return
	}
	fi, err := s.statFile(path)
	if err != nil || cacheCurrent(cached, fi) {
		return // gone (nothing to read), or already current
	}
	media := mediaJSONOf(s.probeTrack(path))
	if media == "" {
		return
	}
	if versionID == 0 {
		_ = s.repo.SetMediaInfoForPath(ctx, movieID, path, media)
	} else {
		_ = s.repo.SetVersionMediaInfo(ctx, versionID, path, media)
	}
}

// EnsureMedia caches a movie's media info where it isn't cached yet or was cached by an
// older probe: the default track and every extra track (the Movies grid's lazy backfill).
func (s *Service) EnsureMedia(ctx context.Context, id int64) {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return
	}
	if m.MediaStale() && m.MovieFilePath != "" {
		s.EnsureTrackMedia(ctx, id, 0)
	}
	extras, err := s.repo.ListVersions(ctx, id)
	if err != nil {
		return
	}
	for _, v := range extras {
		if ctx.Err() != nil {
			return
		}
		if v.HasFile && v.FilePath != "" && (v.File == nil || v.File.MediaVersion < MediaVersion) {
			s.EnsureTrackMedia(ctx, id, v.ID)
		}
	}
}
