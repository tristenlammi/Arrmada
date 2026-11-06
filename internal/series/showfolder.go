package series

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/libfolders"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/parser"
)

// Which folder a show lives in.
//
// It used to be read off ONE episode file: two folders up when the file's folder was named
// "Season N", one up otherwise. Libraries whose season folders are called "S01", "Stagione
// 1", "Saison 1" or anything else got the season folder back as the show's folder, so the
// next import went to <TV folder>/S13/Season 12/… — a new folder every show with an "S13"
// then shared — and once one file was there, every later import followed it (#56).
//
// Now the show folder is the first folder below the TV folder that holds the file, which
// needs no guess about season folder names at all, and the show's folder is the one most
// of its files are in — never a folder named like a season, never a TV folder (or a folder
// named after one), never a folder other shows' files are in.

// SetSeasonFolderMatcher adds the configured season folder template to what counts as a
// season folder (library.Importer.IsSeasonFolderName), so a library Arrmada itself names
// "{title} Season {season}" is read right. Call it at startup, before anything runs.
func (s *Service) SetSeasonFolderMatcher(fn func(name string) bool) { s.seasonFolder = fn }

// seasonLike reports whether a folder name is a season folder (Specials included) —
// something inside a show's folder, which can never be the show's own folder. Extras
// folders don't count here: "Extras" is a show too.
func (s *Service) seasonLike(name string) bool {
	if _, ok := library.SeasonFolderNumber(name); ok {
		return true
	}
	return s.seasonFolder != nil && s.seasonFolder(name)
}

// insideShow reports whether a folder name is one a show keeps inside its folder: a
// season folder, Specials, or Extras. For the folder-name guess only.
func (s *Service) insideShow(name string) bool {
	return library.LooksLikeSeasonFolder(name) || s.seasonLike(name)
}

// ShowFolder is the show folder an episode file lives in, judged by folder names alone:
// one level up from a season folder ("Season 04", "S04", "Stagione 4", "Specials"), the
// file's own folder otherwise. It is the fallback for a file under none of the TV
// folders; inside one, Service.ShowFolderFor reads it from the TV folder instead.
func ShowFolder(episodePath string) string {
	parent := filepath.Dir(episodePath)
	if library.LooksLikeSeasonFolder(filepath.Base(parent)) {
		return filepath.Dir(parent)
	}
	return parent
}

// TVRoots is every TV folder now, the default first.
func (s *Service) TVRoots() []string { return s.libRoots() }

// TVRootOf is the TV folder holding path, and whether one does.
func (s *Service) TVRootOf(path string) (string, bool) { return rootOf(s.libRoots(), path) }

// rootOf is the TV folder holding path (the deepest, when one is inside another) and
// whether there is one.
func rootOf(roots []string, path string) (string, bool) {
	best := ""
	for _, r := range roots {
		if strings.TrimSpace(r) == "" || !libfolders.StrictlyUnder(path, r) {
			continue
		}
		if len(filepath.Clean(r)) > len(best) {
			best = filepath.Clean(r)
		}
	}
	return best, best != ""
}

// filePlace is where an episode file sits, read from the folders around it.
type filePlace struct {
	root string // the TV folder holding it ("" when it's under none)
	// dir is the folder the file belongs to for its show: the folder holding it, stepped
	// out of the show's own episode or season-pack download folder, then a disc folder
	// inside a season folder, then one season folder. "" for a file loose in a TV folder.
	dir string
	// structural: dir was reached by stepping out of a season folder, so it is the show's
	// folder however deep it is (/tv/Anime/Frieren/Season 1/…).
	structural bool
	// unsure: dir is below a folder inside the TV folder, holds the file directly, and is
	// named like a download or a disc ("Show 1080p", "Show S2", "CD2"): it may as well be
	// a season's folder inside the show's as the show's own, so it's never taken for it.
	unsure bool
}

// reStdSeasonDir is a season folder as every release named and read it: "Season 1",
// "Season.01", "Specials" — the ones whose show folder a delete removes once empty.
var reStdSeasonDir = regexp.MustCompile(`(?i)^(season[ ._-]?\d+|specials)$`)

// reDiscFolder is a disc folder inside a season folder ("Disc 1", "CD2").
var reDiscFolder = regexp.MustCompile(`(?i)^(?:disc|disk|cd|dvd|bd)[ ._-]*\d{1,2}$`)

// releaseNamed reports whether a folder's name reads as a download or a disc: a season or
// episode tag, a stated resolution, or "Disc 1".
func releaseNamed(name string) bool {
	if reDiscFolder.MatchString(name) {
		return true
	}
	r := parser.Parse(name)
	return r.Season > 0 || len(r.Episodes) > 0 || r.StatedResolution() != parser.ResUnknown
}

// episodeRelease reports whether a folder is a download's own folder for the show: an
// episode's ("Show.S01E01.1080p-GRP"), or a season pack's named for the show (titles, its
// parser.TitleKeys; nil: episodes only) with a release's tags ("Show.S01.1080p.WEB-DL-GRP").
// A bare quality tag, a year or a pack's season range never makes one: "Breaking Bad
// (2008) [1080p]", "Futurama S01-S07" and "Jujutsu Kaisen S2" are show folders.
func episodeRelease(name string, titles map[string]bool) bool {
	r := parser.Parse(name)
	if len(r.Episodes) > 0 {
		return true
	}
	if r.Season <= 0 || len(titles) == 0 || !titles[parser.TitleKey(r.Title)] {
		return false
	}
	return r.Group != "" && (r.StatedResolution() != parser.ResUnknown || r.Source != parser.SourceUnknown)
}

