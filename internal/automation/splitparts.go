package automation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/videojoin"
)

// Some releases cut one double-length episode into two files and count each as an episode
// of its own: Disney+'s Family Guy has "S13E01 The Simpsons Guy Part 1" and "S13E02 The
// Simpsons Guy Part 2", then "S13E03 The Book of Joe" — while the show's episode list has
// "The Simpsons Guy" once, as S13E01, and "The Book of Joe" as S13E02.
//
// Placed by their numbers, every later episode of the season lands one too far along. And
// the parts themselves have nowhere to go: an episode has one file, and media servers don't
// play an episode kept in several (Plex calls its support limited; Jellyfin and Emby stop
// after the first part or don't read them at all). So the parts are joined into one file —
// a copy of their streams, nothing re-encoded (videojoin) — which is placed on the one
// episode they are, and the files after them are placed that much earlier.
//
// Everything here only reads the download. The parts stay as they are, for seeding.

// rePartSuffix finds a part marker ending an episode title: "Part 1", "Pt 2", "(Part II)".
var rePartSuffix = regexp.MustCompile(`(?i)[\s,:\-]*[\(\[]?\b(?:part|pt)\.?[\s]*(\d|i{1,3}|one|two|three)[\)\]]?\s*$`)

// reAnyPart finds any part-like marker ending a title, numbered however: "Part 4", "Pt IV",
// "(2)", " II", "2", "Part 2 of 2". Never used to make a group, only to refuse one: a file
// of the same story the group doesn't hold ("Part 4" of a four-parter), or a list that
// names the second half as an episode of its own ("Homecoming (2)").
var reAnyPart = regexp.MustCompile(`(?i)[\s,:\-]*[\(\[]?(?:\b(?:part|pt)\.?\s*)?\b(?:\d{1,2}|[ivx]{1,4}|one|two|three|four|five)(?:\s+of\s+\d{1,2})?[\)\]]?\s*$`)

// stripAnyPart is a title without any part-like marker ending it, or "" when it has none
// (or nothing would be left).
func stripAnyPart(title string) string {
	m := reAnyPart.FindStringIndex(title)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(title[:m[0]])
}

// maxSplitParts is the most parts one episode is joined from.
const maxSplitParts = 3

// splitPartTitle splits "The Simpsons Guy Part 2" into its title and part number (0 when
// the title has no part marker, or nothing would be left of it).
func splitPartTitle(title string) (base string, part int) {
	m := rePartSuffix.FindStringSubmatchIndex(title)
	if m == nil {
		return title, 0
	}
	base = strings.TrimSpace(title[:m[0]])
	if base == "" {
		return title, 0
	}
	switch strings.ToLower(title[m[2]:m[3]]) {
	case "1", "i", "one":
		part = 1
	case "2", "ii", "two":
		part = 2
	case "3", "iii", "three":
		part = 3
	default:
		return title, 0
	}
	return base, part
}

// partGroup is one episode's parts within a download: files' indexes in part order, and
// the episode they are.
type partGroup struct {
	files []int
	ref   series.EpisodeRef
	// first, last: the numbers the release gave its first and last part.
	first, last int
}

