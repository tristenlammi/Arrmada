// Package library turns finished downloads into an organized media library:
// it finds the media file, derives a clean name from the parsed release, and
// hardlinks (or copies) it into the library root. This is the module-agnostic
// import engine; media modules refine naming/targets later.
package library

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/tristenlammi/arrmada/internal/extract"
	"github.com/tristenlammi/arrmada/internal/langs"
	"github.com/tristenlammi/arrmada/internal/parser"
)

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true, ".mov": true,
	".wmv": true, ".ts": true, ".mpg": true, ".mpeg": true, ".webm": true, ".flv": true,
}

var ebookExts = map[string]bool{
	".epub": true, ".mobi": true, ".azw3": true, ".azw": true, ".pdf": true,
	".cbz": true, ".cbr": true, ".fb2": true, ".djvu": true, ".lit": true,
}

var audiobookExts = map[string]bool{
	".m4b": true, ".m4a": true, ".mp3": true, ".aac": true, ".flac": true, ".ogg": true, ".opus": true,
}

func isEbook(p string) bool     { return ebookExts[strings.ToLower(filepath.Ext(p))] }
func isAudiobook(p string) bool { return audiobookExts[strings.ToLower(filepath.Ext(p))] }
func isBookFile(p string) bool  { return isEbook(p) || isAudiobook(p) }

// BookFileFormat returns the uppercase format tag for a file path (EPUB, M4B, MP3…).
func BookFileFormat(path string) string {
	return strings.ToUpper(strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."))
}

// IsAudiobookFile reports whether a path is an audiobook file (vs an ebook).
func IsAudiobookFile(p string) bool { return isAudiobook(p) }

// IsBookFile reports whether a path is an ebook or audiobook file, by its extension.
func IsBookFile(p string) bool { return isBookFile(p) }

// The manual-import listings' tests for one file, so anything that counts or previews
// what a listing will show (the folder picker) counts exactly the same files.
//
// ListedMovieVideo is a movie listing's: a video at least MinEpisodeVideoSize.
func ListedMovieVideo(p string, size int64) bool { return isVideo(p) && size >= MinEpisodeVideoSize }

// ListedEpisodeVideo is FindVideos': a movie listing's file that isn't a sample by name.
func ListedEpisodeVideo(p string, size int64) bool {
	return ListedMovieVideo(p, size) && !isSampleName(p)
}

// ListedBookFile is FindBookFiles': an ebook or audiobook file that isn't a sample. Book
// files have no size floor.
func ListedBookFile(p string, _ int64) bool { return isBookFile(p) && !isSampleName(p) }

// FoundFile is a media file discovered on disk (path + size). Shared by video, ebook,
// and audiobook discovery.
type FoundFile = FoundVideo

// Limits for a listing someone is browsing (manual import). Listing a library root used
// to walk every file on the array, and kept walking after the browser had gone; nobody
// reads past a few hundred rows, so stop there and say the list is cut short.
const (
	ListMaxResults = 500     // files returned
	ListMaxVisited = 100_000 // directory entries looked at, files and folders alike
)

// walkLimits counts a bounded walk. Zero limits mean unbounded.
type walkLimits struct {
	ctx                    context.Context
	maxResults, maxVisited int
	visited, results       int
	truncated              bool
}

// step is called for every entry. It returns ctx.Err() once the caller has gone, and
// fs.SkipAll (with truncated set) once too many entries have been looked at.
func (l *walkLimits) step() error {
	if err := l.ctx.Err(); err != nil {
		return err
	}
	l.visited++
	if l.maxVisited > 0 && l.visited > l.maxVisited {
		l.truncated = true
		return fs.SkipAll
	}
	return nil
}

// full reports whether another result would go over the cap; it marks the walk cut
// short, since that result exists.
func (l *walkLimits) full() bool {
	if l.maxResults > 0 && l.results >= l.maxResults {
		l.truncated = true
		return true
	}
	return false
}

// FindBookFiles returns every ebook + audiobook file at contentPath (no size floor).
func FindBookFiles(contentPath string) []FoundFile {
	out, _, _ := FindBookFilesCtx(context.Background(), contentPath, 0, 0)
	return out
}

// FindBookFilesCtx is FindBookFiles bounded for a listing: it stops when ctx ends
// (returning what it found and ctx.Err()), after maxResults files, or after maxVisited
// entries, and reports truncated when there was more. Zero limits mean unbounded.
func FindBookFilesCtx(ctx context.Context, contentPath string, maxResults, maxVisited int) ([]FoundFile, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	info, err := os.Stat(contentPath)
	if err != nil {
		return nil, false, nil
	}
	if !info.IsDir() {
		if isBookFile(contentPath) {
			return []FoundFile{{Path: contentPath, Size: info.Size()}}, false, nil
		}
		return nil, false, nil
	}
	lim := &walkLimits{ctx: ctx, maxResults: maxResults, maxVisited: maxVisited}
	var out []FoundFile
	werr := filepath.WalkDir(contentPath, func(p string, d os.DirEntry, err error) error {
		if serr := lim.step(); serr != nil {
			return serr
		}
		// Hidden folders inside a book are Arrmada's own (merge backups) or a system's
		// (.AppleDouble, .recycle): never book files.
		if err == nil && d.IsDir() && p != contentPath && SkipScanDir(d.Name()) {
			return filepath.SkipDir
		}
		if err != nil || d.IsDir() || !ListedBookFile(p, 0) {
			return nil
		}
		if fi, e := d.Info(); e == nil {
			if lim.full() {
				return fs.SkipAll
			}
			out = append(out, FoundFile{Path: p, Size: fi.Size()})
			lim.results++
		}
		return nil
	})
	return out, lim.truncated, ctxErr(werr)
}

// ctxErr keeps a cancellation from a walk and drops everything else, which the walks
// have always ignored (an unreadable sub-folder just isn't listed).
func ctxErr(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// FindEbooks returns only the ebook files at contentPath.
func FindEbooks(contentPath string) []FoundFile {
	var out []FoundFile
	for _, f := range FindBookFiles(contentPath) {
		if isEbook(f.Path) {
			out = append(out, f)
		}
	}
	return out
}

// BookImport is the result of placing a book edition into the library.
type BookImport struct {
	TargetPath string
	Format     string
	SizeBytes  int64
	FileCount  int
}

// BookFolder is a book's on-disk folder discovered during a library scan.
type BookFolder struct {
	Author     string
	Title      string
	Ebooks     []FoundFile
	Audiobooks []FoundFile
	// Dir is the folder itself, where a Calibre library keeps metadata.opf and cover.jpg.
	Dir string
}

// FindBookFolders walks the book roots (ebook + audiobook, once when they're the same
// folder) for folders containing ebook/audiobook files, grouped by author (parent folder)
// and title (the folder name). Used by the book library scan. The roots are resolved on
// every call, so a folder changed in Settings is scanned from then on.
func (im *Importer) FindBookFolders() []BookFolder {
	return im.FindBookFoldersIn(im.ebookDir(), im.audiobookDir())
}

// FindBookFoldersIn is FindBookFolders over explicit roots (empty/duplicate roots are skipped).
func (im *Importer) FindBookFoldersIn(roots ...string) []BookFolder {
	byDir := map[string]*BookFolder{}
	seen := map[string]bool{}
	{
		var uniq []string
		for _, r := range roots {
			if r != "" && !seen[r] {
				seen[r] = true
				uniq = append(uniq, r)
			}
		}
		roots = uniq
	}
	for _, root := range roots {
		hint := im.bookScanHint(root)
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			// Hidden folders are Arrmada's own (the merge backups under the audiobooks
			// root) or a system's: scanned as books they'd turn into bogus titles, or an
			// edition that vanishes when the backup is pruned.
			if err == nil && d.IsDir() && p != root && SkipScanDir(d.Name()) {
				return filepath.SkipDir
			}
			if err != nil || d.IsDir() || !isBookFile(p) {
				return nil
			}
			if isSampleName(p) {
				return nil
			}
			dir := filepath.Dir(p)
			bf := byDir[dir]
			if bf == nil {
				author := ""
				parent := filepath.Dir(dir)
				if parent != root && parent != "." {
					author = filepath.Base(parent)
				}
				title := filepath.Base(dir)
				author, title = hint.read(root, dir, author, title)
				if author == "" {
					// A flat folder named "Title by Author": the author is not part of
					// the title, and searching the two words together found guides to
					// the book rather than the book.
					title, author = splitTitleBy(title)
				}
				bf = &BookFolder{Author: author, Title: title, Dir: dir}
				byDir[dir] = bf
			}
			fi, _ := d.Info()
			var size int64
			if fi != nil {
				size = fi.Size()
			}
			if isAudiobook(p) {
				bf.Audiobooks = append(bf.Audiobooks, FoundFile{Path: p, Size: size})
			} else {
				bf.Ebooks = append(bf.Ebooks, FoundFile{Path: p, Size: size})
			}
			return nil
		})
	}
	out := make([]BookFolder, 0, len(byDir))
	for _, bf := range byDir {
		bf.Title = stripCalibreID(bf)
		out = append(out, *bf)
	}
	return out
}

// calibreIDRe is the " (123)" Calibre adds to every book folder: the book's id in its
// library ("Golden Son (42)").
var calibreIDRe = regexp.MustCompile(`^(.*\S)\s+\(\d+\)$`)

// stripCalibreID takes Calibre's id off a book folder's title, so the title lookup gets
// "Golden Son", not "Golden Son (42)". Only for a folder holding Calibre's metadata.opf:
// a cover.jpg or a "Title - Author" file name is common outside Calibre too, and stripping
// "Saga (2)" or "Mistborn (3)" to the bare series name would match volume 1 and merge
// the volumes into one wrong book.
func stripCalibreID(bf *BookFolder) string {
	m := calibreIDRe.FindStringSubmatch(bf.Title)
	if m == nil || bf.Dir == "" {
		return bf.Title
	}
	if fi, err := os.Lstat(filepath.Join(bf.Dir, "metadata.opf")); err == nil && fi.Mode().IsRegular() {
		return m[1]
	}
	return bf.Title
}

// titleByRe reads "<title> by <Author Name>" where the name is two to four capitalised
// words or initials — "Death by Chocolate" keeps its title.
var titleByRe = regexp.MustCompile(`^(.*\S)\s+(?i:by)\s+((?:[A-Z][\w.'’\-]*)(?:\s+[A-Z][\w.'’\-]*){1,3}|[A-Z]\.[\w.]*\s+[A-Z][\w'’\-]*)$`)

// splitTitleBy separates a folder name of the form "Title by Author"; anything else
// comes back as the title with no author.
func splitTitleBy(name string) (title, author string) {
	if m := titleByRe.FindStringSubmatch(strings.TrimSpace(name)); m != nil {
		return strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
	}
	return name, ""
}

// ImportBookEdition is ImportBook for a book known only by author and title.
func (im *Importer) ImportBookEdition(author, title string, files []FoundFile) (*BookImport, error) {
	return im.ImportBook(BookName{Author: author, Title: title}, files)
}

// ImportBook hardlinks a book edition into its folder under the book naming scheme
// (default "<root>/<Author>/<Title>/"). A single file is named by the file template
// (default "<Title>.ext"); a multi-file audiobook's files by the track template (default:
// each keeps its own name), numbered in play order; a multi-file ebook keeps its names.
// TargetPath is the file, or the folder for several. Format is the dominant extension.
func (im *Importer) ImportBook(n BookName, files []FoundFile) (*BookImport, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no book files to import")
	}
	audio := isAudiobook(files[0].Path)
	root := im.bookRootForFiles(files)
	if err := im.checkRoot(root); err != nil {
		return nil, err
	}
	nm := im.BookNamingFor(audio)
	dir := BookFolderPath(root, nm, audio, n, BookFileFormat(files[0].Path))
	if err := insideRoot(dir, root); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create book dir: %w", err)
	}
	cleanStaleTemps(dir)
	title := n.Title
	if len(files) == 1 {
		target := filepath.Join(dir, BookFileName(nm, audio, n, files[0].Path))
		method, err := im.linkOrCopy(files[0].Path, target)
		if err != nil {
			return nil, fmt.Errorf("import book: %w", err)
		}
		im.logImport(title, files[0].Path, target, method)
		var size int64
		if fi, _ := os.Stat(target); fi != nil {
			size = fi.Size()
		}
		return &BookImport{TargetPath: target, Format: BookFileFormat(target), SizeBytes: size, FileCount: 1}, nil
	}
	// Multi-file (e.g. an mp3 audiobook): the files go side by side in the book folder.
	// Per-file failures are logged and counted out of FileCount; only a total wipeout errors.
	names := make([]string, len(files))
	if audio {
		base := commonDir(files)
		rels := make([]string, len(files))
		for i, f := range files {
			rels[i] = relOrBase(base, f.Path)
		}
		names = BookTrackNames(nm, n, rels)
	} else {
		for i, f := range files {
			name := clean(strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path)))
			if name == "" {
				name = "Unknown" // a file called "...epub", say: never a hidden ".epub"
			}
			names[i] = name + filepath.Ext(f.Path)
		}
	}
	var total int64
	format := ""
	placed := 0
	var lastErr error
	for i, f := range files {
		target := filepath.Join(dir, names[i])
		method, err := im.linkOrCopy(f.Path, target)
		if err != nil {
			lastErr = err
			im.log.Warn("book file import failed", "title", title, "source", f.Path, "err", err)
			continue
		}
		placed++
		im.logImport(title, f.Path, target, method)
		if fi, _ := os.Stat(target); fi != nil {
			total += fi.Size()
		}
		format = BookFileFormat(f.Path)
	}
	if placed == 0 {
		return nil, fmt.Errorf("import book: no files could be placed: %w", lastErr)
	}
	return &BookImport{TargetPath: dir, Format: format, SizeBytes: total, FileCount: placed}, nil
}