// placeOf reads where the episode file at path sits (filePlace). It never steps out into
// the TV folder itself. Outside every TV folder it's the folder-name guess (ShowFolder).
func (s *Service) placeOf(roots []string, path string, titles map[string]bool) filePlace {
	path = filepath.Clean(path)
	root, ok := rootOf(roots, path)
	if !ok {
		guess := filepath.Dir(path)
		if s.insideShow(filepath.Base(guess)) {
			guess = filepath.Dir(guess)
		}
		if guess == "" || guess == "." || guess == filepath.Dir(guess) {
			return filePlace{}
		}
		return filePlace{dir: guess}
	}
	dir := filepath.Dir(path)
	if !libfolders.StrictlyUnder(dir, root) {
		return filePlace{root: root} // loose in the TV folder
	}
	inside := func(d string) bool { return libfolders.StrictlyUnder(filepath.Dir(d), root) }
	if inside(dir) && episodeRelease(filepath.Base(dir), titles) {
		dir = filepath.Dir(dir)
	}
	structural := false
	switch {
	case inside(dir) && reDiscFolder.MatchString(filepath.Base(dir)) &&
		s.seasonLike(filepath.Base(filepath.Dir(dir))) && inside(filepath.Dir(dir)):
		dir, structural = filepath.Dir(filepath.Dir(dir)), true // Season 1/Disc 1
	case inside(dir) && s.seasonLike(filepath.Base(dir)):
		dir, structural = filepath.Dir(dir), true
	}
	unsure := !structural && !firstLevel(dir, root) && releaseNamed(filepath.Base(dir))
	return filePlace{root: root, dir: dir, structural: structural, unsure: unsure}
}

// firstLevel reports whether dir sits right inside root.
func firstLevel(dir, root string) bool {
	return root != "" && filepath.Clean(filepath.Dir(dir)) == filepath.Clean(root)
}

// ShowFolderFor is the folder the episode file at path is in, for a show (filePlace): the
// folder holding it, or the one above its season folder; the folder-name guess (ShowFolder)
// for a file under no TV folder. It says where the file IS — whether that's a sound folder
// for the show is ShowFolderOf's question — and never names a category above a show. ""
// for a file loose in a TV folder.
func (s *Service) ShowFolderFor(path string) string {
	pl := s.placeOf(s.libRoots(), path, nil)
	if pl.unsure {
		return filepath.Dir(filepath.Clean(path))
	}
	return pl.dir
}

// EpisodeFileFilter reports, for show sr whose folder is showDir, whether a video found in
// that folder may be one of its episodes, for a rescan or the duplicate check — the two
// read exactly the same files through it:
//
//   - a file the show has recorded always may;
//   - any other must sit where the show's episodes sit: right in showDir, in one of its
//     season folders (or a disc folder inside one), or in one of its own download folders —
//     never inside another folder in it (another show's, tracked or not; hidden; however
//     deep), and never in a folder that may as well be a season's (filePlace.unsure);
//   - and when its name carries a title: in a folder provably the show's own (proven), an
//     episode counts unless its name names another library show (conflicts); elsewhere the
//     name must fit the show (fitsShow, with its year and country checks). Another library
//     show's file dropped into one of its season folders is never claimed, nor offered as a
//     duplicate to delete.
func (s *Service) EpisodeFileFilter(ctx context.Context, sr Series, showDir string) func(path string) bool {
	j := s.dbJudge(ctx, sr.ID)
	showDir = filepath.Clean(showDir)
	recorded := map[string]bool{}
	recordedDirs := map[string]bool{}
	if paths, err := s.repo.EpisodeFilePaths(ctx, sr.ID); err == nil {
		for _, p := range paths {
			recorded[filepath.Clean(p)] = true
			recordedDirs[filepath.Dir(filepath.Clean(p))] = true
		}
	}
	root, inRoot := rootOf(j.roots, showDir+string(filepath.Separator)+"x")
	firstLevelShow := inRoot && filepath.Dir(showDir) == root
	stdPlace := func(path string) bool {
		d := filepath.Dir(path)
		return firstLevelShow && (d == showDir || (filepath.Dir(d) == showDir && reStdSeasonDir.MatchString(filepath.Base(d))))
	}
	proven, provenKnown := false, false
	provenDir := func() bool {
		if !provenKnown {
			proven, provenKnown = j.proven(showDir), true
		}
		return proven
	}
	return func(path string) bool {
		path = filepath.Clean(path)
		if recorded[path] {
			return true
		}
		if pl := s.placeOf(j.roots, path, j.titles); pl.unsure || filepath.Clean(pl.dir) != showDir {
			return false
		}
		rel := library.ParseEpisodeFile(path)
		key := parser.TitleKey(rel.Title)
		if key == "" {
			// A name without a title ("S01E01.mkv", "01.mkv") could be anyone's: only one
			// beside the show's own recorded files counts — never one in another folder,
			// such as a Season 1 folder of another show next to the show's S01. A show with
			// no files yet reads its own folder as before.
			return len(recorded) == 0 || recordedDirs[filepath.Dir(path)]
		}
		// Inside a folder that's provably the show's own, an episode is the show's unless its
		// name positively says it's another library show's: romaji, short or tagged names
		// ("Shingeki no Kyojin", "Frieren", "Top Gear 2023 S33E01") are the show's own files.
		// Elsewhere the name must fit the show.
		// Only where every release read it too: a show folder right in a TV folder, the
		// file right in it or in a "Season N"/"Specials" folder. An "S01"-style or category
		// layout keeps the strict fit (v0.3.3 read nothing there).
		if len(rel.Episodes) > 0 && stdPlace(path) && provenDir() {
			return !j.conflicts(rel)
		}
		return fitsShow(rel, sr)
	}
}

