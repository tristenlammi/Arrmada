package movies

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/outbox"
	"github.com/tristenlammi/arrmada/internal/store"
)

// ErrFilesNotRemoved means a movie delete stopped because the recycle bin refused a file.
// The movie stays in the library; nothing is deleted for good as a fallback.
var ErrFilesNotRemoved = errors.New("some files couldn't be moved to the recycle bin — the movie was kept")

// FilesNotRemovedError reports what a refused movie delete had already moved and which
// file the bin refused. It matches ErrFilesNotRemoved and the bin's own error.
type FilesNotRemovedError struct {
	Moved  []string
	Failed []string
	Err    error
}

func (e *FilesNotRemovedError) Error() string {
	msg := "the movie was kept: " + e.Err.Error()
	if len(e.Moved) > 0 {
		msg += fmt.Sprintf(" (%d file(s) already moved are in the recycle bin)", len(e.Moved))
	}
	return msg
}

func (e *FilesNotRemovedError) Unwrap() []error { return []error{ErrFilesNotRemoved, e.Err} }

// Delete removes a movie. With deleteFiles, every version's file (and its subtitles) goes
// to the recycle bin first, or is deleted when the bin is deliberately off. If the bin
// refuses a file, Delete stops before touching the movie's rows and returns a
// FilesNotRemovedError; files it had already moved read as missing so the library tells
// the truth. It never falls back to a permanent delete. It moves no leftover artwork or
// metadata: that takes DeleteWith and the list the preview showed.
func (s *Service) Delete(ctx context.Context, id int64, deleteFiles bool) error {
	return s.DeleteWith(ctx, id, DeleteOptions{Files: deleteFiles})
}

// DeleteOptions says what a movie delete removes from disk.
type DeleteOptions struct {
	// Files moves every version's file and its subtitles to the recycle bin.
	Files bool
	// Leftovers are the artwork and metadata paths the delete dialog listed (the
	// preview's extras). Only those go, and only while a fresh plan still allows each
	// one; a file that turned up since the preview stays. Empty: none go.
	Leftovers []string
}

// DeleteWith is Delete with the files the preview listed (DeleteOptions).
func (s *Service) DeleteWith(ctx context.Context, id int64, opts DeleteOptions) error {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if opts.Files {
		if err := s.removeAllFiles(ctx, m, opts.Leftovers); err != nil {
			return err
		}
	}
	// The path the change carries (Plex scans its folder): the default track's file, or —
	// for a movie whose only files are extra tracks' — the first of those.
	changed := m.MovieFilePath
	if changed == "" {
		if extras, err := s.repo.ListVersions(ctx, id); err == nil {
			for _, v := range extras {
				if v.FilePath != "" {
					changed = v.FilePath
					break
				}
			}
		}
	}
	folder := ""
	if changed != "" {
		folder = filepath.Dir(changed)
	}
	// The movie, its versions and its history go together, with the row that makes
	// Convert and Subtitles forget it.
	err = s.repo.inTx(ctx, func(tx *store.Transaction, r *Repo) error {
		if err := r.Delete(ctx, id); err != nil {
			return err
		}
		return s.enqueue(ctx, tx, outbox.TopicMovieChanged,
			outbox.MovieChanged{MovieID: id, Change: outbox.ChangeDeleted, Path: changed}, movieKey(id))
	})
	if err != nil {
		return err
	}
	s.publish("movie.deleted", map[string]any{"id": id, "tmdb_id": m.TMDBID, "folder": folder})
	return nil
}

// movieFile is one file a movie holds: the default track (versionID 0) or an extra.
type movieFile struct {
	versionID int64
	path      string
}

// trackedFiles is every file movie m's tracks hold, the default first, each path once,
// with each track's label.
func trackedFiles(m Movie, extras []Version) ([]movieFile, map[int64]string) {
	var files []movieFile
	labels := map[int64]string{0: "Default"}
	seen := map[string]bool{}
	add := func(vid int64, p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			files = append(files, movieFile{vid, p})
		}
	}
	add(0, m.MovieFilePath)
	for _, v := range extras {
		labels[v.ID] = v.Label
		add(v.ID, v.FilePath)
	}
	return files, labels
}