// findSplitParts finds the episodes a download carries in parts. A group is only made on
// all of this evidence:
//   - two or three single-episode files of one season whose titles are the same but for
//     "Part 1", "Part 2" (, "Part 3"), every part there once;
//   - numbered by the release one after the other, in part order, or all with one number;
//   - no other file of that season is another part of the same story ("Part 4" beside
//     parts 1 to 3);
//   - and exactly one episode of that season is called that, in a list where no episode is
//     itself a part of it ("Blue Harvest (1)", "Blue Harvest, Part 2", "Homecoming (2)",
//     "Redemption II": a list that counts the parts as the release does needs no joining).
//
// That is only half the evidence: applySplitParts also needs the season's other files to
// bear it out before anything is joined.
//
// titles gives a season's episode titles by number.
func findSplitParts(files []resolvedVideo, titles func(season int) map[int]string) []partGroup {
	type key struct {
		season int
		base   string
	}
	type member struct{ idx, part, num int }
	byKey := map[key][]member{}
	stray := map[key]bool{} // a part-like file of the story that can't be one of its parts
	var order []key
	for i, f := range files {
		if len(f.numbered) == 0 || len(f.parts) > 0 {
			continue
		}
		title := parser.EpisodeTitleFrom(filepath.Base(f.v.Path))
		base, part := splitPartTitle(title)
		k := key{f.numbered[0].Season, strings.Join(parser.TitleWords(base), "")}
		if part == 0 || k.base == "" || len(f.numbered) != 1 {
			if b := stripAnyPart(title); b != "" {
				stray[key{f.numbered[0].Season, strings.Join(parser.TitleWords(b), "")}] = true
			}
			continue
		}
		if byKey[k] == nil {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], member{i, part, f.numbered[0].Episode})
	}
	var out []partGroup
	for _, k := range order {
		ms := byKey[k]
		if len(ms) < 2 || len(ms) > maxSplitParts || stray[k] {
			continue
		}
		sort.Slice(ms, func(a, b int) bool { return ms[a].part < ms[b].part })
		ok, sameNum := true, true
		for i, m := range ms {
			if m.part != i+1 {
				ok = false
			}
			if i > 0 && m.num != ms[0].num {
				sameNum = false
			}
		}
		if !ok {
			continue
		}
		if !sameNum {
			for i, m := range ms {
				if m.num != ms[0].num+i {
					ok = false
				}
			}
			if !ok {
				continue
			}
		}
		base, _ := splitPartTitle(parser.EpisodeTitleFrom(filepath.Base(files[ms[0].idx].v.Path)))
		match, hits := 0, 0
		for num, t := range titles(k.season) {
			if tb := stripAnyPart(t); tb != "" && titlesAlike(base, tb) {
				hits = -1 << 20 // the list counts parts too
				continue
			}
			if titlesAlike(base, t) {
				match, hits = num, hits+1
			}
		}
		if hits != 1 {
			continue
		}
		g := partGroup{ref: series.EpisodeRef{Season: k.season, Episode: match}, first: ms[0].num, last: ms[len(ms)-1].num}
		for _, m := range ms {
			g.files = append(g.files, m.idx)
		}
		out = append(out, g)
	}
	return out
}