// SeasonDirChooser picks, for an episode import of show seriesID, the season folder in its
// show folder (library.SeasonDirChooser):
//
//   - the one folder in the show folder all of that season's recorded files are in,
//     whatever it's called ("Stagione 1", "Staffel 01", "Series 1", "S12"), so a new episode
//     goes next to the others rather than into a new "Season 1" beside them;
//   - refused when that folder is gone (a share that's away, a season folder deleted by
//     hand): a parallel season folder is never made in its place;
//   - otherwise — no files of that season yet, or in several folders, or loose in the show
//     folder — the scheme's folder, or an existing one it joins, as every release did.
func (s *Service) SeasonDirChooser(ctx context.Context, seriesID int64) func(showDir string, season int, def string) (string, error) {
	return func(showDir string, season int, def string) (string, error) {
		paths, err := s.repo.SeasonFilePaths(ctx, seriesID, season)
		if err != nil || len(paths) == 0 {
			return def, nil
		}
		showDir = filepath.Clean(showDir)
		d := filepath.Dir(filepath.Clean(paths[0]))
		for _, p := range paths {
			if filepath.Dir(filepath.Clean(p)) != d {
				return def, nil
			}
		}
		if filepath.Dir(d) != showDir {
			return def, nil // loose in the show folder, or somewhere else: the scheme decides
		}
		// The season's episodes are all in d: the next one goes beside them, even when a
		// folder the scheme would join is there too (a "Season 1" of someone else's files
		// next to the show's own "S01").
		fi, err := os.Stat(d)
		switch {
		case err == nil && fi.IsDir():
			return d, nil
		case errors.Is(err, fs.ErrNotExist):
			return "", fmt.Errorf("%w: season %d's folder %s isn't there (a share that's away, or a folder removed by hand) — rescan the show once it's back", ErrSeasonFolderAway, season, d)
		default:
			return "", fmt.Errorf("%w: season %d's folder %s can't be read: %v", ErrSeasonFolderAway, season, d, err)
		}
	}
}

// ErrSeasonFolderAway is an episode import refused because the season's folder, where the
// show's other episodes of that season are, isn't there.
var ErrSeasonFolderAway = errors.New("the season's folder isn't there")

// NoFilesFolderUnproven says why dir — "<Title> (<Year>)" for show sr, which has no files
// recorded yet — can't be read for it ("" when it can): it isn't a real folder right inside
// a TV folder, it's not provably a show's own (named like a season, another show's files in
// it, another show's folder, episode or a link inside it), or another library show has the
// same title and year.
func (s *Service) NoFilesFolderUnproven(ctx context.Context, sr Series, dir string) string {
	dir = filepath.Clean(dir)
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return fmt.Sprintf("%s isn't a folder", dir)
	}
	if root, ok := rootOf(s.libRoots(), dir); !ok || filepath.Dir(dir) != root {
		return fmt.Sprintf("%s isn't right inside a TV folder", dir)
	}
	j := s.dbJudge(ctx, sr.ID)
	if v := j.judge(dir); v.why != "" {
		return v.why
	}
	if why := j.foreignChildren(dir); why != "" {
		return why
	}
	for _, o := range j.otherShows() {
		if parser.TitleKey(o.Title) == parser.TitleKey(sr.Title) && o.Year == sr.Year {
			return fmt.Sprintf("%q (%d) is in the library twice", o.Title, o.Year)
		}
	}
	return ""
}

// MoveBlocked says why Move to folder must not move show seriesID's folder ("" when it may):
// its folder can't be told (FolderUnclear), or the folder holds — at any depth — an episode
// it hasn't recorded whose name doesn't say it's this show's (strictly: fitsShow, no benefit
// of the doubt), or that names another library show. Moving the folder would carry that
// file along. Any size counts, samples too; only videos not shaped like an episode, and Plex
// extras by name (trailer, theme, featurette…), don't.
func (s *Service) MoveBlocked(ctx context.Context, seriesID int64) string {
	if why := s.FolderUnclear(ctx, seriesID); why != "" {
		return why
	}
	info := s.ShowFolderOf(ctx, seriesID)
	if info.Path == "" {
		return ""
	}
	j := s.dbJudge(ctx, seriesID)
	if j.show == nil {
		return ""
	}
	why := ""
	_ = filepath.WalkDir(info.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			why = fmt.Sprintf("%s can't be read: %v", p, err)
			return filepath.SkipAll
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		n := d.Name()
		// Whatever its size, sample or not: Move is rare and a refusal cheap, while carrying
		// a small other-show episode along is not.
		if !library.IsVideoFile(n) || j.isRecorded(p) || rePlexExtraName.MatchString(n) {
			return nil
		}
		rel := parser.Parse(n)
		if len(rel.Episodes) == 0 {
			return nil
		}
		switch {
		case j.conflicts(rel):
			why = fmt.Sprintf("%s holds %s, an episode of another show in your library", info.Path, p)
		case !fitsShow(rel, *j.show):
			why = fmt.Sprintf("%s holds %s, an episode Arrmada hasn't recorded for %s whose name doesn't say it's this show's; move it out (or import it) first", info.Path, p, j.show.Title)
		default:
			return nil
		}
		return filepath.SkipAll
	})
	return why
}

// yearFolderHint is "Title (Year)" for show seriesID, for a message.
func (s *Service) yearFolderHint(ctx context.Context, seriesID int64) string {
	sr, err := s.repo.Get(ctx, seriesID)
	if err != nil {
		return "Title (Year)"
	}
	if sr.Year > 0 {
		return fmt.Sprintf("%s (%d)", sr.Title, sr.Year)
	}
	return sr.Title
}

// FolderUnclear says why show seriesID's folder can't be used ("" when it can): exactly the
// check an import makes (ShowFolderOf, then the folder's own verdict). Move to folder
// refuses such a show, so a category holding other shows is never carried along.
func (s *Service) FolderUnclear(ctx context.Context, seriesID int64) string {
	info := s.ShowFolderOf(ctx, seriesID)
	if info.Unknown {
		return info.Why
	}
	if info.Path != "" {
		if v := s.dbJudge(ctx, seriesID).judge(filepath.Clean(info.Path)); v.why != "" {
			return v.why
		}
	}
	return ""
}

// FileFitsShow reports whether the name of the file at path says it's show sr's episode
// (fitsShow), read as a file on disk (library.ParseEpisodeFile) like the rescan reads it:
// the duplicate check offers only such files — or ones the show recorded — for deletion,
// never one read on the benefit of the doubt in the show's own folder.
func FileFitsShow(path string, sr Series) bool { return fitsShow(library.ParseEpisodeFile(path), sr) }

// fitsShow reports whether a file's parsed name is show sr's: FitRelease against its title
// and aliases, or against its display title or title override — every one with the year
// and country checks, so "The Office (2001)" or "The Office UK" is never the 2005 US show.
func fitsShow(rel parser.Release, sr Series) bool {
	if FitRelease(rel, sr).OK {
		return true
	}
	key := parser.TitleKey(rel.Title)
	for _, t := range []string{sr.DisplayTitle, sr.TitleOverride} {
		if t == "" || parser.TitleKey(t) != key {
			continue
		}
		alt := sr
		alt.Title, alt.Aliases = t, nil
		if FitRelease(rel, alt).OK {
			return true
		}
	}
	return false
}

