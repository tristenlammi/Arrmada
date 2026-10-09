package movies

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tristenlammi/arrmada/internal/library"
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
// the truth. It never falls back to a permanent delete.
func (s *Service) Delete(ctx context.Context, id int64, deleteFiles bool) error {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if deleteFiles {
		if err := s.removeAllFiles(ctx, m); err != nil {
			return err
		}
	}
	return s.repo.Delete(ctx, id) // the movie, its versions and its history together
}

// movieFile is one file a movie holds: the default track (versionID 0) or an extra.
type movieFile struct {
	versionID int64
	path      string
}

// removeAllFiles moves every file the movie holds to the bin, for Delete.
func (s *Service) removeAllFiles(ctx context.Context, m Movie) error {
	// Read the extras directly: Versions degrades to default-only on a read error, and
	// here that would delete the movie while leaving its other files untracked on disk.
	extras, err := s.repo.ListVersions(ctx, m.ID)
	if err != nil {
		return err
	}
	var files []movieFile
	seen := map[string]bool{}
	add := func(vid int64, p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			files = append(files, movieFile{vid, p})
		}
	}
	add(0, m.MovieFilePath)
	for _, v := range extras {
		add(v.ID, v.FilePath)
	}
	if len(files) == 0 {
		return nil
	}
	// Pre-flight: a bin that can't even be created fails before a single file moves.
	if err := library.CheckBin(s.bin, files[0].path); err != nil {
		_ = s.repo.AddEvent(ctx, m.ID, "delete.failed", "Couldn't use the recycle bin, so nothing was deleted: "+err.Error())
		return &FilesNotRemovedError{Moved: []string{}, Failed: []string{filepath.Base(files[0].path)}, Err: err}
	}
	var moved []movieFile
	for _, f := range files {
		if err := s.removeFile(f.path, nil); err != nil {
			// The moved files are in the bin now; their tracks must read as missing.
			names := make([]string, 0, len(moved))
			for _, mf := range moved {
				names = append(names, filepath.Base(mf.path))
				if mf.versionID == 0 {
					_ = s.repo.ClearFile(ctx, m.ID)
				} else {
					_ = s.repo.ClearVersionFile(ctx, mf.versionID)
				}
			}
			_ = s.repo.AddEvent(ctx, m.ID, "delete.failed",
				fmt.Sprintf("Stopped deleting after %d file(s): %v", len(moved), err))
			return &FilesNotRemovedError{Moved: names, Failed: []string{filepath.Base(f.path)}, Err: err}
		}
		moved = append(moved, f)
	}
	return nil
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
		_ = s.repo.AddEvent(ctx, id, "deleted", "Deleted "+filepath.Base(m.MovieFilePath))
	}
	return s.repo.ClearFile(ctx, id)
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
	if err := s.repo.DeleteVersion(ctx, versionID); err != nil {
		return err
	}
	_ = s.repo.AddEvent(ctx, movieID, "version_removed", "Removed version: "+v.Label)
	return nil
}

// DeleteVersionFile deletes a version's file (vid 0 = the default version). If the bin
// refuses the file, nothing changes.
func (s *Service) DeleteVersionFile(ctx context.Context, movieID, versionID int64) error {
	if versionID == 0 {
		return s.DeleteFile(ctx, movieID)
	}
	v, _, err := s.repo.GetVersion(ctx, versionID)
	if err != nil {
		return err
	}
	if v.FilePath != "" {
		versions, _ := s.VersionRows(ctx, movieID)
		if err := s.removeFile(v.FilePath, otherFiles(versions, v.FilePath)); err != nil {
			return err
		}
		_ = s.repo.AddEvent(ctx, movieID, "deleted", "Deleted "+filepath.Base(v.FilePath)+" ("+v.Label+")")
	}
	return s.repo.ClearVersionFile(ctx, versionID)
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
		subs = sidecarsOf(path, keep)
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
	// Best-effort: remove the movie folder if nothing else is left in it — never the
	// library root itself.
	if dir := filepath.Dir(path); s.root == "" || filepath.Clean(dir) != filepath.Clean(s.root) {
		_ = os.Remove(dir)
	}
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
func sidecarsOf(video string, keep []string) []string {
	var out []string
	for _, sub := range library.Sidecars(video) {
		stem := strings.TrimSuffix(sub, filepath.Ext(sub))
		owned := false
		for _, k := range keep {
			if filepath.Dir(k) != filepath.Dir(sub) {
				continue
			}
			kb := strings.TrimSuffix(k, filepath.Ext(k))
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

// DeletePlan is what deleting a movie with its files would touch, for the dialog to state
// before anything happens.
type DeletePlan struct {
	Versions []PlanFile `json:"versions"` // only files actually on disk
	Sidecars int        `json:"sidecars"` // subtitles that travel with them
	Bytes    int64      `json:"bytes"`    // videos plus subtitles
}

// DeletePlan lists a movie's files and their subtitles without changing anything.
func (s *Service) DeletePlan(ctx context.Context, id int64) (DeletePlan, error) {
	plan := DeletePlan{Versions: []PlanFile{}}
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return plan, err
	}
	extras, err := s.repo.ListVersions(ctx, id)
	if err != nil {
		return plan, err
	}
	all := append([]Version{{ID: 0, Label: "Default", FilePath: m.MovieFilePath}}, extras...)
	seen := map[string]bool{}
	for _, v := range all {
		if v.FilePath == "" || seen[v.FilePath] {
			continue
		}
		seen[v.FilePath] = true
		fi, err := os.Stat(v.FilePath)
		if err != nil {
			continue // already gone from disk: nothing to move
		}
		plan.Versions = append(plan.Versions, PlanFile{ID: v.ID, Label: v.Label, FileName: filepath.Base(v.FilePath), SizeBytes: fi.Size()})
		plan.Bytes += fi.Size()
		for _, sub := range sidecarsOf(v.FilePath, nil) {
			if seen[sub] {
				continue // a prefix-named version's subtitle, already counted
			}
			seen[sub] = true
			plan.Sidecars++
			if si, err := os.Stat(sub); err == nil {
				plan.Bytes += si.Size()
			}
		}
	}
	return plan, nil
}
