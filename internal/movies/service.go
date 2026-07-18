package movies

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/parser"
)

// ProfileResolver reports the resolutions a quality-profile reference allows,
// so imports can be routed to the right version track. Implemented by the
// quality service; kept as an interface to avoid a hard dependency.
type ProfileResolver interface {
	AllowedResolutions(ctx context.Context, ref string) []string
}

// LibraryPrefs supplies user preferences that affect what Arrmada writes into the
// library alongside a movie file (metadata sidecars and artwork).
type LibraryPrefs interface {
	WriteNFO() bool
	DownloadArtwork() bool
}

// Service is the Movies module's application logic.
type Service struct {
	repo     *Repo
	meta     metadata.MovieProvider
	log      *slog.Logger
	root     string            // library root, for rescan/rename/manual-import
	recycle  string            // recycle-bin dir ("" = hard delete)
	imp      *library.Importer // reused for naming + import
	resolver ProfileResolver
	bus      *eventbus.Bus
	prefs    LibraryPrefs // nil → lean import (no .nfo / artwork)
	http     *http.Client

	probing  sync.Map      // movie IDs with an in-flight media probe (dedup, so list polling can't storm ffprobe)
	probeSem chan struct{} // bounds how many probes run at once

	muUnmatched   sync.Mutex
	lastUnmatched []UnmatchedFolder // folders the last scan couldn't identify, for manual pick
}

// UnmatchedFolder is a scanned library folder the scan couldn't confidently
// identify, with the search candidates offered for a manual pick.
type UnmatchedFolder struct {
	Folder     string                 `json:"folder"`
	Title      string                 `json:"title"`
	Year       int                    `json:"year"`
	Candidates []metadata.MovieResult `json:"candidates"`
}

// NewService wires the module. recycleDir is where deleted files are moved
// ("" = hard delete).
func NewService(db *sql.DB, meta metadata.MovieProvider, resolver ProfileResolver, root, recycleDir string, bus *eventbus.Bus, log *slog.Logger) *Service {
	return &Service{
		repo:     NewRepo(db),
		meta:     meta,
		log:      log,
		root:     root,
		recycle:  recycleDir,
		imp:      library.NewImporter(root, log),
		resolver: resolver,
		bus:      bus,
		http:     &http.Client{Timeout: 30 * time.Second},
		probeSem: make(chan struct{}, 3),
	}
}

// SetNaming installs the user-configurable file naming scheme.
func (s *Service) SetNaming(np library.NamingProvider) { s.imp.SetNaming(np) }

// SetPrefs installs library-write preferences (.nfo / artwork).
func (s *Service) SetPrefs(p LibraryPrefs) { s.prefs = p }

func (s *Service) writeNFO() bool        { return s.prefs != nil && s.prefs.WriteNFO() }
func (s *Service) downloadArtwork() bool { return s.prefs != nil && s.prefs.DownloadArtwork() }

// MetadataAvailable reports whether the metadata provider is configured.
func (s *Service) MetadataAvailable() bool { return s.meta.Available() }

// Lookup searches the metadata provider for movies to add.
func (s *Service) Lookup(ctx context.Context, query string) ([]metadata.MovieResult, error) {
	return s.meta.SearchMovie(ctx, query)
}

// CollectionMember is a collection film plus whether it's already in the library.
type CollectionMember struct {
	metadata.MovieResult
	InLibrary bool `json:"in_library"`
}

// Collection returns the members of the given TMDB collection, each flagged with
// whether it's already in the library — the data behind "add whole collection".
func (s *Service) Collection(ctx context.Context, collectionID int) (string, []CollectionMember, error) {
	col, err := s.meta.GetCollection(ctx, collectionID)
	if err != nil {
		return "", nil, err
	}
	have, err := s.repo.ExistingTMDBIDs(ctx)
	if err != nil {
		return "", nil, err
	}
	members := make([]CollectionMember, 0, len(col.Members))
	for _, m := range col.Members {
		members = append(members, CollectionMember{MovieResult: m, InLibrary: have[m.TMDBID]})
	}
	return col.Name, members, nil
}

// List returns the library.
func (s *Service) List(ctx context.Context) ([]Movie, error) { return s.repo.List(ctx) }

// Get returns one movie.
func (s *Service) Get(ctx context.Context, id int64) (Movie, error) { return s.repo.Get(ctx, id) }

// MonitoredMissing returns monitored movies without a file.
func (s *Service) MonitoredMissing(ctx context.Context) ([]Movie, error) {
	return s.repo.MonitoredMissing(ctx)
}