// ShowFoldersOf answers, for each episode file of show seriesID, the folder of the show it
// is in: the folder ShowFolderOf would count it towards, else where it is (ShowFolderFor).
// For Move to folder, which moves a show's folders whole.
func (s *Service) ShowFoldersOf(ctx context.Context, seriesID int64) func(path string) string {
	j := s.dbJudge(ctx, seriesID)
	return func(path string) string {
		if home, _, _ := j.homeOf(path); home != "" {
			return home
		}
		return s.ShowFolderFor(path)
	}
}

// ShowFolderInfo is where a show lives on disk, as far as its episode files tell.
type ShowFolderInfo struct {
	// Path is the show's folder ("" when it has no files, or none in a folder of its own).
	Path string `json:"path"`
	// Files is how many of the show's files are in Path; Total how many it has.
	Files int `json:"files"`
	Total int `json:"total"`
	// Unknown: the show has files, but every folder they're in is unsound — named like a
	// season, a TV folder, or holding other shows' files. Nothing may be imported for the
	// show until that is sorted out; Why says what was found, in plain words.
	Unknown bool   `json:"unknown"`
	Why     string `json:"why,omitempty"`
	// sharedMost: Unknown because most of the show's files are in a folder other shows'
	// files are in too. None of them is "misplaced" then: which show owns the folder is the
	// person's call.
	sharedMost bool
}

// Why a folder can't be a show's own.
const (
	unsoundSeason = "season" // named like a season folder
	unsoundRoot   = "root"   // a TV folder, or named after one
	unsoundShared = "shared" // other shows' files are in it
)

// folderJudge decides which folders can be a show's own. others lists up to a few OTHER
// series with files under a folder; names describes them for a message.
type folderJudge struct {
	svc    *Service
	roots  []string
	others func(dir string) []int64
	names  func(ids []int64) string
	// titles are the show's names (parser.TitleKey), for telling its own season-pack
	// download folders (episodeRelease).
	titles map[string]bool
	// disk: the chosen folder may be looked at on disk (foreignChildren). Off for the
	// health check, which reads the database only.
	disk  bool
	cache map[string]folderVerdict
	// show is the show judged for, and recorded its recorded files (read once, when
	// foreignChildren first needs them): a video loose in its folder that's neither is
	// another show's. nil for the health check.
	show      *Series
	recorded  func() map[string]bool
	rec       map[string]bool
	otherList []Series // otherShows, read once
}

// folderVerdict is a folder judged: why it can't be a show's own ("" when it can), the
// kind of reason (unsound*), and how many other shows' files are in it.
type folderVerdict struct {
	dir, why, kind string
	others         int
}

func (j *folderJudge) judge(dir string) folderVerdict {
	if v, ok := j.cache[dir]; ok {
		return v
	}
	v := j.verdict(dir)
	if j.cache == nil {
		j.cache = map[string]folderVerdict{}
	}
	j.cache[dir] = v
	return v
}

func (j *folderJudge) verdict(dir string) folderVerdict {
	base := filepath.Base(dir)
	if j.svc.seasonLike(base) {
		return folderVerdict{dir: dir, why: fmt.Sprintf("%s is named like a season folder", dir), kind: unsoundSeason}
	}
	for _, r := range j.roots {
		if filepath.Clean(r) == filepath.Clean(dir) {
			return folderVerdict{dir: dir, why: fmt.Sprintf("%s is a TV folder itself", dir), kind: unsoundRoot}
		}
	}
	// A folder named after the TV folder it's in (<TV>/TV/…) is what the old flat-layout
	// bug made. Only that TV folder's name counts: /tv/Anime is a show folder in /tv even
	// when another TV folder is /media/Anime.
	if root, ok := rootOf(j.roots, dir); ok && strings.EqualFold(filepath.Base(root), base) {
		return folderVerdict{dir: dir, why: fmt.Sprintf("%s is named after the TV folder %s", dir, root), kind: unsoundRoot}
	}
	if others := j.others(dir); len(others) > 0 {
		names := "other shows"
		if j.names != nil {
			names = j.names(others)
		}
		return folderVerdict{dir: dir, why: fmt.Sprintf("%s also holds episodes of %s", dir, names), kind: unsoundShared, others: len(others)}
	}
	return folderVerdict{dir: dir}
}

// dbJudge judges folders for one show against the database.
func (s *Service) dbJudge(ctx context.Context, seriesID int64) *folderJudge {
	titles := map[string]bool{}
	var show *Series
	if sr, err := s.repo.Get(ctx, seriesID); err == nil {
		sr.Aliases = s.repo.Aliases(ctx, seriesID)
		show = &sr
		for _, t := range []string{sr.Title, sr.DisplayTitle, sr.TitleOverride} {
			if k := parser.TitleKey(t); k != "" {
				titles[k] = true
			}
		}
	}
	return &folderJudge{
		svc:    s,
		roots:  s.libRoots(),
		others: func(dir string) []int64 { return s.repo.OtherSeriesUnder(ctx, seriesID, dir, 3) },
		names:  func(ids []int64) string { return s.describeSeries(ctx, ids) },
		titles: titles,
		disk:   true,
		show:   show,
		recorded: func() map[string]bool {
			out := map[string]bool{}
			if paths, err := s.repo.EpisodeFilePaths(ctx, seriesID); err == nil {
				for _, p := range paths {
					out[filepath.Clean(p)] = true
				}
			}
			return out
		},
	}
}

// describeSeries names a few shows for a message.
func (s *Service) describeSeries(ctx context.Context, ids []int64) string {
	var names []string
	for _, id := range ids {
		if sr, err := s.repo.Get(ctx, id); err == nil {
			names = append(names, fmt.Sprintf("%q", sr.Title))
		}
	}
	if len(names) == 0 {
		return "other shows"
	}
	if len(ids) >= 3 {
		return strings.Join(names, ", ") + " and possibly more"
	}
	return strings.Join(names, " and ")
}