// commonDir is the deepest folder every file is in.
func commonDir(files []FoundFile) string {
	if len(files) == 0 {
		return ""
	}
	dir := filepath.Dir(files[0].Path)
	for _, f := range files[1:] {
		for !pathWithin(f.Path, dir) {
			parent := filepath.Dir(dir)
			if parent == dir {
				return ""
			}
			dir = parent
		}
	}
	return dir
}

// pathWithin reports whether p is dir or inside it, on whole path segments.
func pathWithin(p, dir string) bool {
	p, dir = filepath.Clean(p), filepath.Clean(dir)
	if p == dir {
		return true
	}
	if !strings.HasSuffix(dir, string(filepath.Separator)) {
		dir += string(filepath.Separator)
	}
	return strings.HasPrefix(p, dir)
}

// PathWithin reports whether p is dir or inside it, on whole path segments.
func PathWithin(p, dir string) bool { return pathWithin(p, dir) }

// ErrOutsideRoot means a path built from metadata (a show, artist or album name) would
// land outside its library folder. clean() already turns a ".." name into "Unknown…";
// this is the last check before anything is created or placed, so no name that slips past
// it can make a folder or a file outside the library.
var ErrOutsideRoot = errors.New("the import target isn't inside its library folder")

// insideRoot refuses a target that isn't strictly inside root. It compares cleaned paths
// (so "a/../.." can't pass) without following symlinks: a library folder may well hold
// symlinks of the owner's, and those are theirs to point where they like.
func insideRoot(target, root string) error {
	if strings.TrimSpace(root) == "" || filepath.Clean(target) == filepath.Clean(root) || !pathWithin(target, root) {
		return fmt.Errorf("%w: %s", ErrOutsideRoot, target)
	}
	return nil
}

// relOrBase is p relative to base with "/" separators, or just its name when it isn't
// under base.
func relOrBase(base, p string) string {
	if base != "" {
		if r, err := filepath.Rel(base, p); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(r)
		}
	}
	return filepath.Base(p)
}

// BookLibraryFiles returns the book files present in a book's library folders (rescan):
// the ebook folder and the audiobook folder the naming scheme gives it, which may be under
// different roots (deduped when they're one folder). The formats are the editions'
// recorded ones, for a scheme that names {Format} in a folder ("" when there's none). n is
// named as the audiobook (its narrator, if typed); the ebook folder is named without it.
func (im *Importer) BookLibraryFiles(n BookName, ebookFormat, audioFormat string) []FoundFile {
	var out []FoundFile
	eDir := ""
	if r := im.ebookDir(); r != "" {
		en := n
		en.Narrator = "" // an ebook has no narrator, so its folder is named without one
		eDir = BookFolderPath(r, im.BookNamingFor(false), false, en, ebookFormat)
		out = FindBookFiles(eDir)
	}
	if r := im.audiobookDir(); r != "" {
		if aDir := BookFolderPath(r, im.BookNamingFor(true), true, n, audioFormat); aDir != eDir {
			out = append(out, FindBookFiles(aDir)...)
		}
	}
	return out
}

// BookEditionCanonical returns the path a single-file book edition belongs at under the
// naming scheme (default "<root>/<Author>/<Title>/<Title>.ext"), rooted by the file's
// kind; "" when that kind has no library folder.
func (im *Importer) BookEditionCanonical(n BookName, srcPath string) string {
	audio := isAudiobook(srcPath)
	root := im.BookRoot(audio)
	if root == "" {
		return ""
	}
	nm := im.BookNamingFor(audio)
	return filepath.Join(BookFolderPath(root, nm, audio, n, BookFileFormat(srcPath)), BookFileName(nm, audio, n, srcPath))
}

// BookDir is the folder a book edition of this kind belongs in under the naming scheme,
// or "" when that kind has no library folder.
func (im *Importer) BookDir(audio bool, n BookName, format string) string {
	root := im.BookRoot(audio)
	if root == "" {
		return ""
	}
	return BookFolderPath(root, im.BookNamingFor(audio), audio, n, format)
}

// BookRoot is the library folder audiobooks (audio) or ebooks are placed under.
func (im *Importer) BookRoot(audio bool) string {
	if audio {
		return im.audiobookDir()
	}
	return im.ebookDir()
}

// SetBookNaming installs the book naming provider (audiobook and ebook templates).
func (im *Importer) SetBookNaming(np BookNamingProvider) { im.bookNaming = np }

// BookNamingFor is the book naming in effect for audiobooks (audio) or ebooks, with a
// blank or broken template replaced by its default.
func (im *Importer) BookNamingFor(audio bool) BookNaming {
	var n BookNaming
	switch {
	case im.bookNaming != nil && audio:
		n = im.bookNaming.AudiobookNaming()
	case im.bookNaming != nil:
		n = im.bookNaming.EbookNaming()
	case audio:
		n = DefaultAudiobookNaming
	default:
		n = DefaultEbookNaming
	}
	return bookNamingOrDefault(n, audio)
}

var reIllegal = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// Default movie naming. Tokens: {title} {year} {quality} {resolution} {source}
// {edition} {plexedition} {codec} {group} {tmdbid} {imdbid}. Folder has no extension; the
// file gets one appended. {plexedition} is Plex's "{edition-Director's Cut}", or nothing.
const (
	DefaultMovieFolder = "{title} ({year})"
	DefaultMovieFile   = "{title} ({year}) - {quality}"
)

// Default series naming. The series folder holds season folders, which hold episode
// files. Tokens: {title} {year} {season} {season00} {episode} {episodetitle} {quality}
// {resolution} {source} {codec} {group}. {episode} is the SxxExx tag (a range for a
// double-episode file); {season00} is the zero-padded season for those who want it. A
// new series folder can also use {tmdbid} {tvdbid} {imdbid}.
const (
	DefaultSeriesFolder = "{title} ({year})"
	DefaultSeasonFolder = "Season {season}"
	DefaultEpisodeFile  = "{title} - {episode} - {episodetitle} - {quality}"
)

// Naming is the configurable movie naming scheme.
type Naming struct {
	Folder string
	File   string
}

// SeriesNaming is the configurable series naming scheme (folder / season folder /
// episode file formats).
type SeriesNaming struct {
	Folder       string
	SeasonFolder string
	EpisodeFile  string
	// YearWhenNeeded keeps the series folder's {year} only when the show needs it to be
	// told apart from another show with the same title ("Dark Matter (2024)" next to
	// "Dark Matter"); otherwise {year} is empty and the brackets around it drop out. It
	// only affects a NEW show folder: a show that has a folder keeps it, and so does a
	// rename. Off by default, which is the folder the template always gave.
	YearWhenNeeded bool
}

// NamingProvider supplies the (possibly user-customized) naming scheme at import
// time, so a settings change takes effect without restarting.
type NamingProvider interface {
	Naming() Naming
}

// SeriesNamingProvider supplies the series naming scheme, read fresh each import so a
// settings change applies live.
type SeriesNamingProvider interface {
	SeriesNaming() SeriesNaming
}

// Importer moves media into a library root.
type Importer struct {
	// root is what a type with no folder of its own falls back to. Only tests and
	// NewImporter's single-folder callers use it: SetRootFuncs clears it, because the
	// fallback used to be ARRMADA_LIBRARY_DIR — the managed volume, not a share the user
	// chose — and an import landing there looked like it had vanished.
	root         string
	roots        RootFuncs
	bin          Bin // when on, a replaced library file is recycled instead of overwritten
	log          *slog.Logger
	naming       NamingProvider       // nil → built-in defaults
	seriesNaming SeriesNamingProvider // nil → built-in defaults
	bookNaming   BookNamingProvider   // nil → built-in defaults
	musicNaming  MusicNamingProvider  // nil → built-in defaults (musicnaming.go)
	// epTitleFn resolves an episode's metadata title for naming ("" when unknown or
	// unset). Keyed by show name/year since the importer only knows the show by name.
	epTitleFn func(seriesTitle string, year, season, episode int) string
	// seriesIDsFn resolves a show's ids by name/year for a new series folder's id tokens.
	seriesIDsFn func(seriesTitle string, year int) SeriesIDs
	// yearNeededFn says whether a new series folder needs the show's year to tell it
	// apart from another show (SeriesNaming.YearWhenNeeded), given the folder's year-less
	// name. nil = always needed.
	yearNeededFn func(seriesTitle string, year int, yearless string) bool
	// copyReport hears this import's copies (Using); nil for every other importer.
	copyReport CopyReport
	// seasonDirs picks the season folder an episode import goes into (WithSeasonDirs);
	// nil = the scheme's, joining an existing one.
	seasonDirs SeasonDirChooser
}

// SeasonDirChooser is the season folder an episode of season goes into inside the show
// folder showDir, given the folder the scheme would use (def): def itself, another folder,
// or an error that refuses the import.
type SeasonDirChooser func(showDir string, season int, def string) (string, error)

// WithSeasonDirs is a copy of the importer whose episode imports put each episode in the
// season folder choose picks.
func (im *Importer) WithSeasonDirs(choose SeasonDirChooser) *Importer {
	if im == nil {
		return im
	}
	c := *im
	c.seasonDirs = choose
	return &c
}

// importTarget is episodeTargetIn for an import (never a rename): the scheme's path, with
// its season folder replaced by the chooser's when one is set.
func (im *Importer) importTarget(seriesFolder, title string, year, season, episode int, rel parser.Release, ext string) (string, error) {
	target := im.episodeTargetIn(seriesFolder, title, year, season, episode, rel, ext, false)
	if im.seasonDirs == nil {
		return target, nil
	}
	def := filepath.Dir(target)
	dir, err := im.seasonDirs(filepath.Dir(def), season, def)
	if err != nil {
		return "", err
	}
	if dir == "" {
		dir = def
	}
	return filepath.Join(dir, filepath.Base(target)), nil
}

// RootFuncs resolve each library folder at the moment it's needed. They're read on every
// import, so a folder changed in Settings → Library applies to the next import with no
// restart; an import that's already running finishes in the folder it started with.
// A nil func, or one that returns "", means that kind of media has no folder, and its
// imports fail with an error saying so.
type RootFuncs struct {
	Movie, TV, Ebook, Audiobook, Music func() string
}

// SetEpisodeTitleFunc installs a lookup so episode files are named with their
// metadata title ("Chuck - S03E06 - Chuck Versus the Nacho Sampler - 1080p BluRay").
func (im *Importer) SetEpisodeTitleFunc(f func(seriesTitle string, year, season, episode int) string) {
	im.epTitleFn = f
}

// SetSeriesIDsFunc installs a lookup of a show's ids by name and year, so a new series
// folder can be named "Show (2022) {tvdb-12345}". Unset (or no match) leaves the id tokens
// empty, and an empty Plex group drops out of the name.
func (im *Importer) SetSeriesIDsFunc(f func(seriesTitle string, year int) SeriesIDs) {
	im.seriesIDsFn = f
}

// SetSeriesYearNeededFunc installs the check behind SeriesNaming.YearWhenNeeded: whether
// a new show folder needs the show's year because another show shares its title. yearless
// is the folder's name without the year. It's asked only by NewSeriesFolderName, and only
// when that setting is on and the folder template has {year}. Unset, the year is kept.
func (im *Importer) SetSeriesYearNeededFunc(f func(seriesTitle string, year int, yearless string) bool) {
	im.yearNeededFn = f
}

// NewSeriesFolderName is the name of a NEW folder for the show: the series folder scheme,
// with the show's ids when the template asks for them, and — with "Add the year only when
// it's needed" on — without its year when no other show shares its title.
//
// It is the one place that decision is made. A caller placing a show with no folder names
// the folder once with it and passes that name down to every file of the placement (and
// to any check of where they'd go), so the check and the write can't disagree and a
// download never spans two folders. A show that already has a folder keeps it, and this
// isn't asked. Every doubt keeps the year — no check installed, no year known — because a
// year that wasn't needed costs nothing, while a missing one can put two shows in one
// folder.
func (im *Importer) NewSeriesFolderName(title string, year int) string {
	sn := im.seriesNamingScheme()
	t := cleanTitleLoose(title)
	if t == "" {
		t = "Unknown"
	}
	var ids SeriesIDs
	if hasIDToken(sn.Folder) {
		ids = im.seriesIDs(title, year)
	}
	withYear := newSeriesFolderName(sn, ids, t, year)
	if !sn.YearWhenNeeded || year <= 0 || !strings.Contains(sn.Folder, "{year}") || im.yearNeededFn == nil {
		return withYear
	}
	yearless := newSeriesFolderName(sn, ids, t, 0)
	if yearless == withYear || im.yearNeededFn(title, year, yearless) {
		return withYear
	}
	return yearless
}

// newSeriesFolderName is a new series folder's name: the scheme, with the show's ids when
// the template asks for them. An existing folder is always reused as it is instead. Year 0
// leaves {year} empty, and the brackets around it drop out.
func newSeriesFolderName(sn SeriesNaming, ids SeriesIDs, t string, year int) string {
	folder := renderName(seriesFolderFormat(sn.Folder, ids), map[string]string{
		"title": t, "year": yearToken(year),
	})
	if folder == "" {
		folder = t
	}
	return folder
}