// applySplitParts folds each group's files into one resolvedVideo on the group's episode,
// and moves the season's later files back by as many numbers as the parts took up beyond
// one — when the release's own titles bear that out.
//
// Nothing is joined or moved in a season unless its files bear the group out:
//   - at least one file after the parts has a title naming exactly the episode the count
//     puts it on (Family Guy's "Baking Bad", numbered E04, is the list's E03);
//   - every file whose title names an episode is where the count puts it;
//   - and no file after the parts carries two episode numbers (it can't be counted).
//
// Otherwise the download's files are returned as they came, and it is judged as before
// (titleConflict): usually held for a person. A pair of real episodes the list names
// differently ("Homecoming", "The Return") never has a later title that agrees with
// joining them, so it is never joined.
//
// A file is moved by the count alone only when its title names nothing (it has none, or
// one the list doesn't know: releases shorten titles).
//
// files may include ones whose number the show doesn't have (beyond): a season's last
// file, numbered one past its end. Those come back only when the arithmetic places them;
// rescued counts them. A group that includes one counts it too.
//
// exists reports whether the show has the episode. It returns the files to place and what
// it moved, for the log.
func applySplitParts(files []resolvedVideo, groups []partGroup, titles func(season int) map[int]string, exists func(series.EpisodeRef) bool) (out []resolvedVideo, moved []string, rescued int) {
	unchanged := func() ([]resolvedVideo, []string, int) {
		out := make([]resolvedVideo, 0, len(files))
		for _, f := range files {
			if !f.beyond {
				out = append(out, f)
			}
		}
		return out, nil, 0
	}
	inGroup := map[int]bool{}
	bySeason := map[int][]partGroup{}
	for _, g := range groups {
		for _, i := range g.files {
			inGroup[i] = true
		}
		bySeason[g.ref.Season] = append(bySeason[g.ref.Season], g)
	}
	// shift is how many numbers the parts before num took up beyond one each.
	shift := func(season, num int) int {
		n := 0
		for _, g := range bySeason[season] {
			if g.last < num {
				n += g.last - g.first
			}
		}
		return n
	}
	// A season's arithmetic holds when everything that names an episode agrees with it.
	holds := map[int]bool{}
	for season, gs := range bySeason {
		holds[season] = true
		for _, g := range gs {
			if g.ref.Episode != g.first-shift(season, g.first) {
				holds[season] = false
			}
		}
	}
	titled := func(f resolvedVideo) (int, bool) { // the episode the file's title names
		if f.beyond {
			return 0, false // a title that named an episode would have placed it
		}
		if f.byTitle {
			return f.refs[0].Episode, true
		}
		t := parser.EpisodeTitleFrom(filepath.Base(f.v.Path))
		if t != "" && titlesAlike(t, titles(f.numbered[0].Season)[f.numbered[0].Episode]) {
			return f.numbered[0].Episode, true
		}
		return 0, false
	}
	confirmed := map[int]bool{}
	after := func(season, num int) bool { // numbered after one of the season's groups
		for _, g := range bySeason[season] {
			if num > g.last {
				return true
			}
		}
		return false
	}
	for i, f := range files {
		if inGroup[i] || len(f.numbered) == 0 {
			continue
		}
		season, num := f.numbered[0].Season, f.numbered[0].Episode
		if _, ok := bySeason[season]; !ok {
			continue
		}
		if len(f.numbered) != 1 || len(f.refs) > 1 {
			if after(season, num) {
				holds[season] = false // two numbers in one file: the count can't place it
			}
			continue
		}
		ep, ok := titled(f)
		switch {
		case !ok:
		case ep != num-shift(season, num):
			holds[season] = false
		case after(season, num):
			confirmed[season] = true
		}
	}
	for season := range bySeason {
		if !holds[season] || !confirmed[season] {
			return unchanged()
		}
	}
	out = make([]resolvedVideo, 0, len(files))
	done := map[int]bool{}
	for i, f := range files {
		if inGroup[i] {
			for _, g := range groups {
				if g.files[0] != i || done[i] {
					continue
				}
				done[i] = true
				first := files[g.files[0]]
				joined := resolvedVideo{v: first.v, rel: first.rel, refs: []series.EpisodeRef{g.ref},
					numbered: first.numbered, byTitle: first.numbered[0] != g.ref}
				joined.v.Size = 0
				for _, j := range g.files {
					joined.parts = append(joined.parts, files[j].v)
					joined.v.Size += files[j].v.Size
					if files[j].beyond {
						rescued++
					}
				}
				out = append(out, joined)
			}
			continue
		}
		if len(f.numbered) == 1 && (f.beyond || (len(f.refs) == 1 && !f.byTitle)) {
			season, num := f.numbered[0].Season, f.numbered[0].Episode
			if n := shift(season, num); n > 0 && holds[season] {
				if _, ok := titled(f); !ok {
					if to := (series.EpisodeRef{Season: season, Episode: num - n}); exists(to) {
						moved = append(moved, fmt.Sprintf("%s to S%02dE%02d", filepath.Base(f.v.Path), to.Season, to.Episode))
						f.refs, f.byTitle, f.byShift = []series.EpisodeRef{to}, true, true
						if f.beyond {
							f.beyond = false
							rescued++
						}
					}
				}
			}
		}
		if f.beyond {
			continue // still a number the show doesn't have: unresolved, as it was counted
		}
		out = append(out, f)
	}
	return out, moved, rescued
}

// joinDirName is the folder, right in a TV folder, that joined episodes are made in before
// they're placed. Hidden: scans and media servers pass it by.
const joinDirName = ".arrmada-join"

const (
	// joinFreeMargin is the free space a join leaves on the TV folder's disk.
	joinFreeMargin = 1 << 30
	// joinTimeout bounds one join: a copy of a few GB, on an array disk that may be asleep.
	joinTimeout = 30 * time.Minute
	// joinLeftoverAge is how old a folder in joinDirName must be to be left over: no join
	// runs longer than joinTimeout.
	joinLeftoverAge = joinTimeout + 10*time.Minute
	// joinSweepEvery is how often the TV folders are looked over for leftovers.
	joinSweepEvery = 10 * time.Minute
	// joinHeldFor is how long a join that couldn't be done is remembered. The download is
	// held in Needs you, so this only stops the same download being joined again straight
	// away (the sweep imports a held download a second time after refreshing the show).
	joinHeldFor = time.Hour
)

// joinState is the joins under way and the ones that couldn't be done.
type joinState struct {
	mu     sync.Mutex
	active map[string]bool     // the folders joins are writing in now
	held   map[string]joinHold // by the first part's path
	swept  time.Time           // the last look for leftovers
}

type joinHold struct {
	why string
	at  time.Time
}

// heldJoin is why an episode's parts couldn't be joined, if that was found within
// joinHeldFor.
func (c *Coordinator) heldJoin(first string) string {
	j := &c.joins
	j.mu.Lock()
	defer j.mu.Unlock()
	if h, ok := j.held[first]; ok && c.clock().Sub(h.at) < joinHeldFor {
		return h.why
	}
	return ""
}