// unsoundUnsure: a folder that may as well be a season's folder inside the show's as the
// show's own (filePlace.unsure), or a show whose files don't settle on one folder.
const unsoundUnsure = "unsure"

// homeOf is the sound folder the file at path belongs to ("" when none), the verdict on
// the folder that was turned down (zero when none was), and whether the file is in a
// folder at all (false: loose in a TV folder). It is never a folder above the one holding
// the file, except the show's folder above its season folder (filePlace).
func (j *folderJudge) homeOf(path string) (string, folderVerdict, bool) {
	pl := j.svc.placeOf(j.roots, path, j.titles)
	if pl.dir == "" {
		return "", folderVerdict{}, false
	}
	if pl.unsure {
		return "", folderVerdict{dir: pl.dir, kind: unsoundUnsure,
			why: fmt.Sprintf("%s is named like a download or disc folder and isn't right inside the TV folder", pl.dir)}, true
	}
	if v := j.judge(pl.dir); v.why != "" {
		return "", v, true
	}
	return pl.dir, folderVerdict{}, true
}

// foreignChildren says why dir looks like a folder of several shows rather than one ("" when
// it doesn't): a folder in it holding videos that isn't a season, specials, extras or disc
// folder, or one of the show's own download folders — another show's, tracked or not.
func (j *folderJudge) foreignChildren(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		n := e.Name()
		// A link (to a folder or a video, hidden or not) may lead to another show's: Move to
		// folder would carry it along, and nothing tells whose it is.
		if e.Type()&fs.ModeSymlink != 0 && library.LinkMayHoldVideo(filepath.Join(dir, n)) {
			return fmt.Sprintf("%s holds %q, a link", dir, n)
		}
		if !e.IsDir() {
			// An episode loose in the folder that isn't one of the show's recorded files and
			// is named as another show's is that show's ("Peppa Pig - S01E01.mkv" in Kids). A
			// theme song, trailer, sample or anything else not shaped like an episode isn't.
			if j.show != nil && j.looseForeignEpisode(filepath.Join(dir, n)) {
				return fmt.Sprintf("%s also holds %q, another show's episode", dir, n)
			}
			continue
		}
		if strings.HasPrefix(n, ".") || library.SkipScanDir(n) {
			continue
		}
		if !j.svc.seasonLike(n) && library.LooksLikeSeasonFolder(n) && !reDiscFolder.MatchString(n) {
			// An extras-like folder ("Other", "Bonus", "Extras") holds featurettes, loose: one
			// with a folder of videos inside (Other, then TNG, then Season 1) holds another show.
			if holdsVideoInSubfolder(filepath.Join(dir, n)) {
				return fmt.Sprintf("%s also holds %q, an extras folder with another show's folder in it", dir, n)
			}
			continue
		}
		// Jellyfin's / Kodi's backdrop and theme folders hold theme videos, not episodes.
		if reThemeFolder.MatchString(n) && !holdsEpisodeVideo(filepath.Join(dir, n)) {
			continue
		}
		if j.svc.insideShow(n) || reDiscFolder.MatchString(n) || episodeRelease(n, j.titles) {
			continue
		}
		if holdsVideo(filepath.Join(dir, n), 4) {
			return fmt.Sprintf("%s also holds %q, a folder with videos that isn't one of the show's season folders", dir, n)
		}
	}
	return ""
}

// rePlexExtraName is a video named as a Plex local extra ("Show - Trailer.mkv",
// "theme.mp4", "Pilot-featurette.mkv"): never an episode.
var rePlexExtraName = regexp.MustCompile(`(?i)(?:^|[ ._-])(?:trailer|theme|featurette|behind ?the ?scenes|deleted ?scenes?|interview|short|other)(?:[ ._-]?\d+)?\.[a-z0-9]+$`)

// looseForeignEpisode reports whether the file at path, loose in a folder, is another
// show's episode: a real video (by the library scan's own size and sample rules, not a
// Plex extra) shaped like an episode (S01E05, 1x05), not recorded for the show, and either
// not named as the show's or naming another show in the library.
func (j *folderJudge) looseForeignEpisode(path string) bool {
	n := filepath.Base(path)
	if !library.IsVideoFile(n) || j.isRecorded(path) || rePlexExtraName.MatchString(n) ||
		strings.Contains(strings.ToLower(n), "sample") {
		return false
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() < library.MinEpisodeVideoSize {
		return false
	}
	rel := parser.Parse(n)
	if len(rel.Episodes) == 0 {
		return false
	}
	return !fitsShow(rel, *j.show) || j.conflicts(rel)
}

// conflicts reports whether a file's parsed name positively says it's another library
// show's: it names a show in the library this one isn't called, or — the two sharing a
// title — carries a year or country tag that fits the other and not this one.
func (j *folderJudge) conflicts(rel parser.Release) bool {
	key := parser.TitleKey(rel.Title)
	if key == "" || j.show == nil {
		return false
	}
	for _, o := range j.otherShows() {
		if parser.TitleKey(o.Title) != key {
			continue
		}
		if !j.titles[key] {
			return true // another show's title, not this one's
		}
		if FitRelease(rel, o).OK && !fitsShow(rel, *j.show) {
			return true
		}
	}
	return false
}

// otherShows is every other show in the library, read once.
func (j *folderJudge) otherShows() []Series {
	if j.otherList == nil {
		j.otherList = []Series{}
		if all, err := j.svc.repo.List(context.Background()); err == nil {
			for _, o := range all {
				if j.show == nil || o.ID != j.show.ID {
					j.otherList = append(j.otherList, o)
				}
			}
		}
	}
	return j.otherList
}

// proven reports whether dir is provably the show's own folder: a sound folder (not named
// like a season or a TV folder, no other show's files in it) holding no other show's folder,
// episode or link. Inside it, an episode's name only has to not conflict (see conflicts).
func (j *folderJudge) proven(dir string) bool {
	return j.judge(filepath.Clean(dir)).why == "" && j.foreignChildren(filepath.Clean(dir)) == ""
}

// reThemeFolder is a media server's theme or backdrop folder inside a show's ("backdrops",
// "theme-music", "theme-videos").
var reThemeFolder = regexp.MustCompile(`(?i)^(?:backdrops?|theme[ ._-]?(?:music|videos?|songs?)|themes?)$`)

// holdsEpisodeVideo reports whether dir holds, at any depth, a video named like an episode
// (S01E05, 1x05). An unreadable folder counts.
func holdsEpisodeVideo(dir string) bool {
	found := false
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			found = true
			return filepath.SkipAll
		}
		if !d.IsDir() && library.IsVideoFile(d.Name()) && len(parser.Parse(d.Name()).Episodes) > 0 {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found || err != nil
}

// isRecorded reports whether path is one of the show's recorded files.
func (j *folderJudge) isRecorded(path string) bool {
	if j.rec == nil {
		j.rec = map[string]bool{}
		if j.recorded != nil {
			j.rec = j.recorded()
		}
	}
	return j.rec[filepath.Clean(path)]
}

// holdsVideoInSubfolder reports whether a folder inside dir holds a video, at any depth.
func holdsVideoInSubfolder(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && holdsVideo(filepath.Join(dir, e.Name()), 16) {
			return true
		}
	}
	return false
}