// Add pulls full metadata for a TMDB id and adds the movie to the library.
func (s *Service) Add(ctx context.Context, tmdbID int, qualityProfile string, monitored bool) (Movie, error) {
	details, err := s.meta.GetMovie(ctx, tmdbID)
	if err != nil {
		return Movie{}, fmt.Errorf("fetch metadata: %w", err)
	}
	m := Movie{
		TMDBID:         details.TMDBID,
		IMDBID:         details.IMDBID,
		Title:          details.Title,
		Year:           details.Year,
		Overview:       details.Overview,
		PosterURL:      details.PosterURL,
		Runtime:        details.Runtime,
		Status:         details.Status,
		Monitored:      monitored,
		QualityProfile: qualityProfile,
		Extra:          extraFrom(details),
	}
	created, err := s.repo.Create(ctx, m)
	if err != nil {
		return Movie{}, err
	}
	s.log.Info("movie added", "title", created.Title, "year", created.Year)
	_ = s.repo.AddEvent(ctx, created.ID, "added", "Added to library")
	return created, nil
}

// ScanResult summarizes a library scan.
type ScanResult struct {
	Imported  int               `json:"imported"`
	Skipped   int               `json:"skipped"`   // already in the library
	Unmatched []UnmatchedFolder `json:"unmatched"` // folders TMDB couldn't confidently identify
}

// ScanLibrary walks the library root, matches each movie folder/file to TMDB,
// and creates entries for anything not already tracked — marked UNMONITORED with
// an "n/a" quality profile (they already exist on disk; Arrmada just catalogs them).
func (s *Service) ScanLibrary(ctx context.Context, rootOverride string) (ScanResult, error) {
	var res ScanResult
	if !s.meta.Available() {
		return res, fmt.Errorf("movie metadata isn't configured — set ARRMADA_TMDB_API_KEY")
	}
	root := rootOverride
	if root == "" {
		root = s.root
	}
	existing, err := s.repo.List(ctx)
	if err != nil {
		return res, err
	}
	have := make(map[int]bool, len(existing))
	for _, m := range existing {
		have[m.TMDBID] = true
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return res, err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // .recycle and friends
		}
		full := filepath.Join(root, name)
		video, _, verr := library.FindVideo(full)
		if verr != nil || video == "" {
			continue // no video here
		}
		rel := parser.Parse(name)
		if rel.Title == "" {
			rel = parser.Parse(filepath.Base(video))
		}
		results, err := s.meta.SearchMovie(ctx, rel.Title)
		if err != nil || len(results) == 0 {
			res.Unmatched = append(res.Unmatched, UnmatchedFolder{Folder: name, Title: rel.Title, Year: rel.Year})
			continue
		}
		match, ok := bestMatch(results, rel.Title, rel.Year)
		if !ok {
			// No confident match — surface the top candidates for a manual pick.
			res.Unmatched = append(res.Unmatched, UnmatchedFolder{Folder: name, Title: rel.Title, Year: rel.Year, Candidates: topMovies(results, 6)})
			continue
		}
		if have[match.TMDBID] {
			res.Skipped++
			continue
		}
		if err := s.importMovieFile(ctx, video, match.TMDBID); err != nil {
			res.Unmatched = append(res.Unmatched, UnmatchedFolder{Folder: name, Title: rel.Title, Year: rel.Year})
			continue
		}
		have[match.TMDBID] = true
		res.Imported++
	}
	s.setLastUnmatched(res.Unmatched)
	return res, nil
}

// importMovieFile catalogs one on-disk video as the given TMDB movie (unmonitored,
// no quality profile — Arrmada is only adopting an existing file).
func (s *Service) importMovieFile(ctx context.Context, video string, tmdbID int) error {
	details, err := s.meta.GetMovie(ctx, tmdbID)
	if err != nil {
		return err
	}
	created, err := s.repo.Create(ctx, Movie{
		TMDBID: details.TMDBID, IMDBID: details.IMDBID, Title: details.Title, Year: details.Year,
		Overview: details.Overview, PosterURL: details.PosterURL, Runtime: details.Runtime, Status: details.Status,
		Monitored: false, QualityProfile: "n/a", Extra: extraFrom(details),
	})
	if err != nil {
		return err
	}
	_ = s.setDefaultFile(ctx, created.ID, video)
	_ = s.repo.AddEvent(ctx, created.ID, "imported", "Found during library scan: "+filepath.Base(video))
	s.log.Info("library scan: imported", "title", details.Title, "year", details.Year)
	return nil
}

// ImportFolderAs catalogs a specific library folder as the chosen TMDB movie —
// the manual pick for a folder the scan couldn't confidently identify.
func (s *Service) ImportFolderAs(ctx context.Context, rootOverride, folder string, tmdbID int) error {
	root := rootOverride
	if root == "" {
		root = s.root
	}
	video, _, err := library.FindVideo(filepath.Join(root, folder))
	if err != nil || video == "" {
		return fmt.Errorf("no video file found in %q", folder)
	}
	if err := s.importMovieFile(ctx, video, tmdbID); err != nil {
		return err
	}
	s.dropUnmatched(folder)
	return nil
}