func (im *Importer) seriesIDs(title string, year int) SeriesIDs {
	if im.seriesIDsFn == nil {
		return SeriesIDs{}
	}
	return im.seriesIDsFn(title, year)
}

// hasIDToken reports whether a template names one of the id tokens.
func hasIDToken(format string) bool {
	return strings.Contains(format, "{tmdbid}") || strings.Contains(format, "{tvdbid}") || strings.Contains(format, "{imdbid}")
}

// seriesFolderFormat fills a series template's id tokens. The Plex recommended
// "{tvdb-{tvdbid}}" falls back to "{tmdb-{tmdbid}}" for a show TVDB doesn't know, so the
// folder still carries an id Plex can match.
func seriesFolderFormat(format string, ids SeriesIDs) string {
	if ids.TVDB <= 0 && ids.TMDB > 0 {
		format = strings.ReplaceAll(format, "{tvdb-{tvdbid}}", "{tmdb-{tmdbid}}")
	}
	return strings.NewReplacer(
		"{tmdbid}", idToken(ids.TMDB),
		"{tvdbid}", idToken(ids.TVDB),
		"{imdbid}", clean(ids.IMDB),
	).Replace(format)
}

// SetRootFuncs makes the importer resolve each library folder live (see RootFuncs). It
// drops the NewImporter root as a fallback: a missing folder is an error, never a
// quiet write somewhere else.
func (im *Importer) SetRootFuncs(f RootFuncs) {
	im.roots = f
	im.root = ""
}

// SetRoots fixes each media type's folder (movies, TV, ebooks, audiobooks). An empty
// value falls back to the NewImporter root. Tests use it; the app uses SetRootFuncs.
func (im *Importer) SetRoots(movie, tv, ebook, audiobook string) {
	fixed := func(p string) func() string {
		if p == "" {
			return nil
		}
		return func() string { return p }
	}
	im.roots.Movie, im.roots.TV, im.roots.Ebook, im.roots.Audiobook = fixed(movie), fixed(tv), fixed(ebook), fixed(audiobook)
}

// rootFor resolves one folder: its func when set, otherwise the NewImporter root.
func (im *Importer) rootFor(fn func() string) string {
	if fn != nil {
		return strings.TrimSpace(fn())
	}
	return im.root
}

func (im *Importer) movieDir() string { return im.rootFor(im.roots.Movie) }
func (im *Importer) tvDir() string    { return im.rootFor(im.roots.TV) }

// movieRoot is the library folder track t belongs in: its own Root when the movie lives in
// one of several Movies folders, else the Movies folder.
func (im *Importer) movieRoot(t MovieTrack) string {
	if r := strings.TrimSpace(t.Root); r != "" {
		return r
	}
	return im.movieDir()
}

// InTV is this importer with its TV folder fixed to root, for a show that lives in one of
// several TV folders (internal/libfolders): every episode import, target and rescan
// through it places files under root. "" returns the importer itself. The copy shares the
// naming, bin and lookups; the importer keeps no other state, so it's safe to make one
// per import.
func (im *Importer) InTV(root string) *Importer {
	root = strings.TrimSpace(root)
	if im == nil || root == "" {
		return im
	}
	c := *im
	c.roots.TV = func() string { return root }
	return &c
}

// TVRoot is the TV library folder, read live.
func (im *Importer) TVRoot() string { return im.tvDir() }

// CheckTVRoot says why nothing should be written in the TV folder: none is set, or it isn't
// there (its disk or share is away). nil when it's there.
func (im *Importer) CheckTVRoot() error { return im.checkRoot(im.tvDir()) }

// MusicDir is where album folders are written.
func (im *Importer) MusicDir() string { return im.rootFor(im.roots.Music) }

func (im *Importer) ebookDir() string { return im.rootFor(im.roots.Ebook) }

// AudiobookRoot is the folder audiobooks are placed under.
func (im *Importer) AudiobookRoot() string { return im.audiobookDir() }

func (im *Importer) audiobookDir() string { return im.rootFor(im.roots.Audiobook) }

// bookRootForFiles picks the audiobook or ebook root from the files being
// imported (a single edition is one kind — the coordinator imports each kind
// separately).
func (im *Importer) bookRootForFiles(files []FoundFile) string {
	if len(files) > 0 && isAudiobook(files[0].Path) {
		return im.audiobookDir()
	}
	return im.ebookDir()
}

// NewImporter creates an importer whose every media type lands under root until
// SetRoots or SetRootFuncs says otherwise. The app passes "" and sets live folders.
func NewImporter(root string, log *slog.Logger) *Importer {
	return &Importer{root: root, log: log}
}

// SetRecycleDir points the importer at the recycle bin: when an import replaces an
// existing library file with different content, the old file is recycled first (so
// the replacement can hardlink) instead of being overwritten in place. Empty means the
// bin is switched off, and a replacement overwrites as before.
func (im *Importer) SetRecycleDir(dir string) { im.bin = SingleBin(dir) }

// SetBin is SetRecycleDir for any Bin, such as the per-library bins (RootBins).
func (im *Importer) SetBin(b Bin) { im.bin = b }

// SetNaming installs a naming provider (user-configurable folder/file formats).
func (im *Importer) SetNaming(np NamingProvider) { im.naming = np }

// SetSeriesNaming installs the series naming provider (folder / season folder /
// episode file formats).
func (im *Importer) SetSeriesNaming(np SeriesNamingProvider) { im.seriesNaming = np }

// seriesNamingScheme returns the series formats in effect, filling any blank with its
// built-in default.
func (im *Importer) seriesNamingScheme() SeriesNaming {
	n := SeriesNaming{}
	if im.seriesNaming != nil {
		n = im.seriesNaming.SeriesNaming()
	}
	if n.Folder == "" {
		n.Folder = DefaultSeriesFolder
	}
	if n.SeasonFolder == "" {
		n.SeasonFolder = DefaultSeasonFolder
	}
	if n.EpisodeFile == "" {
		n.EpisodeFile = DefaultEpisodeFile
	}
	return n
}

// movieNaming returns the folder and file formats in effect (defaults if unset).
func (im *Importer) movieNaming() Naming {
	if im.naming != nil {
		n := im.naming.Naming()
		if n.Folder == "" {
			n.Folder = DefaultMovieFolder
		}
		if n.File == "" {
			n.File = DefaultMovieFile
		}
		return n
	}
	return Naming{Folder: DefaultMovieFolder, File: DefaultMovieFile}
}

// MovieTrack is what one movie track's file is named from. It is the one name builder
// for movie files: the automatic import, a manual import and a rename all go through
// MovieTrackTarget with it, so a track's file is named the same way wherever it's placed.
type MovieTrack struct {
	Title string
	Year  int
	IDs   MovieIDs
	// Folder, when set, is the library folder the movie already lives in: an import keeps
	// the movie there instead of starting a second folder after a naming change. Only a
	// rename moves a movie to the scheme's folder. "" derives the folder from the scheme.
	Folder string
	// Root is the library folder the movie belongs to when the Movies library has more
	// than one (internal/libfolders): a new folder for it is made there. "" is the
	// importer's Movies folder — the default, and the only one before extra folders.
	Root string
	// Edition is the track's own edition ("Director's Cut"). It fills {edition} and
	// {plexedition} in place of whatever the release says, and when the scheme has neither
	// token it's added as Plex's {edition-…}, so a Director's Cut track never shares a file
	// name with the theatrical file next to it.
	Edition string
	// Label is the track's name. It only ever appears in a file name to tell this track's
	// file apart from another track's that would otherwise have the same name.
	Label string
	// Protected are the files the movie's other tracks own. None is ever a target, and none
	// is ever recycled or overwritten to make room.
	Protected []string
	// Own is the file this track records ("" for none), even one marked missing. An import
	// may replace it when it's at the target (an upgrade under the same name, through the
	// recycle bin); any other file already at the target is kept clear of (untrackedAt).
	Own string
}

// MovieIDs are a movie's ids, for {tmdbid} and {imdbid} — what Plex matches a folder
// named "Title (Year) {tmdb-12345}" by.
type MovieIDs struct {
	TMDB int
	IMDB string
}

// SeriesIDs are a show's ids, for {tmdbid}, {tvdbid} and {imdbid} in a new series folder.
type SeriesIDs struct {
	TMDB, TVDB int
	IMDB       string
}

// idToken renders a numeric id for a name, "" for none.
func idToken(id int) string {
	if id <= 0 {
		return ""
	}
	return strconv.Itoa(id)
}

// movieParts renders the folder and file base names for a movie track from the naming
// scheme. rel supplies the quality/edition/codec/group tokens; the track's own edition
// wins over the release's.
func (im *Importer) movieParts(t MovieTrack, rel parser.Release) (folder, file string) {
	title := cleanTitleLoose(t.Title)
	if title == "" {
		title = "Unknown"
	}
	edition := strings.TrimSpace(t.Edition)
	if edition == "" {
		edition = rel.Edition
	}
	tok := map[string]string{
		"title":       title,
		"year":        yearToken(t.Year),
		"quality":     qualityTag(rel),
		"resolution":  string(rel.StatedResolution()),
		"source":      statedSource(rel),
		"edition":     edition,
		"plexedition": plexEdition(edition),
		"codec":       string(rel.Codec),
		"group":       rel.Group,
		"tmdbid":      idToken(t.IDs.TMDB),
		"imdbid":      clean(t.IDs.IMDB),
	}
	n := im.movieNaming()
	folder = renderName(n.Folder, tok)
	if folder == "" {
		folder = title
	}
	file = renderName(withTrackEdition(n.File, t.Edition), tok)
	if file == "" {
		file = folder
	}
	return folder, file
}

// plexEdition renders an edition the way Plex reads one from a file name, "{edition-…}",
// or nothing at all when there's no edition.
func plexEdition(edition string) string {
	if edition = clean(edition); edition == "" {
		return ""
	}
	return "{edition-" + edition + "}"
}

// withTrackEdition makes sure a track with an edition gets it into its file name. A scheme
// that names no edition ("{title} ({year}) - {quality}", the default) gets {plexedition}
// right after the year, where Plex documents it, or at the end when there's no year. Only
// a track with an edition is touched, so a movie with no extra tracks keeps the names it has.
func withTrackEdition(format, trackEdition string) string {
	if strings.TrimSpace(trackEdition) == "" ||
		strings.Contains(format, "{edition}") || strings.Contains(format, "{plexedition}") {
		return format
	}
	if i := strings.Index(format, "({year})"); i >= 0 {
		at := i + len("({year})")
		return format[:at] + " {plexedition}" + format[at:]
	}
	return format + " {plexedition}"
}

// reEmptyPlexGroup is a Plex tag left with nothing in it: "{edition-}" from a hand-written
// "{edition-{edition}}" on a release with no edition, or "{tmdb-}" for a movie with no id.
// Plex would read the stray braces as part of the title.
var reEmptyPlexGroup = regexp.MustCompile(`\{(edition|tmdb|imdb|tvdb)-\s*\}`)

// renderName substitutes {tokens} and tidies up separators left by empty ones — an
// empty {episodetitle} in "{title} - {episode} - {episodetitle} - {quality}" must not
// leave a stranded "- -".
func renderName(format string, tok map[string]string) string {
	out := format
	for k, v := range tok {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	out = reEmptyPlexGroup.ReplaceAllString(out, "")
	out = strings.ReplaceAll(out, "()", "") // empty {year} etc. left bare brackets
	out = strings.ReplaceAll(out, "[]", "")
	out = strings.Join(strings.Fields(out), " ") // collapse whitespace
	for strings.Contains(out, "- -") {           // collapse "- - " runs from empty middle tokens
		out = strings.ReplaceAll(out, "- -", "-")
	}
	out = strings.Trim(out, " -") // drop dangling separators at either end
	return clean(out)
}

func yearToken(year int) string {
	if year > 0 {
		return fmt.Sprintf("%d", year)
	}
	return ""
}

func tokenOrEmpty(v, unknown string) string {
	if v == "" || v == unknown {
		return ""
	}
	return v
}

// Result describes a completed import.
type Result struct {
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
	Title      string `json:"title"`
	Year       int    `json:"year"`
	SizeBytes  int64  `json:"size_bytes"`
}

// prepareContent unpacks any archives at contentPath and returns the path the
// importer should search for media. A directory has its archive tree extracted in
// place; a single-file contentPath that is itself an archive (.rar/.zip — a lone
// archive torrent) is extracted into a scratch subdirectory next to it, which is
// returned as the search path. Shared by Import and ImportAs so both behave
// identically.
func (im *Importer) prepareContent(contentPath string) string {
	info, err := os.Stat(contentPath)
	if err != nil {
		return contentPath
	}
	if info.IsDir() {
		// Unpack any archives (scene releases often ship RAR'd), recursively so a
		// nested folder of parts is reached too.
		if n, err := extract.ExtractTree(contentPath); err != nil {
			im.log.Warn("extraction failed", "path", contentPath, "err", err)
		} else if n > 0 {
			im.log.Info("extracted archives", "count", n, "path", contentPath)
		}
		return contentPath
	}
	if extract.IsArchive(contentPath) {
		scratch := contentPath + ".extracted"
		if err := os.MkdirAll(scratch, 0o755); err != nil {
			im.log.Warn("extraction scratch dir failed", "path", scratch, "err", err)
			return contentPath
		}
		if err := extract.ExtractArchive(contentPath, scratch); err != nil {
			im.log.Warn("extraction failed", "path", contentPath, "err", err)
		} else {
			im.log.Info("extracted archive", "path", contentPath)
		}
		return scratch
	}
	return contentPath
}

// Import organizes a finished download. name is the release name (used for
// parsing); contentPath is the file or folder on disk to import from.
func (im *Importer) Import(name, contentPath string) (*Result, error) {
	searchPath := im.prepareContent(contentPath)

	src, size, err := findMediaFile(searchPath)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		// Same guard episodes have: a 0-byte file means the download is mid-move or
		// its data was lost, and importing it would replace a good library file with
		// nothing.
		return nil, fmt.Errorf("refusing to import empty (0-byte) source: %s", src)
	}

	rel := parser.Parse(name)
	root := im.movieDir()
	if rel.IsTV() {
		root = im.tvDir()
	}
	if err := im.checkRoot(root); err != nil {
		return nil, err
	}
	target := im.targetPath(rel, filepath.Ext(src))
	if !rel.IsTV() {
		// No library movie was matched, so no file at the target is one this import may
		// replace — except the same file (untrackedAt). An episode keeps its name: the file
		// at a scheme-built SxxEyy name in the show's folder is that episode's own.
		var ok bool
		if target, ok = im.clearOfUntracked(MovieTrack{Title: rel.Title, Year: rel.Year}, rel, filepath.Ext(src), src, target); !ok {
			return nil, fmt.Errorf("%w: %s", ErrUntrackedTarget, target)
		}
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, fmt.Errorf("create library dir: %w", err)
	}
	cleanStaleTemps(filepath.Dir(target))
	method, err := im.linkOrCopy(src, target)
	if err != nil {
		return nil, fmt.Errorf("import file: %w", err)
	}
	im.logImport(rel.Title, src, target, method)
	im.importSidecarSubs(searchPath, src, target)
	return &Result{SourcePath: src, TargetPath: target, Title: rel.Title, Year: rel.Year, SizeBytes: size}, nil
}