// holdsVideo reports whether dir holds a video file within depth folders.
func holdsVideo(dir string, depth int) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			if library.IsVideoFile(e.Name()) {
				return true
			}
			continue
		}
		if depth > 1 && !library.SkipScanDir(e.Name()) && holdsVideo(filepath.Join(dir, e.Name()), depth-1) {
			return true
		}
	}
	return false
}

// showFolderFrom is ShowFolderOf for a show's files, judged by j.
//
// The folder holding most of the show's files wins, then must pass two checks, or nothing
// is guessed: no other folder that got files may be inside it or above it, and (on disk)
// it may hold no folder with videos that isn't its own season, extras or disc folder.
func (s *Service) showFolderFrom(paths []string, j *folderJudge) ShowFolderInfo {
	info := ShowFolderInfo{Total: len(paths)}
	if len(paths) == 0 {
		return info
	}
	counts := map[string]int{}
	// Files in folders that are probably the show's but can't be used: other shows' files
	// are there too, or it can't be told whether the folder is the show's (unsure).
	held := map[string]int{}
	heldWhy := map[string]string{}
	var whys []string
	placed := false // any file in a folder below a TV folder (or outside them)
	for _, p := range paths {
		home, first, inFolder := j.homeOf(p)
		placed = placed || inFolder
		switch {
		case home != "":
			counts[home]++
		case first.why != "":
			whys = append(whys, first.why)
			if first.kind == unsoundShared || first.kind == unsoundUnsure {
				held[first.dir]++
				heldWhy[first.dir] = first.why
			}
		}
	}
	if len(counts) == 0 {
		if !placed {
			return info // only loose files in a TV folder: a new folder from the scheme
		}
		info.Unknown = true
		info.Why = firstDistinct(whys)
		return info
	}
	dirs := make([]string, 0, len(counts))
	for d := range counts {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, k int) bool {
		if counts[dirs[i]] != counts[dirs[k]] {
			return counts[dirs[i]] > counts[dirs[k]]
		}
		return dirs[i] < dirs[k]
	})
	best := dirs[0]
	unsure := func(why string) ShowFolderInfo {
		info.Unknown, info.sharedMost, info.Why = true, true, why
		return info
	}
	// A stray folder holding a few of the show's files never wins over a folder holding
	// more of them that another show's files are in too, or that can't be told to be the
	// show's: that one is probably the show's real folder, and the person decides.
	for dir, n := range held {
		if n > counts[best] {
			return unsure(heldWhy[dir])
		}
	}
	if len(dirs) > 1 && counts[dirs[1]] == counts[best] {
		return unsure(fmt.Sprintf("its files are split evenly between %s and %s", best, dirs[1]))
	}
	for _, d := range append(dirs[1:], keys(held)...) {
		if libfolders.StrictlyUnder(d, best) || libfolders.StrictlyUnder(best, d) {
			return unsure(fmt.Sprintf("its files are in %s and in %s, one inside the other", best, d))
		}
	}
	if j.disk {
		if why := j.foreignChildren(best); why != "" {
			return unsure(why)
		}
	}
	info.Path, info.Files = best, counts[best]
	return info
}

func keys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ShowFolderOf works out the folder a show lives in from all of its episode files: the
// sound folder (see folderJudge) holding most of them. Files already misplaced by the old
// season-folder bug — in <TV folder>/S13/Season 12 — are outvoted by the show's real
// folder, and never chosen on their own.
func (s *Service) ShowFolderOf(ctx context.Context, seriesID int64) ShowFolderInfo {
	paths, err := s.repo.EpisodeFilePaths(ctx, seriesID)
	if err != nil {
		return ShowFolderInfo{Unknown: true, Why: "the show's files couldn't be read: " + err.Error()}
	}
	j := s.dbJudge(ctx, seriesID)
	kept := s.dropGoneMisplaced(paths, j)
	if len(paths) > 0 && len(kept) == 0 {
		// Every file it had was in a folder no show should own and is gone now — moved back
		// by hand, deleted, or on a share that's down. Where the show lives isn't known; it
		// isn't a new show.
		return ShowFolderInfo{Total: len(paths), Unknown: true, Why: "the files it had are no longer where they were recorded; put them in a folder named with the show's year, such as \"" + s.yearFolderHint(ctx, seriesID) + "\", and rescan it, or delete the show in Arrmada keeping its files and use Scan library"}
	}
	return s.showFolderFrom(kept, j)
}

// followVisitLimit bounds FollowMovedEpisodeFiles' walk of a TV folder.
const followVisitLimit = 500_000