func topMovies(results []metadata.MovieResult, n int) []metadata.MovieResult {
	if len(results) > n {
		return results[:n]
	}
	return results
}

func (s *Service) setLastUnmatched(u []UnmatchedFolder) {
	s.muUnmatched.Lock()
	s.lastUnmatched = u
	s.muUnmatched.Unlock()
}

// LastUnmatched returns the folders the most recent scan couldn't identify.
func (s *Service) LastUnmatched() []UnmatchedFolder {
	s.muUnmatched.Lock()
	defer s.muUnmatched.Unlock()
	return append([]UnmatchedFolder(nil), s.lastUnmatched...)
}

func (s *Service) dropUnmatched(folder string) {
	s.muUnmatched.Lock()
	defer s.muUnmatched.Unlock()
	out := s.lastUnmatched[:0]
	for _, u := range s.lastUnmatched {
		if u.Folder != folder {
			out = append(out, u)
		}
	}
	s.lastUnmatched = out
}

// bestMatch resolves a scanned folder to a search result, requiring a confident
// match (exact normalized title, optionally confirmed by year) rather than
// guessing the most popular hit. Returns ok=false when nothing matches.
func bestMatch(results []metadata.MovieResult, title string, year int) (metadata.MovieResult, bool) {
	return metadata.TitleYearMatch(results, title, year,
		func(r metadata.MovieResult) string { return r.Title },
		func(r metadata.MovieResult) int { return r.Year })
}

// extraFrom projects provider details into the stored MovieExtra blob.
func extraFrom(d *metadata.MovieDetails) *MovieExtra {
	ex := &MovieExtra{
		Genres:           d.Genres,
		Studios:          d.Studios,
		OriginalLanguage: d.OriginalLanguage,
		Certification:    d.Certification,
		BackdropURL:      d.BackdropURL,
		ReleaseDate:      d.ReleaseDate,
		CollectionID:     d.CollectionID,
		CollectionName:   d.CollectionName,
		VoteAverage:      d.VoteAverage,
	}
	for _, c := range d.Cast {
		ex.Cast = append(ex.Cast, CastMember{Name: c.Name, Character: c.Character, ProfileURL: c.ProfileURL})
	}
	return ex
}

// Delete removes a movie.
func (s *Service) Delete(ctx context.Context, id int64, deleteFiles bool) error {
	if deleteFiles {
		// Recycle the default file and every extra version's file first.
		versions, _ := s.Versions(ctx, id)
		for _, v := range versions {
			if v.FilePath != "" {
				s.removeFile(v.FilePath)
			}
		}
	}
	_ = s.repo.DeleteVersionsForMovie(ctx, id)
	return s.repo.Delete(ctx, id)
}

// SetMonitored toggles monitoring.
func (s *Service) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	return s.repo.SetMonitored(ctx, id, monitored)
}

// SetQualityProfile changes a movie's quality profile.
func (s *Service) SetQualityProfile(ctx context.Context, id int64, profile string) error {
	return s.repo.SetQualityProfile(ctx, id, profile)
}

// MarkImported records that a movie now has a file (called by the import
// pipeline once a download for it lands). If the movie already had a different
// file, the old one is deleted from disk first — an upgrade replaces, it doesn't
// accumulate (multi-version is a separate, opt-in feature).
func (s *Service) MarkImported(ctx context.Context, id int64, path, sourceRelease string) error {
	versions, err := s.Versions(ctx, id)
	if err != nil {
		return err
	}
	target := s.routeVersion(ctx, versions, path)

	// Upgrade is scoped to the target version: replacing the 1080p track's file
	// never touches the 4K track's file.
	upgrade := false
	if target.HasFile && target.FilePath != "" && target.FilePath != path {
		s.removeFile(target.FilePath)
		s.log.Info("replaced older file on upgrade", "movie_id", id, "version", target.Label, "old", target.FilePath, "new", path)
		upgrade = true
	}

	var size int64
	if fi, statErr := os.Stat(path); statErr == nil {
		size = fi.Size()
	}
	if target.IsDefault {
		if err := s.setDefaultFile(ctx, id, path); err != nil {
			return err
		}
		if sourceRelease != "" {
			_ = s.repo.SetSourceRelease(ctx, id, sourceRelease)
		}
	} else {
		if err := s.repo.SetVersionFile(ctx, target.ID, path, size); err != nil {
			return err
		}
		if sourceRelease != "" {
			_ = s.repo.SetVersionSourceRelease(ctx, target.ID, sourceRelease)
		}
	}

	event, detail := "imported", "Imported "+filepath.Base(path)
	if upgrade {
		event, detail = "upgraded", "Upgraded to "+filepath.Base(path)
	}
	if !target.IsDefault {
		detail += " (" + target.Label + ")"
	}
	_ = s.repo.AddEvent(ctx, id, event, detail)

	// Write Plex/Jellyfin-readable metadata into the movie folder.
	if m, err := s.repo.Get(ctx, id); err == nil {
		s.writeLibraryMetadata(ctx, filepath.Dir(path), m)
	}
	return nil
}

