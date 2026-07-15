package download

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// Service manages download clients and dispatches downloads to them.
type Service struct {
	repo     *Repo
	registry *Registry
	log      *slog.Logger
}

// NewService wires a Service over the database.
func NewService(db *sql.DB, log *slog.Logger) *Service {
	return &Service{repo: NewRepo(db), registry: NewRegistry(), log: log}
}

// List returns all configured clients.
func (s *Service) List(ctx context.Context) ([]Client, error) { return s.repo.List(ctx) }

// EnsureBundled registers the packaged qBittorrent companion as a download
// client on first startup (idempotent). Auth is bypassed on the private Docker
// network, so no credentials are needed.
func (s *Service) EnsureBundled(ctx context.Context, url string) error {
	clients, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	for _, c := range clients {
		if c.URL == url {
			return nil // already registered
		}
	}
	_, err = s.repo.Create(ctx, Client{
		Name:     "qBittorrent (bundled)",
		Kind:     KindQbittorrent,
		URL:      url,
		Category: "arrmada",
		Enabled:  true,
	})
	if err == nil {
		s.log.Info("registered bundled qBittorrent", "url", url)
	}
	return err
}

// Create stores a new client.
func (s *Service) Create(ctx context.Context, c Client) (Client, error) { return s.repo.Create(ctx, c) }

// Delete removes a client.
func (s *Service) Delete(ctx context.Context, id int64) error { return s.repo.Delete(ctx, id) }

// Test checks connectivity + auth for a stored client.
func (s *Service) Test(ctx context.Context, id int64) error {
	c, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	impl, ok := s.registry.For(c.Kind)
	if !ok {
		return fmt.Errorf("no downloader for kind %q", c.Kind)
	}
	return impl.Test(ctx, c)
}

// Add dispatches a download to the first enabled client (later: route by
// protocol / user choice).
func (s *Service) Add(ctx context.Context, req AddRequest) error {
	clients, err := s.repo.ListEnabled(ctx)
	if err != nil {
		return err
	}
	if len(clients) == 0 {
		return fmt.Errorf("no enabled download client configured")
	}
	c := clients[0]
	impl, ok := s.registry.For(c.Kind)
	if !ok {
		return fmt.Errorf("no downloader for kind %q", c.Kind)
	}
	if err := impl.Add(ctx, c, req); err != nil {
		return err
	}
	s.log.Info("download added", "client", c.Name, "release", req.Name)
	return nil
}

// Remove deletes a torrent (and optionally its data) from whichever enabled
// client holds it.
func (s *Service) Remove(ctx context.Context, hash string, deleteData bool) error {
	clients, err := s.repo.ListEnabled(ctx)
	if err != nil {
		return err
	}
	var lastErr error
	for _, c := range clients {
		impl, ok := s.registry.For(c.Kind)
		if !ok {
			continue
		}
		if err := impl.Remove(ctx, c, hash, deleteData); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

// portManager is implemented by clients whose incoming port Arrmada manages.
type portManager interface {
	SetListenPort(ctx context.Context, dc Client, port int) error
	ListenPort(ctx context.Context, dc Client) (int, error)
}

// SetBundledPort pins the incoming-connection port on the client at url.
func (s *Service) SetBundledPort(ctx context.Context, url string, port int) error {
	clients, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	for _, c := range clients {
		if c.URL != url {
			continue
		}
		impl, ok := s.registry.For(c.Kind)
		if !ok {
			return fmt.Errorf("no downloader for kind %q", c.Kind)
		}
		if pm, ok := impl.(portManager); ok {
			return pm.SetListenPort(ctx, c, port)
		}
		return nil // client kind has no managed port
	}
	return fmt.Errorf("client %q not found", url)
}

// ListenPort reports a client's incoming-connection port (0 if not applicable).
func (s *Service) ListenPort(ctx context.Context, id int64) (int, error) {
	c, err := s.repo.Get(ctx, id)
	if err != nil {
		return 0, err
	}
	impl, ok := s.registry.For(c.Kind)
	if !ok {
		return 0, nil
	}
	if pm, ok := impl.(portManager); ok {
		return pm.ListenPort(ctx, c)
	}
	return 0, nil
}

// Pause stops a torrent on whichever enabled client holds it.
func (s *Service) Pause(ctx context.Context, hash string) error {
	return s.onHash(ctx, func(impl Downloader, c Client) error { return impl.Pause(ctx, c, hash) })
}

// Resume restarts a stopped torrent on whichever enabled client holds it.
func (s *Service) Resume(ctx context.Context, hash string) error {
	return s.onHash(ctx, func(impl Downloader, c Client) error { return impl.Resume(ctx, c, hash) })
}

// Action runs a hash-scoped command (recheck/reannounce/prio_up/prio_down).
func (s *Service) Action(ctx context.Context, hash, action string) error {
	return s.onHash(ctx, func(impl Downloader, c Client) error { return impl.TorrentAction(ctx, c, hash, action) })
}

// GetSettings returns the tunable settings of a client (if it supports them).
func (s *Service) GetSettings(ctx context.Context, id int64) (ClientSettings, error) {
	c, err := s.repo.Get(ctx, id)
	if err != nil {
		return ClientSettings{}, err
	}
	impl, ok := s.registry.For(c.Kind)
	if !ok {
		return ClientSettings{}, fmt.Errorf("no downloader for kind %q", c.Kind)
	}
	if sm, ok := impl.(settingsManager); ok {
		return sm.GetSettings(ctx, c)
	}
	return ClientSettings{}, fmt.Errorf("%q has no tunable settings", c.Kind)
}

// SetSettings writes the tunable settings of a client.
func (s *Service) SetSettings(ctx context.Context, id int64, cs ClientSettings) error {
	c, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	impl, ok := s.registry.For(c.Kind)
	if !ok {
		return fmt.Errorf("no downloader for kind %q", c.Kind)
	}
	if sm, ok := impl.(settingsManager); ok {
		return sm.SetSettings(ctx, c, cs)
	}
	return fmt.Errorf("%q has no tunable settings", c.Kind)
}

// onHash runs fn against each enabled client, returning on the first success.
func (s *Service) onHash(ctx context.Context, fn func(Downloader, Client) error) error {
	clients, err := s.repo.ListEnabled(ctx)
	if err != nil {
		return err
	}
	var lastErr error
	for _, c := range clients {
		impl, ok := s.registry.For(c.Kind)
		if !ok {
			continue
		}
		if err := fn(impl, c); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

// CompletedInCategory returns finished (100%) downloads in the given category
// (empty = any) — the candidates for import.
func (s *Service) CompletedInCategory(ctx context.Context, category string) ([]Item, error) {
	all, err := s.Queue(ctx)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, it := range all {
		if it.Progress >= 1.0 && (category == "" || it.Category == category) {
			out = append(out, it)
		}
	}
	return out, nil
}

// Queue aggregates live download items across all enabled clients.
func (s *Service) Queue(ctx context.Context) ([]Item, error) {
	clients, err := s.repo.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, c := range clients {
		impl, ok := s.registry.For(c.Kind)
		if !ok {
			continue
		}
		part, err := impl.List(ctx, c)
		if err != nil {
			s.log.Warn("download client list failed", "client", c.Name, "err", err)
			continue
		}
		items = append(items, part...)
	}
	return items, nil
}
