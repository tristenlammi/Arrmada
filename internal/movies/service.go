package movies

import (
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/outbox"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/safego"
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
	root     string            // library root, for rescan/rename/manual-import (see libRoot)
	rootFn   func() string     // the live library root; nil = root
	bin      library.Bin       // where deleted/replaced files go (off = permanent delete)
	imp      *library.Importer // reused for naming + import
	resolver ProfileResolver
	bus      *eventbus.Bus
	outbox   outbox.Enqueuer // durable side effects of file changes (events.go); nil = none
	prefs    LibraryPrefs    // nil → lean import (no .nfo / artwork)
	http     *http.Client
	// onFileRemoved runs synchronously whenever a library file is deleted, so the import
	// pipeline forgets it before anything can import it back (nil = nothing to tell).
	onFileRemoved func(ctx context.Context, path string)

	// The only ways this service looks at a library file: ffprobe and stat. Swappable so
	// tests can count them — periodic jobs must cause neither (see VersionRows).
	probe func(path string) (mediainfo.Info, error)
	stat  func(path string) (os.FileInfo, error)
	// readDir lists a folder for the detail page's sidecar subtitles (nil = os.ReadDir).
	readDir func(dir string) ([]os.DirEntry, error)

	probing  sync.Map       // tracks ("movie:version") with an in-flight media read (dedup, so polling can't storm ffprobe)
	probeSem chan struct{}  // bounds how many probes run at once
	bg       sync.WaitGroup // background track reads in flight (tests wait on it)
	// backfillTried is when each movie was last handed to BackfillStaleMedia, so a file
	// that can't be probed isn't retried on every poll of the grid.
	backfillTried sync.Map

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
	imp := library.NewImporter(root, log)
	// Manual imports and renames that replace a file go through the same bin as deletes.
	imp.SetRecycleDir(recycleDir)
	return &Service{
		repo:     NewRepo(db),
		meta:     meta,
		log:      log,
		root:     root,
		bin:      library.SingleBin(recycleDir),
		imp:      imp,
		resolver: resolver,
		bus:      bus,
		http:     &http.Client{Timeout: 30 * time.Second},
		probeSem: make(chan struct{}, 3),
		probe:    probeIfInstalled,
		stat:     os.Stat,
	}
}

// SetRootFunc makes the movies folder live: scans, manual imports and renames read it on
// every use, so a folder changed in Settings → Library applies without a restart. Call
// it at startup, before anything runs.
func (s *Service) SetRootFunc(fn func() string) {
	s.rootFn = fn
	s.imp.SetRootFuncs(library.RootFuncs{Movie: s.libRoot})
}

// SetBin routes deleted and replaced files to bin (the per-library bins in the app).
// Call it at startup, before anything runs.
func (s *Service) SetBin(b library.Bin) {
	s.bin = b
	s.imp.SetBin(b)
}

// libRoot is the movies folder now.
func (s *Service) libRoot() string {
	if s.rootFn != nil {
		return strings.TrimSpace(s.rootFn())
	}
	return s.root
}

// SetFileAccess replaces how the service probes and stats library files (nil keeps the
// current one). It exists for tests, which count the calls to prove the periodic sweeps
// make none.
func (s *Service) SetFileAccess(probe func(path string) (mediainfo.Info, error), stat func(path string) (os.FileInfo, error)) {
	if probe != nil {
		s.probe = probe
	}
	if stat != nil {
		s.stat = stat
	}
}

// probeFile and statFile go through the swappable functions, falling back to the real
// ones on a Service built without NewService.
func (s *Service) probeFile(path string) (mediainfo.Info, error) {
	if s.probe == nil {
		return probeIfInstalled(path)
	}
	return s.probe(path)
}

func (s *Service) statFile(path string) (os.FileInfo, error) {
	if s.stat == nil {
		return os.Stat(path)
	}
	return s.stat(path)
}

// errNoFFprobe is what probeIfInstalled reports when ffprobe isn't installed.
var errNoFFprobe = errors.New("ffprobe not installed")

// probeIfInstalled is the default probe: ffprobe when it's installed, otherwise an error,
// which every caller treats as "use the filename".
func probeIfInstalled(path string) (mediainfo.Info, error) {
	if !mediainfo.Available() {
		return mediainfo.Info{}, errNoFFprobe
	}
	return mediainfo.Probe(path)
}

// SetNaming installs the user-configurable file naming scheme.
func (s *Service) SetNaming(np library.NamingProvider) { s.imp.SetNaming(np) }

// SetOnFileRemoved installs the hook told about every library file this service deletes
// (or recycles), synchronously, before the file.removed event goes out. The import
// manager uses it to forget the import, so a deleted file whose torrent is still seeding
// isn't imported straight back — even when a busy bus would have dropped the event.
func (s *Service) SetOnFileRemoved(fn func(ctx context.Context, path string)) { s.onFileRemoved = fn }

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

// SearchTargets returns the movies with a monitored track that has no file — the
// search-missing and RSS sweeps' work list, chosen in SQL so a complete library costs
// those sweeps nothing.
func (s *Service) SearchTargets(ctx context.Context) ([]Movie, error) {
	return s.repo.SearchTargets(ctx)
}

