package library

import (
	"context"
	"database/sql"
	"log/slog"
	"os"

	"github.com/tristenlammi/arrmada/internal/eventbus"
)

// fileExists reports whether a path is present on disk.
func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// Candidate is a finished download to (maybe) import. Kept package-local so the
// library doesn't depend on the download package.
type Candidate struct {
	Hash        string
	Name        string
	ContentPath string
	Category    string
}

// TitleResolver maps a finished download to the library title it belongs to, so
// imports are named from the metadata record (deterministic, matches the movie
// Arrmada tracks) instead of the scene release. ok=false → name from the release.
type TitleResolver interface {
	ResolveMovie(ctx context.Context, releaseName string) (title string, year int, ok bool)
}

// ImportGate can hold a finished download back from import for admin review — it
// returns a non-empty reason and hold=true when the content doesn't match what it
// was grabbed for. The Manager then skips it (records nothing) so the admin can
// resolve it. nil → import everything.
type ImportGate func(ctx context.Context, hash, name, contentPath string) (reason string, hold bool)

// ImportFailure is called when a candidate could not be imported, so the caller can
// react (blocklist the release, remove junk) rather than let the sweep retry forever.
type ImportFailure func(ctx context.Context, hash, name, contentPath string, cause error)

// Manager orchestrates importing finished downloads: dedupe, import, record,
// and announce.
type Manager struct {
	imp      *Importer
	repo     *importRepo
	bus      *eventbus.Bus
	log      *slog.Logger
	resolver TitleResolver // nil → name from the parsed release
	gate     ImportGate    // nil → no review gate
	onFail   ImportFailure // nil → failures are only logged
}

// SetGate installs the review gate that can hold a candidate back from import.
func (m *Manager) SetGate(g ImportGate) { m.gate = g }

// SetFailureHook installs a callback for candidates that fail to import, so the caller
// can blocklist the release / clean up junk. Without it the import sweep just logs and
// retries the same broken download on every pass, forever.
func (m *Manager) SetFailureHook(f ImportFailure) { m.onFail = f }

// NewManager wires the import manager against the library root.
func NewManager(db *sql.DB, root string, bus *eventbus.Bus, log *slog.Logger) *Manager {
	return &Manager{
		imp:  NewImporter(root, log),
		repo: &importRepo{db: db},
		bus:  bus,
		log:  log,
	}
}

// Process imports every not-yet-imported candidate and returns how many it
// imported. Individual failures are logged, not fatal.
func (m *Manager) Process(ctx context.Context, cands []Candidate) int {
	imported := 0
	for _, c := range cands {
		if c.Hash == "" || c.ContentPath == "" {
			continue
		}
		if m.gate != nil {
			if reason, hold := m.gate(ctx, c.Hash, c.Name, c.ContentPath); hold {
				m.log.Info("import held for review", "name", c.Name, "reason", reason)
				continue
			}
		}
		target, done, err := m.repo.targetFor(ctx, c.Hash)
		if err != nil {
			m.log.Warn("import dedupe check failed", "hash", c.Hash, "err", err)
			continue
		}
		if done {
			// Already imported AND the file is still on disk → nothing to do.
			if fileExists(target) {
				continue
			}
			// Stale record: the imported file is gone (deleted or cleaned up).
			// Forget it and re-import so the library is made whole again.
			_ = m.repo.forgetByHash(ctx, c.Hash)
			m.log.Info("re-importing: previously-imported file is missing", "hash", c.Hash, "was", target)
		}

		res, err := m.importOne(ctx, c)
		if err != nil {
			m.log.Warn("import failed", "name", c.Name, "err", err)
			if m.onFail != nil {
				m.onFail(ctx, c.Hash, c.Name, c.ContentPath, err)
			}
			continue
		}
		_ = m.repo.record(ctx, ImportRecord{
			Hash: c.Hash, SourcePath: res.SourcePath, TargetPath: res.TargetPath,
			Title: res.Title, SizeBytes: res.SizeBytes,
		})
		if m.bus != nil {
			m.bus.Publish("download.imported", map[string]any{
				"title":  res.Title,
				"year":   res.Year,
				"target": res.TargetPath,
				"name":   c.Name, // the release/torrent name — scored for upgrade decisions
			})
		}
		imported++
	}
	return imported
}

// importOne names by the matched library title when the resolver knows it (so the
// folder matches the movie record), otherwise parses the release name.
func (m *Manager) importOne(ctx context.Context, c Candidate) (*Result, error) {
	if m.resolver != nil {
		if title, year, ok := m.resolver.ResolveMovie(ctx, c.Name); ok {
			return m.imp.ImportAs(title, year, c.ContentPath)
		}
	}
	return m.imp.Import(c.Name, c.ContentPath)
}

// SetNaming installs the user-configurable naming scheme on the import path.
func (m *Manager) SetNaming(np NamingProvider) { m.imp.SetNaming(np) }

// SetTitleResolver installs the canonical-title lookup used at import time so
// movie folders match the library record rather than the scene release name.
func (m *Manager) SetTitleResolver(r TitleResolver) { m.resolver = r }

// SetRoots routes imports to per-media-type destinations (movies, TV, ebooks,
// audiobooks); empty values fall back to the base library root.
func (m *Manager) SetRoots(movie, tv, ebook, audiobook string) {
	m.imp.SetRoots(movie, tv, ebook, audiobook)
}

// Recent returns the latest imports.
func (m *Manager) Recent(ctx context.Context, limit int) ([]ImportRecord, error) {
	return m.repo.recent(ctx, limit)
}

// ImportedHashes returns the download hashes already imported into the library.
func (m *Manager) ImportedHashes(ctx context.Context) (map[string]bool, error) {
	return m.repo.importedHashes(ctx)
}

// WatchDeletions forgets import records when their files are deleted, so the
// same release can be re-imported after (e.g.) removing and re-adding it.
func (m *Manager) WatchDeletions(ctx context.Context) {
	if m.bus == nil {
		return
	}
	events, cancel := m.bus.Subscribe("file.removed")
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-events:
			data, ok := ev.Data.(map[string]any)
			if !ok {
				continue
			}
			if path, _ := data["path"].(string); path != "" {
				if err := m.repo.forgetByTarget(ctx, path); err != nil {
					m.log.Warn("forget import failed", "path", path, "err", err)
				}
			}
		}
	}
}
