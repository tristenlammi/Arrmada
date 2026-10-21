package movies

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The missing-file sweep: once a day, notice movie files that have vanished from disk, so a
// film whose file is gone stops reading "Downloaded" in the grid. It only ever sets or
// clears movies.file_missing_since — it never deletes, moves, clears a record or searches;
// the owner decides from the detail page (File missing → Clear record).
//
// The danger is an Unraid array that isn't started, or a share that didn't mount: every
// file looks gone at once. So the sweep marks nothing unless the folder each file lives in
// is clearly there (it exists and has something in it), and nothing at all when more files
// vanished in one pass than a real deletion would explain. Either way it raises a health
// warning instead.

// MissingSweep is what the last missing-file sweep did.
type MissingSweep struct {
	At      time.Time `json:"at"`
	Checked int       `json:"checked"` // files with a record, stat'd this pass
	Flagged int       `json:"flagged"` // newly marked missing
	Cleared int       `json:"cleared"` // marked missing before, back on disk now
	Skipped int       `json:"skipped"` // couldn't be judged (unreadable, or a folder that looks unmounted)
	// Refused is set when the sweep marked nothing because the library looked unmounted: the
	// health warning's text.
	Refused string `json:"refused,omitempty"`
}

// missingThreshold is how many files may newly vanish in one pass before the sweep decides
// it's looking at an unmounted disk rather than deletions: 5% of the files checked, and
// never fewer than 20.
func missingThreshold(checked int) int {
	return max(20, checked*5/100)
}

// sweepFile is one default-track file the sweep looks at.
type sweepFile struct {
	ID           int64
	Title        string
	Path         string
	MissingSince string // "" = present as far as we know
}

