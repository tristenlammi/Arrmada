// Package library turns finished downloads into an organized media library:
// it finds the media file, derives a clean name from the parsed release, and
// hardlinks (or copies) it into the library root. This is the module-agnostic
// import engine; media modules refine naming/targets later.
package library

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/tristenlammi/arrmada/internal/extract"
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

// FoundFile is a media file discovered on disk (path + size). Shared by video, ebook,
// and audiobook discovery.
type FoundFile = FoundVideo

// FindBookFiles returns every ebook + audiobook file at contentPath (no size floor).
func FindBookFiles(contentPath string) []FoundFile {
	info, err := os.Stat(contentPath)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		if isBookFile(contentPath) {
			return []FoundFile{{Path: contentPath, Size: info.Size()}}
		}
		return nil
	}
	var out []FoundFile
	_ = filepath.WalkDir(contentPath, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isBookFile(p) {
			return nil
		}
		if isSampleName(p) {
			return nil
		}
		if fi, e := d.Info(); e == nil {
			out = append(out, FoundFile{Path: p, Size: fi.Size()})
		}
		return nil
	})
	return out
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
}

// SetBookRoots configures the folders scanned for books (ebook + audiobook). Duplicates and
// empties are collapsed, so passing the same path twice (books under one folder) scans it once.
func (im *Importer) SetBookRoots(roots ...string) {
	seen := map[string]bool{}
	im.bookRoots = im.bookRoots[:0]
	for _, r := range roots {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		im.bookRoots = append(im.bookRoots, r)
	}
}

// FindBookFolders walks the configured book roots (ebook + audiobook, falling back to the
// library root) for folders containing ebook/audiobook files, grouped by author (parent folder)
// and title (the folder name). Used by the book library scan.
func (im *Importer) FindBookFolders() []BookFolder {
	roots := im.bookRoots
	if len(roots) == 0 {
		roots = []string{im.root}
	}
	return im.FindBookFoldersIn(roots...)
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
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
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
				bf = &BookFolder{Author: author, Title: filepath.Base(dir)}
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
		out = append(out, *bf)
	}
	return out
}

// ImportBookEdition hardlinks a book edition into "<root>/<Author>/<Title>/". A single
// file becomes "<Title>.ext"; a multi-file audiobook keeps each file's name in the
// book folder (TargetPath then points at the folder). Format is the dominant extension.
func (im *Importer) ImportBookEdition(author, title string, files []FoundFile) (*BookImport, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no book files to import")
	}
	dir := im.bookDirIn(im.bookRootForFiles(files), author, title)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create book dir: %w", err)
	}
	if len(files) == 1 {
		target := filepath.Join(dir, clean(title)+filepath.Ext(files[0].Path))
		method, err := linkOrCopy(files[0].Path, target)
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
	// Multi-file (e.g. an mp3 audiobook): keep names inside the book folder.
	var total int64
	format := ""
	for _, f := range files {
		target := filepath.Join(dir, clean(strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path)))+filepath.Ext(f.Path))
		if method, err := linkOrCopy(f.Path, target); err == nil {
			im.logImport(title, f.Path, target, method)
			if fi, _ := os.Stat(target); fi != nil {
				total += fi.Size()
			}
			format = BookFileFormat(f.Path)
		}
	}
	return &BookImport{TargetPath: dir, Format: format, SizeBytes: total, FileCount: len(files)}, nil
}

// BookLibraryFiles returns the book files present in a book's library folder
// (rescan). Ebooks and audiobooks may live under different roots, so both are
// checked (deduped when they share one folder).
func (im *Importer) BookLibraryFiles(author, title string) []FoundFile {
	out := FindBookFiles(im.bookDirIn(im.ebookDir(), author, title))
	if im.audiobookDir() != im.ebookDir() {
		out = append(out, FindBookFiles(im.bookDirIn(im.audiobookDir(), author, title))...)
	}
	return out
}