// removeAllFiles carries out the movie's purge plan for Delete: every tracked file and
// its subtitles go to the bin, then — only where the folder is provably the movie's own
// — its leftover artwork and metadata, then each folder the plan empties. A tracked file
// the bin refuses stops the delete (FilesNotRemovedError); a leftover it refuses is
// logged and left, with its folder. Only leftovers in confirmed (the ones the preview
// listed) go: the plan is made again here, and a leftover it no longer allows, or one the
// preview didn't show, stays.
func (s *Service) removeAllFiles(ctx context.Context, m Movie, confirmed []string) error {
	// Read the extras directly: Versions degrades to default-only on a read error, and
	// here that would delete the movie while leaving its other files untracked on disk.
	extras, err := s.repo.ListVersions(ctx, m.ID)
	if err != nil {
		return err
	}
	files, labels := trackedFiles(m, extras)
	if len(files) == 0 {
		return nil
	}
	// Pre-flight: a bin that can't even be created fails before a single file moves.
	if err := library.CheckBin(s.bin, files[0].path); err != nil {
		_ = s.repo.AddEvent(ctx, m.ID, "delete.failed", "Couldn't use the recycle bin, so nothing was deleted: "+err.Error())
		return &FilesNotRemovedError{Moved: []string{}, Failed: []string{filepath.Base(files[0].path)}, Err: err}
	}
	plan := s.purgePlanFor(ctx, m, files, labels)
	plan.keepOnly(confirmed)
	var moved []movieFile
	for _, v := range plan.videos {
		dst, err := library.RemoveToBin(s.bin, v.path)
		if err != nil {
			// The moved files are in the bin now; their tracks must read as missing.
			names := make([]string, 0, len(moved))
			for _, mf := range moved {
				names = append(names, filepath.Base(mf.path))
				if mf.versionID == 0 {
					_ = s.repo.ClearFile(ctx, m.ID)
				} else {
					_ = s.repo.ClearVersionFile(ctx, mf.versionID)
				}
				s.publish("movie.file_deleted", map[string]any{"id": m.ID, "version_id": mf.versionID, "path": mf.path})
			}
			if len(moved) > 0 {
				s.enqueueChange(ctx, outbox.MovieChanged{MovieID: m.ID, VersionID: moved[0].versionID, Change: outbox.ChangeFileDeleted, Path: moved[0].path})
			}
			_ = s.repo.AddEvent(ctx, m.ID, "delete.failed",
				fmt.Sprintf("Stopped deleting after %d file(s): %v", len(moved), err))
			return &FilesNotRemovedError{Moved: names, Failed: []string{filepath.Base(v.path)}, Err: err}
		}
		if dst != "" {
			s.log.Info("moved to recycle bin", "from", v.path, "to", dst)
		}
		s.fileRemoved(v.path)
		moved = append(moved, movieFile{v.versionID, v.path})
		for _, sub := range v.subs {
			if _, err := library.RemoveToBin(s.bin, sub); err != nil {
				s.log.Warn("movie: subtitle left behind", "path", sub, "err", err)
				continue
			}
			s.fileRemoved(sub)
		}
	}
	s.clearFolders(ctx, m, plan)
	return nil
}

// clearFolders moves each planned folder's leftovers to the bin and removes the folders
// the plan empties. A leftover the bin refuses stays, with its folder: a warning, never a
// failure, since the movie's own files are already gone. Leftovers are never deleted for
// good: if the bin is off by now, they stay.
func (s *Service) clearFolders(ctx context.Context, m Movie, plan purgePlan) {
	for _, f := range plan.folders {
		left := 0
		if len(f.extras) > 0 {
			if reason := s.recheckFolder(ctx, plan.pc, f); reason != "" {
				// Something changed since the plan (an import, a new file, another film's
				// record): every leftover stays.
				left = len(f.extras)
				s.log.Warn("movie delete: the folder changed during the delete, so its other files stay",
					"movie", m.Title, "folder", f.dir, "why", reason)
			} else {
				for _, p := range f.extras {
					// The very file the plan saw, reached through real folders only: a link
					// swapped in since, or a file rewritten since, stays.
					if why := s.leftoverChanged(f, p); why != "" {
						left++
						s.log.Warn("movie: leftover left in the folder", "movie", m.Title, "path", p, "why", why)
						continue
					}
					dst, err := library.RemoveToBin(s.bin, p)
					if err != nil {
						left++
						s.log.Warn("movie: leftover left in the folder", "movie", m.Title, "path", p, "err", err)
						continue
					}
					if dst != "" {
						s.log.Info("moved to recycle bin", "from", p, "to", dst)
					}
					s.fileRemoved(p)
				}
			}
		}
		for _, d := range f.subdirs {
			s.removeEmptyDir(d)
		}
		if f.remove {
			s.removeEmptyDir(f.dir)
		}
		if left > 0 {
			s.log.Warn("movie delete: some of the folder's other files stayed, so the folder did too",
				"movie", m.Title, "folder", f.dir, "left", left)
		}
	}
}