// UpgradeTargets returns the monitored movies that have a file — the upgrade sweep's work
// list.
func (s *Service) UpgradeTargets(ctx context.Context) ([]Movie, error) {
	return s.repo.UpgradeTargets(ctx)
}

// SearchState returns the movie's last sweep time and consecutive-miss count.
func (s *Service) SearchState(ctx context.Context, id int64) (string, int) {
	return s.repo.SearchState(ctx, id)
}

// RecordSearchMiss notes that a sweep found nothing grabbable for this movie.
func (s *Service) RecordSearchMiss(ctx context.Context, id int64) { s.repo.RecordSearchMiss(ctx, id) }

// ResetSearchMisses clears the search backoff (a grab succeeded).
func (s *Service) ResetSearchMisses(ctx context.Context, id int64) { s.repo.ResetSearchMisses(ctx, id) }

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
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"` // already in the library with this file
	// Attached counts films already in the library without a file that the scan gave the
	// file it found — the files the sweeps would otherwise have downloaded again.
	Attached int `json:"attached"`
	// Duplicates are folders for a film that already has a different file: maybe another
	// cut, maybe a stray copy — the owner decides, nothing is changed.
	Duplicates []ScanDuplicate   `json:"duplicates"`
	Unmatched  []UnmatchedFolder `json:"unmatched"` // folders TMDB couldn't confidently identify
}

// ScanDuplicate is a scanned folder whose film already has a different file.
type ScanDuplicate struct {
	MovieID int64  `json:"movie_id"`
	Title   string `json:"title"`
	Folder  string `json:"folder"`
	Path    string `json:"path"`
}

// ScanOptions says how a scan catalogs the films it adds. The zero value is the old
// behaviour: unmonitored, with no profile ("n/a"), so Arrmada only catalogs them.
type ScanOptions struct {
	// Monitor adds the films monitored, so the upgrade sweep can replace their files.
	Monitor bool `json:"monitor"`
	// QualityProfile is the profile they get ("" = "n/a").
	QualityProfile string `json:"quality_profile"`
}