func (c *Coordinator) holdJoin(first, why string) string {
	j := &c.joins
	now := c.clock()
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.held == nil {
		j.held = map[string]joinHold{}
	}
	for k, h := range j.held {
		if now.Sub(h.at) >= joinHeldFor {
			delete(j.held, k)
		}
	}
	j.held[first] = joinHold{why: why, at: now}
	return why
}

// errJoinNotNow is a join that couldn't start for a reason that passes (the TV folder's
// share is away, its disk is full right now, Arrmada is stopping). Nothing was written;
// the download is tried again next sweep, as any import that couldn't place a file is.
var errJoinNotNow = errors.New("can't join the parts right now")

// joinSplitParts joins an episode's parts into one file beside the show's folder, and
// returns its path in a folder of its own that cleanup removes once the file is placed.
// The file is named as the first part is, so it's placed and recorded with that release's
// quality.
//
// hold is set, in words for Needs you, when the parts can't or couldn't be joined: they
// aren't alike enough, ffmpeg isn't there, or the join or its check failed. Nothing is
// placed from the download then, and it isn't joined again on its own: a join rewrites
// the whole episode, and retrying a failed one every sweep would do that every 30 s.
// err (errJoinNotNow) is a join that couldn't start for now; nothing was written.
func (c *Coordinator) joinSplitParts(ctx context.Context, imp *library.Importer, f resolvedVideo) (path string, cleanup func(), hold string, err error) {
	names := make([]string, len(f.parts))
	paths := make([]string, len(f.parts))
	for i, p := range f.parts {
		names[i], paths[i] = filepath.Base(p.Path), p.Path
	}
	ep := fmt.Sprintf("S%02dE%02d", f.refs[0].Season, f.refs[0].Episode)
	about := fmt.Sprintf("This download has %s in %d parts (%s)", ep, len(f.parts), strings.Join(names, ", "))
	if why := c.heldJoin(paths[0]); why != "" {
		return "", nil, why, nil
	}
	missing := func(tool string) string {
		return c.holdJoin(paths[0], fmt.Sprintf("%s, and joining them into one file needs ffmpeg and ffprobe, but %s isn't installed where Arrmada runs (the Docker image has both). "+
			"Install them for episodes like this to be joined; for this download, Map files… places the rest by hand, or Dismiss leaves it to you.", about, tool))
	}

	probe := c.probeVideoFn
	if probe == nil {
		probe = videojoin.Probe
	}
	layouts := make([]videojoin.Layout, len(paths))
	for i, p := range paths {
		l, err := probe(ctx, p)
		switch {
		case errors.Is(err, exec.ErrNotFound):
			return "", nil, missing("ffprobe"), nil
		case err != nil && ctx.Err() != nil:
			return "", nil, "", fmt.Errorf("%w: %v", errJoinNotNow, ctx.Err())
		case err != nil:
			return "", nil, c.holdJoin(paths[0], fmt.Sprintf("%s, and %s couldn't be read to join them: %v. "+
				"Reject it to look for another release, or map the files to episodes by hand.", about, names[i], err)), nil
		}
		layouts[i] = l
	}
	for i := 1; i < len(layouts); i++ {
		if why := videojoin.Compatible(layouts[0], layouts[i]); why != "" {
			return "", nil, c.holdJoin(paths[0], fmt.Sprintf("%s, and they can't be joined into one file without re-encoding: %s. "+
				"Reject it to look for another release, or map the files to episodes by hand.", about, why)), nil
		}
	}

	// Never anywhere but a TV folder that's there: with its share away, the folder can be
	// an empty mount point on the system disk, or in a container's own layer.
	tvRoot := imp.TVRoot()
	if err := imp.CheckTVRoot(); err != nil {
		return "", nil, "", fmt.Errorf("%w: %v", errJoinNotNow, err)
	}
	if !library.FolderLooksMounted(tvRoot) {
		return "", nil, "", fmt.Errorf("%w: the TV folder %s is empty — is its disk or share mounted?", errJoinNotNow, tvRoot)
	}
	if u, ok := diskspace.Of(tvRoot); ok && u.FreeBytes < uint64(f.v.Size)+joinFreeMargin {
		return "", nil, "", fmt.Errorf("%w: not enough free space in %s (%d MB needed)", errJoinNotNow, tvRoot, (f.v.Size+joinFreeMargin)>>20)
	}

	base := filepath.Join(tvRoot, joinDirName)
	j := &c.joins
	j.mu.Lock()
	dir, err := func() (string, error) {
		if err := os.MkdirAll(base, 0o755); err != nil {
			return "", err
		}
		return os.MkdirTemp(base, "j-")
	}()
	if err == nil {
		if j.active == nil {
			j.active = map[string]bool{}
		}
		j.active[dir] = true
	}
	j.mu.Unlock()
	if err != nil {
		return "", nil, "", fmt.Errorf("%w: %v", errJoinNotNow, err)
	}
	cleanup = func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		_ = os.RemoveAll(dir)
		delete(j.active, dir)
		_ = os.Remove(base) // only goes when empty
	}
	out := filepath.Join(dir, names[0])
	join := c.joinVideoFn
	if join == nil {
		join = videojoin.Join
	}
	jctx, cancel := context.WithTimeout(ctx, joinTimeout)
	defer cancel()
	if err := join(jctx, paths, layouts, out); err != nil {
		cleanup()
		switch {
		case errors.Is(err, exec.ErrNotFound):
			return "", nil, missing("ffmpeg"), nil
		case ctx.Err() != nil: // Arrmada is stopping
			return "", nil, "", fmt.Errorf("%w: %v", errJoinNotNow, ctx.Err())
		}
		return "", nil, c.holdJoin(paths[0], fmt.Sprintf("%s, and joining them into one file failed: %v. The parts are untouched. "+
			"Reject it to look for another release, or map the files to episodes by hand.", about, err)), nil
	}
	return out, cleanup, "", nil
}