// removeEmptyDir removes dir when it's empty, a real folder and not a Movies folder. It
// never removes anything that still holds a file, and never a symlink.
func (s *Service) removeEmptyDir(dir string) {
	if s.prunable(dir) {
		_ = os.Remove(dir) // only succeeds when empty
	}
}

// DeleteFile removes a movie's file from disk and clears its file record,
// flipping the movie back to Wanted (if monitored). If the bin refuses the file,
// nothing changes and the error says why.
func (s *Service) DeleteFile(ctx context.Context, id int64) error {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if m.MovieFilePath != "" {
		versions, _ := s.VersionRows(ctx, id)
		if err := s.removeFile(m.MovieFilePath, otherFiles(versions, m.MovieFilePath)); err != nil {
			return err
		}
		s.log.Info("deleted movie file", "movie", m.Title, "path", m.MovieFilePath)
	}
	return s.clearTrack(ctx, id, 0, m.MovieFilePath, func(r *Repo) error {
		if m.MovieFilePath != "" {
			_ = r.AddEvent(ctx, id, "deleted", "Deleted "+filepath.Base(m.MovieFilePath))
		}
		return r.ClearFile(ctx, id)
	})
}

// ErrFileExists means a "missing" file is on disk after all, so its record wasn't cleared.
var ErrFileExists = errors.New("the file is back on disk — refresh instead")

// ForgetMissingFile clears the record of a track whose file is gone from disk (versionID 0
// is the default track). It never touches the disk: it checks the file really is gone and
// refuses with ErrFileExists when it's there, because a remounted disk can bring a
// "missing" file back between the page loading and the click. Clearing the record lets a
// monitored movie be searched for again.
func (s *Service) ForgetMissingFile(ctx context.Context, movieID, versionID int64) error {
	var path, label string
	var hasFile bool
	if versionID == 0 {
		m, err := s.repo.Get(ctx, movieID)
		if err != nil {
			return err
		}
		path, hasFile = m.MovieFilePath, m.HasFile
	} else {
		v, owner, err := s.repo.GetVersion(ctx, versionID)
		if err != nil {
			return err
		}
		if owner != movieID {
			return ErrNotFound
		}
		path, hasFile, label = v.FilePath, v.HasFile, v.Label
	}
	if !hasFile {
		return nil // nothing recorded: already cleared
	}
	if path != "" {
		if _, err := s.statFile(path); err == nil {
			return ErrFileExists
		} else if !errors.Is(err, fs.ErrNotExist) {
			// Unreadable is not the same as gone (a permissions slip, a flaky mount):
			// keep the record rather than guess.
			return fmt.Errorf("couldn't check the file on disk: %w", err)
		}
	}
	detail := "Cleared record of missing file"
	if path != "" {
		detail += " " + filepath.Base(path)
	}
	if label != "" {
		detail += " (" + label + ")"
	}
	err := s.clearTrack(ctx, movieID, versionID, path, func(r *Repo) error {
		_ = r.AddEvent(ctx, movieID, "missing_cleared", detail)
		if versionID == 0 {
			return r.ClearFile(ctx, movieID)
		}
		return r.ClearVersionFile(ctx, versionID)
	})
	// The import pipeline forgets the file too, so a re-grab of the same release imports
	// again instead of being deduped against a file that no longer exists.
	if err == nil && s.onFileRemoved != nil && path != "" {
		s.onFileRemoved(ctx, path)
	}
	return err
}

// clearTrack runs a track's record change (clear) in one transaction with the
// movie.changed row that makes Convert and Subtitles drop the removed file, then announces
// it on the bus. path is the file that went ("" when the track had none: nothing to tell).
func (s *Service) clearTrack(ctx context.Context, movieID, versionID int64, path string, clear func(r *Repo) error) error {
	err := s.repo.inTx(ctx, func(tx *store.Transaction, r *Repo) error {
		if err := clear(r); err != nil {
			return err
		}
		if path == "" {
			return nil
		}
		return s.enqueue(ctx, tx, outbox.TopicMovieChanged,
			outbox.MovieChanged{MovieID: movieID, VersionID: versionID, Change: outbox.ChangeFileDeleted, Path: path}, movieKey(movieID))
	})
	if err != nil {
		return err
	}
	if path != "" {
		s.publish("movie.file_deleted", map[string]any{"id": movieID, "version_id": versionID, "path": path})
	}
	return nil
}