// ErrNoLibraryFolder is an import for a kind of media with no folder set. It used to
// land in ARRMADA_LIBRARY_DIR instead, inside the managed Docker volume.
var ErrNoLibraryFolder = errors.New("no library folder is set for this kind of media — choose one in Settings → Library")

// checkRoot verifies a configured library root exists before anything is created
// under it. When a mount is down, MkdirAll would silently recreate the root on the
// host filesystem and imports would land there — better to fail loudly.
func (im *Importer) checkRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		return ErrNoLibraryFolder
	}
	fi, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("library root missing/unmounted: %s: %w", root, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("library root is not a directory: %s", root)
	}
	return nil
}

// logImport records how the file reached the library. A hardlink is instant and
// costs no extra space (the download keeps seeding from the same data); a copy
// means the source and library are on different filesystems — worth surfacing,
// because it doubles disk use and is almost always a volume-mapping mistake.
func (im *Importer) logImport(title, src, target, method string) {
	if method == "copy" {
		im.log.Warn("imported by COPY (not hardlink) — source and library are on different filesystems; map downloads and library under one shared volume to hardlink",
			"title", title, "source", src, "target", target)
		return
	}
	im.log.Info("imported (hardlinked)", "title", title, "target", target)
}

// FindVideo returns the primary (largest) video file at contentPath (a file or
// directory) plus its size. Exposed for rescan/manual-import.
//
// Unlike the import's own pick (findMediaFile) it settles for the largest video in the
// parts it could read: it finds a film folder already in the library, and a folder with
// one subfolder Arrmada can't open is still that film's.
func FindVideo(contentPath string) (string, int64, error) { return pickMediaFile(contentPath, false) }

// FoundVideo is one video file discovered in a download.
type FoundVideo struct {
	Path string
	Size int64
}

// MinEpisodeVideoSize is the smallest video FindVideos takes for an episode or film:
// anything smaller is a sample, a theme or a stub.
const MinEpisodeVideoSize = 50 << 20

// FindVideos returns every video file at contentPath larger than 50 MB (skipping
// samples/extras). Used to import a season/multi-season pack — many files at once.
func FindVideos(contentPath string) ([]FoundVideo, error) {
	out, _, err := FindVideosCtx(context.Background(), contentPath, 0, 0)
	return out, err
}

// FindVideosCtx is FindVideos bounded for a listing: it stops when ctx ends (returning
// what it found and ctx.Err()), after maxResults files, or after maxVisited entries, and
// reports truncated when there was more. Zero limits mean unbounded. A missing
// contentPath is an error, as it always was.
//
// So is any part of it that couldn't be read: a folder inside it Arrmada isn't allowed
// into or can't list, or a file it can't stat (I/O error, permissions). The walk carries
// on, and what it found in the rest is returned with the first such error
// (ErrPartlyUnreadable). A file that was listed and is gone by the time it's looked at is
// one too (ErrChanged): the folder is changing under the walk, so what it found can't be
// trusted either way, and the caller tries again later. It used to skip all of those
// in silence, so a download whose episodes were all in an unreadable folder read as one
// with no video, which is what gets a release blocklisted and its data deleted. A caller
// deciding anything from the result treats an error as "can't tell"; one that only lists
// files for a person to pick from may show what was found.
func FindVideosCtx(ctx context.Context, contentPath string, maxResults, maxVisited int) ([]FoundVideo, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	info, err := os.Stat(contentPath)
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		if isVideo(contentPath) {
			return []FoundVideo{{Path: contentPath, Size: info.Size()}}, false, nil
		}
		return nil, false, nil
	}
	lim := &walkLimits{ctx: ctx, maxResults: maxResults, maxVisited: maxVisited}
	var out []FoundVideo
	var unread error
	werr := filepath.WalkDir(contentPath, func(p string, d os.DirEntry, err error) error {
		if serr := lim.step(); serr != nil {
			return serr
		}
		if err != nil {
			unread = firstUnreadable(unread, err)
			return nil // a folder that can't be listed is skipped; the rest is still read
		}
		if d.IsDir() || !isVideo(p) {
			return nil
		}
		fi, e := entryInfo(d)
		if e != nil {
			unread = firstUnreadable(unread, e)
			return nil
		}
		if !ListedEpisodeVideo(p, fi.Size()) {
			return nil
		}
		if lim.full() {
			return fs.SkipAll
		}
		out = append(out, FoundVideo{Path: p, Size: fi.Size()})
		lim.results++
		return nil
	})
	if err := ctxErr(werr); err != nil {
		return out, lim.truncated, err
	}
	return out, lim.truncated, unread
}

// ErrPartlyUnreadable marks a walk of a download that couldn't read all of it: what was
// found is incomplete, so nothing may be concluded from what's missing.
var ErrPartlyUnreadable = errors.New("part of the folder couldn't be read")

// ErrChanged is a walk that found something gone between listing a folder and looking at
// it: the download is being moved, rechecked or renamed under the walk. It is a kind of
// ErrPartlyUnreadable, so every caller holds the download, and tries again later.
//
// Skipping what vanished isn't safe. A film moved out mid-walk left its featurette as the
// largest video, and the import took that for the film; a folder renamed as a whole left
// every file gone and none found, which reads as a release with no video: blocklisted,
// its grab failed, its download deleted.
var ErrChanged = fmt.Errorf("%w: it changed while it was read", ErrPartlyUnreadable)

// entryInfo stats a walked entry (a test makes one vanish between listing and stat).
var entryInfo = func(d fs.DirEntry) (fs.FileInfo, error) { return d.Info() }

// StatEntriesWithForTest swaps how the walks stat what they listed, so another package's
// test can make files vanish mid-walk; restore puts it back. Tests only.
func StatEntriesWithForTest(fn func(fs.DirEntry) (fs.FileInfo, error)) (restore func()) {
	orig := entryInfo
	entryInfo = fn
	return func() { entryInfo = orig }
}

// firstUnreadable keeps the first error a walk met (wrapped in ErrPartlyUnreadable, or in
// ErrChanged for something that's gone); it names the folder or file that failed, which is
// what a person needs to fix it. A real read error replaces an earlier "changed": it's the
// one that won't clear by itself.
func firstUnreadable(have, err error) error {
	changed := errors.Is(err, fs.ErrNotExist)
	if have != nil && (changed || !errors.Is(have, ErrChanged)) {
		return have
	}
	if changed {
		return fmt.Errorf("%w: %w", ErrChanged, err)
	}
	return fmt.Errorf("%w: %w", ErrPartlyUnreadable, err)
}

// EpisodeImport describes one imported episode file.
type EpisodeImport struct {
	Season     int
	Episode    int   // the first episode this file covers (kept for single-episode callers)
	Episodes   []int // every episode the file covers — >1 for a multi-episode file
	SourcePath string
	TargetPath string
	SizeBytes  int64
	Method     string // "hardlink" | "copy" | "already" (already present, unchanged)
}

// ImportEpisode hardlinks a single episode video into
// "<root>/<Series (Year)>/Season NN/<Series - SxxExx - quality>.ext", deriving the
// season/episode from the file name. Returns nil (no error) with a false ok if the
// file has no SxxExx marker.
func (im *Importer) ImportEpisode(seriesTitle string, year int, videoPath string) (*EpisodeImport, bool, error) {
	return im.ImportEpisodeInto("", seriesTitle, year, videoPath)
}

// ImportEpisodeInto is ImportEpisode with an explicit series folder name to place
// the episode under (relative to the TV root). When non-empty it overrides the
// derived "<Title> (<Year>)" folder, so new episodes join the show's existing
// on-disk folder instead of spawning a duplicate.
func (im *Importer) ImportEpisodeInto(seriesFolder, seriesTitle string, year int, videoPath string) (*EpisodeImport, bool, error) {
	return im.ImportEpisodeIntoWith(seriesFolder, seriesTitle, year, videoPath, parser.Release{})
}

// ImportEpisodeIntoWith is ImportEpisodeInto with quality inherited from the release the
// file came from, for the fields its own name doesn't state.
//
// A pack states its quality once, on the folder — "Parks and Recreation S01-S07 (1080p
// BDRip x265)" — and names the files plainly. The importer parses only the filename, so
// {quality} rendered empty and every imported episode lost the " - 1080p BluRay" suffix
// the rest of the library carries. A file that names its own quality still wins; a pack
// can hold mixed encodes and the file is the more specific claim.
func (im *Importer) ImportEpisodeIntoWith(seriesFolder, seriesTitle string, year int, videoPath string, hint parser.Release) (*EpisodeImport, bool, error) {
	rel := parser.Parse(filepath.Base(videoPath))
	// Likewise an inferred resolution (an HDTV file read as SD) yields to a stated one.
	if rel.Resolution == parser.ResUnknown || (rel.ResolutionInferred && hint.StatedResolution() != parser.ResUnknown) {
		rel.Resolution, rel.ResolutionInferred = hint.Resolution, hint.ResolutionInferred
	}
	// A source the file only implied (fansub conventions) yields to one the pack states.
	if rel.Source == parser.SourceUnknown || (rel.SourceInferred && hint.Source != parser.SourceUnknown && !hint.SourceInferred) {
		rel.Source, rel.SourceInferred = hint.Source, hint.SourceInferred
	}
	if rel.Codec == parser.CodecUnknown {
		rel.Codec = hint.Codec
	}
	if rel.Group == "" {
		rel.Group = hint.Group
	}
	// No episode list means no S/E marker at all. Season 0 WITH episodes is a real
	// special (S00E01) and imports into the "Specials" folder — refusing it made
	// specials in complete packs silently unimportable while still counted handled.
	if len(rel.Episodes) == 0 {
		return nil, false, nil // can't place a file with no S/E marker
	}
	// Refuse empty source files — a 0-byte file means the download is mid-move or
	// its data was lost (e.g. a broken hardlink), and importing it would overwrite a
	// good library file with nothing and leave qBittorrent nothing to seed.
	if fi, err := os.Stat(videoPath); err == nil && fi.Size() == 0 {
		return nil, false, nil
	}
	ep := rel.Episodes[0]
	ext := filepath.Ext(videoPath)
	if err := im.checkRoot(im.tvDir()); err != nil {
		return nil, false, err
	}
	target, err := im.importTarget(seriesFolder, seriesTitle, year, rel.Season, ep, rel, ext)
	if err != nil {
		return nil, false, err
	}
	if err := insideRoot(target, im.tvDir()); err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, false, fmt.Errorf("create season dir: %w", err)
	}
	cleanStaleTemps(filepath.Dir(target))
	method, err := im.linkOrCopy(videoPath, target)
	if err != nil {
		return nil, false, fmt.Errorf("import episode: %w", err)
	}
	if method != "already" { // don't re-log / re-extract subs for an unchanged file
		im.logImport(seriesTitle, videoPath, target, method)
		im.importEpisodeSubs(videoPath, target)
	}
	fi, _ := os.Stat(target)
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	return &EpisodeImport{Season: rel.Season, Episode: ep, Episodes: rel.Episodes, SourcePath: videoPath, TargetPath: target, SizeBytes: size, Method: method}, true, nil
}