// sweepJoinLeftovers removes what joins stopped by a restart or a crash left in the TV
// folders, at most every joinSweepEvery. It never creates anything, and never touches a
// join that's running.
func (c *Coordinator) sweepJoinLeftovers(roots func() []string) {
	j := &c.joins
	now := c.clock()
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.swept.IsZero() && now.Sub(j.swept) < joinSweepEvery {
		return
	}
	j.swept = now
	for _, root := range roots() {
		if strings.TrimSpace(root) == "" {
			continue
		}
		base := filepath.Join(root, joinDirName)
		sweepJoinDir(base, now, j.active)
		_ = os.Remove(base) // only goes when empty
	}
}

// sweepJoinDir removes join folders in base older than joinLeftoverAge that no join is
// using. Called with the joins' lock held.
func sweepJoinDir(base string, now time.Time, active map[string]bool) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(base, e.Name())
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "j-") || active[p] {
			continue
		}
		if info, err := e.Info(); err != nil || now.Sub(info.ModTime()) < joinLeftoverAge {
			continue
		}
		_ = os.RemoveAll(p)
	}
}

// placeJoinedEpisode places an episode joined from its parts (joined) on its episode. It
// reports the episodes that got the file, and whether it failed (the download is then
// tried again).
func (c *Coordinator) placeJoinedEpisode(ctx context.Context, s series.Series, imp *library.Importer, folder string, f resolvedVideo, joined string, wanted []series.EpisodeRef, sourceName string, escapes []series.EpisodeRef) (placed []series.EpisodeRef, failed bool) {
	names := make([]string, len(f.parts))
	for i, p := range f.parts {
		names[i] = filepath.Base(p.Path)
	}
	ref := wanted[0]
	label := fmt.Sprintf("S%02dE%02d", ref.Season, ref.Episode)
	ei, ok, ierr := imp.ImportEpisodeAs(folder, s.Title, s.Year, ref.Season, ref.Episode, joined)
	if ierr != nil || !ok {
		c.log.Warn("series import: couldn't place the joined episode",
			"series", s.Title, "episode", label, "parts", strings.Join(names, ", "), "err", ierr)
		return nil, true
	}
	if ei.Method == "already" {
		return nil, false
	}
	c.log.Info("series import: an episode the release split into parts was joined into one file (nothing re-encoded; the parts stay in the download)",
		"series", s.Title, "episode", label, "parts", strings.Join(names, ", "), "file", filepath.Base(ei.TargetPath))
	// escapes: the episodes the gate took as their one language escape (#137), recorded
	// with the file.
	if c.series.SupersedeEpisodeFileEscaping(ctx, s.ID, ref.Season, ref.Episode, ei.TargetPath, ei.SizeBytes, sourceName,
		refsContain(escapes, ref.Season, ref.Episode)) == nil {
		placed = append(placed, ref)
		c.series.AddEvent(ctx, s.ID, "imported", fmt.Sprintf("%s: joined from %d parts the release split it into (%s). Nothing was re-encoded.", label, len(names), strings.Join(names, ", ")))
	}
	return placed, false
}