// BookEditionCanonical returns the canonical path a single-file book edition should
// live at ("<root>/<Author>/<Title>/<Title>.ext"), rooted by the file's kind.
func (im *Importer) BookEditionCanonical(author, title, srcPath string) string {
	root := im.ebookDir()
	if isAudiobook(srcPath) {
		root = im.audiobookDir()
	}
	return filepath.Join(im.bookDirIn(root, author, title), clean(title)+filepath.Ext(srcPath))
}

func (im *Importer) bookDirIn(root, author, title string) string {
	a := clean(author)
	if a == "" {
		a = "Unknown Author"
	}
	t := clean(title)
	if t == "" {
		t = "Unknown"
	}
	return filepath.Join(root, a, t)
}

var reIllegal = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// Default movie naming. Tokens: {title} {year} {quality} {resolution} {source}
// {edition} {codec} {group}. Folder has no extension; the file gets one appended.
const (
	DefaultMovieFolder = "{title} ({year})"
	DefaultMovieFile   = "{title} ({year}) - {quality}"
)

// Naming is the configurable movie naming scheme.
type Naming struct {
	Folder string
	File   string
}

// NamingProvider supplies the (possibly user-customized) naming scheme at import
// time, so a settings change takes effect without restarting.
type NamingProvider interface {
	Naming() Naming
}

// Importer moves media into a library root.
type Importer struct {
	root string
	// Per-media-type import destinations. Empty → fall back to root, so a
	// single-library setup (no per-library dirs configured) keeps working.
	movieRoot     string
	tvRoot        string
	ebookRoot     string
	audiobookRoot string
	bookRoots     []string // ebook + audiobook scan roots (falls back to root)
	log           *slog.Logger
	naming        NamingProvider // nil → built-in defaults
	// epTitleFn resolves an episode's metadata title for naming ("" when unknown or
	// unset). Keyed by show name/year since the importer only knows the show by name.
	epTitleFn func(seriesTitle string, year, season, episode int) string
}

// SetEpisodeTitleFunc installs a lookup so episode files are named with their
// metadata title ("Chuck - S03E06 - Chuck Versus the Nacho Sampler - 1080p BluRay").
func (im *Importer) SetEpisodeTitleFunc(f func(seriesTitle string, year, season, episode int) string) {
	im.epTitleFn = f
}

// SetRoots routes each media type to its own library folder (movies, TV, ebooks,
// audiobooks). Any empty value falls back to the importer's base root, so an
// unconfigured type still lands in the shared library.
func (im *Importer) SetRoots(movie, tv, ebook, audiobook string) {
	im.movieRoot, im.tvRoot, im.ebookRoot, im.audiobookRoot = movie, tv, ebook, audiobook
}

func (im *Importer) movieDir() string {
	if im.movieRoot != "" {
		return im.movieRoot
	}
	return im.root
}

func (im *Importer) tvDir() string {
	if im.tvRoot != "" {
		return im.tvRoot
	}
	return im.root
}

func (im *Importer) ebookDir() string {
	if im.ebookRoot != "" {
		return im.ebookRoot
	}
	return im.root
}

func (im *Importer) audiobookDir() string {
	if im.audiobookRoot != "" {
		return im.audiobookRoot
	}
	return im.root
}

// bookRootForFiles picks the audiobook or ebook root from the files being
// imported (a single edition is one kind — the coordinator imports each kind
// separately).
func (im *Importer) bookRootForFiles(files []FoundFile) string {
	if len(files) > 0 && isAudiobook(files[0].Path) {
		return im.audiobookDir()
	}
	return im.ebookDir()
}

// NewImporter creates an importer targeting the given library root directory.
func NewImporter(root string, log *slog.Logger) *Importer {
	return &Importer{root: root, log: log}
}

// SetNaming installs a naming provider (user-configurable folder/file formats).
func (im *Importer) SetNaming(np NamingProvider) { im.naming = np }

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

