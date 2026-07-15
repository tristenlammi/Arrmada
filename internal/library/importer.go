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
	"strings"

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
		if strings.Contains(strings.ToLower(filepath.Base(p)), "sample") {
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
			if strings.Contains(strings.ToLower(filepath.Base(p)), "sample") {
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
	dir := im.bookDir(author, title)
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

// BookLibraryFiles returns the book files present in a book's library folder (rescan).
func (im *Importer) BookLibraryFiles(author, title string) []FoundFile {
	return FindBookFiles(im.bookDir(author, title))
}

// BookEditionCanonical returns the canonical path a single-file book edition should
// live at ("<root>/<Author>/<Title>/<Title>.ext").
func (im *Importer) BookEditionCanonical(author, title, srcPath string) string {
	return filepath.Join(im.bookDir(author, title), clean(title)+filepath.Ext(srcPath))
}

func (im *Importer) bookDir(author, title string) string {
	a := clean(author)
	if a == "" {
		a = "Unknown Author"
	}
	t := clean(title)
	if t == "" {
		t = "Unknown"
	}
	return filepath.Join(im.root, a, t)
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
	root      string
	bookRoots []string // ebook + audiobook scan roots (falls back to root)
	log       *slog.Logger
	naming    NamingProvider // nil → built-in defaults
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
	t := clean(title)
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
	// Unpack any archives first (scene releases often ship RAR'd).
	if info, err := os.Stat(contentPath); err == nil && info.IsDir() {
		if n, err := extract.ExtractAll(contentPath); err != nil {
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
		if strings.Contains(strings.ToLower(filepath.Base(p)), "sample") {
			return nil
		}
		out = append(out, FoundVideo{Path: p, Size: fi.Size()})
		return nil
	})
	return out, nil
}

// EpisodeImport describes one imported episode file.
type EpisodeImport struct {
	Season     int
	Episode    int
	SourcePath string
	TargetPath string
	SizeBytes  int64
}

// ImportEpisode hardlinks a single episode video into
// "<root>/<Series (Year)>/Season NN/<Series - SxxExx - quality>.ext", deriving the
// season/episode from the file name. Returns nil (no error) with a false ok if the
// file has no SxxExx marker.
func (im *Importer) ImportEpisode(seriesTitle string, year int, videoPath string) (*EpisodeImport, bool, error) {
	rel := parser.Parse(filepath.Base(videoPath))
	if rel.Season == 0 || len(rel.Episodes) == 0 {
		return nil, false, nil // can't place a file with no S/E marker
	}
	ep := rel.Episodes[0]
	ext := filepath.Ext(videoPath)
	target := im.episodeTarget(seriesTitle, year, rel.Season, ep, rel, ext)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, false, fmt.Errorf("create season dir: %w", err)
	}
	method, err := linkOrCopy(videoPath, target)
	if err != nil {
		return nil, false, fmt.Errorf("import episode: %w", err)
	}
	im.logImport(seriesTitle, videoPath, target, method)
	fi, _ := os.Stat(target)
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	return &EpisodeImport{Season: rel.Season, Episode: ep, SourcePath: videoPath, TargetPath: target, SizeBytes: size}, true, nil
}

// EpisodeTarget builds the library path an episode file should live at, deriving the
// quality tag from sourceName (usually the current filename). Used by rename preview.
func (im *Importer) EpisodeTarget(title string, year, season, episode int, sourceName, ext string) string {
	return im.episodeTarget(title, year, season, episode, parser.Parse(sourceName), ext)
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
	folder := clean(title)
	if folder == "" {
		folder = "Unknown"
	}
	if year > 0 {
		folder = fmt.Sprintf("%s (%d)", folder, year)
	}
	vids, _ := FindVideos(filepath.Join(im.root, folder))
	var out []EpisodeImport
	for _, v := range vids {
		p := parser.Parse(filepath.Base(v.Path))
		if p.Season == 0 || len(p.Episodes) == 0 {
			continue
		}
		out = append(out, EpisodeImport{Season: p.Season, Episode: p.Episodes[0], SourcePath: v.Path, TargetPath: v.Path, SizeBytes: v.Size})
	}
	return out
}

// episodeTarget builds "<root>/<Series (Year)>/Season NN/<Series - SxxExx - q>.ext".
func (im *Importer) episodeTarget(title string, year, season, episode int, rel parser.Release, ext string) string {
	folder := clean(title)
	if folder == "" {
		folder = "Unknown"
	}
	if year > 0 {
		folder = fmt.Sprintf("%s (%d)", folder, year)
	}
	file := fmt.Sprintf("%s - S%02dE%02d", clean(title), season, episode)
	if q := qualityTag(rel); q != "" {
		file += " - " + q
	}
	return filepath.Join(im.root, folder, fmt.Sprintf("Season %02d", season), clean(file)+ext)
}

// MovieTarget builds the library path for a movie file using the configured
// naming scheme. The quality tag is parsed from qualitySource (usually the
// source/release filename).
func (im *Importer) MovieTarget(title string, year int, qualitySource, ext string) string {
	folder, file := im.movieParts(title, year, parser.Parse(qualitySource))
	return filepath.Join(im.root, folder, file+ext)
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
		return filepath.Join(im.root, folder, season, clean(file)+ext)
	}

	// Movie.
	folder, file := im.movieParts(r.Title, r.Year, r)
	return filepath.Join(im.root, folder, file+ext)
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

// linkOrCopy hardlinks src→dst (instant, no extra space, keeps seeding), falling
// back to a copy across filesystems. It reports which path was taken ("hardlink"
// or "copy") so callers can surface a misconfigured volume mapping.
func linkOrCopy(src, dst string) (string, error) {
	if err := os.Link(src, dst); err == nil {
		return "hardlink", nil
	}
	if err := copyFile(src, dst); err != nil {
		return "", err
	}
	return "copy", nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