// DeleteVersion removes an extra version track and its file. If the bin refuses the
// file, the version is kept.
func (s *Service) DeleteVersion(ctx context.Context, versionID int64) error {
	v, movieID, err := s.repo.GetVersion(ctx, versionID)
	if err != nil {
		return err
	}
	if v.FilePath != "" {
		versions, _ := s.VersionRows(ctx, movieID)
		if err := s.removeFile(v.FilePath, otherFiles(versions, v.FilePath)); err != nil {
			return err
		}
	}
	return s.clearTrack(ctx, movieID, versionID, v.FilePath, func(r *Repo) error {
		if err := r.DeleteVersion(ctx, versionID); err != nil {
			return err
		}
		_ = r.AddEvent(ctx, movieID, "version_removed", "Removed version: "+v.Label)
		return nil
	})
}

// DeleteVersionFile deletes a version's file (vid 0 = the default version). If the bin
// refuses the file, nothing changes.
func (s *Service) DeleteVersionFile(ctx context.Context, movieID, versionID int64) error {
	if versionID == 0 {
		return s.DeleteFile(ctx, movieID)
	}
	v, owner, err := s.repo.GetVersion(ctx, versionID)
	if err != nil {
		return err
	}
	if owner != movieID {
		return ErrNotFound // another movie's track: never delete through this one
	}
	if v.FilePath != "" {
		versions, _ := s.VersionRows(ctx, movieID)
		if err := s.removeFile(v.FilePath, otherFiles(versions, v.FilePath)); err != nil {
			return err
		}
	}
	return s.clearTrack(ctx, movieID, versionID, v.FilePath, func(r *Repo) error {
		if v.FilePath != "" {
			_ = r.AddEvent(ctx, movieID, "deleted", "Deleted "+filepath.Base(v.FilePath)+" ("+v.Label+")")
		}
		return r.ClearVersionFile(ctx, versionID)
	})
}

// removeFile moves a library file and its subtitles to the recycle bin (or deletes them
// when the bin is deliberately off) and prunes the emptied movie folder. If the bin
// refuses the video, nothing is removed and the error says why. keep lists other files
// in the same folder that stay, so their subtitles aren't swept up by a name prefix
// ("Movie.mkv" vs "Movie.Directors.Cut.mkv").
func (s *Service) removeFile(path string, keep []string) error {
	// A same-name container swap (X.mp4 replaced by X.mkv) keeps the subtitles: they pair
	// with the new video too.
	var subs []string
	if !library.SharesBase(path) {
		// Every other recorded file in the folder keeps its subtitles too — another
		// film's, and one that's missing from disk (whose subtitles pairing can't see).
		recorded, err := s.repo.OtherRecordedPaths(context.Background(), 0)
		if err != nil {
			s.log.Warn("movie: couldn't read the library's other files, so the subtitles stay", "path", path, "err", err)
		} else {
			keep = append([]string(nil), keep...)
			for _, r := range recorded {
				if r != path && strings.EqualFold(filepath.Dir(filepath.Clean(r)), filepath.Dir(filepath.Clean(path))) {
					keep = append(keep, r)
				}
			}
			subs = sidecarsOf(path, keep)
		}
	}
	dst, err := library.RemoveToBin(s.bin, path)
	if err != nil {
		return err
	}
	if dst != "" {
		s.log.Info("moved to recycle bin", "from", path, "to", dst)
	}
	s.fileRemoved(path)
	for _, sub := range subs {
		if _, err := library.RemoveToBin(s.bin, sub); err != nil {
			s.log.Warn("movie: subtitle left behind", "path", sub, "err", err)
			continue
		}
		s.fileRemoved(sub)
	}
	// Best-effort: remove the movie folder if nothing else is left in it — never a
	// library root, and never a symlinked folder (os.Remove would unlink the link however
	// full the folder it points at is).
	s.removeEmptyDir(filepath.Dir(path))
	return nil
}

// fileRemoved lets the import pipeline forget a file, so re-grabbing the same release
// (e.g. after deleting a version) imports again instead of being deduped, and a
// still-seeding torrent isn't imported straight back. The hook runs directly so it can't
// be missed; the bus event stays for the UI.
func (s *Service) fileRemoved(path string) {
	if s.onFileRemoved != nil {
		s.onFileRemoved(context.Background(), path)
	}
	if s.bus != nil {
		s.bus.Publish("file.removed", map[string]any{"path": path})
	}
}