// movieParts renders the folder and file base names for a movie from the naming
// scheme. rel supplies quality/edition/codec/group tokens.
func (im *Importer) movieParts(title string, year int, rel parser.Release) (folder, file string) {
	t := cleanTitleLoose(title)
	if t == "" {
		t = "Unknown"
	}
	tok := map[string]string{
		"title":      t,
		"year":       yearToken(year),
		"quality":    qualityTag(rel),
		"resolution": tokenOrEmpty(string(rel.Resolution), string(parser.ResUnknown)),
		"source":     tokenOrEmpty(string(rel.Source), string(parser.SourceUnknown)),
		"edition":    rel.Edition,
		"codec":      string(rel.Codec),
		"group":      rel.Group,
	}
	n := im.movieNaming()
	folder = renderName(n.Folder, tok)
	if folder == "" {
		folder = t
	}
	file = renderName(n.File, tok)
	if file == "" {
		file = folder
	}
	return folder, file
}

// renderName substitutes {tokens} and tidies up separators left by empty ones.
func renderName(format string, tok map[string]string) string {
	out := format
	for k, v := range tok {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	out = strings.Join(strings.Fields(out), " ") // collapse whitespace
	out = strings.TrimRight(out, " -")           // drop a dangling "- " from an empty trailing token
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

// Import organizes a finished download. name is the release name (used for
// parsing); contentPath is the file or folder on disk to import from.
func (im *Importer) Import(name, contentPath string) (*Result, error) {
	// Unpack any archives first (scene releases often ship RAR'd), recursively so a
	// nested folder of parts is reached too.
	if info, err := os.Stat(contentPath); err == nil && info.IsDir() {
		if n, err := extract.ExtractTree(contentPath); err != nil {
			im.log.Warn("extraction failed", "path", contentPath, "err", err)
		} else if n > 0 {
			im.log.Info("extracted archives", "count", n, "path", contentPath)
		}
	}

	src, size, err := findMediaFile(contentPath)
	if err != nil {
		return nil, err
	}

	rel := parser.Parse(name)
	target := im.targetPath(rel, filepath.Ext(src))

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, fmt.Errorf("create library dir: %w", err)
	}
	method, err := linkOrCopy(src, target)
	if err != nil {
		return nil, fmt.Errorf("import file: %w", err)
	}
	im.logImport(rel.Title, src, target, method)
	im.importSidecarSubs(contentPath, src, target)
	return &Result{SourcePath: src, TargetPath: target, Title: rel.Title, Year: rel.Year, SizeBytes: size}, nil
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
func FindVideo(contentPath string) (string, int64, error) { return findMediaFile(contentPath) }

// FoundVideo is one video file discovered in a download.
type FoundVideo struct {
	Path string
	Size int64
}

// FindVideos returns every video file at contentPath larger than 50 MB (skipping
// samples/extras). Used to import a season/multi-season pack — many files at once.
func FindVideos(contentPath string) ([]FoundVideo, error) {
	info, err := os.Stat(contentPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		if isVideo(contentPath) {
			return []FoundVideo{{Path: contentPath, Size: info.Size()}}, nil
		}
		return nil, nil
	}
	var out []FoundVideo
	_ = filepath.WalkDir(contentPath, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isVideo(p) {
			return nil
		}
		fi, e := d.Info()
		if e != nil || fi.Size() < 50<<20 {
			return nil
		}
		if isSampleName(p) {
			return nil
		}
		out = append(out, FoundVideo{Path: p, Size: fi.Size()})
		return nil
	})
	return out, nil
}

// EpisodeImport describes one imported episode file.
type EpisodeImport struct {
	Season   int
	Episode  int   // the first episode this file covers (kept for single-episode callers)
	Episodes []int // every episode the file covers — >1 for a multi-episode file
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
	rel := parser.Parse(filepath.Base(videoPath))
	if rel.Season == 0 || len(rel.Episodes) == 0 {
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
	target := im.episodeTargetIn(seriesFolder, seriesTitle, year, rel.Season, ep, rel, ext)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, false, fmt.Errorf("create season dir: %w", err)
	}
	method, err := linkOrCopy(videoPath, target)
	if err != nil {
		return nil, false, fmt.Errorf("import episode: %w", err)
	}
	if method != "already" { // don't re-log / re-extract subs for an unchanged file
		im.logImport(seriesTitle, videoPath, target, method)
		im.importSidecarSubs(videoPath, videoPath, target)
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
	if season <= 0 || episode <= 0 {
		return nil, false, nil
	}
	if fi, err := os.Stat(videoPath); err == nil && fi.Size() == 0 {
		return nil, false, nil // refuse 0-byte files (see ImportEpisodeInto)
	}
	rel := parser.Parse(filepath.Base(videoPath)) // for the quality tag only
	ext := filepath.Ext(videoPath)
	target := im.episodeTargetIn(seriesFolder, seriesTitle, year, season, episode, rel, ext)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, false, fmt.Errorf("create season dir: %w", err)
	}
	method, err := linkOrCopy(videoPath, target)
	if err != nil {
		return nil, false, fmt.Errorf("import episode: %w", err)
	}
	if method != "already" {
		im.logImport(seriesTitle, videoPath, target, method)
		im.importSidecarSubs(videoPath, videoPath, target)
	}
	var size int64
	if fi, _ := os.Stat(target); fi != nil {
		size = fi.Size()
	}
	return &EpisodeImport{Season: season, Episode: episode, Episodes: []int{episode}, SourcePath: videoPath, TargetPath: target, SizeBytes: size, Method: method}, true, nil
}

// EpisodeTarget builds the library path an episode file should live at, deriving the
// quality tag from sourceName (usually the current filename). Used by rename preview.
func (im *Importer) EpisodeTarget(title string, year, season, episode int, sourceName, ext string) string {
	return im.episodeTarget(title, year, season, episode, parser.Parse(sourceName), ext)
}

// EpisodeTargetIn is EpisodeTarget scoped to an explicit series folder (empty =
// derive "<Title> (<Year>)").
func (im *Importer) EpisodeTargetIn(seriesFolder, title string, year, season, episode int, sourceName, ext string) string {
	return im.episodeTargetIn(seriesFolder, title, year, season, episode, parser.Parse(sourceName), ext)
}

// MoveEpisodeSubs moves subtitle sidecars paired with oldVideo so they stay next to
// the renamed video (newVideo), preserving each ".<lang>"/".forced" suffix. Only files
// sharing the old video's base name are moved, so unrelated neighbors are left alone.
func (im *Importer) MoveEpisodeSubs(oldVideo, newVideo string) {
	oldBase := strings.TrimSuffix(oldVideo, filepath.Ext(oldVideo))
	newBase := strings.TrimSuffix(newVideo, filepath.Ext(newVideo))
	entries, err := os.ReadDir(filepath.Dir(oldVideo))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !subtitleExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		p := filepath.Join(filepath.Dir(oldVideo), e.Name())
		stem := strings.TrimSuffix(p, filepath.Ext(p))
		if stem != oldBase && !strings.HasPrefix(stem, oldBase+".") {
			continue // not this video's sidecar
		}
		target := newBase + stem[len(oldBase):] + filepath.Ext(p) // carry ".en"/".forced"
		if err := im.Move(p, target); err == nil {
			im.log.Info("moved subtitle with rename", "from", p, "to", target)
		}
	}
}

