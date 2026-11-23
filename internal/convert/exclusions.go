package convert

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/series"
)

// What Convert leaves alone because you said so: a title ("Don't convert this" on a film, a
// show or one season), a whole library folder, or every file of a resolution ("Don't convert
// 4K files"). The rules are read with the other settings (prefs) and applied in
// planForTitle, so the runner, the lists, the counts and the plan card all agree. Nothing is
// left out until you choose it.

// The rules, as Needs.Excluded names them.
const (
	ExcludeTitle      = "title"
	ExcludeFolder     = "folder"
	ExcludeResolution = "resolution"
)

const (
	keySkipFolders     = "convert_skip_folders"     // CSV of library folder keys ("movie:0", "series:4")
	keySkipResolutions = "convert_skip_resolutions" // CSV of "SD", "720p", "1080p", "2160p"
)

// resolutions are the resolution classes a file can be left out by, smallest first, as the
// analysis labels them (resolutionLabel).
var resolutions = []string{"SD", "720p", "1080p", "2160p"}

// normResolution maps what a client sends onto a resolution class; "" when it's none.
func normResolution(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "sd", "480p", "576p":
		return "SD"
	case "720p":
		return "720p"
	case "1080p":
		return "1080p"
	case "2160p", "4k", "uhd":
		return "2160p"
	}
	return ""
}

// resolutionName is a class as people say it.
func resolutionName(res string) string {
	if res == "2160p" {
		return "4K"
	}
	return res
}

// LibraryFolder is one Movies or TV library folder, as the folder switches see it. Key is
// "movie:<id>" or "series:<id>" (id 0 is the library's default folder).
type LibraryFolder struct {
	Key   string `json:"key"`
	Media string `json:"media"` // "movie" | "series"
	Path  string `json:"path"`
	Label string `json:"label"`
}

// FolderKey is a library folder's key.
func FolderKey(media string, id int64) string { return media + ":" + strconv.FormatInt(id, 10) }

// SetLibraryFolders tells Convert how to list the Movies and TV library folders (read on
// every use, so an added or moved folder is seen at once). Nil leaves folder rules unused.
func (s *Service) SetLibraryFolders(fn func(ctx context.Context) []LibraryFolder) {
	s.libFolders = fn
}

func (s *Service) libraryFolders(ctx context.Context) []LibraryFolder {
	if s.libFolders == nil {
		return nil
	}
	return s.libFolders(ctx)
}

type seasonRef struct {
	seriesID int64
	season   int
}

// exclusions is every rule, read once per prefs.
type exclusions struct {
	movies  map[int64]bool
	series  map[int64]bool
	seasons map[seasonRef]bool
	// folders is every library folder with whether it's left out; nil when none is, so a
	// file needs no path test at all.
	folders []skipFolder
	res     map[string]bool
	key     string // changes whenever any rule does, for cached views
}

type skipFolder struct {
	LibraryFolder
	skip bool
}

// any reports whether any rule is set.
func (e exclusions) any() bool {
	return len(e.movies) > 0 || len(e.series) > 0 || len(e.seasons) > 0 || e.folders != nil || len(e.res) > 0
}

// match says whether a file is left out, by which rule, and why in plain words. A title
// rule wins over a folder one, and a folder over a resolution, so the reason shown is the
// most specific one.
func (e exclusions) match(mi *MediaInfo, path string, t titleRef) (rule, why string) {
	switch t.Kind {
	case "movie":
		if t.MovieID > 0 && e.movies[t.MovieID] {
			return ExcludeTitle, "you chose not to convert this film"
		}
	case "episode":
		if t.SeriesID > 0 && e.series[t.SeriesID] {
			return ExcludeTitle, "you chose not to convert this show"
		}
		if t.SeriesID > 0 && e.seasons[seasonRef{t.SeriesID, t.Season}] {
			return ExcludeTitle, "you chose not to convert this season"
		}
	}
	if e.folders != nil && path != "" {
		media := "movie"
		if t.Kind == "episode" {
			media = "series"
		}
		if f, ok := ownerFolder(e.folders, media, path); ok && f.skip {
			return ExcludeFolder, fmt.Sprintf("Convert leaves the library folder %q alone", f.Label)
		}
	}
	if len(e.res) > 0 && mi != nil {
		res := mi.Resolution
		if res == "" {
			res = resolutionLabel(mi.Width, mi.Height)
		}
		if res != "" && e.res[res] {
			return ExcludeResolution, "Convert leaves " + resolutionName(res) + " files alone"
		}
	}
	return "", ""
}