// writeLibraryMetadata writes a Kodi/Jellyfin/Plex-readable movie.nfo and, if
// missing, downloads poster.jpg / fanart.jpg into the movie folder. Best-effort:
// failures are logged, never fatal to the import.
func (s *Service) writeLibraryMetadata(ctx context.Context, dir string, m Movie) {
	if dir == "" {
		return
	}
	if s.writeNFO() {
		if err := s.writeMovieNFO(dir, m); err != nil {
			s.log.Warn("write nfo failed", "movie", m.Title, "err", err)
		}
	}
	if s.downloadArtwork() {
		s.fetchArt(ctx, filepath.Join(dir, "poster.jpg"), m.PosterURL)
		if m.Extra != nil {
			s.fetchArt(ctx, filepath.Join(dir, "fanart.jpg"), m.Extra.BackdropURL)
		}
	}
}

// nfoMovie is the Kodi movie.nfo schema (understood by Plex/Jellyfin/Emby too).
type nfoMovie struct {
	XMLName   xml.Name `xml:"movie"`
	Title     string   `xml:"title"`
	Year      int      `xml:"year,omitempty"`
	Plot      string   `xml:"plot,omitempty"`
	Runtime   int      `xml:"runtime,omitempty"`
	MPAA      string   `xml:"mpaa,omitempty"`
	Rating    float64  `xml:"rating,omitempty"`
	Premiered string   `xml:"premiered,omitempty"`
	Studios   []string `xml:"studio,omitempty"`
	Genres    []string `xml:"genre,omitempty"`
	TMDBID    int      `xml:"tmdbid,omitempty"`
	IMDBID    string   `xml:"id,omitempty"`
	UniqueIDs []nfoUID `xml:"uniqueid"`
}

type nfoUID struct {
	Type    string `xml:"type,attr"`
	Default bool   `xml:"default,attr,omitempty"`
	Value   string `xml:",chardata"`
}

func (s *Service) writeMovieNFO(dir string, m Movie) error {
	n := nfoMovie{
		Title:   m.Title,
		Year:    m.Year,
		Plot:    m.Overview,
		Runtime: m.Runtime,
		IMDBID:  m.IMDBID,
		TMDBID:  m.TMDBID,
	}
	if m.Extra != nil {
		n.MPAA = m.Extra.Certification
		n.Rating = m.Extra.VoteAverage
		n.Premiered = m.Extra.ReleaseDate
		n.Studios = m.Extra.Studios
		n.Genres = m.Extra.Genres
	}
	if m.TMDBID > 0 {
		n.UniqueIDs = append(n.UniqueIDs, nfoUID{Type: "tmdb", Value: strconv.Itoa(m.TMDBID)})
	}
	if m.IMDBID != "" {
		n.UniqueIDs = append(n.UniqueIDs, nfoUID{Type: "imdb", Default: true, Value: m.IMDBID})
	}
	body, err := xml.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	out := append([]byte(xml.Header), body...)
	return os.WriteFile(filepath.Join(dir, "movie.nfo"), out, 0o644)
}