// Move relocates a file within the library (same volume), creating parent dirs. A
// no-op when from == to.
func (im *Importer) Move(from, to string) error {
	if from == to {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.Rename(from, to)
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
	vids, _ := FindVideos(filepath.Join(im.tvDir(), folder))
	return vids
}

// SeriesLibraryFilesIn is SeriesLibraryFiles scoped to an explicit series folder
// (the show's real on-disk directory). When empty it derives "<Title> (<Year>)".
// Using the real folder lets rescan reconcile a show whose folder doesn't match
// the derived name. FindVideos recurses, so any season-folder naming is found.
func (im *Importer) SeriesLibraryFilesIn(seriesFolder, title string, year int) []EpisodeImport {
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
	vids, _ := FindVideos(filepath.Join(im.tvDir(), folder))
	var out []EpisodeImport
	for _, v := range vids {
		p := parser.Parse(filepath.Base(v.Path))
		if p.Season == 0 || len(p.Episodes) == 0 {
			continue
		}
		// A multi-episode file (SxxE21-E22) satisfies every episode it covers, so
		// emit one entry per episode — otherwise E22 looks missing next to E21.
		for _, ep := range p.Episodes {
			out = append(out, EpisodeImport{Season: p.Season, Episode: ep, Episodes: p.Episodes, SourcePath: v.Path, TargetPath: v.Path, SizeBytes: v.Size})
		}
	}
	return out
}

// episodeTarget builds "<root>/<Series (Year)>/Season NN/<Series - SxxExx - q>.ext".
func (im *Importer) episodeTarget(title string, year, season, episode int, rel parser.Release, ext string) string {
	return im.episodeTargetIn("", title, year, season, episode, rel, ext)
}

// episodeTargetIn builds the episode path under an explicit series folder. When
// seriesFolder is empty it derives the conventional "<Title> (<Year>)" folder;
// otherwise it uses the given folder verbatim so episodes land in the show's
// existing on-disk directory.
func (im *Importer) episodeTargetIn(seriesFolder, title string, year, season, episode int, rel parser.Release, ext string) string {
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
	epPart := fmt.Sprintf("S%02dE%02d", season, episode)
	if len(rel.Episodes) > 1 {
		epPart = episodeTag(rel) // multi-episode file → "S03E21-E22"
	}
	file := fmt.Sprintf("%s - %s", clean(title), epPart)
	if im.epTitleFn != nil {
		if et := cleanTitleLoose(im.epTitleFn(title, year, season, episode)); et != "" {
			file += " - " + et
		}
	}
	if q := qualityTag(rel); q != "" {
		file += " - " + q
	}
	seriesDir := filepath.Join(im.tvDir(), folder)
	return filepath.Join(seriesDir, seasonDirName(seriesDir, season), clean(file)+ext)
}

// seasonDirName returns the season directory to place an episode in: an existing
// one for that season if the show already has it (matching any padding — "Season 1",
// "Season 01", "Specials"), otherwise the zero-padded default. This stops a grab
// from creating a duplicate "Season 01" next to an existing "Season 1".
func seasonDirName(seriesDir string, season int) string {
	def := fmt.Sprintf("Season %02d", season)
	if season == 0 {
		def = "Specials"
	}
	entries, err := os.ReadDir(seriesDir)
	if err != nil {
		return def
	}
	for _, e := range entries {
		if e.IsDir() {
			if n, ok := parseSeasonDir(e.Name()); ok && n == season {
				return e.Name() // reuse the show's existing layout
			}
		}
	}
	return def
}

// reSeasonDir matches "Season 1", "Season 01", "S1", "S01" (any zero-padding).
var reSeasonDir = regexp.MustCompile(`(?i)^s(?:eason)?\s*0*(\d+)$`)

// parseSeasonDir extracts a season number from a directory name ("Specials" → 0).
func parseSeasonDir(name string) (int, bool) {
	name = strings.TrimSpace(name)
	if strings.EqualFold(name, "specials") {
		return 0, true
	}
	if m := reSeasonDir.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n, true
		}
	}
	return 0, false
}