// otherFiles lists the movie's files other than path.
func otherFiles(versions []Version, path string) []string {
	var out []string
	for _, v := range versions {
		if v.FilePath != "" && v.FilePath != path {
			out = append(out, v.FilePath)
		}
	}
	return out
}

// sidecarsOf is library.Sidecars minus any subtitle that belongs to one of the keep files.
// A subtitle that pairs with both belongs to the longer, more specific name: deleting
// "Movie.Directors.Cut.mkv" takes "Movie.Directors.Cut.en.srt" with it even though the
// kept "Movie.mkv" is a prefix of that name.
func sidecarsOf(video string, keep []string) []string {
	vb := strings.TrimSuffix(video, filepath.Ext(video))
	var out []string
	for _, sub := range library.Sidecars(video) {
		stem := strings.TrimSuffix(sub, filepath.Ext(sub))
		owned := false
		for _, k := range keep {
			if filepath.Dir(k) != filepath.Dir(sub) {
				continue
			}
			kb := strings.TrimSuffix(k, filepath.Ext(k))
			if len(kb) <= len(vb) {
				continue // a shorter name only reaches this subtitle through the video's own
			}
			if stem == kb || strings.HasPrefix(stem, kb+".") {
				owned = true
				break
			}
		}
		if !owned {
			out = append(out, sub)
		}
	}
	return out
}

// PlanFile is one file a movie delete would move: the default track (ID 0) or an extra.
type PlanFile struct {
	ID        int64  `json:"id"`
	Label     string `json:"label"`
	FileName  string `json:"file_name"`
	SizeBytes int64  `json:"size_bytes"`
}

// PlanExtra is a leftover file (artwork, metadata) a movie delete would move with the
// movie's own folder.
type PlanExtra struct {
	Folder    string `json:"folder"` // the movie folder's name
	Name      string `json:"name"`   // relative to that folder ("poster.jpg", "extrafanart/1.jpg")
	Path      string `json:"path"`   // the full path, which the delete is sent back to confirm it
	SizeBytes int64  `json:"size_bytes"`
}

// PlanFolder is a folder holding the movie's files and whether the delete removes it.
type PlanFolder struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Removed bool   `json:"removed"`
	// Kept says why the folder's other files stay ("" when there are none, or they go).
	Kept string `json:"kept,omitempty"`
}

// DeletePlan is what deleting a movie with its files would touch, for the dialog to state
// before anything happens. It is the plan the delete carries out (purgePlanFor).
type DeletePlan struct {
	Versions []PlanFile   `json:"versions"` // only files actually on disk
	Sidecars int          `json:"sidecars"` // subtitles that travel with them
	Extras   []PlanExtra  `json:"extras"`   // leftovers in the movie's own folder
	Folders  []PlanFolder `json:"folders"`  // the folders holding its files
	Bytes    int64        `json:"bytes"`    // videos, subtitles and leftovers
	paths    []string     // every path the delete removes, for tests
}

// DeletePlan lists what deleting a movie with its files removes, without changing anything.
func (s *Service) DeletePlan(ctx context.Context, id int64) (DeletePlan, error) {
	plan := DeletePlan{Versions: []PlanFile{}, Extras: []PlanExtra{}, Folders: []PlanFolder{}}
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return plan, err
	}
	extras, err := s.repo.ListVersions(ctx, id)
	if err != nil {
		return plan, err
	}
	files, labels := trackedFiles(m, extras)
	p := s.purgePlanFor(ctx, m, files, labels)
	size := func(path string) int64 {
		if fi, err := os.Stat(path); err == nil {
			return fi.Size()
		}
		return 0
	}
	for _, v := range p.videos {
		plan.Versions = append(plan.Versions, PlanFile{ID: v.versionID, Label: v.label, FileName: filepath.Base(v.path), SizeBytes: v.size})
		plan.Bytes += v.size
		for _, sub := range v.subs {
			plan.Sidecars++
			plan.Bytes += size(sub)
		}
	}
	for _, f := range p.folders {
		for _, x := range f.extras {
			rel, err := filepath.Rel(f.dir, x)
			if err != nil {
				rel = filepath.Base(x)
			}
			n := size(x)
			plan.Extras = append(plan.Extras, PlanExtra{Folder: filepath.Base(f.dir), Name: filepath.ToSlash(rel), Path: x, SizeBytes: n})
			plan.Bytes += n
		}
		plan.Folders = append(plan.Folders, PlanFolder{Name: filepath.Base(f.dir), Path: f.dir, Removed: f.remove, Kept: f.kept})
	}
	plan.paths = p.paths()
	return plan, nil
}