// ownerFolder is the library folder holding path: the longest of the media's folders it
// lies inside (a folder inside another belongs to itself, not its parent).
func ownerFolder(folders []skipFolder, media, path string) (skipFolder, bool) {
	var best skipFolder
	found := false
	for _, f := range folders {
		if f.Media != media || strings.TrimSpace(f.Path) == "" || !under(path, f.Path) {
			continue
		}
		if !found || len(filepath.Clean(f.Path)) > len(filepath.Clean(best.Path)) {
			best, found = f, true
		}
	}
	return best, found
}

// under reports whether p lies inside dir (not dir itself), on a separator boundary.
func under(p, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// loadExclusions reads every rule.
func (s *Service) loadExclusions(ctx context.Context) exclusions {
	e := exclusions{movies: map[int64]bool{}, series: map[int64]bool{}, seasons: map[seasonRef]bool{}, res: map[string]bool{}}
	var keyParts []string
	if s.db != nil {
		if rows, err := s.db.QueryContext(ctx, `SELECT kind, title_id, season FROM convert_exclusions ORDER BY kind, title_id, season`); err == nil {
			for rows.Next() {
				var kind string
				var id int64
				var season int
				if rows.Scan(&kind, &id, &season) != nil {
					continue
				}
				switch {
				case kind == "movie":
					e.movies[id] = true
				case kind == "series" && season < 0:
					e.series[id] = true
				case kind == "series":
					e.seasons[seasonRef{id, season}] = true
				}
				keyParts = append(keyParts, fmt.Sprintf("%s%d/%d", kind, id, season))
			}
			_ = rows.Close()
		}
	}
	for _, r := range splitCSV(s.settings.Get(ctx, keySkipResolutions, "")) {
		if c := normResolution(r); c != "" {
			e.res[c] = true
		}
	}
	for _, r := range resolutions {
		if e.res[r] {
			keyParts = append(keyParts, "res:"+r)
		}
	}
	if skip := s.skippedFolderKeys(ctx); len(skip) > 0 {
		folders := s.libraryFolders(ctx)
		for _, f := range folders {
			sf := skipFolder{LibraryFolder: f, skip: skip[f.Key]}
			e.folders = append(e.folders, sf)
			if sf.skip {
				// The path is part of the key: moving a left-out folder changes which files it holds.
				keyParts = append(keyParts, "dir:"+f.Key+"="+f.Path)
			}
		}
		if e.folders == nil {
			e.folders = []skipFolder{} // a rule is set; there's just no folder to apply it to
		}
	}
	e.key = strings.Join(keyParts, ",")
	return e
}

// skippedFolderKeys is the saved set of left-out folder keys.
func (s *Service) skippedFolderKeys(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	for _, k := range splitCSV(s.settings.Get(ctx, keySkipFolders, "")) {
		out[k] = true
	}
	return out
}

// skipFolderList and skipResolutionList are the saved rules for the settings page.
func (s *Service) skipFolderList(ctx context.Context) []string {
	out := []string{}
	for k := range s.skippedFolderKeys(ctx) {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *Service) skipResolutionList(ctx context.Context) []string {
	saved := map[string]bool{}
	for _, r := range splitCSV(s.settings.Get(ctx, keySkipResolutions, "")) {
		if c := normResolution(r); c != "" {
			saved[c] = true
		}
	}
	out := []string{}
	for _, r := range resolutions {
		if saved[r] {
			out = append(out, r)
		}
	}
	return out
}

// cleanSkipResolutions validates a resolution list and returns it as saved.
func cleanSkipResolutions(in []string) (string, error) {
	seen := map[string]bool{}
	for _, v := range in {
		c := normResolution(v)
		if c == "" {
			return "", fmt.Errorf("unknown resolution %q — use SD, 720p, 1080p or 4K", v)
		}
		seen[c] = true
	}
	var out []string
	for _, r := range resolutions {
		if seen[r] {
			out = append(out, r)
		}
	}
	return strings.Join(out, ","), nil
}

// cleanSkipFolders validates a folder list against the library's folders. A folder that is
// already saved stays valid even when it's no longer listed (removed outside the app), so
// switching another folder doesn't fail on it.
func (s *Service) cleanSkipFolders(ctx context.Context, in []string) (string, error) {
	known := map[string]bool{}
	for _, f := range s.libraryFolders(ctx) {
		known[f.Key] = true
	}
	saved := s.skippedFolderKeys(ctx)
	seen := map[string]bool{}
	var out []string
	for _, k := range in {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		if !known[k] && !saved[k] {
			return "", fmt.Errorf("%q isn't one of your Movies or TV library folders", k)
		}
		seen[k] = true
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ","), nil
}

// --- titles ------------------------------------------------------------------------------

// TitleExclusion is one title left out of Convert.
type TitleExclusion struct {
	Kind     string `json:"kind"` // "movie" | "series"
	MovieID  int64  `json:"movie_id,omitempty"`
	SeriesID int64  `json:"series_id,omitempty"`
	// Season is one season of a show; -1 = the whole title.
	Season    int    `json:"season"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
}

// ExcludeRequest turns "Don't convert this" on or off for a film, a show or one season.
type ExcludeRequest struct {
	Kind     string `json:"kind"` // "movie" | "series"
	MovieID  int64  `json:"movie_id"`
	SeriesID int64  `json:"series_id"`
	// Season, for a show: nil or -1 = the whole show.
	Season   *int `json:"season"`
	Excluded bool `json:"excluded"`
}

// ExcludeResult says what changed besides the rule itself.
type ExcludeResult struct {
	Excluded bool `json:"excluded"`
	// Dropped is how many files waiting in the Convert-now queue were taken off it.
	Dropped int `json:"dropped"`
	// Running names a conversion already under way for the title. It's left to finish (it
	// has been checked all along and replaces the file only once verified); cancel it from
	// Convert if you'd rather it didn't.
	Running string `json:"running,omitempty"`
}

// ErrBadExclusion is a request that names no film or show.
var ErrBadExclusion = errors.New("name a film (kind movie, movie_id) or a show (kind series, series_id)")

// ErrNoSuchTitle is a film or show that isn't in the library.
var ErrNoSuchTitle = errors.New("that title isn't in your library")

// TitleExclusions lists the titles left out, newest first. A title deleted from the library
// since is dropped from the list as it's read.
func (s *Service) TitleExclusions(ctx context.Context) ([]TitleExclusion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, title_id, season, title, created_at FROM convert_exclusions ORDER BY created_at DESC, title`)
	if err != nil {
		return nil, err
	}
	var all []TitleExclusion
	for rows.Next() {
		var x TitleExclusion
		var id int64
		if err := rows.Scan(&x.Kind, &id, &x.Season, &x.Title, &x.CreatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if x.Kind == "movie" {
			x.MovieID = id
		} else {
			x.SeriesID = id
		}
		all = append(all, x)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	out := []TitleExclusion{}
	for _, x := range all {
		if s.titleGone(ctx, x.Kind, x.MovieID+x.SeriesID) {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM convert_exclusions WHERE kind = ? AND title_id = ?`, x.Kind, x.MovieID+x.SeriesID)
			continue
		}
		out = append(out, x)
	}
	return out, nil
}

// titleGone reports whether a title is known to be deleted (not merely unreadable now).
func (s *Service) titleGone(ctx context.Context, kind string, id int64) bool {
	if kind == "movie" {
		if s.movies == nil {
			return false
		}
		_, err := s.movies.Get(ctx, id)
		return errors.Is(err, movies.ErrNotFound)
	}
	if s.series == nil {
		return false
	}
	_, err := s.series.GetSummary(ctx, id)
	return errors.Is(err, series.ErrNotFound)
}

// titleName is a title's name for the list, and whether it exists.
func (s *Service) titleName(ctx context.Context, kind string, id int64) (string, error) {
	if kind == "movie" {
		if s.movies == nil {
			return "", nil
		}
		m, err := s.movies.Get(ctx, id)
		if err != nil {
			return "", err
		}
		return m.Title, nil
	}
	if s.series == nil {
		return "", nil
	}
	sr, err := s.series.GetSummary(ctx, id)
	if err != nil {
		return "", err
	}
	return sr.Title, nil
}

// SetTitleExclusion turns "Don't convert this" on or off. Turning it on also takes the
// title's files off the Convert-now queue; a conversion already running is left to finish.
func (s *Service) SetTitleExclusion(ctx context.Context, r ExcludeRequest) (ExcludeResult, error) {
	var id int64
	season := -1
	switch r.Kind {
	case "movie":
		id = r.MovieID
	case "series":
		id = r.SeriesID
		if r.Season != nil && *r.Season >= 0 {
			season = *r.Season
		}
	}
	if id <= 0 {
		return ExcludeResult{}, ErrBadExclusion
	}
	res := ExcludeResult{Excluded: r.Excluded}
	if !r.Excluded {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM convert_exclusions WHERE kind = ? AND title_id = ? AND season = ?`, r.Kind, id, season); err != nil {
			return ExcludeResult{}, err
		}
		s.invalidateLibraryCache()
		s.wakeUp()
		return res, nil
	}
	title, err := s.titleName(ctx, r.Kind, id)
	if errors.Is(err, movies.ErrNotFound) || errors.Is(err, series.ErrNotFound) {
		return ExcludeResult{}, ErrNoSuchTitle
	}
	if err != nil {
		return ExcludeResult{}, err
	}
	if season >= 0 {
		title = fmt.Sprintf("%s — %s", title, seasonLabel(season))
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO convert_exclusions (kind, title_id, season, title) VALUES (?, ?, ?, ?)
		 ON CONFLICT(kind, title_id, season) DO UPDATE SET title = excluded.title`,
		r.Kind, id, season, title); err != nil {
		return ExcludeResult{}, err
	}
	// Waiting Convert-now requests for the title come off the queue: you've just said not to.
	for _, q := range s.requests.list(ctx) {
		if it, ok := parseKey(q.Key); ok && exclusionCovers(r.Kind, id, season, it) {
			s.requests.remove(ctx, q.Key)
			res.Dropped++
		}
	}
	s.mu.Lock()
	for key, j := range s.pending {
		if it, ok := parseKey(key); ok && exclusionCovers(r.Kind, id, season, it) && activeState(j.State) {
			res.Running = j.Title
			break
		}
	}
	s.mu.Unlock()
	s.invalidateLibraryCache()
	return res, nil
}

// exclusionCovers reports whether a title rule covers a library file.
func exclusionCovers(kind string, id int64, season int, it item) bool {
	if kind == "movie" {
		return it.Kind == "movie" && it.MovieID == id
	}
	return it.Kind == "episode" && it.SeriesID == id && (season < 0 || it.Season == season)
}

// seasonLabel is "Season 2", or "Specials" for season 0.
func seasonLabel(n int) string {
	if n == 0 {
		return "Specials"
	}
	return "Season " + strconv.Itoa(n)
}

// LeftOut counts the files each rule keeps from converting — files that would otherwise be
// worth converting — and the space they'd have freed, so the page can say why something
// isn't being converted.
type LeftOut struct {
	Title      int   `json:"title"`
	Folder     int   `json:"folder"`
	Resolution int   `json:"resolution"`
	SaveBytes  int64 `json:"save_bytes"`
}

func (l *LeftOut) add(n Needs) {
	if n.Excluded == "" || !n.leftOutWorth {
		return
	}
	switch n.Excluded {
	case ExcludeTitle:
		l.Title++
	case ExcludeFolder:
		l.Folder++
	case ExcludeResolution:
		l.Resolution++
	}
	l.SaveBytes += n.leftOutSave
}