// sweepFiles is every movie with a recorded default file, by id. A database read only.
func (r *Repo) sweepFiles(ctx context.Context) ([]sweepFile, error) {
	rows, err := r.q().QueryContext(ctx, `SELECT id, title, movie_file_path, COALESCE(file_missing_since, '')
		FROM movies WHERE has_file = 1 AND movie_file_path != '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sweepFile
	for rows.Next() {
		var f sweepFile
		if err := rows.Scan(&f.ID, &f.Title, &f.Path, &f.MissingSince); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// flagMissing marks a movie's file missing as of at, only while it still records that
// file and isn't flagged already. It reports whether it changed the row.
func (r *Repo) flagMissing(ctx context.Context, id int64, path, at string) (bool, error) {
	res, err := r.q().ExecContext(ctx, `UPDATE movies SET file_missing_since = ?
		WHERE id = ? AND has_file = 1 AND movie_file_path = ? AND file_missing_since IS NULL`, at, id, path)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// clearMissing ends a movie's missing flag while it still records that file. It reports
// whether it changed the row.
func (r *Repo) clearMissing(ctx context.Context, id int64, path string) (bool, error) {
	res, err := r.q().ExecContext(ctx, `UPDATE movies SET file_missing_since = NULL
		WHERE id = ? AND movie_file_path = ? AND file_missing_since IS NOT NULL`, id, path)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// missingState holds the last sweep's outcome for the health panel.
type missingState struct {
	mu   sync.Mutex
	last MissingSweep
}

// LastMissingSweep is the most recent sweep's outcome (zero before the first). It lives in
// memory: after a restart the next sweep (daily, at the end of a library scan, or Run now)
// says again; a folder that's gone entirely is also on the library-folders health check.
func (s *Service) LastMissingSweep() MissingSweep {
	s.missing.mu.Lock()
	defer s.missing.mu.Unlock()
	return s.missing.last
}

// MissingSweepWarning is the health warning from the last sweep ("" when all was well).
func (s *Service) MissingSweepWarning() string { return s.LastMissingSweep().Refused }

// within reports whether p is root or inside it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// anchorOf is the folder that has to be there for a missing file to count as missing: the
// Movies folder holding the file (the longest, when there are several), else the folder
// holding the film's own folder (a file left in an old library folder). Each Movies folder
// is judged on its own, so a Kids share that's unmounted never marks its films missing
// while the main share is fine.
func anchorOf(roots []string, path string) string {
	best := ""
	for _, root := range roots {
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if within(root, path) && len(root) > len(best) {
			best = root
		}
	}
	if best != "" {
		return best
	}
	return filepath.Dir(filepath.Dir(path))
}

// CheckMissingFiles is the missing-file sweep. It stats each recorded default-track file
// once, one after another (no ffprobe, no folder walks), and:
//   - clears the flag on a file that's back;
//   - flags a newly missing file (event "missing"), but only when its library folder is
//     clearly mounted and the pass isn't over the threshold — otherwise it flags nothing
//     at all and the outcome's Refused carries the health warning.
//
// Only a "no such file" counts as missing: an unreadable file (permissions, a share that
// errors) is skipped. A sweep cut short by ctx writes nothing. Extra tracks aren't swept;
// the detail page shows theirs live.
func (s *Service) CheckMissingFiles(ctx context.Context) (MissingSweep, error) {
	out := MissingSweep{At: time.Now()}
	files, err := s.repo.sweepFiles(ctx)
	if err != nil {
		return out, err
	}
	roots := s.libRoots()
	mounted := map[string]bool{}
	anchorOK := func(dir string) bool {
		ok, seen := mounted[dir]
		if !seen {
			ok = library.FolderLooksMounted(dir)
			mounted[dir] = ok
		}
		return ok
	}
	var flag, clear []sweepFile
	unmounted := map[string]int{}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return out, err // a partial pass decides nothing
		}
		out.Checked++
		_, statErr := s.statFile(f.Path)
		switch {
		case statErr == nil:
			if f.MissingSince != "" {
				clear = append(clear, f)
			}
		case errors.Is(statErr, fs.ErrNotExist):
			anchor := anchorOf(roots, filepath.Clean(f.Path))
			switch {
			case !anchorOK(anchor):
				unmounted[anchor]++
				out.Skipped++
			case f.MissingSince == "":
				flag = append(flag, f)
			}
		default:
			out.Skipped++ // unreadable is not gone
		}
	}

	switch {
	case len(unmounted) > 0:
		dirs := make([]string, 0, len(unmounted))
		for d := range unmounted {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		out.Refused = fmt.Sprintf("The movies folder %s looks unmounted (it's missing or empty), so the daily missing-file check marked nothing. Check the share or the array is mounted, then run the check again from Settings → Status → Tasks.", strings.Join(dirs, ", "))
	case len(flag) > missingThreshold(out.Checked):
		out.Refused = fmt.Sprintf("%d movie files vanished since the last check — more than a deletion would explain — so nothing was marked missing. Part of the array or a share may be unmounted; check it, then run the check again from Settings → Status → Tasks.", len(flag))
	}
	if out.Refused != "" {
		out.Skipped += len(flag)
		flag = nil
		s.log.Warn("movies folder looks unmounted — the missing-file check marked nothing", "unmounted", unmounted, "would_flag", out.Skipped, "checked", out.Checked)
	}

	// A file that's back is always safe to clear: it was just seen on disk.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	stamp := out.At.UTC().Format(time.RFC3339)
	var changed []int64
	err = s.repo.inTx(wctx, func(_ *store.Transaction, r *Repo) error {
		for _, f := range clear {
			ok, err := r.clearMissing(wctx, f.ID, f.Path)
			if err != nil {
				return err
			}
			if ok {
				out.Cleared++
				changed = append(changed, f.ID)
				_ = r.AddEvent(wctx, f.ID, "detected", "File is back on disk: "+filepath.Base(f.Path))
			}
		}
		for _, f := range flag {
			ok, err := r.flagMissing(wctx, f.ID, f.Path, stamp)
			if err != nil {
				return err
			}
			if ok {
				out.Flagged++
				changed = append(changed, f.ID)
				_ = r.AddEvent(wctx, f.ID, "missing", "File missing from disk: "+filepath.Base(f.Path))
			}
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	if out.Flagged > 0 || out.Cleared > 0 {
		s.log.Info("missing-file check", "checked", out.Checked, "missing", out.Flagged, "back", out.Cleared)
	}
	for _, id := range changed {
		s.publish("movie.refreshed", map[string]any{"id": id})
	}
	s.missing.mu.Lock()
	s.missing.last = out
	s.missing.mu.Unlock()
	return out, nil
}

// noteDefaultFile keeps the missing flag in step with what the detail page just saw of the
// default track's file: cleared when it's there, set when it's gone — and set only when its
// library folder is clearly mounted, so opening a film while the array is down never marks
// it. statErr is the page's own stat of path; nothing else touches the disk unless the
// state changes.
func (s *Service) noteDefaultFile(ctx context.Context, id int64, path, missingSince string, statErr error) {
	switch {
	case statErr == nil && missingSince != "":
		if ok, _ := s.repo.clearMissing(ctx, id, path); ok {
			_ = s.repo.AddEvent(ctx, id, "detected", "File is back on disk: "+filepath.Base(path))
		}
	case errors.Is(statErr, fs.ErrNotExist) && missingSince == "":
		if !library.FolderLooksMounted(anchorOf(s.libRoots(), filepath.Clean(path))) {
			return
		}
		if ok, _ := s.repo.flagMissing(ctx, id, path, time.Now().UTC().Format(time.RFC3339)); ok {
			_ = s.repo.AddEvent(ctx, id, "missing", "File missing from disk: "+filepath.Base(path))
		}
	}
}