// fetchArt downloads an image to dst unless it already exists. No-op on empty URL.
func (s *Service) fetchArt(ctx context.Context, dst, url string) {
	if url == "" {
		return
	}
	if _, err := os.Stat(dst); err == nil {
		return // already present — don't re-download
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	f, err := os.Create(dst)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = io.Copy(f, io.LimitReader(resp.Body, 25<<20))
}

// Versions returns all tracks for a movie: the default (the movie row) followed
// by any extra version tracks, each enriched with on-disk file info.
func (s *Service) Versions(ctx context.Context, id int64) ([]Version, error) {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	def := Version{
		ID: 0, IsDefault: true, Label: "Default",
		QualityProfile: m.QualityProfile, Monitored: m.Monitored,
		HasFile: m.HasFile, FilePath: m.MovieFilePath, SourceRelease: m.SourceRelease,
		File: s.fileInfo(m.MovieFilePath, m.HasFile),
	}
	out := []Version{def}
	extras, err := s.repo.ListVersions(ctx, id)
	if err != nil {
		return out, nil // degrade to default-only rather than failing the page
	}
	for _, v := range extras {
		v.File = s.fileInfo(v.FilePath, v.HasFile)
		out = append(out, v)
	}
	return out, nil
}

// HasExtraVersions reports whether a movie has any opt-in extra tracks.
func (s *Service) HasExtraVersions(ctx context.Context, id int64) bool {
	extras, err := s.repo.ListVersions(ctx, id)
	return err == nil && len(extras) > 0
}

// routeVersion picks which version an imported file belongs to, by matching the
// file's resolution against each version's quality profile. Falls back to the
// default version when nothing matches.
func (s *Service) routeVersion(ctx context.Context, versions []Version, path string) Version {
	res := s.resolutionOf(path)
	best := versions[0] // default
	bestScore := s.matchScore(ctx, versions[0].QualityProfile, res)
	for _, v := range versions[1:] {
		if sc := s.matchScore(ctx, v.QualityProfile, res); sc > bestScore {
			best, bestScore = v, sc
		}
	}
	return best
}

// resolutionOf returns a file's resolution, preferring real media info (ffprobe)
// over the filename so a mislabeled release routes to the right track.
func (s *Service) resolutionOf(path string) string {
	if mediainfo.Available() {
		if mi, err := mediainfo.Probe(path); err == nil && mi.Resolution != "" {
			return mi.Resolution
		}
	}
	return string(parser.Parse(filepath.Base(path)).Resolution)
}

// matchScore rates how well a profile wants a resolution: higher = more specific.
// -1 = the profile forbids it, 0 = accepts any resolution, >0 = explicitly lists
// it (fewer allowed resolutions ⇒ more specific ⇒ higher score).
func (s *Service) matchScore(ctx context.Context, profileRef, res string) int {
	if s.resolver == nil {
		return 0
	}
	allowed := s.resolver.AllowedResolutions(ctx, profileRef)
	if len(allowed) == 0 {
		return 0 // any resolution
	}
	for _, a := range allowed {
		if a == res {
			return 100 - len(allowed)
		}
	}
	return -1
}

// AddVersion adds an opt-in extra version track to a movie.
func (s *Service) AddVersion(ctx context.Context, movieID int64, label, profile, edition string, monitored bool) (Version, error) {
	if _, err := s.repo.Get(ctx, movieID); err != nil {
		return Version{}, err
	}
	if label == "" {
		label = "Version"
	}
	v, err := s.repo.CreateVersion(ctx, movieID, Version{Label: label, QualityProfile: profile, Edition: edition, Monitored: monitored})
	if err != nil {
		return Version{}, err
	}
	_ = s.repo.AddEvent(ctx, movieID, "version_added", "Added version: "+label)
	return v, nil
}

// UpdateVersion edits an extra version's mutable fields.
func (s *Service) UpdateVersion(ctx context.Context, versionID int64, label, profile, edition string, monitored bool) error {
	return s.repo.UpdateVersion(ctx, versionID, label, profile, edition, monitored)
}

// DeleteVersion removes an extra version track and its file.
func (s *Service) DeleteVersion(ctx context.Context, versionID int64) error {
	v, movieID, err := s.repo.GetVersion(ctx, versionID)
	if err != nil {
		return err
	}
	if v.FilePath != "" {
		s.removeFile(v.FilePath)
	}
	if err := s.repo.DeleteVersion(ctx, versionID); err != nil {
		return err
	}
	_ = s.repo.AddEvent(ctx, movieID, "version_removed", "Removed version: "+v.Label)
	return nil
}

// DeleteVersionFile deletes a version's file (vid 0 = the default version).
func (s *Service) DeleteVersionFile(ctx context.Context, movieID, versionID int64) error {
	if versionID == 0 {
		return s.DeleteFile(ctx, movieID)
	}
	v, _, err := s.repo.GetVersion(ctx, versionID)
	if err != nil {
		return err
	}
	if v.FilePath != "" {
		s.removeFile(v.FilePath)
		_ = s.repo.AddEvent(ctx, movieID, "deleted", "Deleted "+filepath.Base(v.FilePath)+" ("+v.Label+")")
	}
	return s.repo.ClearVersionFile(ctx, versionID)
}

// FileInfo returns on-disk details for a movie's (default) file, or nil if none.
func (s *Service) FileInfo(ctx context.Context, id int64) (*MovieFile, error) {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.fileInfo(m.MovieFilePath, m.HasFile), nil
}

// setDefaultFile records the default file path AND caches its media info, so the
// table view can show attributes without re-probing on every list request.
func (s *Service) setDefaultFile(ctx context.Context, id int64, path string) error {
	if err := s.repo.SetFile(ctx, id, path); err != nil {
		return err
	}
	if info := s.fileInfo(path, true); info != nil {
		if b, err := json.Marshal(info); err == nil {
			_ = s.repo.SetMediaInfo(ctx, id, string(b))
		}
	}
	return nil
}

// EnsureMedia caches a movie's media info if it isn't cached yet (lazy backfill
// for movies imported before media caching existed).
func (s *Service) EnsureMedia(ctx context.Context, id int64) {
	// Dedup: list polling fires one of these per uncached file on every request. Skip if this
	// movie is already being probed, and bound total concurrent probes so a big library can't
	// storm ffprobe. Once a probe finishes the info is cached, so it won't be re-spawned.
	if _, busy := s.probing.LoadOrStore(id, struct{}{}); busy {
		return
	}
	defer s.probing.Delete(id)
	s.probeSem <- struct{}{}
	defer func() { <-s.probeSem }()

	m, err := s.repo.Get(ctx, id)
	if err != nil || m.File != nil || !m.HasFile || m.MovieFilePath == "" {
		return
	}
	if info := s.fileInfo(m.MovieFilePath, true); info != nil {
		if b, mErr := json.Marshal(info); mErr == nil {
			_ = s.repo.SetMediaInfo(ctx, id, string(b))
		}
	}
}

// fileInfo builds media-info for a file path (shared by the default file and
// each version). Returns nil when there is no file.
func (s *Service) fileInfo(path string, hasFile bool) *MovieFile {
	if !hasFile || path == "" {
		return nil
	}
	rel := parser.Parse(filepath.Base(path))
	f := &MovieFile{
		Path:     path,
		Filename: filepath.Base(path),
		Quality:  qualityLabel(path),
		Codec:    string(rel.Codec),
		Audio:    rel.Audio,
		HDR:      rel.HDR,
		Group:    rel.Group,
	}
	if fi, statErr := os.Stat(path); statErr == nil {
		f.SizeBytes = fi.Size()
		f.Subtitles = sidecarSubtitles(path)
		// Prefer real media info from the file over the (fallible) filename.
		if mediainfo.Available() {
			if mi, err := mediainfo.Probe(path); err == nil {
				f.Probed = true
				if mi.VideoCodec != "" {
					f.Codec = mi.VideoCodec
				}
				if mi.Resolution != "" {
					f.Resolution = mi.Resolution
					// Show the badge from the REAL resolution + the parsed source,
					// so the detail page and table present the same true facts.
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
			}
		}
	} else {
		f.Missing = true
	}
	return f
}

// audioChannels renders a channel count as a familiar layout label.
func audioChannels(n int) string {
	switch n {
	case 1:
		return "1.0"
	case 2:
		return "2.0"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	default:
		return strconv.Itoa(n) + "ch"
	}
}

var subtitleExts = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".vtt": true}

// sidecarSubtitles lists subtitle files sitting next to the movie file.
func sidecarSubtitles(moviePath string) []string {
	dir := filepath.Dir(moviePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var subs []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if subtitleExts[strings.ToLower(filepath.Ext(e.Name()))] {
			subs = append(subs, e.Name())
		}
	}
	return subs
}

// DeleteFile removes a movie's file from disk and clears its file record,
// flipping the movie back to Wanted (if monitored).
func (s *Service) DeleteFile(ctx context.Context, id int64) error {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if m.MovieFilePath != "" {
		s.removeFile(m.MovieFilePath)
		s.log.Info("deleted movie file", "movie", m.Title, "path", m.MovieFilePath)
		_ = s.repo.AddEvent(ctx, id, "deleted", "Deleted "+filepath.Base(m.MovieFilePath))
	}
	return s.repo.ClearFile(ctx, id)
}

// removeFile deletes a file (moving it to the recycle bin if configured) and
// prunes its now-empty parent directory.
func (s *Service) removeFile(path string) {
	if s.recycle != "" {
		if err := s.recycleFile(path); err != nil {
			s.log.Warn("recycle failed, hard-deleting", "path", path, "err", err)
			_ = os.Remove(path)
		}
	} else if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		s.log.Warn("could not delete file", "path", path, "err", err)
		return
	}
	// Best-effort: remove the movie folder if nothing else is left in it.
	_ = os.Remove(filepath.Dir(path))
	// Let the import pipeline forget this file so re-grabbing the same release
	// (e.g. after deleting a version) imports again instead of being deduped.
	if s.bus != nil {
		s.bus.Publish("file.removed", map[string]any{"path": path})
	}
}

