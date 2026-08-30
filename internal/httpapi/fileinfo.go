package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/convert"
)

// "Which torrent is this file?" had no answer anywhere in the app. The chain exists —
// the import records the download hash that produced a target path, and the grab
// records what was downloaded under that hash — it had just never been joined up and
// shown.
type fileDetails struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Dir      string `json:"dir"`
	Exists   bool   `json:"exists"`
	Size     int64  `json:"size_bytes"`
	Modified int64  `json:"modified_ms"`
	// MissingReason explains a file the library still believes in but that isn't on
	// disk — a far more useful answer than an empty panel.
	MissingReason string `json:"missing_reason,omitempty"`

	Media     *convert.MediaInfo `json:"media,omitempty"`
	MediaNote string             `json:"media_note,omitempty"`

	Source *fileSource `json:"source,omitempty"`
	// SourceNote says why provenance is absent or partial, so "no release shown" can be
	// told apart from "we don't know".
	SourceNote string `json:"source_note,omitempty"`
}

// fileSource is where the file came from: the release that was grabbed and the torrent
// it arrived in.
type fileSource struct {
	Release        string  `json:"release"`
	Indexer        string  `json:"indexer,omitempty"`
	InfoHash       string  `json:"info_hash,omitempty"`
	QualityProfile string  `json:"quality_profile,omitempty"`
	GrabbedMS      int64   `json:"grabbed_ms,omitempty"`
	ImportedMS     int64   `json:"imported_ms,omitempty"`
	SourcePath     string  `json:"source_path,omitempty"`
	Manual         bool    `json:"manual"`
	SeedEnabled    bool    `json:"seed_enabled"`
	SeedRatio      float64 `json:"seed_ratio,omitempty"`
	SeedHours      int     `json:"seed_hours,omitempty"`
	// FromPack marks a release that brought in more than this one file, so the UI can
	// say "this episode came from that season pack" rather than implying a 1:1 grab.
	FromPack bool `json:"from_pack"`
	// InClient reports whether the torrent is still in the download client — the
	// difference between "seeding now" and "long gone".
	InClient bool    `json:"in_client"`
	State    string  `json:"state,omitempty"`
	Ratio    float64 `json:"ratio,omitempty"`
}

