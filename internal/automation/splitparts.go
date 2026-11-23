package automation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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

// probeSplitParts reads every group's parts and says, in words for Needs you, why one
// can't be joined ("" when all can). layouts is filled per joined file, in files' order.
func (c *Coordinator) probeSplitParts(ctx context.Context, files []resolvedVideo) (map[int][]videojoin.Layout, string) {
	probe := c.probeVideoFn
	if probe == nil {
		probe = videojoin.Probe
	}
	layouts := map[int][]videojoin.Layout{}
	for i, f := range files {
		if len(f.parts) < 2 {
			continue
		}
		names := make([]string, len(f.parts))
		for j, p := range f.parts {
			names[j] = filepath.Base(p.Path)
			l, err := probe(ctx, p.Path)
			if err != nil {
				return nil, fmt.Sprintf("This download has one episode in %d parts (%s), and %s couldn't be read to join them: %v. "+
					"Reject it to look for another release, or map the files to episodes by hand.",
					len(f.parts), strings.Join(names[:j+1], ", "), names[j], err)
			}
			layouts[i] = append(layouts[i], l)
		}
		for j := 1; j < len(layouts[i]); j++ {
			if why := videojoin.Compatible(layouts[i][0], layouts[i][j]); why != "" {
				return nil, fmt.Sprintf("This download has S%02dE%02d in %d parts (%s), and they can't be joined into one file without re-encoding: %s. "+
					"Reject it to look for another release, or map the files to episodes by hand.",
					f.refs[0].Season, f.refs[0].Episode, len(f.parts), strings.Join(names, ", "), why)
			}
		}
	}
	return layouts, ""
}

// joinDirName is the folder, right in a TV folder, that joined episodes are made in before
// they're placed. Hidden: scans and media servers pass it by.
const joinDirName = ".arrmada-join"

// joinSplitParts joins a group's parts into one file beside the show's folder and returns
// its path, in a folder of its own that the caller removes (cleanup) once the file has
// been placed. The file is named as the first part is, so it's placed and recorded with
// that release's quality.
func (c *Coordinator) joinSplitParts(ctx context.Context, tvRoot string, f resolvedVideo, layouts []videojoin.Layout) (path string, cleanup func(), err error) {
	base := filepath.Join(tvRoot, joinDirName)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", nil, err
	}
	sweepJoinDir(base, time.Now())
	if u, ok := diskspace.Of(base); ok && u.FreeBytes < uint64(f.v.Size)+joinFreeMargin {
		return "", nil, fmt.Errorf("not enough free space in %s to join the parts (%d MB needed)", tvRoot, (f.v.Size+joinFreeMargin)>>20)
	}
	dir, err := os.MkdirTemp(base, "j-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir); _ = os.Remove(base) } // base only goes when empty
	out := filepath.Join(dir, filepath.Base(f.parts[0].Path))
	paths := make([]string, len(f.parts))
	for i, p := range f.parts {
		paths[i] = p.Path
	}
	join := c.joinVideoFn
	if join == nil {
		join = videojoin.Join
	}
	jctx, cancel := context.WithTimeout(ctx, joinTimeout)
	defer cancel()
	if err := join(jctx, paths, layouts, out); err != nil {
		cleanup()
		return "", nil, err
	}
	return out, cleanup, nil
}

const (
	// joinFreeMargin is the free space a join leaves on the TV folder's disk.
	joinFreeMargin = 1 << 30
	// joinTimeout bounds one join: a copy of a few GB, on an array disk that may be asleep.
	joinTimeout = 30 * time.Minute
	// joinLeftoverAge is how old a folder in joinDirName must be to be a crashed join's.
	joinLeftoverAge = 6 * time.Hour
)

// sweepJoinDir removes what a join interrupted by a restart left behind.
func sweepJoinDir(base string, now time.Time) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !e.IsDir() || !strings.HasPrefix(e.Name(), "j-") || now.Sub(info.ModTime()) < joinLeftoverAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(base, e.Name()))
	}
}

// placeJoinedEpisode joins f's parts and places the result on its episode. It reports the
// episodes that got the file, and whether it failed (the download is then tried again).
func (c *Coordinator) placeJoinedEpisode(ctx context.Context, s series.Series, imp *library.Importer, folder string, f resolvedVideo, layouts []videojoin.Layout, wanted []series.EpisodeRef, sourceName string) (placed []series.EpisodeRef, failed bool) {
	names := make([]string, len(f.parts))
	for i, p := range f.parts {
		names[i] = filepath.Base(p.Path)
	}
	ref := wanted[0]
	label := fmt.Sprintf("S%02dE%02d", ref.Season, ref.Episode)
	joined, cleanup, err := c.joinSplitParts(ctx, imp.TVRoot(), f, layouts)
	if err != nil {
		c.log.Warn("series import: couldn't join an episode's parts into one file — nothing was placed for it, and the parts are untouched",
			"series", s.Title, "episode", label, "parts", strings.Join(names, ", "), "err", err)
		return nil, true
	}
	defer cleanup()
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
	if c.series.SupersedeEpisodeFile(ctx, s.ID, ref.Season, ref.Episode, ei.TargetPath, ei.SizeBytes, sourceName) == nil {
		placed = append(placed, ref)
		c.series.AddEvent(ctx, s.ID, "imported", fmt.Sprintf("%s: joined from %d parts the release split it into (%s). Nothing was re-encoded.", label, len(names), strings.Join(names, ", ")))
	}
	return placed, false
}