// ImportEpisodeAs places a video under an explicit (season, episode) — for anime
// files numbered absolutely ("[Group] Show - 137"), where the caller has already
// resolved the absolute number to a season/episode. Naming follows the standard
// "<Series> - SxxEyy" scheme (quality tag parsed from the source name).
func (im *Importer) ImportEpisodeAs(seriesFolder, seriesTitle string, year, season, episode int, videoPath string) (*EpisodeImport, bool, error) {
	// Season 0 is valid: specials place into the "Specials" folder.
	if season < 0 || episode <= 0 {
		return nil, false, nil
	}
	if fi, err := os.Stat(videoPath); err == nil && fi.Size() == 0 {
		return nil, false, nil // refuse 0-byte files (see ImportEpisodeInto)
	}
	rel := parser.Parse(filepath.Base(videoPath)) // for the quality tag only
	ext := filepath.Ext(videoPath)
	if err := im.checkRoot(im.tvDir()); err != nil {
		return nil, false, err
	}
	target, err := im.importTarget(seriesFolder, seriesTitle, year, season, episode, rel, ext)
	if err != nil {
		return nil, false, err
	}
	if err := insideRoot(target, im.tvDir()); err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, false, fmt.Errorf("create season dir: %w", err)
	}
	cleanStaleTemps(filepath.Dir(target))
	method, err := im.linkOrCopy(videoPath, target)
	if err != nil {
		return nil, false, fmt.Errorf("import episode: %w", err)
	}
	if method != "already" {
		im.logImport(seriesTitle, videoPath, target, method)
		im.importEpisodeSubs(videoPath, target)
	}
	var size int64
	if fi, _ := os.Stat(target); fi != nil {
		size = fi.Size()
	}
	return &EpisodeImport{Season: season, Episode: episode, Episodes: []int{episode}, SourcePath: videoPath, TargetPath: target, SizeBytes: size, Method: method}, true, nil
}

// EpisodeTargetIn is the canonical library path for an episode file, in an explicit series
// folder (empty = derive it from the scheme), with the quality tokens read from sourceName
// alone. The rename after a renumber still names files this way: it runs unattended, so it
// only moves a file to its new episode and never rewrites the tokens its name carries. The
// Rename button uses EpisodeRenameTarget, which reads the recorded release too.
func (im *Importer) EpisodeTargetIn(seriesFolder, title string, year, season, episode int, sourceName, ext string) string {
	return im.episodeTargetIn(seriesFolder, title, year, season, episode, parser.Parse(sourceName), ext, true)
}

// MoveEpisodeSubs moves subtitle sidecars paired with oldVideo so they stay next to
// the renamed video (newVideo), preserving each ".<lang>"/".forced" suffix. Only files
// sharing the old video's base name are moved, so unrelated neighbors are left alone.
func (im *Importer) MoveEpisodeSubs(oldVideo, newVideo string) {
	oldBase := strings.TrimSuffix(filepath.Base(oldVideo), filepath.Ext(oldVideo))
	newBase := strings.TrimSuffix(newVideo, filepath.Ext(newVideo))
	for _, p := range PairedSidecars(oldVideo) {
		// The pairing ignores case, so cut by length: the part after the old base name
		// is the ".en"/".forced" suffix to carry over.
		stem := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		if len(stem) < len(oldBase) {
			continue // a case fold that changed the length; leave it rather than guess
		}
		target := newBase + stem[len(oldBase):] + filepath.Ext(p)
		if err := im.Move(p, target); err == nil {
			im.log.Info("moved subtitle with rename", "from", p, "to", target)
		} else if errors.Is(err, ErrTargetExists) {
			// Another subtitle already has that name; both stay where they are.
			im.log.Warn("subtitle not moved with rename — a different file already has its new name", "from", p, "to", target)
		}
	}
}

// Sidecars is PairedSidecars (sidecars.go), kept under its older name for existing callers.
func Sidecars(video string) []string { return PairedSidecars(video) }

// RemoveDirIfEmpty deletes dir only when it contains no entries — used after a rename
// empties a legacy season folder ("Season 04") so it doesn't linger next to the new
// "Season 4". Safe: a folder still holding artwork/nfo/leftovers is left untouched.
func (im *Importer) RemoveDirIfEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return
	}
	if err := os.Remove(dir); err == nil {
		im.log.Info("removed empty folder after rename", "dir", dir)
	}
}

// ErrTargetExists is returned by Move when a different file already sits at the target.
var ErrTargetExists = errors.New("a different file already exists there")

// Move relocates a file within the library (same volume), creating parent dirs. A
// no-op when from == to.
//
// It never replaces an existing file. os.Rename silently overwrites its target on Linux,
// so a rename after a renumber — S03E01's row now holding S02E22's file — used to land on
// the real S03E01 and destroy it. A target that's just another name for the same file (a
// hardlink, common because imports hardlink) isn't a different file: the source name is
// dropped and the file stays put under the target name.
func (im *Importer) Move(from, to string) error {
	if from == to {
		return nil
	}
	fromInfo, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if toInfo, err := os.Lstat(to); err == nil {
		if os.SameFile(fromInfo, toInfo) {
			// A case-only rename on a case-insensitive filesystem stats as the same file
			// but is ONE directory entry — removing "from" there would delete the file
			// itself. Two hardlinks named "a.mkv" and "A.mkv" on a case-sensitive disk
			// are two entries, and renaming one onto the other does nothing at all.
			if strings.EqualFold(from, to) && !twoDirEntries(from, to) {
				return os.Rename(from, to)
			}
			return os.Remove(from)
		}
		return fmt.Errorf("%w: %s", ErrTargetExists, to)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return moveNoClobber(from, to, fromInfo.IsDir())
}

// moveNoClobber puts from at to, which was free a moment ago. The check above leaves a
// window — something can land on the target between the stat and the rename, and a rename
// would replace it — so a file moves as a hardlink plus removing the old name: creating a
// link fails if the target exists, so it can never overwrite anything. Only where links
// aren't possible (another disk, an Unraid user share across disks, a filesystem without
// them) does it fall back to a rename, straight after a fresh existence check.
func moveNoClobber(from, to string, isDir bool) error {
	if !isDir {
		err := os.Link(from, to)
		if err == nil {
			if rerr := os.Remove(from); rerr != nil {
				// Two names for one file. Drop the new one so the move reads as not done,
				// rather than leave the caller unsure where the file is.
				_ = os.Remove(to)
				return rerr
			}
			return nil
		}
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrTargetExists, to)
		}
	}
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("%w: %s", ErrTargetExists, to)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(from, to)
}

// twoDirEntries reports whether two paths that differ only in case are separate directory
// entries (so the filesystem is case-sensitive) rather than one entry reached either way.
// Anything it can't tell answers false, which makes Move rename instead of remove: at
// worst a name is left over, never a file deleted.
func twoDirEntries(from, to string) bool {
	fromDir, err1 := os.Stat(filepath.Dir(from))
	toDir, err2 := os.Stat(filepath.Dir(to))
	if err1 != nil || err2 != nil {
		return false
	}
	if !os.SameFile(fromDir, toDir) {
		return true // two folders whose names differ in case: only a case-sensitive disk has both
	}
	if filepath.Base(from) == filepath.Base(to) {
		return false // the same name in the same folder
	}
	entries, err := os.ReadDir(filepath.Dir(to))
	if err != nil {
		return false
	}
	var hasFrom, hasTo bool
	for _, e := range entries {
		switch e.Name() {
		case filepath.Base(from):
			hasFrom = true
		case filepath.Base(to):
			hasTo = true
		}
	}
	return hasFrom && hasTo
}

// SeriesLibraryFiles walks a series' library folder and returns the episode files
// present on disk (season/episode parsed from each filename). Used by rescan.
func (im *Importer) SeriesLibraryFiles(title string, year int) []EpisodeImport {
	return im.SeriesLibraryFilesIn("", title, year)
}

// SeriesLibraryVideos returns the raw video files in a series' on-disk folder (path +
// size), leaving parsing/resolution to the caller — so a rescan can run each name
// through the anime-aware resolver (which handles absolute-numbered files that carry no
// SxxExx). seriesFolder empty → derive "<Title> (<Year>)".
func (im *Importer) SeriesLibraryVideos(seriesFolder, title string, year int) []FoundVideo {
	dir, ok := im.seriesLibraryDir(seriesFolder, title, year)
	if !ok {
		return nil
	}
	vids, _ := FindVideos(dir)
	return vids
}

// SeriesEpisodeVideos is SeriesLibraryVideos without the files in a Plex local-extras
// folder anywhere in the show's folder (Extras, Featurettes, Behind The Scenes…): a
// featurette called "Show - S01E01 - Behind the Scenes" is never an episode. Rescans and
// the duplicate check read a show's episodes through it. dir is the folder it walked (""
// when none).
func (im *Importer) SeriesEpisodeVideos(seriesFolder, title string, year int) (dir string, vids []FoundVideo) {
	dir, ok := im.seriesLibraryDir(seriesFolder, title, year)
	if !ok {
		return "", nil
	}
	found, _ := FindVideos(dir)
	out := found[:0:0]
	for _, v := range found {
		if !InExtrasFolder(dir, v.Path) {
			out = append(out, v)
		}
	}
	return dir, out
}

// YearFolderName is "<Title> (<Year>)" as a show folder's name ("" without a title or year):
// the one folder a show with no files yet may be read from.
func YearFolderName(title string, year int) string {
	t := clean(title)
	if t == "" || year <= 0 {
		return ""
	}
	return fmt.Sprintf("%s (%d)", t, year)
}

// seriesLibraryDir is the show's folder under the TV root: seriesFolder, or
// "<Title> (<Year>)" when that's empty. ok is false for anything that isn't strictly
// inside the TV root, so a rescan never walks the root itself (and every other show in it)
// or a folder above it.
func (im *Importer) seriesLibraryDir(seriesFolder, title string, year int) (string, bool) {
	folder := clean(seriesFolder)
	if folder == "" {
		folder = clean(title)
		if folder == "" {
			folder = "Unknown"
		}
		if year > 0 {
			folder = fmt.Sprintf("%s (%d)", folder, year)
		}
	}
	dir := filepath.Join(im.tvDir(), folder)
	if insideRoot(dir, im.tvDir()) != nil {
		return "", false
	}
	return dir, true
}

// SeriesLibraryFilesIn is SeriesLibraryFiles scoped to an explicit series folder
// (the show's real on-disk directory). When empty it derives "<Title> (<Year>)".
// Using the real folder lets rescan reconcile a show whose folder doesn't match
// the derived name. FindVideos recurses, so any season-folder naming is found.
func (im *Importer) SeriesLibraryFilesIn(seriesFolder, title string, year int) []EpisodeImport {
	dir, ok := im.seriesLibraryDir(seriesFolder, title, year)
	if !ok {
		return nil
	}
	vids, _ := FindVideos(dir)
	var out []EpisodeImport
	for _, v := range vids {
		p := parser.Parse(filepath.Base(v.Path))
		if len(p.Episodes) == 0 {
			continue // no S/E marker — S00Exx specials DO count
		}
		// A multi-episode file (SxxE21-E22) satisfies every episode it covers, so
		// emit one entry per episode — otherwise E22 looks missing next to E21.
		for _, ep := range p.Episodes {
			out = append(out, EpisodeImport{Season: p.Season, Episode: ep, Episodes: p.Episodes, SourcePath: v.Path, TargetPath: v.Path, SizeBytes: v.Size})
		}
	}
	return out
}

// episodeTargetIn builds the episode path under an explicit series folder, rendering
// the user's series naming scheme (series folder / season folder / episode file). When
// seriesFolder is empty it derives the folder from the scheme; otherwise it uses the
// given folder verbatim so episodes join the show's existing on-disk directory.
func (im *Importer) episodeTargetIn(seriesFolder, title string, year, season, episode int, rel parser.Release, ext string, canonicalSeason bool) string {
	sn := im.seriesNamingScheme()
	t := cleanTitleLoose(title)
	if t == "" {
		t = "Unknown"
	}
	// The show's ids, looked up only when a template asks for one.
	var ids SeriesIDs
	folder := clean(seriesFolder)
	if (folder == "" && hasIDToken(sn.Folder)) || hasIDToken(sn.EpisodeFile) {
		ids = im.seriesIDs(title, year)
	}
	if folder == "" {
		folder = newSeriesFolderName(sn, ids, t, year)
	}

	epPart := fmt.Sprintf("S%02dE%02d", season, episode)
	if len(rel.Episodes) > 1 {
		epPart = episodeTag(rel) // multi-episode file → "S03E21-E22"
	}
	epTitle := ""
	if im.epTitleFn != nil {
		epTitle = cleanTitleLoose(im.epTitleFn(title, year, season, episode))
	}
	file := renderName(seriesFolderFormat(sn.EpisodeFile, ids), map[string]string{
		"title":        t,
		"year":         yearToken(year),
		"season":       strconv.Itoa(season),
		"season00":     fmt.Sprintf("%02d", season),
		"episode":      epPart,
		"episodetitle": epTitle,
		"quality":      qualityTag(rel),
		"resolution":   string(rel.StatedResolution()),
		"source":       statedSource(rel),
		"codec":        string(rel.Codec),
		"group":        rel.Group,
	})
	if file == "" {
		file = fmt.Sprintf("%s - %s", t, epPart)
	}

	seriesDir := filepath.Join(im.tvDir(), folder)
	// Import reuses an existing season folder (any padding) so it never duplicates one;
	// rename forces the scheme's canonical name so "Season 04" becomes "Season 4".
	canonical := im.seasonFolderName(sn.SeasonFolder, season, t, year)
	seasonSub := canonical
	if !canonicalSeason {
		seasonSub = seasonDirNameBy(seriesDir, season, canonical, parseSeasonDir)
	}
	return filepath.Join(seriesDir, seasonSub, clean(file)+ext)
}