// MovieTarget builds the library path for a movie file using the configured
// naming scheme. The quality tag is parsed from qualitySource (usually the
// source/release filename).
func (im *Importer) MovieTarget(title string, year int, qualitySource, ext string) string {
	folder, file := im.movieParts(title, year, parser.Parse(qualitySource))
	return filepath.Join(im.movieDir(), folder, file+ext)
}

// ImportAs imports a file into the library under a known movie's title/year
// (rather than parsing the title from the release name). Used by manual import.
func (im *Importer) ImportAs(title string, year int, contentPath string) (*Result, error) {
	src, size, err := findMediaFile(contentPath)
	if err != nil {
		return nil, err
	}
	target := im.MovieTarget(title, year, filepath.Base(src), filepath.Ext(src))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, fmt.Errorf("create library dir: %w", err)
	}
	method, err := linkOrCopy(src, target)
	if err != nil {
		return nil, fmt.Errorf("import file: %w", err)
	}
	im.logImport(title, src, target, method)
	im.importSidecarSubs(contentPath, src, target)
	return &Result{SourcePath: src, TargetPath: target, Title: title, Year: year, SizeBytes: size}, nil
}

// targetPath builds "<root>/<folder>/<file>.<ext>" from the parsed release.
func (im *Importer) targetPath(r parser.Release, ext string) string {
	title := clean(r.Title)
	if title == "" {
		title = "Unknown"
	}
	quality := qualityTag(r)

	if r.IsTV() {
		folder := title
		season := fmt.Sprintf("Season %02d", r.Season)
		file := fmt.Sprintf("%s - %s", title, episodeTag(r))
		if quality != "" {
			file += " - " + quality
		}
		return filepath.Join(im.tvDir(), folder, season, clean(file)+ext)
	}

	// Movie.
	folder, file := im.movieParts(r.Title, r.Year, r)
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
	if r.Resolution != parser.ResUnknown {
		parts = append(parts, string(r.Resolution))
	}
	if r.Source != parser.SourceUnknown {
		parts = append(parts, string(r.Source))
	}
	return strings.Join(parts, " ")
}