// recycleFile moves a file into the recycle bin, keeping its folder name so it's
// identifiable, and de-duplicating on collision.
func (s *Service) recycleFile(path string) error {
	dst, err := library.RecycleFile(s.recycle, path)
	if err != nil {
		return err
	}
	if dst != "" {
		s.log.Info("moved to recycle bin", "from", path, "to", dst)
	}
	return nil
}

// qualityLabel renders the resolution + source parsed from a filename, e.g.
// "2160p BluRay". Empty when nothing recognizable is present.
func qualityLabel(path string) string {
	r := parser.Parse(filepath.Base(path))
	var parts []string
	if r.Resolution != "" {
		parts = append(parts, string(r.Resolution))
	}
	if r.Source != "" {
		parts = append(parts, string(r.Source))
	}
	return strings.Join(parts, " ")
}

// SetMinAvailability changes when a movie becomes eligible for searching.
func (s *Service) SetMinAvailability(ctx context.Context, id int64, avail string) error {
	switch avail {
	case "announced", "inCinemas", "released":
	default:
		return fmt.Errorf("invalid availability %q", avail)
	}
	return s.repo.SetMinAvailability(ctx, id, avail)
}

// Events returns a movie's activity timeline.
func (s *Service) Events(ctx context.Context, id int64, limit int) ([]Event, error) {
	return s.repo.Events(ctx, id, limit)
}