// SeriesFolderPath is the show folder episodes go into: seriesFolder under the TV folder,
// or, when seriesFolder is "", the new folder the show would get — NewSeriesFolderName, the
// one place a new show folder is named (with "Add the year only when it's needed"
// decided there). A placement that writes names the folder once and passes it in, so this
// and the write agree. Read-only.
func (im *Importer) SeriesFolderPath(seriesFolder, title string, year int) string {
	folder := clean(seriesFolder)
	if folder == "" {
		folder = im.NewSeriesFolderName(title, year)
	}
	return filepath.Join(im.tvDir(), folder)
}

// seasonFolderName renders the season folder from the scheme. Season 0 is always
// "Specials" (the convention parseSeasonDir round-trips), regardless of the format.
func (im *Importer) seasonFolderName(format string, season int, title string, year int) string {
	if season == 0 {
		return "Specials"
	}
	name := renderName(format, map[string]string{
		"season":   strconv.Itoa(season),
		"season00": fmt.Sprintf("%02d", season),
		"title":    title,
		"year":     yearToken(year),
	})
	if name == "" {
		name = fmt.Sprintf("Season %d", season)
	}
	return name
}

// seasonDirName returns the season directory to place an episode in. READ-ONLY:
// deriving a path must never mutate the disk as a side effect (a rename here fired
// on every target computation, including previews). If the show already has a
// directory for that season — under any spelling ("Season 04" vs "Season 4", "S04",
// "Stagione 4") — its actual name is reused so imports never spawn a parallel folder;
// otherwise the canonical name from the scheme is returned. Normalizing legacy
// spellings is the explicit SeriesRename flow's job.
func seasonDirName(seriesDir string, season int, canonical string) string {
	return seasonDirNameBy(seriesDir, season, canonical, parseSeasonDir)
}

// seasonDirNameBy is seasonDirName reading folder names with parse.
func seasonDirNameBy(seriesDir string, season int, canonical string, parse func(string) (int, bool)) string {
	entries, err := os.ReadDir(seriesDir)
	if err != nil {
		return canonical
	}
	existing := ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, ok := parse(e.Name())
		if !ok || n != season {
			continue
		}
		if e.Name() == canonical {
			return canonical // exact match wins over any variant spelling
		}
		if existing == "" {
			existing = e.Name()
		}
	}
	if existing != "" {
		return existing
	}
	return canonical
}

// SeasonDirIn is the season folder an episode of season goes into inside the show folder
// showDir: the folder the show already has for that season, under whatever name it has
// ("S13", "Stagione 13"), else the scheme's name. Read-only.
func (im *Importer) SeasonDirIn(showDir, title string, year, season int) string {
	canonical := im.seasonFolderName(im.seriesNamingScheme().SeasonFolder, season, cleanTitleLoose(title), year)
	return seasonDirNameBy(showDir, season, canonical, im.plainSeasonNumber)
}

// plainSeasonNumber is seasonNumberOf without the folders that only look like a season:
// one with a tail ("Season 1 (Extras)", "Series 1 (Behind the scenes)") or a lone "Special".
func (im *Importer) plainSeasonNumber(name string) (int, bool) {
	name = strings.TrimSpace(name)
	if strings.ContainsAny(name, "([{") || strings.EqualFold(name, "special") {
		return 0, false
	}
	return im.seasonNumberOf(name)
}

// reSeasonDir matches "Season 1", "Season 01", "S1", "S01" (any zero-padding): the season
// folders an import joins, as v0.3.1 had them.
var reSeasonDir = regexp.MustCompile(`(?i)^s(?:eason)?\s*0*(\d+)$`)

// reSeasonDirPunct is "Season.01", "Season_01", "Season-01": the same, spelt with
// punctuation, which an import also joins rather than making a "Season 1" beside it.
var reSeasonDirPunct = regexp.MustCompile(`(?i)^season[._-]0*(\d+)$`)

// parseSeasonDir extracts a season number from a directory name ("Specials" → 0), for
// choosing the existing season folder an import joins. Deliberately narrow: a folder with a
// tail ("Season 1 (Extras)", "Series 1 (Behind the scenes)") or in another language is
// left alone — the wider SeasonFolderNumber only recognises season folders when working
// out a show's folder.
func parseSeasonDir(name string) (int, bool) {
	name = strings.TrimSpace(name)
	if strings.EqualFold(name, "specials") {
		return 0, true
	}
	for _, re := range []*regexp.Regexp{reSeasonDir, reSeasonDirPunct} {
		if m := re.FindStringSubmatch(name); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// MovieTarget builds the library path for a movie file using the configured
// naming scheme. The quality tag is parsed from qualitySource (usually the
// source/release filename).
func (im *Importer) MovieTarget(title string, year int, qualitySource, ext string) string {
	return im.MovieTrackTarget(MovieTrack{Title: title, Year: year}, qualitySource, ext)
}

// MovieTrackTarget is the library path for a file on movie track t: the naming scheme,
// then, only when that path is a file another track owns (t.Protected), the track's label
// (" - Director's Cut"), then a counter. It never returns a protected path, so whatever
// places a file there can't replace another track's file. The import, manual import and
// rename all name a track's file with this.
func (im *Importer) MovieTrackTarget(t MovieTrack, qualitySource, ext string) string {
	return im.movieTrackTarget(t, parser.Parse(qualitySource), ext)
}

// movieTrackTarget is MovieTrackTarget with the release already parsed.
func (im *Importer) movieTrackTarget(t MovieTrack, rel parser.Release, ext string) string {
	folder, file := im.movieParts(t, rel)
	dir := filepath.Join(im.movieRoot(t), folder)
	if t.Folder != "" {
		dir = t.Folder
	}
	target := filepath.Join(dir, file+ext)
	if !OwnedByOther(target, t.Protected) {
		return target
	}
	label := clean(t.Label)
	if label == "" {
		label = "Version"
	}
	base := file + " - " + label
	for n := 1; n < 100; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s (%d)", base, n)
		}
		if target = filepath.Join(dir, name+ext); !OwnedByOther(target, t.Protected) {
			return target
		}
	}
	return target // 99 tracks with one name: ImportAsNamed still refuses to place it
}

// OwnedByOther reports whether path is one of the protected files: the same path, or the
// same file on disk under another name (a hardlink). Paths compare case-insensitively on
// Windows, where the filesystem does.
func OwnedByOther(path string, protected []string) bool {
	if path == "" || len(protected) == 0 {
		return false
	}
	want := filepath.Clean(path)
	for _, p := range protected {
		if p == "" {
			continue
		}
		if pc := filepath.Clean(p); pc == want || (runtime.GOOS == "windows" && strings.EqualFold(pc, want)) {
			return true
		}
	}
	fi, err := os.Stat(want)
	if err != nil {
		return false // nothing at path, so no other name can be the same file
	}
	for _, p := range protected {
		if p == "" {
			continue
		}
		if pi, err := os.Stat(p); err == nil && os.SameFile(fi, pi) {
			return true
		}
	}
	return false
}

// SamePlace reports whether a and b are one directory entry spelt two ways: equal once
// symlinks are resolved (a symlinked folder), or differing only in case on a disk that
// ignores case (both spellings name one file). A second hardlink is another name, and
// isn't the same place: an import that replaces one still recycles it.
func SamePlace(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	if ra == rb {
		return true
	}
	if !strings.EqualFold(ra, rb) {
		return false
	}
	fa, err := os.Lstat(ra)
	if err != nil {
		return false
	}
	fb, err := os.Lstat(rb)
	return err == nil && os.SameFile(fa, fb)
}

// ErrProtectedTarget is an import aimed at a file another track of the movie owns. It
// can't come from MovieTrackTarget; it's the last guard before anything is placed.
var ErrProtectedTarget = errors.New("that file belongs to another version of this movie")

// ErrUntrackedTarget is an import whose every name is taken by files no record owns. The
// files are left alone and nothing is placed.
var ErrUntrackedTarget = errors.New("files Arrmada doesn't track already have every name this file could take")

// untrackedAt reports whether a file already at target must be kept clear of: one that
// isn't the track's own recorded file (own) and isn't the import itself (the same bytes —
// a re-import, or a file adopted where it lies). Replacing it would recycle, or with the
// bin off overwrite, a file no record knows of: a remux kept by hand under the scheme's
// name, say. A file that can't be checked counts as in the way.
func untrackedAt(target, own, src string) bool {
	di, err := os.Stat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	if own != "" && OwnedByOther(target, []string{own}) {
		return false
	}
	si, err := os.Stat(src)
	if err != nil {
		return true
	}
	same, err := sameContent(src, target, si, di)
	return err != nil || !same
}

// clearOfUntracked is target, or — when an untracked file is there (untrackedAt) — the
// track's next name clear of it, made the way a name another track owns is kept clear of
// (" - <label>", then a counter). ok is false when no name is free; nothing is placed then.
// A target with nothing untracked at it comes back unchanged, so every other import is
// named exactly as before.
func (im *Importer) clearOfUntracked(t MovieTrack, rel parser.Release, ext, src, target string) (string, bool) {
	if !untrackedAt(target, t.Own, src) {
		return target, true
	}
	t.Protected = append([]string(nil), t.Protected...) // never the caller's array
	for n := 0; n < 100; n++ {
		t.Protected = append(t.Protected, target)
		target = im.movieTrackTarget(t, rel, ext)
		if OwnedByOther(target, t.Protected) {
			return target, false
		}
		if !untrackedAt(target, t.Own, src) {
			im.log.Info("a file Arrmada doesn't track has the import's name — placing the import beside it", "kept", t.Protected[len(t.Protected)-1], "target", target)
			return target, true
		}
	}
	return target, false
}

// ImportAsNamed imports a download into movie track t: named per track (MovieTrackTarget),
// and never onto a file another track owns — that file is neither recycled nor
// overwritten. A file already at the target that is this track's own (t.Own: its older
// file under the same name) is replaced as before, through the recycle bin; any other
// file there (untrackedAt) is kept, and the import takes the track's next name beside it.
// Archives are unpacked first, exactly like Import.
func (im *Importer) ImportAsNamed(t MovieTrack, contentPath string) (*Result, error) {
	searchPath := im.prepareContent(contentPath)
	src, size, err := findMediaFile(searchPath)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, fmt.Errorf("refusing to import empty (0-byte) source: %s", src)
	}
	if err := im.checkRoot(im.movieRoot(t)); err != nil {
		return nil, err
	}
	rel := parser.Parse(filepath.Base(src))
	target := im.movieTrackTarget(t, rel, filepath.Ext(src))
	if OwnedByOther(target, t.Protected) {
		return nil, fmt.Errorf("%w: %s", ErrProtectedTarget, target)
	}
	target, ok := im.clearOfUntracked(t, rel, filepath.Ext(src), src, target)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUntrackedTarget, target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, fmt.Errorf("create library dir: %w", err)
	}
	cleanStaleTemps(filepath.Dir(target))
	method, err := im.linkOrCopy(src, target)
	if err != nil {
		return nil, fmt.Errorf("import file: %w", err)
	}
	im.logImport(t.Title, src, target, method)
	im.importSidecarSubs(searchPath, src, target)
	return &Result{SourcePath: src, TargetPath: target, Title: t.Title, Year: t.Year, SizeBytes: size}, nil
}

// ImportAs imports a file into the library under a known movie's title/year
// (rather than parsing the title from the release name), with no other track to keep
// clear of. Archives are unpacked first, exactly like Import.
func (im *Importer) ImportAs(title string, year int, contentPath string) (*Result, error) {
	return im.ImportAsNamed(MovieTrack{Title: title, Year: year}, contentPath)
}

// targetPath builds "<root>/<folder>/<file>.<ext>" from the parsed release.
func (im *Importer) targetPath(r parser.Release, ext string) string {
	title := clean(r.Title)
	if title == "" {
		title = "Unknown"
	}

	if r.IsTV() {
		// Route TV through the same series-naming path as episode imports so the
		// configured scheme is honored everywhere.
		ep := 0
		if len(r.Episodes) > 0 {
			ep = r.Episodes[0]
		}
		return im.episodeTargetIn("", r.Title, r.Year, r.Season, ep, r, ext, false)
	}

	// Movie.
	folder, file := im.movieParts(MovieTrack{Title: r.Title, Year: r.Year}, r)
	return filepath.Join(im.movieDir(), folder, file+ext)
}

func episodeTag(r parser.Release) string {
	if len(r.Episodes) == 0 {
		return fmt.Sprintf("S%02d", r.Season)
	}
	if len(r.Episodes) == 1 {
		return fmt.Sprintf("S%02dE%02d", r.Season, r.Episodes[0])
	}
	return fmt.Sprintf("S%02dE%02d-E%02d", r.Season, r.Episodes[0], r.Episodes[len(r.Episodes)-1])
}

func qualityTag(r parser.Release) string {
	var parts []string
	// Stated only, like the source: an inferred 480p written into a name would read back
	// as though the release had said it.
	if res := r.StatedResolution(); res != parser.ResUnknown {
		parts = append(parts, string(res))
	}
	if src := statedSource(r); src != "" {
		parts = append(parts, src)
	}
	return strings.Join(parts, " ")
}

// statedSource is the source for a library file name: only one the release actually
// states. A source the parser inferred from fansub conventions is good enough to rank on,
// but writing "WEB-DL" into a file name would turn that guess into a claim.
func statedSource(r parser.Release) string {
	if r.SourceInferred {
		return ""
	}
	return tokenOrEmpty(string(r.Source), string(parser.SourceUnknown))
}

// clean makes s safe as one folder or file name: illegal characters out, whitespace
// collapsed. "." and ".." come back empty, so every caller falls back to its "Unknown…"
// name: they name no folder of their own, and ".." — a show or an album TMDB or
// MusicBrainz really calls that — would climb out of the library. "..." and longer are
// ordinary names (insideRoot backs this up where files are placed).
func clean(s string) string {
	s = reIllegal.ReplaceAllString(s, "")
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if s == "." || s == ".." {
		return ""
	}
	return s
}