func clean(s string) string {
	s = reIllegal.ReplaceAllString(s, "")
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// reTitlePunct is the sentence punctuation dropped from a metadata title so it
// reads like a scene release ("tick, tick... BOOM!" → "tick tick BOOM"). Hyphens
// and ampersands are kept (Spider-Man, Fast & Furious).
var reTitlePunct = regexp.MustCompile(`[.,!?:;'"…]+`)

// cleanTitleLoose sanitizes a metadata title into a folder/file name: it drops
// sentence punctuation (so the name doesn't carry commas/ellipses/bangs), then
// removes filesystem-illegal characters and collapses whitespace. This keeps the
// clean, release-style look while still sourcing the name from the metadata title.
func cleanTitleLoose(s string) string {
	return clean(reTitlePunct.ReplaceAllString(s, " "))
}

// findMediaFile returns the primary video file at contentPath (which may be a
// single file or a directory) plus its size.
func findMediaFile(contentPath string) (string, int64, error) {
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
	_ = filepath.WalkDir(contentPath, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isVideo(p) {
			return nil
		}
		fi, e := d.Info()
		if e != nil {
			return nil
		}
		if fi.Size() > bestSize {
			bestSize, best = fi.Size(), p
		}
		return nil
	})
	if best == "" {
		return "", 0, fmt.Errorf("no video file found in %s", contentPath)
	}
	return best, bestSize, nil
}

func isVideo(p string) bool {
	return videoExts[strings.ToLower(filepath.Ext(p))]
}

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

// langNames maps common subtitle language tokens (codes + English names) to the
// ISO 639-1 tag used by the Subtitles module's sidecar convention.
var langNames = map[string]string{
	"en": "en", "eng": "en", "english": "en",
	"es": "es", "spa": "es", "spanish": "es",
	"fr": "fr", "fre": "fr", "fra": "fr", "french": "fr",
	"de": "de", "ger": "de", "deu": "de", "german": "de",
	"it": "it", "ita": "it", "italian": "it",
	"pt": "pt", "por": "pt", "portuguese": "pt",
	"nl": "nl", "dut": "nl", "nld": "nl", "dutch": "nl",
	"ru": "ru", "rus": "ru", "russian": "ru",
	"ja": "ja", "jpn": "ja", "japanese": "ja",
	"zh": "zh", "chi": "zh", "zho": "zh", "chinese": "zh",
	"ko": "ko", "kor": "ko", "korean": "ko",
	"ar": "ar", "ara": "ar", "arabic": "ar",
}