// FollowMovedEpisodeFiles is the way out for a show whose folder can't be told: once its
// files are moved by hand into a folder of its own — out of a folder named like a season,
// a folder another show is in, or anywhere — each recorded file that's gone is re-pointed
// at the one file of the same name now in that TV folder. Only a name that says it is this
// show's episode (its title and an episode number) is followed, never one like "01.mkv";
// nothing is followed while the TV folder looks unmounted; only a regular file no other
// show has recorded counts, and only when exactly one has that name; hidden and system
// folders (recycle bins, thumbnails) are never looked in, nor are links followed. Only a
// file recorded where no show should be is looked for — one gone from the show's own
// folder is deleted or on a share that's away — and only into a folder named with the
// show's own year or already holding its files: a year-less "Show" may be a namesake's.
// Nothing is written unless, with every re-point, the show then has a sound folder
// of its own holding every followed file: a show that would still be refused keeps its
// records as they were. It returns how many files it re-pointed. Refresh & rescan calls it
// for a show it would refuse, then judges the show again.
func (s *Service) FollowMovedEpisodeFiles(ctx context.Context, seriesID int64) int {
	paths, err := s.repo.EpisodeFilePaths(ctx, seriesID)
	if err != nil {
		return 0
	}
	sr, err := s.Get(ctx, seriesID)
	if err != nil {
		return 0
	}
	roots := s.libRoots()
	j := s.dbJudge(ctx, seriesID)
	// The show folders this show's files still on disk are in: a followed file may go into
	// one of them, or into a folder named with the show's own year — never anywhere else.
	homes := map[string]bool{}
	for _, p := range paths {
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		if home, _, _ := j.homeOf(p); home != "" {
			homes[home] = true
		}
	}
	want := map[string][]string{} // file name -> gone recorded paths with it
	walk := map[string]bool{}     // TV folders to look in
	for _, p := range paths {
		p = filepath.Clean(p)
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			continue // still there, or can't tell
		}
		// Only a file recorded where no show should be (a folder named like a season, one
		// other shows are in, a TV folder) is followed. One gone from a sound folder of the
		// show's is deleted or on a share that's away: it's never looked for elsewhere.
		if home, _, _ := j.homeOf(p); home != "" {
			continue
		}
		root, ok := rootOf(roots, p)
		if !ok || !library.FolderLooksMounted(root) {
			continue // a TV folder that's down: nothing is gone, nothing is followed
		}
		// Only a name that says it's this show's episode is looked for: "01.mkv" or
		// "Episode 1.mkv" could be anyone's.
		rel := parser.Parse(filepath.Base(p))
		key := parser.TitleKey(rel.Title)
		if len(rel.Episodes) == 0 || key == "" || !fitsShow(rel, sr) {
			continue
		}
		want[filepath.Base(p)] = append(want[filepath.Base(p)], p)
		walk[root] = true
	}
	if len(want) == 0 {
		return 0
	}
	found := map[string][]string{}
	for root := range walk {
		visited := 0
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if visited++; visited > followVisitLimit || ctx.Err() != nil {
				return filepath.SkipAll
			}
			if d.IsDir() {
				if p != root && strings.ContainsAny(d.Name()[:1], ".#@$") {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() && len(want[d.Name()]) > 0 {
				found[d.Name()] = append(found[d.Name()], p)
			}
			return nil
		})
	}
	// Work out every re-point first, in memory.
	type move struct {
		from, to string
		size     int64
	}
	var moves []move
	repl := map[string]string{}
	for name, olds := range want {
		if len(found[name]) != 1 || len(olds) != 1 {
			continue // not there, or more than one: the person decides
		}
		to := found[name][0]
		if s.repo.FileRecorded(ctx, to) {
			continue // another show's (or already this show's) file
		}
		// Only into a folder that's provably this show's: one its files already are in, or
		// one named with its own year ("Show (2019)" for the 2019 show). A year-less "Show"
		// may as well be a namesake's (the 2012 show of that title), and is never followed into.
		pl := s.placeOf(roots, to, j.titles)
		if pl.dir == "" || pl.unsure {
			continue
		}
		if !homes[pl.dir] && (sr.Year <= 0 || parser.Parse(filepath.Base(pl.dir)).Year != sr.Year) {
			continue
		}
		fi, err := os.Stat(to)
		if err != nil {
			continue
		}
		moves = append(moves, move{olds[0], to, fi.Size()})
		repl[olds[0]] = to
	}
	if len(moves) == 0 {
		return 0
	}
	// Then judge the show as it would read with them: nothing is written unless it then has
	// a sound folder of its own that every followed file is in. A show that would still be
	// refused keeps its records exactly as they were.
	after := make([]string, 0, len(paths))
	for _, p := range paths {
		if to, ok := repl[filepath.Clean(p)]; ok {
			p = to
		}
		after = append(after, p)
	}
	info := s.showFolderFrom(s.dropGoneMisplaced(after, j), j)
	if info.Unknown || info.Path == "" {
		return 0
	}
	for _, m := range moves {
		if home, _, _ := j.homeOf(m.to); home != info.Path {
			return 0
		}
	}
	moved := 0
	for _, m := range moves {
		if n, err := s.repo.RepointEpisodeFile(ctx, seriesID, m.from, m.to, m.size); err == nil && n > 0 {
			moved++
		}
	}
	if moved > 0 {
		s.repo.AddEvent(ctx, seriesID, "files.followed", fmt.Sprintf("Found %d moved episode file(s) in their new folder", moved))
	}
	return moved
}

