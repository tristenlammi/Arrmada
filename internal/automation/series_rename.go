package automation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/series"
)

// SeriesRenameItem is one proposed episode-file rename. Conflict is set when the move
// can't happen — another file already has the new name — and says why.
type SeriesRenameItem struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Season   int    `json:"season"`
	Episode  int    `json:"episode"`
	Conflict string `json:"conflict,omitempty"`
}

// RenameSkip is a rename that wasn't applied, and why.
type RenameSkip struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
	Reason  string `json:"reason"`
}

// SeriesRenameResult is what a rename actually did: how many files moved, and the ones
// it left alone.
type SeriesRenameResult struct {
	Moved   int          `json:"renamed"`
	Skipped []RenameSkip `json:"skipped"`
}

const (
	reasonTargetExists   = "a different file already exists there"
	reasonSameTarget     = "another episode file is being renamed to the same name"
	reasonChangedPreview = "changed since preview"
)

// renameRow is one episode row with a file: where the file is, and where the naming
// scheme says this episode's file belongs.
type renameRow struct {
	Season, Episode int
	Path, Target    string
	Size            int64
}

// renameStep is one file to move. A multi-episode file is one step carrying every row
// that points at it, so it's moved once and all its rows follow.
type renameStep struct {
	From, To string
	Rows     []renameRow
	// viaTemp: another step's target is this step's file, so it must get out of the way
	// first — a chain (A takes B's name, B takes C's) or a swap.
	viaTemp bool
}

func (st renameStep) skip(reason string) RenameSkip {
	return RenameSkip{From: st.From, To: st.To, Season: st.Rows[0].Season, Episode: st.Rows[0].Episode, Reason: reason}
}

// planSeriesRename turns episode rows into moves that can't destroy a file.
//
// Renames used to run row by row as bare renames. After a renumber, S03E01's row holds
// S02E22's file, and renaming it to S03E01's name landed on the real S03E01 file and
// replaced it — then the loss cascaded down the season. Here the whole set is planned
// first:
//   - rows sharing a file are one step;
//   - a target that's another step's source is a chain or a swap, routed through a
//     temporary name so nothing is overwritten mid-way;
//   - a target held by anything that isn't moving away (an untracked file, a file that
//     stays put, a step that itself can't run) is a conflict, and that file stays put.
//
// occupied reports whether something other than from itself already sits at to.
func planSeriesRename(rows []renameRow, occupied func(from, to string) bool) (steps []renameStep, conflicts []RenameSkip) {
	byPath := map[string]int{}
	var groups []renameStep
	for _, r := range rows {
		if r.Path == "" {
			continue
		}
		if i, ok := byPath[r.Path]; ok {
			groups[i].Rows = append(groups[i].Rows, r)
			continue
		}
		byPath[r.Path] = len(groups)
		// The first row names a multi-episode file: rows arrive in (season, episode)
		// order, and the naming scheme gives a multi-episode file one name either way.
		groups = append(groups, renameStep{From: r.Path, To: r.Target, Rows: []renameRow{r}})
	}

	// Paths whose file isn't moving: a file already at its name, or one with no target.
	staying := map[string]bool{}
	var moving []renameStep
	for _, g := range groups {
		if g.To == "" || g.To == g.From {
			staying[g.From] = true
			continue
		}
		moving = append(moving, g)
	}

	// Drop conflicts until the plan is stable: a step that can't run leaves its file in
	// place, which can in turn block a step that was counting on that path coming free.
	for {
		sources := make(map[string]bool, len(moving))
		for _, st := range moving {
			sources[st.From] = true
		}
		taken := map[string]bool{}
		kept := moving[:0:0]
		dropped := false
		for _, st := range moving {
			reason := ""
			switch {
			case taken[st.To]:
				reason = reasonSameTarget
			case staying[st.To]:
				reason = reasonTargetExists
			case !sources[st.To] && occupied(st.From, st.To):
				reason = reasonTargetExists
			}
			if reason != "" {
				conflicts = append(conflicts, st.skip(reason))
				staying[st.From] = true
				dropped = true
				continue
			}
			taken[st.To] = true
			kept = append(kept, st)
		}
		moving = kept
		if !dropped {
			break
		}
	}

	targets := make(map[string]bool, len(moving))
	for _, st := range moving {
		targets[st.To] = true
	}
	for i := range moving {
		moving[i].viaTemp = targets[moving[i].From]
	}
	return moving, conflicts
}