// importSidecarSubs links external subtitle files from a finished download next to
// the imported video, following the Plex/Jellyfin "<base>.<lang>.<ext>" convention
// so the Subtitles module and players pick them up. It only takes subtitles that
// belong to this release: for a single-file torrent (contentPath is the file, in a
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

	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !subtitleExts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		if isSampleName(p) {
			return nil
		}
		stem := strings.ToLower(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
		lang, ok := subLangFor(stem, srcBase, ownFolder)
		if !ok {
			return nil // a neighbor's subtitle in a shared save dir — skip
		}
		target := targetBase + strings.ToLower(filepath.Ext(p))
		if lang != "" {
			target = targetBase + "." + lang + strings.ToLower(filepath.Ext(p))
		}
		target = uniqueSubPath(target)
		if method, err := linkOrCopy(p, target); err == nil {
			im.log.Info("imported subtitle", "lang", lang, "target", target, "method", method)
		}
		return nil
	})
}

// subLangFor derives a subtitle's language tag and whether it belongs to this
// release. A bare "<base>.srt" → no tag; "<base>.en.srt" → "en"; inside the
// release's own folder, an unrelated name is language-sniffed; in a shared save
// dir an unrelated name is rejected so single-file torrents don't grab neighbors.
func subLangFor(stem, srcBase string, ownFolder bool) (lang string, ok bool) {
	switch {
	case stem == srcBase:
		return "", true
	case strings.HasPrefix(stem, srcBase+"."):
		rest := stem[len(srcBase)+1:]
		if i := strings.IndexByte(rest, '.'); i >= 0 {
			rest = rest[:i] // "en" from "en.forced"
		}
		if code, known := langNames[rest]; known {
			return code, true
		}
		return rest, true
	case ownFolder:
		return detectLang(stem), true
	default:
		return "", false
	}
}

// detectLang finds a known language token anywhere in a subtitle filename.
func detectLang(stem string) string {
	for _, tok := range strings.FieldsFunc(stem, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if code, ok := langNames[tok]; ok {
			return code
		}
	}
	return ""
}

// uniqueSubPath returns path, or path with a numeric suffix if it already exists,
// so a second same-language subtitle doesn't clobber the first.
func uniqueSubPath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s.%d%s", base, i, ext)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
}

// linkOrCopy hardlinks src→dst (instant, no extra space, keeps seeding), falling
// back to a copy across filesystems. It reports which path was taken ("hardlink"
// or "copy") so callers can surface a misconfigured volume mapping.
func linkOrCopy(src, dst string) (string, error) {
	// If the destination already holds this exact file (a prior hardlinked import),
	// do NOTHING. Re-copying would truncate dst — and since dst shares the source's
	// inode, that truncation zeroes the source torrent too (the 0-byte-both bug).
	if si, err := os.Stat(src); err == nil {
		if di, err := os.Stat(dst); err == nil {
			if os.SameFile(si, di) {
				return "already", nil // same inode — already imported, leave it alone
			}
			if si.Size() > 0 && di.Size() == si.Size() {
				return "already", nil // same content already present (prior copy)
			}
		}
	}
	if err := os.Link(src, dst); err == nil {
		return "hardlink", nil
	}
	if err := copyFile(src, dst); err != nil {
		return "", err
	}
	return "copy", nil
}

// copyFile copies src → dst atomically: it writes to a temp file and renames into
// place, so it never truncates an existing dst in-place (which would zero the
// source when dst is a hardlink of it).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".arrmada-tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