// ScanLibrary walks the library root and matches each movie folder/file to TMDB. A film
// not in the library is created (as opts says; by default UNMONITORED with an "n/a"
// profile — Arrmada just catalogs it). A film already in the library without a file gets
// the file attached, so the sweeps stop looking for something the owner already has. A
// film that already has a different file is reported as a duplicate and left alone.
func (s *Service) ScanLibrary(ctx context.Context, rootOverride string, opts ScanOptions) (ScanResult, error) {
	res := ScanResult{Duplicates: []ScanDuplicate{}, Unmatched: []UnmatchedFolder{}}
	if !s.meta.Available() {
		return res, fmt.Errorf("movie metadata isn't configured — add a TMDB key in Settings → System → API keys")
	}
	root := rootOverride
	if root == "" {
		root = s.libRoot()
	}
	tracks, existing, err := s.LibraryVersionRows(ctx)
	if err != nil {
		return res, err
	}
	byTMDB := make(map[int]Movie, len(existing))
	for _, m := range existing {
		byTMDB[m.TMDBID] = m
	}
	// Every path any track holds: a file already recorded is never attached twice or
	// reported as a duplicate of itself.
	owned := map[string]bool{}
	for _, vs := range tracks {
		for _, v := range vs {
			if v.HasFile && v.FilePath != "" {
				owned[filepath.Clean(v.FilePath)] = true
			}
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return res, err
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			break
		}
		name := e.Name()
		if library.SkipScanDir(name) {
			continue // the recycle bins and other hidden folders
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
		if m, inLibrary := byTMDB[match.TMDBID]; inLibrary {
			switch {
			case owned[filepath.Clean(video)]:
				res.Skipped++
			case !m.HasFile:
				if err := s.attachExisting(ctx, m, video, "Attached during library scan: "); err != nil {
					s.log.Warn("library scan: couldn't attach the file to the film already in the library",
						"title", m.Title, "path", video, "err", err)
					res.Unmatched = append(res.Unmatched, UnmatchedFolder{Folder: name, Title: rel.Title, Year: rel.Year})
					continue
				}
				m.HasFile, m.MovieFilePath = true, video
				byTMDB[match.TMDBID] = m
				owned[filepath.Clean(video)] = true
				res.Attached++
			default:
				res.Duplicates = append(res.Duplicates, ScanDuplicate{MovieID: m.ID, Title: m.Title, Folder: name, Path: video})
			}
			continue
		}
		created, err := s.importMovieFile(ctx, video, match.TMDBID, opts)
		if err != nil {
			res.Unmatched = append(res.Unmatched, UnmatchedFolder{Folder: name, Title: rel.Title, Year: rel.Year})
			continue
		}
		byTMDB[match.TMDBID] = created
		owned[filepath.Clean(video)] = true
		res.Imported++
	}
	s.setLastUnmatched(res.Unmatched)
	return res, nil
}

// importMovieFile catalogs one on-disk video as the given TMDB movie — by default
// unmonitored with no quality profile, since Arrmada is only adopting an existing file.
func (s *Service) importMovieFile(ctx context.Context, video string, tmdbID int, opts ScanOptions) (Movie, error) {
	details, err := s.meta.GetMovie(ctx, tmdbID)
	if err != nil {
		return Movie{}, err
	}
	profile := strings.TrimSpace(opts.QualityProfile)
	if profile == "" {
		profile = "n/a"
	}
	created, err := s.repo.Create(ctx, Movie{
		TMDBID: details.TMDBID, IMDBID: details.IMDBID, Title: details.Title, Year: details.Year,
		Overview: details.Overview, PosterURL: details.PosterURL, Runtime: details.Runtime, Status: details.Status,
		Monitored: opts.Monitor, QualityProfile: profile, Extra: extraFrom(details),
	})
	if err != nil {
		return Movie{}, err
	}
	_ = s.setDefaultFile(ctx, created.ID, video)
	_ = s.repo.AddEvent(ctx, created.ID, "imported", "Found during library scan: "+filepath.Base(video))
	s.log.Info("library scan: imported", "title", details.Title, "year", details.Year)
	// For the UI only, and without a title: a scan adopting a thousand existing files is
	// not a thousand "Imported" alerts. No outbox row either — Convert's nightly sweep
	// indexes new paths, and queueing Subtitles for a whole adopted library is the
	// 6-hourly sweep's call, not a scan's.
	s.publish("movie.downloaded", map[string]any{"id": created.ID, "version_id": int64(0), "path": video, "source": "scan"})
	created.HasFile, created.MovieFilePath = true, video
	return created, nil
}

// attachExisting gives a film already in the library, with no file, the file found on
// disk: read once, recorded as its default file with a 'detected' event, and announced
// like an import (the outbox row lets Convert and Subtitles index it, and tells whoever
// requested the film that it's ready — it was wanted, so that's news).
func (s *Service) attachExisting(ctx context.Context, m Movie, video, eventPrefix string) error {
	media := mediaJSONOf(s.probeTrack(video))
	err := s.repo.inTx(ctx, func(tx *sql.Tx, r *Repo) error {
		// Re-check inside the transaction: an import may have landed since the scan read it.
		cur, err := r.Get(ctx, m.ID)
		if err != nil {
			return err
		}
		if cur.HasFile {
			return fmt.Errorf("%s already has a file (%s)", cur.Title, filepath.Base(cur.MovieFilePath))
		}
		if err := setDefaultFileIn(ctx, r, m.ID, video, media); err != nil {
			return err
		}
		if err := r.ClearConvertedFrom(ctx, m.ID, 0); err != nil {
			return err
		}
		_ = r.AddEvent(ctx, m.ID, "detected", eventPrefix+filepath.Base(video))
		return s.enqueue(ctx, tx, outbox.TopicMovieImported,
			outbox.MovieImported{MovieID: m.ID, VersionID: 0, Path: video}, movieKey(m.ID))
	})
	if err != nil {
		return err
	}
	s.log.Info("library scan: attached file to film in library", "title", m.Title, "path", video)
	s.publish("movie.downloaded", map[string]any{"id": m.ID, "version_id": int64(0), "path": video, "title": m.Title})
	return nil
}

// ImportFolderAs catalogs a specific library folder as the chosen TMDB movie —
// the manual pick for a folder the scan couldn't confidently identify. A film already in
// the library without a file gets this one attached; one that has a file says which.
func (s *Service) ImportFolderAs(ctx context.Context, rootOverride, folder string, tmdbID int) error {
	root := rootOverride
	if root == "" {
		root = s.libRoot()
	}
	video, _, err := library.FindVideo(filepath.Join(root, folder))
	if err != nil || video == "" {
		return fmt.Errorf("no video file found in %q", folder)
	}
	m, err := s.repo.GetByTMDB(ctx, tmdbID)
	switch {
	case err == nil && m.HasFile && filepath.Clean(m.MovieFilePath) == filepath.Clean(video):
		// Already this film's file: nothing to do.
	case err == nil && m.HasFile:
		return fmt.Errorf("%w with %s", ErrExists, filepath.Base(m.MovieFilePath))
	case err == nil:
		if err := s.attachExisting(ctx, m, video, "Attached from the library folder: "); err != nil {
			return err
		}
	case errors.Is(err, ErrNotFound):
		if _, err := s.importMovieFile(ctx, video, tmdbID, ScanOptions{}); err != nil {
			return err
		}
	default:
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

// SetMonitored toggles monitoring.
func (s *Service) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	return s.repo.SetMonitored(ctx, id, monitored)
}

// SetQualityProfile changes a movie's quality profile. A real change ends the default
// file's upgrade hold.
func (s *Service) SetQualityProfile(ctx context.Context, id int64, profile string) error {
	return s.repo.SetQualityProfile(ctx, id, profile)
}

// HoldUpgrades keeps the given movies' default files and extra tracks' files out of
// profile-driven upgrades ("keep existing files"), returning how many rows it held.
func (s *Service) HoldUpgrades(ctx context.Context, movieIDs, versionIDs []int64) (movies, versions int, err error) {
	return s.repo.HoldUpgrades(ctx, movieIDs, versionIDs)
}

// ResumeUpgrades ends the upgrade hold on a movie and all its tracks, returning how many
// were held. The movie must exist.
func (s *Service) ResumeUpgrades(ctx context.Context, id int64) (int, error) {
	if _, err := s.repo.Get(ctx, id); err != nil {
		return 0, err
	}
	n, err := s.repo.ResumeUpgrades(ctx, id)
	if err == nil && n > 0 {
		_ = s.repo.AddEvent(ctx, id, "upgrades.resumed", "Upgrades resumed")
	}
	return n, err
}

// ErrWorseQuality is returned when an automatic import would replace an existing
// file with a lower-resolution one; the existing file is kept untouched.
var ErrWorseQuality = fmt.Errorf("import is lower quality than the existing file")

// MarkImported records that a movie now has a file (called by the import
// pipeline once a download for it lands). If the movie already had a different
// file, the old one is deleted from disk first — an upgrade replaces, it doesn't
// accumulate (multi-version is a separate, opt-in feature). A replacement whose
// resolution is LOWER than the existing file's is refused with ErrWorseQuality.
func (s *Service) MarkImported(ctx context.Context, id int64, path, sourceRelease string) error {
	return s.markImported(ctx, id, path, sourceRelease, false)
}

// MarkImportedManual is MarkImported for user-driven imports: the user picked
// this file deliberately, so the worse-quality replacement gate does not apply.
func (s *Service) MarkImportedManual(ctx context.Context, id int64, path, sourceRelease string) error {
	return s.markImported(ctx, id, path, sourceRelease, true)
}

func (s *Service) markImported(ctx context.Context, id int64, path, sourceRelease string, manual bool) error {
	// The database rows (with the default track's cached media info) are all routing
	// and the quality gate need; probing every track's file here cost one ffprobe per
	// track on every import.
	versions, err := s.VersionRows(ctx, id)
	if err != nil {
		return err
	}
	// The one read of the new file: it routes the file to its track, and its facts and size
	// are what the track caches. Probed before any transaction opens — a probe can take
	// seconds and every other writer waits on an open transaction.
	info := s.probeTrack(path)
	target := s.routeVersion(ctx, versions, trackResolution(info, path))

	// Upgrade is scoped to the target version: replacing the 1080p track's file
	// never touches the 4K track's file.
	upgrade := false
	if target.HasFile && target.FilePath != "" && target.FilePath != path {
		// Quality gate (auto imports only): never replace a file with a KNOWN
		// lower resolution — a mislabeled or wrongly-routed 720p grab must not
		// clobber the 2160p already on disk. Unknown resolutions can't be judged,
		// so they pass through (equal/higher always replaces, as before).
		if !manual {
			newRes := importResolution(path, sourceRelease)
			oldRes := existingResolution(target)
			if newRes != parser.ResUnknown && oldRes != parser.ResUnknown &&
				parser.ResolutionRank(newRes) < parser.ResolutionRank(oldRes) {
				s.log.Warn("refusing to replace file with lower-resolution import — keeping existing",
					"movie_id", id, "version", target.Label,
					"existing", string(oldRes), "existing_path", target.FilePath,
					"incoming", string(newRes), "incoming_path", path)
				return fmt.Errorf("%w (%s < %s)", ErrWorseQuality, newRes, oldRes)
			}
		}
		// The new file and the other versions' files keep their subtitles.
		keep := append(otherFiles(versions, target.FilePath), path)
		if err := s.removeFile(target.FilePath, keep); err != nil {
			// The new file is already in place, so failing the import would only strand
			// it. Keep the old file where it is instead of deleting it for good, and say so.
			s.log.Warn("upgrade: the recycle bin refused the old file — kept it on disk",
				"movie_id", id, "old", target.FilePath, "err", err)
			_ = s.repo.AddEvent(ctx, id, "file.kept", "Old file kept: the recycle bin refused it ("+err.Error()+")")
		} else {
			s.log.Info("replaced older file on upgrade", "movie_id", id, "version", target.Label, "old", target.FilePath, "new", path)
		}
		upgrade = true
	}

	var size int64
	if info != nil && !info.Missing {
		size = info.SizeBytes
	}
	media := mediaJSONOf(info)

	event, detail := "imported", "Imported "+filepath.Base(path)
	if upgrade {
		event, detail = "upgraded", "Upgraded to "+filepath.Base(path)
	}
	if !target.IsDefault {
		detail += " (" + target.Label + ")"
	}
	// Attaching the same file again (a retried attach, the same file imported by hand) is
	// not news for the timeline; the side effects below still run, and they're idempotent.
	again := target.HasFile && target.FilePath == path

	// The file record and the outbox row land together: once the movie reads as
	// downloaded, Convert, Subtitles and the requester are certain to hear about it. If
	// anything fails nothing is written, and the import's attach is retried.
	err = s.repo.inTx(ctx, func(tx *sql.Tx, r *Repo) error {
		if target.IsDefault {
			if err := setDefaultFileIn(ctx, r, id, path, media); err != nil {
				return err
			}
			if sourceRelease != "" {
				_ = r.SetSourceRelease(ctx, id, sourceRelease)
			}
		} else {
			if err := r.SetVersionFile(ctx, target.ID, path, size, media); err != nil {
				return err
			}
			if sourceRelease != "" {
				_ = r.SetVersionSourceRelease(ctx, target.ID, sourceRelease)
			}
		}
		// A new file: whatever an earlier one was before Convert shrank it says nothing
		// about this one. Convert's own path changes go through RepointMovieFile and keep it.
		if err := r.ClearConvertedFrom(ctx, id, target.ID); err != nil {
			return err
		}
		if !again {
			_ = r.AddEvent(ctx, id, event, detail)
			// A new file ends this track's upgrade hold: the hold kept the file it had, and
			// that file is gone. (A convert or rename repoints instead, and keeps it.)
			if err := r.clearHold(ctx, id, target.ID); err != nil {
				return err
			}
		}
		return s.enqueue(ctx, tx, outbox.TopicMovieImported,
			outbox.MovieImported{MovieID: id, VersionID: target.ID, Path: path, Upgrade: upgrade}, movieKey(id))
	})
	if err != nil {
		return err
	}

	m, getErr := s.repo.Get(ctx, id)
	ev := map[string]any{"id": id, "version_id": target.ID, "path": path, "upgrade": upgrade}
	if upgrade {
		ev["old_path"] = target.FilePath
	}
	if getErr == nil {
		ev["title"] = m.Title
	}
	s.publish("movie.downloaded", ev)

	// Write Plex/Jellyfin-readable metadata into the movie folder — off the
	// caller's goroutine, because artwork downloads can take seconds and the
	// import pipeline shouldn't stall on them. Safe to detach: the helpers only
	// write sidecar files and do independent HTTP fetches (no shared mutable
	// state), and the movie snapshot is captured before the goroutine starts.
	// The DB updates above already happened synchronously.
	if getErr == nil {
		dir := filepath.Dir(path)
		safego.Go(s.log, "movies: write library metadata", func() {
			// The caller's ctx may be cancelled as soon as it returns; the sidecar
			// work should still finish, just not run forever.
			bctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			s.writeLibraryMetadata(bctx, dir, m)
		})
	}
	return nil
}

// importResolution is the resolution the NEW file claims: the release name it
// was grabbed from, falling back to the filename.
func importResolution(path, sourceRelease string) parser.Resolution {
	if sourceRelease != "" {
		if r := parser.Parse(sourceRelease).Resolution; r != parser.ResUnknown {
			return r
		}
	}
	return parser.Parse(filepath.Base(path)).Resolution
}

// existingResolution is the resolution of a version's current file, tried in
// order of trustworthiness: the stored source release, probed/parsed file info,
// then the filename (mirrors the automation coordinator's upgrade baseline).
func existingResolution(v Version) parser.Resolution {
	if sr := strings.TrimSpace(v.SourceRelease); sr != "" {
		if r := parser.Parse(sr).Resolution; r != parser.ResUnknown {
			return r
		}
	}
	if v.File != nil {
		if v.File.Resolution != "" { // real (ffprobe) resolution when available
			if r := parser.Resolution(v.File.Resolution); parser.ResolutionRank(r) > 0 {
				return r
			}
		}
		if v.File.Quality != "" { // e.g. "1080p BluRay"
			if r := parser.Parse(v.File.Quality).Resolution; r != parser.ResUnknown {
				return r
			}
		}
	}
	if v.FilePath != "" {
		return parser.Parse(filepath.Base(v.FilePath)).Resolution
	}
	return parser.ResUnknown
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

// SetConvertedFrom records what the file at path was before Convert shrank it, on every
// track of the movie holding that file: its recorded release and sizeBytes (0 = unknown).
// Convert calls it just before RepointMovieFile, while the release still names the
// original codec. A track that already has a baseline keeps its first one.
func (s *Service) SetConvertedFrom(ctx context.Context, movieID int64, path string, sizeBytes int64) error {
	return s.repo.SetConvertedFromForPath(ctx, movieID, path, sizeBytes)
}

// RepointMovieFile updates the movie's file records for a PATH-ONLY change (convert,
// rename): every version whose file sits at oldPath follows to newPath, and the
// recorded source_release is preserved — optionally gaining a codec token.
//
// This exists so Convert does NOT go through MarkImported: stamping source_release
// with a synthetic tag there destroyed the upgrade baseline (the tag parsed to
// nothing, every release outscored it, and automation re-downloaded the exact
// release the file came from — an endless download → re-encode loop), and the
// import quality gate could even refuse the update, leaving the record pointing at
// a recycled file. codecToken (e.g. "x265" after an HEVC conversion) keeps
// bitrate-upgrade scoring honest: without it the halved file was costed at the old
// codec's efficiency and looked like a starving encode begging to be replaced. The token
// replaces the old codec in place (parser.RestampCodec) rather than being appended, so
// the name still reads as the new codec and its "-GROUP" still parses.
func (s *Service) RepointMovieFile(ctx context.Context, movieID int64, oldPath, newPath string, size int64, codecToken string) (int, error) {
	// The pre-conversion baseline is NOT touched here: a repoint is the same content under
	// a new path or codec, and SetConvertedFrom recorded what it was before.
	versions, err := s.VersionRows(ctx, movieID)
	if err != nil {
		return 0, err
	}
	restamp := func(rel string) string {
		if rel == "" || codecToken == "" {
			return rel
		}
		codec := parser.Parse("x " + codecToken).Codec
		if parser.Parse(rel).Codec == codec {
			return rel // already reads as the new codec
		}
		return parser.RestampCodec(rel, codec)
	}
	n := 0
	var moved []int64 // version ids whose record changed path just now
	defer func() {
		// Convert reindexes this movie itself after a swap; the change still goes out so
		// Subtitles and anything watching paths (the UI, a Plex scan) follow the new name.
		if len(moved) == 0 || oldPath == newPath {
			return
		}
		s.enqueueChange(ctx, outbox.MovieChanged{MovieID: movieID, VersionID: moved[0], Change: outbox.ChangeRenamed, OldPath: oldPath, Path: newPath})
		for _, vid := range moved {
			s.publish("movie.renamed", map[string]any{"id": movieID, "version_id": vid, "old_path": oldPath, "new_path": newPath})
		}
	}()
	for _, v := range versions {
		if !v.HasFile || v.FilePath != newPath && v.FilePath != oldPath {
			continue
		}
		if v.FilePath == newPath {
			n++ // already repointed (retry after a partial failure)
			continue
		}
		// The cached media info follows the file: carried over unread for a pure rename
		// (same size and mtime), read once for a changed file. Left describing the original,
		// the upgrade sweep costed a converted file at its old size, and Convert's analysis
		// of it (trusted only for a matching size) was never used.
		info := s.movedMedia(v.File, newPath)
		if v.IsDefault {
			if err := s.setDefaultFileWith(ctx, movieID, newPath, info); err != nil {
				return n, err
			}
			if upd := restamp(v.SourceRelease); upd != v.SourceRelease {
				_ = s.repo.SetSourceRelease(ctx, movieID, upd)
			}
		} else {
			vsize := size
			if vsize <= 0 && info != nil && !info.Missing {
				vsize = info.SizeBytes
			}
			if err := s.repo.SetVersionFile(ctx, v.ID, newPath, vsize, mediaJSONOf(info)); err != nil {
				return n, err
			}
			if upd := restamp(v.SourceRelease); upd != v.SourceRelease {
				_ = s.repo.SetVersionSourceRelease(ctx, v.ID, upd)
			}
		}
		moved = append(moved, v.ID)
		n++
	}
	if n > 0 {
		s.log.Info("movie: file repointed", "movie_id", movieID, "new", newPath, "records", n)
	}
	return n, nil
}

// VersionRows returns all tracks for a movie — the default (the movie row) followed by
// any extra version tracks — from the database alone: no stat, no directory listing, no
// ffprobe. The default track's File is the media info cached on the movie row (nil when
// none is cached yet); extra tracks carry no File, only their recorded SizeBytes.
//
// This is what everything except the movie detail page uses. The periodic sweeps
// (search-missing, RSS, upgrades, stall detection) used to go through VersionsLive, which
// forked ffprobe for every file in the library every five minutes.
func (s *Service) VersionRows(ctx context.Context, id int64) ([]Version, error) {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []Version{defaultVersionRow(m)}
	extras, err := s.repo.ListVersions(ctx, id)
	if err != nil {
		return out, nil // degrade to default-only, as VersionsLive does
	}
	return append(out, extras...), nil
}

// LibraryVersionRows is VersionRows for every movie at once (keyed by movie id), in two
// queries however big the library: the whole-library passes (a profile edit's dry run)
// use it rather than asking per movie.
func (s *Service) LibraryVersionRows(ctx context.Context) (map[int64][]Version, []Movie, error) {
	all, err := s.repo.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	extras, err := s.repo.ListAllVersions(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := make(map[int64][]Version, len(all))
	for _, m := range all {
		out[m.ID] = append([]Version{defaultVersionRow(m)}, extras[m.ID]...)
	}
	return out, all, nil
}

// defaultVersionRow is the default track as the database has it: the movie row, with the
// cached media info as its File (nil when none is cached yet).
func defaultVersionRow(m Movie) Version {
	def := Version{
		ID: 0, IsDefault: true, Label: "Default",
		QualityProfile: m.QualityProfile, Monitored: m.Monitored,
		HasFile: m.HasFile, FilePath: m.MovieFilePath, SourceRelease: m.SourceRelease, UpgradeHold: m.UpgradeHold,
		ConvertedFromRelease: m.ConvertedFromRelease, ConvertedFromSize: m.ConvertedFromSize,
	}
	if m.HasFile && m.File != nil {
		f := *m.File
		def.File = &f
		def.SizeBytes = f.SizeBytes
	}
	return def
}

// HasExtraVersions reports whether a movie has any opt-in extra tracks.
func (s *Service) HasExtraVersions(ctx context.Context, id int64) bool {
	extras, err := s.repo.ListVersions(ctx, id)
	return err == nil && len(extras) > 0
}

// routeVersion picks which version an imported file belongs to, by matching the
// file's resolution (trackResolution: the probed one, else the filename's) against each
// version's quality profile.
//
// A file whose resolution can't be determined routes to the DEFAULT track
// explicitly — never to whichever extra track happens to score least badly. A
// file with a KNOWN resolution never lands on a track whose profile forbids it
// (-1) while another track accepts it; if every track forbids it, it falls back
// to the default track (whose file the MarkImported quality gate still protects).
func (s *Service) routeVersion(ctx context.Context, versions []Version, res string) Version {
	if res == "" {
		return versions[0] // unknown resolution → default track
	}
	best := versions[0] // default
	bestScore := s.matchScore(ctx, versions[0].QualityProfile, res)
	for _, v := range versions[1:] {
		if sc := s.matchScore(ctx, v.QualityProfile, res); sc > bestScore {
			best, bestScore = v, sc
		}
	}
	if bestScore < 0 {
		return versions[0] // every track forbids it → default track
	}
	return best
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

// setDefaultFile records the default file path AND caches its media info — the one read
// of that file — so neither the table nor the detail page has to probe it again.
func (s *Service) setDefaultFile(ctx context.Context, id int64, path string) error {
	return s.setDefaultFileWith(ctx, id, path, s.probeTrack(path))
}

// setDefaultFileWith records the default file with media info already read (or moved
// along with the file by movedMedia).
func (s *Service) setDefaultFileWith(ctx context.Context, id int64, path string, info *MovieFile) error {
	media := mediaJSONOf(info)
	return s.repo.inTx(ctx, func(_ *sql.Tx, r *Repo) error {
		return setDefaultFileIn(ctx, r, id, path, media)
	})
}

// backfillRetry is how long a movie whose probe was tried waits before the list asks for
// it again. A file ffprobe can't read stays "stale"; without the wait every poll of the
// Movies grid would start another backfill for it.
const backfillRetry = time.Hour

// StaleMediaPending filters ids down to the ones no backfill has tried recently.
func (s *Service) StaleMediaPending(ids []int64) []int64 {
	now := time.Now()
	var out []int64
	for _, id := range ids {
		if at, ok := s.backfillTried.Load(id); ok && now.Sub(at.(time.Time)) < backfillRetry {
			continue
		}
		out = append(out, id)
	}
	return out
}

// BackfillStaleMedia probes the given movies' files and caches their media info — the
// Movies grid's lazy backfill, run as one job rather than a goroutine per movie per poll.
// At most cap(probeSem) probes run at once (EnsureMedia takes the slot). It returns how
// many movies it looked at.
func (s *Service) BackfillStaleMedia(ctx context.Context, ids []int64) int {
	work := make(chan int64)
	var wg sync.WaitGroup
	for i := 0; i < cap(s.probeSem); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				s.EnsureMedia(ctx, id)
			}
		}()
	}
	n := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		s.backfillTried.Store(id, time.Now())
		work <- id
		n++
	}
	close(work)
	wg.Wait()
	return n
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

// sidecarSubtitles lists the subtitle files paired with the movie file — named for it
// ("<base>.srt", "<base>.en.srt"), which is what Plex shows with it.
func sidecarSubtitles(moviePath string) []string {
	return baseNames(library.PairedSidecars(moviePath))
}

// orphanSubtitles lists the subtitles in the movie's folder that pair with no video there:
// left over from an old name or release, and shown by Plex for nothing.
func orphanSubtitles(moviePath string) []string {
	return baseNames(library.OrphanSidecars(filepath.Dir(moviePath)))
}

func baseNames(paths []string) []string {
	var out []string
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
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
	adopted := s.rescan(ctx, &m)
	// 3. Re-read the file once so cached media info (codec/resolution/…) stays truthful —
	// unless the rescan just adopted it, which read it already.
	if m.HasFile && m.MovieFilePath != "" && !adopted {
		_ = s.setDefaultFile(ctx, id, m.MovieFilePath)
	}
	_ = s.repo.AddEvent(ctx, id, "refreshed", "Refreshed metadata and rescanned disk")
	return s.repo.Get(ctx, id)
}

// rescan reconciles a movie's file record against its library folder in place, reporting
// whether it adopted (and so read) a file.
func (s *Service) rescan(ctx context.Context, m *Movie) (adopted bool) {
	folder := s.movieFolder(*m)
	found, _, err := library.FindVideo(folder)
	switch {
	case err == nil && found != "":
		if found != m.MovieFilePath {
			old := m.MovieFilePath
			if err := s.setDefaultFile(ctx, m.ID, found); err != nil {
				s.log.Warn("rescan: couldn't record the file on disk", "movie", m.Title, "path", found, "err", err)
				return false
			}
			adopted = true
			m.MovieFilePath, m.HasFile = found, true
			s.log.Info("rescan: adopted file on disk", "movie", m.Title, "path", found)
			_ = s.repo.AddEvent(ctx, m.ID, "detected", "Found file on disk: "+filepath.Base(found))
			s.enqueueChange(ctx, outbox.MovieChanged{MovieID: m.ID, Change: outbox.ChangeDetected, OldPath: old, Path: found})
			if old != "" {
				s.publish("movie.renamed", map[string]any{"id": m.ID, "version_id": int64(0), "old_path": old, "new_path": found})
			} else {
				s.publish("movie.downloaded", map[string]any{"id": m.ID, "version_id": int64(0), "path": found, "source": "scan"})
			}
		}
	default:
		// No video in the folder. If we thought we had one, clear it.
		if m.HasFile {
			old := m.MovieFilePath
			if err := s.repo.ClearFile(ctx, m.ID); err != nil {
				s.log.Warn("rescan: couldn't clear the missing file", "movie", m.Title, "err", err)
				return false
			}
			m.HasFile, m.MovieFilePath = false, ""
			s.log.Info("rescan: tracked file no longer on disk", "movie", m.Title)
			_ = s.repo.AddEvent(ctx, m.ID, "missing", "Tracked file no longer on disk")
			s.enqueueChange(ctx, outbox.MovieChanged{MovieID: m.ID, Change: outbox.ChangeFileDeleted, Path: old})
			s.publish("movie.file_deleted", map[string]any{"id": m.ID, "version_id": int64(0), "path": old})
		}
	}
	return adopted
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
//
// The walk is bounded: it stops when ctx ends (returning what it found with ctx.Err()),
// after maxResults candidates, or after library.ListMaxVisited entries, and truncated
// says the list was cut short. Listing a library root used to walk the whole array, and
// kept going after the browser had gone.
func (s *Service) ManualImportCandidates(ctx context.Context, dir string, maxResults int) ([]ImportCandidate, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	var out []ImportCandidate
	visited, truncated := 0, false
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if visited++; visited > library.ListMaxVisited {
			truncated = true
			return fs.SkipAll
		}
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
		if maxResults > 0 && len(out) >= maxResults {
			truncated = true
			return fs.SkipAll
		}
		out = append(out, ImportCandidate{
			Path:      p,
			Filename:  filepath.Base(p),
			SizeBytes: fi.Size(),
			Quality:   qualityLabel(p),
		})
		return nil
	})
	return out, truncated, err
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
	// use it as the release name for upgrade scoring. Manual: the user picked this
	// file on purpose, so the worse-quality gate must not refuse it.
	return s.MarkImportedManual(ctx, id, res.TargetPath, filepath.Base(res.SourcePath))
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
	// Through the importer's Move, not os.Rename: a bare rename replaces whatever already
	// sits at the target, and Move refuses to.
	if err := s.imp.Move(m.MovieFilePath, target); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	s.imp.MoveEpisodeSubs(m.MovieFilePath, target) // carry sidecar subtitles along
	if newDir := filepath.Dir(target); newDir != oldDir {
		s.imp.RemoveDirIfEmpty(oldDir) // the movie moved to a renamed folder; drop the empty old one
	}
	// The same file under a new name: its cached facts follow it unread.
	media := mediaJSONOf(s.movedMedia(m.File, target))
	err = s.repo.inTx(ctx, func(tx *sql.Tx, r *Repo) error {
		if err := setDefaultFileIn(ctx, r, id, target, media); err != nil {
			return err
		}
		_ = r.AddEvent(ctx, id, "renamed", filepath.Base(m.MovieFilePath)+" → "+filepath.Base(target))
		return s.enqueue(ctx, tx, outbox.TopicMovieChanged,
			outbox.MovieChanged{MovieID: id, Change: outbox.ChangeRenamed, OldPath: m.MovieFilePath, Path: target}, movieKey(id))
	})
	if err != nil {
		return err
	}
	s.publish("movie.renamed", map[string]any{"id": id, "version_id": int64(0), "old_path": m.MovieFilePath, "new_path": target})
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

// Matcher indexes a library snapshot once and returns a title/year matcher, so a caller
// resolving many releases in one pass (the downloads feed) does a single scan instead of
// reloading and re-normalizing the whole movies table per release.
func (s *Service) Matcher(all []Movie) func(title string, year int) (Movie, bool) {
	byTitle := make(map[string][]Movie, len(all))
	for _, m := range all {
		k := normalizeTitle(m.Title)
		byTitle[k] = append(byTitle[k], m)
	}
	return func(title string, year int) (Movie, bool) {
		for _, m := range byTitle[normalizeTitle(title)] {
			if year == 0 || m.Year == 0 || abs(m.Year-year) <= 1 {
				return m, true
			}
		}
		return Movie{}, false
	}
}

// Match finds the library movie a parsed release belongs to, comparing
// normalized titles and (when the release has a year) the year within ±1.
//
// A release with no parseable year only matches when exactly ONE library movie
// shares the title key: with two Cinderellas in the library, a "Cinderella.1080p"
// release must stay unmatched rather than attach to whichever was added last.
func (s *Service) Match(ctx context.Context, title string, year int) (Movie, bool) {
	all, err := s.repo.List(ctx)
	if err != nil {
		return Movie{}, false
	}
	want := normalizeTitle(title)
	var candidates []Movie
	for _, m := range all {
		if normalizeTitle(m.Title) != want {
			continue
		}
		if year != 0 {
			if m.Year == 0 || abs(m.Year-year) <= 1 {
				return m, true
			}
			continue
		}
		candidates = append(candidates, m)
	}
	switch len(candidates) {
	case 0:
		return Movie{}, false
	case 1:
		return candidates[0], true
	default:
		if s.log != nil {
			names := make([]string, 0, len(candidates))
			for _, c := range candidates {
				names = append(names, fmt.Sprintf("%s (%d)", c.Title, c.Year))
			}
			s.log.Debug("year-less release title is ambiguous — not matching",
				"title", title, "candidates", strings.Join(names, ", "))
		}
		return Movie{}, false
	}
}

// normalizeTitle keys a title for matching. It MUST agree with the search side
// (automation's titleKey), which also uses parser.TitleKey: if search-side matching
// accepts a release the import-side can't re-match (accents, "&" vs "and"), the
// grab stays pending forever and the sweep re-grabs a duplicate.
func normalizeTitle(s string) string { return parser.TitleKey(s) }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