// renameOccupied reports whether a file other than from sits at to. Another name for the
// same file (a hardlink, or a case-only change on a case-insensitive disk) doesn't count:
// Move handles those without losing anything. An unreadable target counts as taken.
func renameOccupied(from, to string) bool {
	toInfo, err := os.Lstat(to)
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	fromInfo, err := os.Lstat(from)
	return err != nil || !os.SameFile(fromInfo, toInfo)
}

// renameTempMarker is what a chained file's temporary name carries; see renameTempPath.
const renameTempMarker = ".arrmada-rename-"

// renameTempPath is where a chained file waits during a rename: a dot-file in its own
// folder whose extension isn't a video one, so a library scan never mistakes it for an
// episode while it's there.
func renameTempPath(from string, n int) string {
	return filepath.Join(filepath.Dir(from), fmt.Sprintf(".%s%s%d", filepath.Base(from), renameTempMarker, n))
}

// freeRenameTempPath is the first temporary name for from that nothing holds yet. A
// leftover from an interrupted rename would otherwise block every later rename of the file
// (Move never replaces), and the leftover itself must not be touched.
func freeRenameTempPath(from string) string {
	for n := 0; ; n++ {
		tmp := renameTempPath(from, n)
		if _, err := os.Lstat(tmp); err != nil {
			return tmp // free — or unreadable, and the move into it will say so
		}
	}
}

// renameTempOriginal reports whether path is a rename's temporary name, and the name the
// file had before it.
func renameTempOriginal(path string) (string, bool) {
	base := filepath.Base(path)
	i := strings.LastIndex(base, renameTempMarker)
	if !strings.HasPrefix(base, ".") || i < 2 {
		return "", false
	}
	return filepath.Join(filepath.Dir(path), base[1:i]), true
}

// restoreRenameTemps puts back any episode file an interrupted rename left under its
// temporary name. If the process stopped between moving a file aside and moving it on,
// the database still points at the original name, which is now empty: a rescan marked the
// episode missing and it was downloaded again, while the real file sat hidden for good. A
// file goes back only when its original name is free; Move never replaces anything.
func (c *Coordinator) restoreRenameTemps(s series.Series) {
	for _, sn := range s.Seasons {
		for _, e := range sn.Episodes {
			if !e.HasFile || e.FilePath == "" || fileExists(e.FilePath) {
				continue
			}
			dir := filepath.Dir(e.FilePath)
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			prefix := "." + filepath.Base(e.FilePath) + renameTempMarker
			for _, ent := range entries {
				if ent.IsDir() || !strings.HasPrefix(ent.Name(), prefix) {
					continue
				}
				tmp := filepath.Join(dir, ent.Name())
				if err := c.imp.Move(tmp, e.FilePath); err != nil {
					c.log.Warn("series: couldn't put back a file an interrupted rename left aside",
						"file", tmp, "original", e.FilePath, "err", err)
					break
				}
				c.imp.MoveEpisodeSubs(tmp, e.FilePath)
				c.log.Info("series: put back a file an interrupted rename left aside",
					"series", s.Title, "file", e.FilePath)
				break
			}
		}
	}
}

// seriesRenameRows lists every episode row with a file, and its canonical path.
func (c *Coordinator) seriesRenameRows(s series.Series, folder string) []renameRow {
	var rows []renameRow
	for _, sn := range s.Seasons {
		for _, e := range sn.Episodes {
			if !e.HasFile || e.FilePath == "" {
				continue
			}
			// A file a failed rename couldn't put back is recorded under its temporary
			// name. Its target comes from the name it had, so it gets a real video
			// extension back rather than keeping ".arrmada-rename-N".
			name := e.FilePath
			if orig, ok := renameTempOriginal(name); ok {
				name = orig
			}
			target := c.imp.EpisodeTargetIn(folder, s.Title, s.Year, e.SeasonNumber, e.EpisodeNumber, filepath.Base(name), filepath.Ext(name))
			rows = append(rows, renameRow{Season: e.SeasonNumber, Episode: e.EpisodeNumber, Path: e.FilePath, Target: target, Size: e.SizeBytes})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Season != rows[j].Season {
			return rows[i].Season < rows[j].Season
		}
		return rows[i].Episode < rows[j].Episode
	})
	return rows
}