// AddEvent appends a timeline event (used by the coordinator on grab).
func (s *Service) AddEvent(ctx context.Context, id int64, event, detail string) {
	_ = s.repo.AddEvent(ctx, id, event, detail)
}

// IsAvailable reports whether a movie has reached its minimum-availability
// threshold and should therefore be searched.
func (s *Service) IsAvailable(m Movie) bool {
	return available(m, time.Now())
}

func available(m Movie, now time.Time) bool {
	switch m.MinAvailability {
	case "announced":
		return true
	case "inCinemas", "released":
		// A known release date is the source of truth. If it's still in the future the movie
		// isn't out yet — don't search — even when TMDB's status says "Released" (it flips early
		// on some entries, and bad/duplicate entries can be flat-out wrong). This is what stops a
		// months-away film from being searched (and grabbing a wrong-title release) prematurely.
		if m.Extra != nil && m.Extra.ReleaseDate != "" {
			if t, err := time.Parse("2006-01-02", m.Extra.ReleaseDate); err == nil {
				return !now.Before(t)
			}
		}
		if m.Status == "Released" {
			return true
		}
		// No parseable date and not marked Released — treat "released" strictly, cinemas leniently.
		return m.MinAvailability == "inCinemas"
	default:
		return true
	}
}

// Refresh re-pulls metadata from the provider and reconciles the movie's file
// record with what's actually on disk. Returns the updated movie.
func (s *Service) Refresh(ctx context.Context, id int64) (Movie, error) {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return Movie{}, err
	}
	// 1. Metadata refresh (best-effort — a provider hiccup shouldn't block rescan).
	if s.meta.Available() {
		if d, derr := s.meta.GetMovie(ctx, m.TMDBID); derr == nil {
			m.IMDBID, m.Title, m.Year = d.IMDBID, d.Title, d.Year
			m.Overview, m.PosterURL, m.Runtime, m.Status = d.Overview, d.PosterURL, d.Runtime, d.Status
			m.Extra = extraFrom(d)
			if uerr := s.repo.UpdateMetadata(ctx, id, m); uerr != nil {
				s.log.Warn("refresh: metadata update failed", "movie", m.Title, "err", uerr)
			}
		} else {
			s.log.Warn("refresh: metadata fetch failed", "movie", m.Title, "err", derr)
		}
	}
	// 2. Disk rescan: reconcile has_file/path with reality.
	s.rescan(ctx, &m)
	// 3. Re-probe the file so cached media info (codec/resolution/…) stays truthful.
	if m.HasFile && m.MovieFilePath != "" {
		_ = s.setDefaultFile(ctx, id, m.MovieFilePath)
	}
	_ = s.repo.AddEvent(ctx, id, "refreshed", "Refreshed metadata and rescanned disk")
	return s.repo.Get(ctx, id)
}

// rescan reconciles a movie's file record against its library folder in place.
func (s *Service) rescan(ctx context.Context, m *Movie) {
	folder := s.movieFolder(*m)
	found, _, err := library.FindVideo(folder)
	switch {
	case err == nil && found != "":
		if found != m.MovieFilePath {
			_ = s.setDefaultFile(ctx, m.ID, found)
			m.MovieFilePath, m.HasFile = found, true
			s.log.Info("rescan: adopted file on disk", "movie", m.Title, "path", found)
			_ = s.repo.AddEvent(ctx, m.ID, "detected", "Found file on disk: "+filepath.Base(found))
		}
	default:
		// No video in the folder. If we thought we had one, clear it.
		if m.HasFile {
			_ = s.repo.ClearFile(ctx, m.ID)
			m.HasFile, m.MovieFilePath = false, ""
			s.log.Info("rescan: tracked file no longer on disk", "movie", m.Title)
			_ = s.repo.AddEvent(ctx, m.ID, "missing", "Tracked file no longer on disk")
		}
	}
}