// reTitlePunct is the sentence punctuation dropped from a metadata title so it
// reads like a scene release ("tick, tick... BOOM!" → "tick tick BOOM"). Hyphens
// and ampersands are kept (Spider-Man, Fast & Furious).
//
// Apostrophes are NOT here. They sit INSIDE a word, so replacing one with a space
// splits the word in two: "Tom's Divorce" became "Tom s Divorce" and "Li'l Sebastian"
// became "Li l Sebastian". They're legal on every filesystem we target, they're what
// the title actually is, and library files scanned in from elsewhere keep them — so
// stripping them also made Arrmada's own naming inconsistent with the rest of a library.
var reTitlePunct = regexp.MustCompile(`[.,!?:;"…]+`)

// cleanTitleLoose sanitizes a metadata title into a folder/file name: it drops
// sentence punctuation (so the name doesn't carry commas/ellipses/bangs), then
// removes filesystem-illegal characters and collapses whitespace. This keeps the
// clean, release-style look while still sourcing the name from the metadata title.
func cleanTitleLoose(s string) string {
	return clean(reTitlePunct.ReplaceAllString(s, " "))
}

// minVideoSize is the floor below which a video in a directory is treated as a
// sample/extra, matching FindVideos.
const minVideoSize = 50 << 20

// ErrOnlySamples is returned when a directory holds video files but every one is
// sample-named or under the size floor. Callers treat it like no-video-found (a
// failed import), which must never select — or delete — real data.
var ErrOnlySamples = errors.New("only sample-sized videos found")

// findMediaFile returns the primary video file at contentPath (which may be a
// single file or a directory) plus its size. When picking from a directory it
// applies the same sample-name filter and ~50 MB floor as FindVideos, so a loose
// sample.mkv is never selected as the movie. A single-file contentPath is taken
// as-is (the caller explicitly chose it).
//
// A download it can't read all of is an error (ErrPartlyUnreadable), even when the rest
// holds a video: the largest of the files it could see may be a featurette beside a film
// in the folder it couldn't, and an import of that would replace the film's library file
// with it. The movie import treats the error as one to retry, never as "no video".
func findMediaFile(contentPath string) (string, int64, error) {
	return pickMediaFile(contentPath, true)
}

// pickMediaFile is findMediaFile; strict refuses a directory it couldn't read all of, and
// otherwise the largest video in the parts it could read is taken.
func pickMediaFile(contentPath string, strict bool) (string, int64, error) {
	info, err := os.Stat(contentPath)
	if err != nil {
		return "", 0, err
	}
	if !info.IsDir() {
		if isVideo(contentPath) {
			return contentPath, info.Size(), nil
		}
		return "", 0, fmt.Errorf("not a video file: %s", contentPath)
	}

	var best string
	var bestSize int64
	var unread error
	sawExcluded := false // a video existed but was sample-named or under the floor
	_ = filepath.WalkDir(contentPath, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			unread = firstUnreadable(unread, err)
			return nil
		}
		if d.IsDir() || !isVideo(p) {
			return nil
		}
		fi, e := entryInfo(d)
		if e != nil {
			unread = firstUnreadable(unread, e)
			return nil
		}
		if isSampleName(p) || fi.Size() < minVideoSize {
			sawExcluded = true
			return nil
		}
		if fi.Size() > bestSize {
			bestSize, best = fi.Size(), p
		}
		return nil
	})
	if unread != nil && (strict || best == "") {
		return "", 0, fmt.Errorf("%s: %w", contentPath, unread)
	}
	if best == "" {
		if sawExcluded {
			return "", 0, fmt.Errorf("%w in %s", ErrOnlySamples, contentPath)
		}
		return "", 0, fmt.Errorf("no video file found in %s", contentPath)
	}
	return best, bestSize, nil
}

func isVideo(p string) bool {
	return videoExts[strings.ToLower(filepath.Ext(p))]
}

// IsVideoFile reports whether p has a video file's extension.
func IsVideoFile(p string) bool { return isVideo(p) }

// IsSampleName reports whether a file's name marks it as a sample clip (see isSampleName).
func IsSampleName(path string) bool { return isSampleName(path) }

// isSampleName reports whether a file is a sample clip. "sample" must appear as a
// whole token (bounded by the separators in a filename), NOT merely as a substring —
// otherwise a legit episode like "Chuck Versus the Nacho Sampler" is wrongly skipped.
func isSampleName(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	for _, tok := range strings.FieldsFunc(base, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if tok == "sample" { // singular only — "Sampler"/"Free Samples" are real titles
			return true
		}
	}
	return false
}

// subtitleExts are external subtitle sidecar files worth importing with the video.
var subtitleExts = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".vtt": true, ".idx": true}

// importSidecarSubs links external subtitle files from a finished download next to
// the imported video, following the Plex/Jellyfin "<base>.<lang>[.forced|.sdh].<ext>"
// convention so the Subtitles module and players pick them up. It only takes subtitles
// that belong to this release: for a single-file torrent (contentPath is the file, in a
// shared save dir) it matches only the video's own base name, never a neighbor's.
func (im *Importer) importSidecarSubs(contentPath, srcVideo, targetVideo string) {
	info, err := os.Stat(contentPath)
	if err != nil {
		return
	}
	ownFolder := info.IsDir() // a folder download is entirely this release's
	dir := contentPath
	if !ownFolder {
		dir = filepath.Dir(contentPath)
	}
	srcBase := strings.ToLower(strings.TrimSuffix(filepath.Base(srcVideo), filepath.Ext(srcVideo)))
	targetBase := strings.TrimSuffix(targetVideo, filepath.Ext(targetVideo))

	var subs []subCandidate
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !subtitleExts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		if isSampleName(p) {
			return nil
		}
		stem := strings.ToLower(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
		lang, variant, ok := subLangFor(stem, srcBase, ownFolder)
		if !ok {
			return nil // a neighbor's subtitle in a shared save dir — skip
		}
		subs = append(subs, subCandidate{path: p, lang: lang, variant: variant})
		return nil
	})
	im.placeSubs(subs, targetBase)
}

// importEpisodeSubs imports the subtitles belonging to one episode file: its own
// sidecars (same base name, next to it) plus any matching subtitles in the release
// folder's Subs/Subtitles subdirectory — the season-pack convention the plain
// sidecar scan never reached.
func (im *Importer) importEpisodeSubs(videoPath, targetVideo string) {
	im.importSidecarSubs(videoPath, videoPath, targetVideo)
	im.importPackSubs(videoPath, targetVideo)
}

// importPackSubs scans a "Subs"/"Subtitles" folder next to an episode file for
// subtitles that belong to it: matching the video's base name, sitting in a
// per-episode subfolder named after it, or anything at all when the release folder
// holds exactly one video (then every subtitle is unambiguously that video's).
func (im *Importer) importPackSubs(videoPath, targetVideo string) {
	dir := filepath.Dir(videoPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	videoStem := strings.ToLower(strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath)))
	targetBase := strings.TrimSuffix(targetVideo, filepath.Ext(targetVideo))
	videos := 0
	var subDirs []string
	for _, e := range entries {
		if e.IsDir() {
			switch strings.ToLower(e.Name()) {
			case "subs", "subtitles":
				subDirs = append(subDirs, filepath.Join(dir, e.Name()))
			}
			continue
		}
		if isVideo(e.Name()) && !isSampleName(e.Name()) {
			videos++
		}
	}
	onlyVideo := videos == 1
	var subs []subCandidate
	for _, sd := range subDirs {
		_ = filepath.WalkDir(sd, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !subtitleExts[strings.ToLower(filepath.Ext(p))] || isSampleName(p) {
				return nil
			}
			stem := strings.ToLower(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
			parentStem := strings.ToLower(filepath.Base(filepath.Dir(p)))
			belongs := onlyVideo || stem == videoStem || strings.HasPrefix(stem, videoStem+".") || parentStem == videoStem
			if !belongs {
				return nil
			}
			lang, variant, _ := subLangFor(stem, videoStem, true) // ownFolder semantics: sniff the name for a language
			subs = append(subs, subCandidate{path: p, lang: lang, variant: variant})
			return nil
		})
	}
	im.placeSubs(subs, targetBase)
}

// subCandidate is one release subtitle on its way into the library.
type subCandidate struct {
	path, lang, variant string
	size                int64
}

// placeSubs places one release's subtitles for one video. They are placed full first,
// then SDH, then forced, so a forced file can never take the plain "<base>.en.srt" name
// just because it sorted first in the folder.
//
// Releases often ship the forced track without saying so — "2_English.srt" beside
// "3_English.srt" — so when one language has two or more unqualified subtitles and one is
// under a quarter of the size of the largest, the small one is taken to be the forced
// track: a full subtitle that short beside a much longer one in the same language is
// signs and songs, not the dialogue.
func (im *Importer) placeSubs(subs []subCandidate, targetBase string) {
	largest := map[string]int64{}
	unqualified := map[string]int{}
	for i := range subs {
		c := &subs[i]
		if fi, err := os.Stat(c.path); err == nil {
			c.size = fi.Size()
		}
		if c.lang != "" && c.variant == langs.VariantFull {
			unqualified[c.lang]++
			largest[c.lang] = max(largest[c.lang], c.size)
		}
	}
	for i := range subs {
		c := &subs[i]
		if c.lang != "" && c.variant == langs.VariantFull && unqualified[c.lang] >= 2 && c.size*4 < largest[c.lang] {
			c.variant = langs.VariantForced
		}
	}
	rank := map[string]int{langs.VariantFull: 0, langs.VariantSDH: 1, langs.VariantForced: 2}
	sort.SliceStable(subs, func(i, j int) bool { return rank[subs[i].variant] < rank[subs[j].variant] })
	for _, c := range subs {
		im.placeSub(c.path, targetBase, c.lang, c.variant)
	}
}

// placeSub links a subtitle to "<targetBase>[.<lang>][.<variant>]<ext>". The
// already-imported check runs BEFORE any uniqueness suffixing: if the natural target (or
// one of its numbered variants) already holds this subtitle — same inode or same bytes —
// the import is skipped, so re-imports don't stack "movie.en.2.srt" duplicates. A
// genuinely different subtitle at the natural path still gets a numeric suffix, even
// when it happens to be the same size.
func (im *Importer) placeSub(src, targetBase, lang, variant string) {
	si, err := os.Stat(src)
	if err != nil {
		return
	}
	ext := strings.ToLower(filepath.Ext(src))
	base := targetBase
	if lang != "" {
		base += "." + lang
	}
	if variant != langs.VariantFull {
		base += "." + variant
	}
	target := base + ext
	for i := 2; ; i++ {
		di, err := os.Stat(target)
		if os.IsNotExist(err) {
			break // free slot — import here
		}
		if err == nil {
			same, cerr := sameContent(src, target, si, di)
			if cerr != nil {
				im.log.Warn("couldn't compare subtitles — treating them as different", "src", src, "target", target, "err", cerr)
			}
			if same {
				return // already imported — skip, don't stack a duplicate
			}
		}
		target = fmt.Sprintf("%s.%d%s", base, i, ext)
	}
	if method, err := linkOrCopy(src, target); err == nil {
		im.log.Info("imported subtitle", "lang", lang, "variant", variant, "target", target, "method", method)
	}
}

// subLangFor derives a subtitle's language tag and variant, and whether it belongs to
// this release. A bare "<base>.srt" → no tag; "<base>.en.srt" → "en" (any spelling the
// shared language table knows, canonicalised: ".eng" → "en", ".pt-BR" → "pt-br"), with
// its qualifier kept: ".en.forced" → en forced, ".en.sdh" and Bazarr's ".en.hi" → en SDH.
// Inside the release's own folder, an unrelated name is sniffed for a language and for
// the words forced/SDH; in a shared save dir an unrelated name is rejected so
// single-file torrents don't grab neighbors.
func subLangFor(stem, srcBase string, ownFolder bool) (lang, variant string, ok bool) {
	switch {
	case stem == srcBase:
		return "", langs.VariantFull, true
	case strings.HasPrefix(stem, srcBase+"."):
		rest := stem[len(srcBase)+1:]
		n := langs.ParseSidecarTag(strings.Split(rest, "."))
		if n.Lang != "" || n.Token == "" {
			return langs.SidecarTag(n.Lang), n.Variant, true
		}
		// Not a language: keep the tag as written so the file isn't placed under the
		// bare name, which would claim the first wanted language.
		return n.Token, n.Variant, true
	case ownFolder:
		lang, variant := detectLang(stem)
		return lang, variant, true
	default:
		return "", langs.VariantFull, false
	}
}