// SeriesRenamePreview computes which episode files aren't at their canonical library
// path yet, with the moves that can't happen flagged. It moves nothing; SeriesRename
// applies the same plan.
func (c *Coordinator) SeriesRenamePreview(ctx context.Context, seriesID int64) ([]SeriesRenameItem, error) {
	if c.series == nil || c.imp == nil {
		return nil, nil
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	steps, conflicts := planSeriesRename(c.seriesRenameRows(s, c.series.ExistingFolderName(ctx, seriesID)), renameOccupied)
	items := make([]SeriesRenameItem, 0, len(steps)+len(conflicts))
	for _, st := range steps {
		items = append(items, SeriesRenameItem{From: st.From, To: st.To, Season: st.Rows[0].Season, Episode: st.Rows[0].Episode})
	}
	for _, cf := range conflicts {
		items = append(items, SeriesRenameItem{From: cf.From, To: cf.To, Season: cf.Season, Episode: cf.Episode, Conflict: cf.Reason})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Season != items[j].Season {
			return items[i].Season < items[j].Season
		}
		return items[i].Episode < items[j].Episode
	})
	return items, nil
}

// SeriesRename renames episode files to the canonical scheme without ever replacing a
// file. only, when non-nil, is the list the user previewed and confirmed: a move that
// isn't on it, or whose target has changed since, is skipped rather than applied.
//
// Chained sources are first moved aside to temporary names, then everything moves to its
// target. A move that fails puts its file back. The database follows only files that
// actually moved.
func (c *Coordinator) SeriesRename(ctx context.Context, seriesID int64, only []SeriesRenameItem) (SeriesRenameResult, error) {
	res := SeriesRenameResult{Skipped: []RenameSkip{}}
	if c.series == nil || c.imp == nil {
		return res, fmt.Errorf("series module not ready")
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return res, err
	}
	c.restoreRenameTemps(s)
	rows := c.seriesRenameRows(s, c.series.ExistingFolderName(ctx, seriesID))

	if only != nil {
		rows, res.Skipped = holdUnpreviewed(rows, only)
	}
	steps, conflicts := planSeriesRename(rows, renameOccupied)
	res.Skipped = append(res.Skipped, conflicts...)

	// Phase 1: move every chained source out of the way, subtitles with it.
	temps := make(map[int]string)
	failed := make(map[int]bool)
	for i, st := range steps {
		if !st.viaTemp {
			continue
		}
		tmp := freeRenameTempPath(st.From)
		if err := c.imp.Move(st.From, tmp); err != nil {
			c.log.Warn("series: rename — couldn't move a file aside", "from", st.From, "err", err)
			res.Skipped = append(res.Skipped, st.skip(err.Error()))
			failed[i] = true
			continue
		}
		c.imp.MoveEpisodeSubs(st.From, tmp)
		temps[i] = tmp
	}

	// Phase 2: chained files first (their targets were all freed in phase 1), then the
	// rest — so a chained file that fails can still go back to its own name before
	// anything else has moved into it.
	order := make([]int, 0, len(steps))
	for i := range steps {
		if steps[i].viaTemp {
			order = append(order, i)
		}
	}
	for i := range steps {
		if !steps[i].viaTemp {
			order = append(order, i)
		}
	}
	oldDirs := map[string]bool{} // season folders emptied by the moves, to prune after
	for _, i := range order {
		if failed[i] {
			continue
		}
		st := steps[i]
		src := st.From
		if tmp, ok := temps[i]; ok {
			src = tmp
		}
		if err := c.imp.Move(src, st.To); err != nil {
			c.log.Warn("series: rename failed", "from", st.From, "to", st.To, "err", err)
			skip := st.skip(err.Error())
			if src != st.From {
				if rerr := c.imp.Move(src, st.From); rerr != nil {
					// Its old name was taken in the meantime. Keep the database pointing at
					// where the file really is, so it isn't lost, and say so.
					c.log.Error("series: rename — couldn't put a file back; it's still under its temporary name",
						"file", src, "original", st.From, "err", rerr)
					for _, r := range st.Rows {
						_ = c.series.MarkEpisodeImported(ctx, seriesID, r.Season, r.Episode, src, r.Size)
					}
					skip.Reason = fmt.Sprintf("%s; left as %s — rename it by hand", err.Error(), filepath.Base(src))
				} else {
					c.imp.MoveEpisodeSubs(src, st.From)
				}
			}
			res.Skipped = append(res.Skipped, skip)
			continue
		}
		c.imp.MoveEpisodeSubs(src, st.To) // keep paired subtitles alongside
		if od := filepath.Dir(st.From); od != filepath.Dir(st.To) {
			oldDirs[od] = true // a season folder that changed name (e.g. "Season 04" → "Season 4")
		}
		for _, r := range st.Rows {
			_ = c.series.MarkEpisodeImported(ctx, seriesID, r.Season, r.Episode, st.To, r.Size)
		}
		res.Moved++
	}
	for od := range oldDirs {
		c.imp.RemoveDirIfEmpty(od) // drop the now-empty legacy season folder
	}
	if res.Moved > 0 {
		c.series.AddEvent(ctx, seriesID, "renamed", fmt.Sprintf("Renamed %d episode file%s", res.Moved, plural(res.Moved)))
		c.bus.Publish("series.renamed", map[string]any{"id": seriesID, "count": res.Moved})
	}
	if len(res.Skipped) > 0 {
		c.series.AddEvent(ctx, seriesID, "rename.skipped", renameSkippedEvent(res.Skipped))
	}
	return res, nil
}

