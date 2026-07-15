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

// Manager orchestrates importing finished downloads: dedupe, import, record,
// and announce.
type Manager struct {
	imp  *Importer
	repo *importRepo
	bus  *eventbus.Bus
	log  *slog.Logger
}

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

		res, err := m.imp.Import(c.Name, c.ContentPath)
		if err != nil {
			m.log.Warn("import failed", "name", c.Name, "err", err)
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

// SetNaming installs the user-configurable naming scheme on the import path.
func (m *Manager) SetNaming(np NamingProvider) { m.imp.SetNaming(np) }

// Recent returns the latest imports.
func (m *Manager) Recent(ctx context.Context, limit int) ([]ImportRecord, error) {
	return m.repo.recent(ctx, limit)
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