// handleFileInfo describes one library file: what's on disk, what's inside it, and which
// release it came from.
func (a *api) handleFileInfo(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		a.writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	// Only ever describe files inside a configured library root. Without this the
	// endpoint reads arbitrary paths on the host for any signed-in manager.
	if !a.underLibraryRoot(path) {
		a.writeError(w, http.StatusForbidden, "that path isn't inside a library folder")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	out := fileDetails{Path: path, Name: filepath.Base(path), Dir: filepath.Dir(path)}
	if st, err := os.Stat(path); err == nil {
		out.Exists, out.Size, out.Modified = true, st.Size(), st.ModTime().UnixMilli()
	} else if errors.Is(err, os.ErrNotExist) {
		out.MissingReason = "the file isn't on disk — it was moved or deleted outside Arrmada"
	} else {
		out.MissingReason = err.Error()
	}

	if out.Exists && a.deps.Convert != nil {
		if mi, err := a.deps.Convert.MediaInfoFor(ctx, path); err == nil {
			out.Media = mi
		} else {
			out.MediaNote = "couldn't read the media details: " + err.Error()
		}
	} else if out.Exists {
		out.MediaNote = "the Convert module isn't running, so media details aren't available"
	}

	out.Source, out.SourceNote = a.fileSourceFor(ctx, path)
	a.writeJSON(w, http.StatusOK, out)
}

// underLibraryRoot reports whether path sits inside a configured media root. Compared on
// cleaned absolute paths with a separator boundary, so "/library-old" can't pass as
// "/library".
func (a *api) underLibraryRoot(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	c := a.deps.Config
	for _, root := range []string{
		c.LibraryDir, c.MoviesDir, c.TVDir, c.EbooksDir, c.AudiobooksDir, c.MusicDir, c.DownloadsDir,
	} {
		if strings.TrimSpace(root) == "" {
			continue
		}
		rabs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rabs = filepath.Clean(rabs)
		if abs == rabs || strings.HasPrefix(abs, rabs+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// fileSourceFor joins a library path back to the release it came from:
//
//	imports.target_path → imports.download_hash → grabs.info_hash
//
// A season pack imports many files under one hash, and imports only records one
// target_path for it, so an exact hit isn't always available. In that case the hash is
// matched by SOURCE path prefix instead, which still identifies the torrent — and the
// result is flagged as a pack so the UI doesn't imply a one-file grab.
func (a *api) fileSourceFor(ctx context.Context, path string) (*fileSource, string) {
	if a.deps.Store == nil {
		return nil, ""
	}
	db := a.deps.Store.DB()

	var hash, sourcePath, importTitle string
	var importedAt sql.NullTime
	var fromPack bool

	err := db.QueryRowContext(ctx,
		`SELECT download_hash, source_path, title, imported_at FROM imports WHERE target_path = ?`,
		path).Scan(&hash, &sourcePath, &importTitle, &importedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// Not the file the import row named. A pack's other episodes land beside it, so
		// find the import whose target sits in the same folder.
		dir := filepath.Dir(path) + string(filepath.Separator)
		err = db.QueryRowContext(ctx,
			`SELECT download_hash, source_path, title, imported_at FROM imports
			 WHERE target_path LIKE ? ORDER BY imported_at DESC LIMIT 1`, dir+"%").
			Scan(&hash, &sourcePath, &importTitle, &importedAt)
		fromPack = err == nil
	}
	if err != nil {
		return nil, "no import record for this file — it was probably added by a library scan rather than grabbed by Arrmada"
	}

	src := &fileSource{
		Release: importTitle, InfoHash: hash, SourcePath: sourcePath, FromPack: fromPack,
	}
	if importedAt.Valid {
		src.ImportedMS = importedAt.Time.UnixMilli()
	}

	// The grab carries what the import doesn't: which indexer, which profile, whether
	// the user picked it by hand, and the seed goal it's being held to.
	var grabbedAt sql.NullTime
	var manual, seedEnabled int
	var title, indexer, profile string
	gerr := db.QueryRowContext(ctx,
		`SELECT title, indexer, quality_profile, manual, seed_enabled, seed_ratio, seed_hours, grabbed_at
		   FROM grabs WHERE info_hash = ? AND info_hash != '' ORDER BY id DESC LIMIT 1`, hash).
		Scan(&title, &indexer, &profile, &manual, &seedEnabled, &src.SeedRatio, &src.SeedHours, &grabbedAt)
	switch {
	case gerr == nil:
		if strings.TrimSpace(title) != "" {
			src.Release = title // the grabbed release name beats the import's own label
		}
		src.Indexer, src.QualityProfile = indexer, profile
		src.Manual, src.SeedEnabled = manual == 1, seedEnabled == 1
		if grabbedAt.Valid {
			src.GrabbedMS = grabbedAt.Time.UnixMilli()
		}
	case !errors.Is(gerr, sql.ErrNoRows):
		a.deps.Log.Debug("file info: grab lookup failed", "err", gerr)
	}

	// Is the torrent still around? "Seeding" and "removed months ago" look identical
	// without asking the client.
	if a.deps.Downloads != nil && hash != "" {
		if items, err := a.deps.Downloads.Queue(ctx); err == nil {
			for _, it := range items {
				if strings.EqualFold(it.Hash, hash) {
					src.InClient, src.State, src.Ratio = true, it.State, it.Ratio
					break
				}
			}
		}
	}

	note := ""
	if gerr != nil && errors.Is(gerr, sql.ErrNoRows) {
		note = "the torrent is recorded but its grab history has since been cleared"
	}
	return src, note
}