// holdUnpreviewed keeps every file the user didn't confirm exactly where it is: its rows
// get their own path as the target, so the planner treats it as staying put. Previewed
// moves that no longer exist are reported too, so nothing confirmed vanishes silently.
func holdUnpreviewed(rows []renameRow, only []SeriesRenameItem) ([]renameRow, []RenameSkip) {
	confirmed := make(map[string]string, len(only))
	for _, it := range only {
		confirmed[it.From] = it.To
	}
	// A file's move is decided by its first row, as in the planner.
	planned := map[string]string{}
	for _, r := range rows {
		if _, ok := planned[r.Path]; !ok {
			planned[r.Path] = r.Target
		}
	}
	skipped := []RenameSkip{}
	reported := map[string]bool{}
	out := make([]renameRow, len(rows))
	for i, r := range rows {
		out[i] = r
		to := planned[r.Path]
		if to == "" || to == r.Path {
			continue
		}
		if want, ok := confirmed[r.Path]; ok && want == to {
			continue
		}
		out[i].Target = r.Path
		if !reported[r.Path] {
			reported[r.Path] = true
			// Only worth reporting when it was previewed (with another target); a move
			// that appeared after the preview simply wasn't asked for.
			if want, ok := confirmed[r.Path]; ok {
				skipped = append(skipped, RenameSkip{From: r.Path, To: want, Season: r.Season, Episode: r.Episode, Reason: reasonChangedPreview})
			}
		}
	}
	for _, it := range only {
		if to, ok := planned[it.From]; !ok || to == it.From {
			skipped = append(skipped, RenameSkip{From: it.From, To: it.To, Season: it.Season, Episode: it.Episode, Reason: reasonChangedPreview})
		}
	}
	return out, skipped
}

// LogRenameSkips warns about each file a rename after a renumber left alone. Those
// renames run unattended, so the log and the History event are the only record.
func (c *Coordinator) LogRenameSkips(seriesID int64, res SeriesRenameResult) {
	for _, sk := range res.Skipped {
		c.log.Warn("series: rename after renumber skipped a file",
			"series_id", seriesID, "from", sk.From, "to", sk.To, "reason", sk.Reason)
	}
}

// renameSkippedEvent is the History line for files a rename left alone.
func renameSkippedEvent(skipped []RenameSkip) string {
	names := make([]string, 0, 5)
	for i, sk := range skipped {
		if i == 5 {
			names = append(names, fmt.Sprintf("+%d more", len(skipped)-5))
			break
		}
		names = append(names, filepath.Base(sk.From))
	}
	return fmt.Sprintf("Rename skipped %d file%s: %s", len(skipped), plural(len(skipped)), strings.Join(names, ", "))
}