// detectLang finds a language and variant in a free-form subtitle filename. A dotted
// name that ends in a two-letter or regional code is read the sidecar way
// ("Other.Name.de.forced"). Otherwise the words are sniffed strictly — 3-letter codes and
// full names only — because two-letter codes are everyday words: "Its.A.Trap" is not
// Italian, but "2_English_Forced" is English forced and "Brazilian.Portuguese" is pt-br.
// "hi" is an SDH marker only beside a language; alone it is Hindi.
func detectLang(stem string) (lang, variant string) {
	if n := langs.ParseSidecarTag(strings.Split(stem, ".")); n.Lang != "" &&
		((len(n.Token) == 2 && !trailingWord[n.Token]) || langs.Base(n.Lang) != n.Lang) {
		return langs.SidecarTag(n.Lang), n.Variant
	}
	forced, sdh := false, false
	for _, tok := range strings.FieldsFunc(stem, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		switch {
		case tok == "forced":
			forced = true
		case tok == "sdh" || tok == "cc" || tok == "hi":
			sdh = true
		case langs.IsToken(tok, true):
			// The first language word wins, unless a later one names a region of it
			// ("Portuguese.Brazilian").
			if t := langs.SidecarTag(tok); lang == "" || (langs.Base(t) == lang && t != lang) {
				lang = t
			}
		}
	}
	switch {
	case forced:
		variant = langs.VariantForced
	case sdh && lang != "":
		variant = langs.VariantSDH
	}
	return lang, variant
}

// trailingWord are two-letter language codes that are far more often the last word of a
// title ("Let.It.Be", "Born.To") than a tag. Italian (it), Norwegian (no) and Hebrew (he)
// stay readable as tags: they are common subtitle languages.
var trailingWord = map[string]bool{
	"am": true, "an": true, "as": true, "be": true, "is": true, "la": true, "my": true,
	"or": true, "so": true, "to": true,
}

// ErrReplacementRefused means an import would have replaced a library file whose old copy
// the recycle bin couldn't take. The old file is left exactly as it was; the import fails
// and is retried on the normal backoff, so it lands once the bin is fixed.
var ErrReplacementRefused = errors.New("replacement refused: couldn't move the old file to the recycle bin")

// linkOrCopy is the Importer-aware wrapper around the package-level linkOrCopy:
// when dst already exists with different content it recycles the old file first so
// the new file can hardlink into place — otherwise the replacement silently degraded
// to a full copy over dst. If the bin is on but refuses the old file, the import is
// refused instead: overwriting it there was a permanent delete in disguise. Only a bin
// that is deliberately off still overwrites, and that is logged.
func (im *Importer) linkOrCopy(src, dst string) (string, error) {
	if si, err := os.Stat(src); err == nil {
		if di, err := os.Stat(dst); err == nil {
			// Only the same bytes count as already imported. Matching size alone used to,
			// so a different release of identical size was never placed while its name
			// and quality were stamped onto the old file. A failed comparison counts as
			// different: the old file goes to the bin first, so nothing is lost.
			same, cerr := sameContent(src, dst, si, di)
			if cerr != nil {
				im.log.Warn("couldn't compare the import with the library file — treating them as different", "src", src, "target", dst, "err", cerr)
			}
			if same {
				return "already", nil
			}
			binDir, binErr := "", ErrRecycleDisabled
			if im.bin != nil {
				binDir, binErr = im.bin.For(dst)
			}
			switch {
			case errors.Is(binErr, ErrRecycleDisabled):
				im.log.Warn("replacing existing library file (recycle bin is off)", "target", dst)
			default:
				binDst := ""
				if binErr == nil {
					binDst, binErr = RecycleFile(binDir, dst)
				}
				if binErr != nil {
					im.log.Warn("import refused: the recycle bin couldn't take the file it would replace", "target", dst, "err", binErr)
					return "", fmt.Errorf("%w (%v)", ErrReplacementRefused, binErr)
				}
				im.log.Info("recycled replaced file", "target", dst, "recycled_to", binDst)
			}
		}
	}
	return linkOrCopyReport(src, dst, im.copyReport)
}

// linkOrCopy hardlinks src→dst (instant, no extra space, keeps seeding), falling
// back to a copy across filesystems. It reports which path was taken ("hardlink"
// or "copy") so callers can surface a misconfigured volume mapping.
func linkOrCopy(src, dst string) (string, error) { return linkOrCopyReport(src, dst, nil) }

// linkOrCopyReport is linkOrCopy whose copy, if it has to copy, reports to report.
func linkOrCopyReport(src, dst string, report CopyReport) (string, error) {
	// If the destination already holds this exact file (a prior hardlinked import),
	// do NOTHING. Re-copying would truncate dst — and since dst shares the source's
	// inode, that truncation zeroes the source torrent too (the 0-byte-both bug).
	// A prior copy with the same bytes is left alone too. An unreadable comparison
	// counts as different; copyFile replaces dst by rename, never in place.
	if si, err := os.Stat(src); err == nil {
		if di, err := os.Stat(dst); err == nil {
			if same, _ := sameContent(src, dst, si, di); same {
				return "already", nil
			}
		}
	}
	if !forceCopy.Load() {
		if err := linkFn(src, dst); err == nil {
			return "hardlink", nil
		}
	}
	if err := copyFile(src, dst, report); err != nil {
		return "", err
	}
	return "copy", nil
}

// linkFn is os.Link, swappable so tests can force the cross-device (EXDEV) failure that
// sends an import down the copy branch.
var linkFn = os.Link

// forceCopy makes every import copy rather than hardlink (ForceCopies).
var forceCopy atomic.Bool

// ForceCopies makes every import take the copy path, as it does across filesystems, until
// restore is called. It's for tests in other packages (an import job's copy progress);
// nothing in the app calls it.
func ForceCopies() (restore func()) {
	forceCopy.Store(true)
	return func() { forceCopy.Store(false) }
}

// Content comparison. Files up to fullCompareMax are compared byte for byte; larger
// ones by three sampleSize slices (start, middle, end), which tells two releases apart
// at the cost of 3 MiB of reads, and only ever when the sizes already match.
const (
	fullCompareMax = 3 << 20
	sampleSize     = 1 << 20
)

// sameContent reports whether src and dst hold the same file: the same inode, or the
// same non-empty size and the same bytes. A read error is returned with false.
func sameContent(src, dst string, si, di os.FileInfo) (bool, error) {
	if os.SameFile(si, di) {
		return true, nil
	}
	size := si.Size()
	if size == 0 || di.Size() != size || !si.Mode().IsRegular() || !di.Mode().IsRegular() {
		return false, nil
	}
	a, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer a.Close()
	b, err := os.Open(dst)
	if err != nil {
		return false, err
	}
	defer b.Close()
	if size <= fullCompareMax {
		return sameRange(a, b, 0, size)
	}
	for _, off := range []int64{0, size/2 - sampleSize/2, size - sampleSize} {
		same, err := sameRange(a, b, off, sampleSize)
		if err != nil || !same {
			return false, err
		}
	}
	return true, nil
}

// sameRange compares n bytes of a and b starting at off.
func sameRange(a, b io.ReaderAt, off, n int64) (bool, error) {
	const chunk = 64 << 10
	bufA, bufB := make([]byte, chunk), make([]byte, chunk)
	for n > 0 {
		k := int64(chunk)
		if n < k {
			k = n
		}
		if err := readFull(a, bufA[:k], off); err != nil {
			return false, err
		}
		if err := readFull(b, bufB[:k], off); err != nil {
			return false, err
		}
		if !bytes.Equal(bufA[:k], bufB[:k]) {
			return false, nil
		}
		off += k
		n -= k
	}
	return true, nil
}

// readFull fills buf from r at off. A reader may report io.EOF alongside the last bytes
// of the file; only a short read is an error.
func readFull(r io.ReaderAt, buf []byte, off int64) error {
	n, err := r.ReadAt(buf, off)
	if n == len(buf) {
		return nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// copyFile copies src → dst atomically: it writes to a uniquely-named temp file
// (so two concurrent copies of the same target can't collide), fsyncs, and renames
// into place — it never truncates an existing dst in-place (which would zero the
// source when dst is a hardlink of it). The temp is removed on any error. report, when
// set, hears how far the copy has got.
func copyFile(src, dst string, report CopyReport) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".*"+PartialSuffix)
	if err != nil {
		return err
	}
	tmp := out.Name()
	fail := func(e error) error {
		out.Close()
		_ = os.Remove(tmp)
		return e
	}
	var size int64
	if fi, err := in.Stat(); err == nil {
		size = fi.Size()
	}
	// An import running as a job reports its bytes as it goes (copyContents keeps the
	// kernel's fast copy either way).
	if err := copyContents(out, in, src, size, report); err != nil {
		return fail(err)
	}
	if err := out.Sync(); err != nil { // data on disk before the rename publishes it
		return fail(err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil { // CreateTemp makes 0600
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// cleanStaleTemps removes leftover *.arrmada-tmp files older than a day directly in
// dir — litter from a crash mid-copy. Called lazily on each import's target dir so
// no global sweep is needed. Fresh temps are left alone (a copy may be in flight).
func cleanStaleTemps(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), PartialSuffix) {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// AlbumTrackTarget builds the library path for one track:
//
//	<MusicDir>/<Artist>/<Album> (Year)/<NN> - <Title>.<ext>
//
// Multi-disc albums get the disc into the filename ("1-04") rather than a subfolder, so a
// player that ignores folders still orders the album correctly.
func (im *Importer) AlbumTrackTarget(artist, album string, year, disc, track int, title, ext string) string {
	folder := clean(album)
	if folder == "" {
		folder = "Unknown Album"
	}
	if year > 0 {
		folder = fmt.Sprintf("%s (%d)", folder, year)
	}
	num := fmt.Sprintf("%02d", track)
	if disc > 1 {
		num = fmt.Sprintf("%d-%02d", disc, track)
	}
	name := num
	if t := clean(title); t != "" {
		name = num + " - " + t
	}
	artistDir := clean(artist)
	if artistDir == "" {
		artistDir = "Unknown Artist"
	}
	return filepath.Join(im.MusicDir(), artistDir, folder, name+ext)
}

// PlaceFile hardlinks src to dst (copying when a link isn't possible), creating parents.
// Used by the album importer, where each track is placed individually.
func (im *Importer) PlaceFile(src, dst string) error {
	if err := im.checkRoot(im.MusicDir()); err != nil {
		return err
	}
	if err := insideRoot(dst, im.MusicDir()); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create album dir: %w", err)
	}
	_, err := im.linkOrCopy(src, dst)
	return err
}

// PlaceAlbumTrack is PlaceFile for a track that may replace one already held: the file
// at dst (same name, a better copy) and, when the better copy has a different extension,
// the old file at old ("" for none). Neither is ever deleted outright. Each goes to the
// recycle bin, and when the bin is off or can't take it nothing is placed at all and
// ErrReplacementRefused comes back: the album keeps what it has, and a later import tries
// again. A file outside the music folder isn't Arrmada's to move and is left where it is.
func (im *Importer) PlaceAlbumTrack(src, dst, old string) error {
	if err := im.checkRoot(im.MusicDir()); err != nil {
		return err
	}
	if err := insideRoot(dst, im.MusicDir()); err != nil {
		return err
	}
	if old == dst {
		old = ""
	}
	if old != "" {
		oi, err := os.Lstat(old)
		if err != nil || insideRoot(old, im.MusicDir()) != nil {
			old = "" // gone already, or not in the music folder
		} else if di, derr := os.Stat(dst); derr == nil && os.SameFile(oi, di) {
			// The same file under another spelling (a case-insensitive share: "X.FLAC" and
			// "X.flac"): it's dst itself, handled below, not a sibling to recycle.
			old = ""
		}
	}
	// Everything that would be displaced must have a bin to go to before anything moves.
	replaceDst := false
	if di, err := os.Stat(dst); err == nil {
		same := false
		if si, serr := os.Stat(src); serr == nil {
			same, _ = sameContent(src, dst, si, di)
		}
		replaceDst = !same
	}
	for _, p := range []string{dst, old} {
		if p == "" || (p == dst && !replaceDst) {
			continue
		}
		if err := im.binFor(p); err != nil {
			return fmt.Errorf("%w (%v)", ErrReplacementRefused, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create album dir: %w", err)
	}
	if replaceDst {
		if err := im.replaceThroughBin(src, dst); err != nil {
			return err
		}
	} else if _, err := im.linkOrCopy(src, dst); err != nil {
		return err
	}
	if old != "" {
		// The better copy is in place; the old one goes to the bin. If that fails now,
		// both stay on disk: nothing is lost, and the warning says which file is extra.
		if moved, err := RemoveToBin(im.bin, old); err != nil {
			im.log.Warn("music: kept the replaced file — the recycle bin couldn't take it", "path", old, "err", err)
		} else {
			im.log.Info("recycled replaced file", "target", old, "recycled_to", moved)
		}
	}
	return nil
}

// replaceThroughBin puts src in dst's place when dst holds something else. The new copy is
// made first, beside dst under a temporary name; only once it exists does dst go to the
// recycle bin and the copy take its name. A link and a copy that both fail leave dst
// untouched (the temporary file, ours, is removed); a bin that refuses leaves dst where it
// was. The one window that loses nothing either way is the final rename: dst is in the
// bin, restorable, and the new copy sits beside it.
func (im *Importer) replaceThroughBin(src, dst string) error {
	tmp := dst + ".arrmada-part"
	_ = os.Remove(tmp) // a temporary copy of ours left by an earlier, interrupted try
	if _, err := linkOrCopyReport(src, tmp, im.copyReport); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	moved, err := RemoveToBin(im.bin, dst)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w (%v)", ErrReplacementRefused, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("the replaced file is in the recycle bin (%s) but the new copy couldn't take its place: %w", moved, err)
	}
	im.log.Info("recycled replaced file", "target", dst, "recycled_to", moved)
	return nil
}

// binFor checks that a file could go to the recycle bin right now. Unlike CheckBin, a
// bin that is switched off is a refusal: the caller replaces only through the bin.
func (im *Importer) binFor(path string) error {
	if im.bin == nil {
		return ErrRecycleDisabled
	}
	dir, err := im.bin.For(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}