// movieFolder is the library directory for a movie: the tracked file's folder if
// it has one, else the canonical "<root>/<Title (Year)>" (derived via the
// importer so the naming/cleaning rules stay in one place).
func (s *Service) movieFolder(m Movie) string {
	if m.MovieFilePath != "" {
		return filepath.Dir(m.MovieFilePath)
	}
	return filepath.Dir(s.imp.MovieTarget(m.Title, m.Year, "", ".mkv"))
}

// ImportCandidate is an unmatched video file available for manual import.
type ImportCandidate struct {
	Path      string `json:"path"`
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes"`
	Quality   string `json:"quality,omitempty"`
}

// ManualImportCandidates lists video files under dir that could be imported
// (larger than a sample-clip threshold).
func (s *Service) ManualImportCandidates(dir string) ([]ImportCandidate, error) {
	var out []ImportCandidate
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !isVideoFile(p) {
			return nil
		}
		fi, e := d.Info()
		if e != nil || fi.Size() < 50<<20 { // skip < 50 MB (samples)
			return nil
		}
		out = append(out, ImportCandidate{
			Path:      p,
			Filename:  filepath.Base(p),
			SizeBytes: fi.Size(),
			Quality:   qualityLabel(p),
		})
		return nil
	})
	return out, err
}

// ManualImport imports a specific on-disk file into a movie and marks it.
func (s *Service) ManualImport(ctx context.Context, id int64, srcPath string) error {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	res, err := s.imp.ImportAs(m.Title, m.Year, srcPath)
	if err != nil {
		return err
	}
	// The source filename usually carries the quality tags (scene/p2p naming), so
	// use it as the release name for upgrade scoring.
	return s.MarkImported(ctx, id, res.TargetPath, filepath.Base(res.SourcePath))
}

// RenamePreview returns the canonical name a movie's file should have, and
// whether it already matches.
func (s *Service) RenamePreview(ctx context.Context, id int64) (current, proposed string, matches bool, err error) {
	m, gerr := s.repo.Get(ctx, id)
	if gerr != nil {
		return "", "", false, gerr
	}
	if m.MovieFilePath == "" {
		return "", "", true, nil
	}
	target := s.imp.MovieTarget(m.Title, m.Year, filepath.Base(m.MovieFilePath), filepath.Ext(m.MovieFilePath))
	return filepath.Base(m.MovieFilePath), filepath.Base(target), target == m.MovieFilePath, nil
}

// Rename renames a movie's file to the canonical scheme in place.
func (s *Service) Rename(ctx context.Context, id int64) error {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if m.MovieFilePath == "" {
		return fmt.Errorf("movie has no file to rename")
	}
	target := s.imp.MovieTarget(m.Title, m.Year, filepath.Base(m.MovieFilePath), filepath.Ext(m.MovieFilePath))
	if target == m.MovieFilePath {
		return nil
	}
	oldDir := filepath.Dir(m.MovieFilePath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	if err := os.Rename(m.MovieFilePath, target); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	s.imp.MoveEpisodeSubs(m.MovieFilePath, target) // carry sidecar subtitles along
	if newDir := filepath.Dir(target); newDir != oldDir {
		s.imp.RemoveDirIfEmpty(oldDir) // the movie moved to a renamed folder; drop the empty old one
	}
	if err := s.setDefaultFile(ctx, id, target); err != nil {
		return err
	}
	_ = s.repo.AddEvent(ctx, id, "renamed", filepath.Base(m.MovieFilePath)+" → "+filepath.Base(target))
	return nil
}

var videoFileExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true, ".mov": true,
	".wmv": true, ".ts": true, ".mpg": true, ".mpeg": true, ".webm": true, ".flv": true,
}

func isVideoFile(p string) bool { return videoFileExts[strings.ToLower(filepath.Ext(p))] }

// MatchRelease finds the library movie a raw release/download name belongs to.
func (s *Service) MatchRelease(ctx context.Context, name string) (Movie, bool) {
	r := parser.Parse(name)
	return s.Match(ctx, r.Title, r.Year)
}

// Match finds the library movie a parsed release belongs to, comparing
// normalized titles and (when the release has a year) the year within ±1.
func (s *Service) Match(ctx context.Context, title string, year int) (Movie, bool) {
	all, err := s.repo.List(ctx)
	if err != nil {
		return Movie{}, false
	}
	want := normalizeTitle(title)
	for _, m := range all {
		if normalizeTitle(m.Title) != want {
			continue
		}
		if year == 0 || m.Year == 0 || abs(m.Year-year) <= 1 {
			return m, true
		}
	}
	return Movie{}, false
}

func normalizeTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