// dropGoneMisplaced leaves out files recorded in a folder no show should own that are no
// longer there — moved back by hand before a rescan caught up — so they don't keep a show
// from its folder. Only those files are looked at on disk; every other path is kept as
// recorded, so a share that's down never changes where a show lives.
func (s *Service) dropGoneMisplaced(paths []string, j *folderJudge) []string {
	gone := s.fileGone
	if gone == nil {
		gone = func(p string) bool {
			_, err := os.Stat(p)
			return errors.Is(err, fs.ErrNotExist)
		}
	}
	out := paths[:0:0]
	for _, p := range paths {
		if home, _, inFolder := j.homeOf(p); home == "" && inFolder && gone(p) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// MisplacedFile is one of a show's episode files that is outside its show's folder, in a
// folder no show should own: one named like a season folder right under the TV folder
// (<TV>/S13/Season 12, what #56 made), a TV folder itself, or — when the show does have a
// folder of its own — a folder other shows' files are in.
type MisplacedFile struct {
	Path string `json:"path"`
	Why  string `json:"why"`
}

// misplacedFrom lists the misplaced files among a show's files (see MisplacedFile).
func (s *Service) misplacedFrom(info ShowFolderInfo, paths []string, j *folderJudge) []MisplacedFile {
	if info.sharedMost {
		return nil // most of its files are in a shared folder: nothing to put "back"
	}
	var out []MisplacedFile
	for _, p := range paths {
		if info.Path != "" && (libfolders.StrictlyUnder(p, info.Path)) {
			continue
		}
		home, first, inFolder := j.homeOf(p)
		if home != "" || !inFolder || first.why == "" {
			continue // in a sound folder (a second one is the owner's business), or loose
		}
		if first.kind == unsoundUnsure {
			continue // may well be the show's own folder: not "misplaced"
		}
		if first.kind == unsoundShared && info.Path == "" {
			continue // two shows in one ordinary folder, and nowhere better to put either
		}
		out = append(out, MisplacedFile{Path: p, Why: first.why})
	}
	return out
}

// MisplacedFiles is where the show lives and its episode files that are elsewhere, in a
// folder no show should own (MisplacedFile).
func (s *Service) MisplacedFiles(ctx context.Context, seriesID int64) (ShowFolderInfo, []MisplacedFile, error) {
	paths, err := s.repo.EpisodeFilePaths(ctx, seriesID)
	if err != nil {
		return ShowFolderInfo{}, nil, err
	}
	j := s.dbJudge(ctx, seriesID)
	paths = s.dropGoneMisplaced(paths, j)
	info := s.showFolderFrom(paths, j)
	return info, s.misplacedFrom(info, paths, j), nil
}

// MisplacedShow is a show with episode files outside its folder, for the health check.
type MisplacedShow struct {
	SeriesID int64  `json:"series_id"`
	Title    string `json:"title"`
	Files    int    `json:"files"`
	// NoFolder: none of its files are in a folder of its own.
	NoFolder bool `json:"no_folder"`
}

// MisplacedShows lists every show with misplaced episode files (MisplacedFiles), worked
// out from the database alone in one pass — it never looks at the disk, so it can't wake
// a sleeping array.
func (s *Service) MisplacedShows(ctx context.Context) ([]MisplacedShow, error) {
	rows, err := s.repo.AllEpisodeFilePaths(ctx)
	if err != nil {
		return nil, err
	}
	roots := s.libRoots()
	// Every folder above each file, up to its TV folder, with the shows that have files in
	// it: "other shows under this folder" without a query per folder.
	under := map[string]map[int64]bool{}
	byShow := map[int64][]string{}
	for _, r := range rows {
		byShow[r.SeriesID] = append(byShow[r.SeriesID], r.Path)
		root, inRoot := rootOf(roots, r.Path)
		for d := filepath.Dir(filepath.Clean(r.Path)); ; d = filepath.Dir(d) {
			if inRoot && !libfolders.StrictlyUnder(d, root) {
				break
			}
			if d == filepath.Dir(d) {
				break
			}
			if under[d] == nil {
				under[d] = map[int64]bool{}
			}
			under[d][r.SeriesID] = true
		}
	}
	var out []MisplacedShow
	for id, paths := range byShow {
		id := id
		j := &folderJudge{svc: s, roots: roots, others: func(dir string) []int64 {
			var ids []int64
			for o := range under[filepath.Clean(dir)] {
				if o != id {
					ids = append(ids, o)
					if len(ids) == 3 {
						break
					}
				}
			}
			return ids
		}}
		info := s.showFolderFrom(paths, j)
		if m := s.misplacedFrom(info, paths, j); len(m) > 0 {
			out = append(out, MisplacedShow{SeriesID: id, Files: len(m), NoFolder: info.Path == ""})
		}
	}
	for i := range out {
		if sr, err := s.repo.Get(ctx, out[i].SeriesID); err == nil {
			out[i].Title = sr.Title
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Files != out[b].Files {
			return out[a].Files > out[b].Files
		}
		return out[a].Title < out[b].Title
	})
	return out, nil
}

func firstDistinct(whys []string) string {
	seen := map[string]bool{}
	var out []string
	for _, w := range whys {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
		if len(out) == 2 {
			break
		}
	}
	return strings.Join(out, "; ")
}

// ShowFolderError is a show whose episodes can't be imported because there is no sound
// folder to put them in. The download goes to Review with Error() as its reason.
type ShowFolderError struct {
	Title string
	Why   string
}

// ErrNoShowFolder is what every ShowFolderError is (errors.Is), for handlers to answer
// with a conflict rather than a server fault.
var ErrNoShowFolder = errors.New("no safe folder for the show")

// Unwrap makes a ShowFolderError an ErrNoShowFolder.
func (e *ShowFolderError) Unwrap() error { return ErrNoShowFolder }

func (e *ShowFolderError) Error() string {
	return fmt.Sprintf("Can't tell which folder %s lives in (%s), so nothing was imported. Move the show's files into a folder of its own (the show's page offers to put misplaced files back), then use Refresh & rescan on its page.", e.Title, e.Why)
}

// ImportDir is the folder the show's own folder sits in — the TV folder, normally — for
// the importer to place its episodes under (with ExistingFolderName as the show folder).
// A show whose folder is deeper than one level below its TV folder (a TV folder set one
// level too high) gets that folder's parent, so its episodes join it rather than a new
// folder beside it. "" is the importer's default TV folder.
func (s *Service) ImportDir(ctx context.Context, sr Series) string {
	return s.ImportDirOf(ctx, sr, s.ShowFolderOf(ctx, sr.ID))
}

// ImportDirOf is ImportDir for a show whose folder was already worked out (info).
func (s *Service) ImportDirOf(ctx context.Context, sr Series, info ShowFolderInfo) string {
	if p := info.Path; p != "" {
		if _, ok := rootOf(s.libRoots(), p); ok {
			return filepath.Dir(p)
		}
	}
	return s.RootFor(ctx, sr)
}

// UnsafeImportFolder says why dir must not take sr's episodes ("" when it may): it's
// named like a season folder, it's a TV folder (or named after one), or another show's
// files are in it. The last check before any episode file is placed.
func (s *Service) UnsafeImportFolder(ctx context.Context, sr Series, dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return s.dbJudge(ctx, sr.ID).judge(filepath.Clean(dir)).why
}
