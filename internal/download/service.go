package download

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/connstatus"
	"github.com/tristenlammi/arrmada/internal/safego"
)

// Service manages download clients and dispatches downloads to them.
type Service struct {
	repo     *Repo
	registry *Registry
	log      *slog.Logger
	// status records each client's last queue read and Test (see connstatus). Clients
	// are never paused — a dead one is only shown — so outcomes carry no backoff.
	status *connstatus.Tracker
	// snap is the shared, briefly cached queue read every caller goes through (snapshot.go).
	snap snapshotCache
}

// SetStatus wires the integration status tracker; nil records nothing.
func (s *Service) SetStatus(t *connstatus.Tracker) { s.status = t }

// record notes one client's outcome. A failure while ctx is done (shutdown, a closed
// page) says nothing about the client and is dropped.
func (s *Service) record(ctx context.Context, c Client, err error, dur time.Duration) {
	if s.status == nil || (err != nil && ctx.Err() != nil) {
		return
	}
	s.status.Record(connstatus.KindDownloadClient, strconv.FormatInt(c.ID, 10),
		connstatus.Outcome{Err: err, Dur: dur, Name: c.Name})
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
func (s *Service) Create(ctx context.Context, c Client) (Client, error) {
	defer s.invalidateSnapshot()
	return s.repo.Create(ctx, c)
}

// Get returns one stored client.
func (s *Service) Get(ctx context.Context, id int64) (Client, error) { return s.repo.Get(ctx, id) }

// sessionForgetter is a client implementation that caches a login per client id.
type sessionForgetter interface {
	Forget(id int64)
}

// forget drops any cached login for the client, so the next call logs in with what's
// stored now rather than riding a session made with the old URL or password.
func (s *Service) forget(id int64) {
	for _, impl := range s.registry.impls {
		if f, ok := impl.(sessionForgetter); ok {
			f.Forget(id)
		}
	}
}

// Update changes a stored client in place (a blank password keeps the stored one) and
// returns it as saved. A disabled client gets no new downloads (Add reads ListEnabled)
// and drops out of the health check, but the torrents already in it are still read and
// acted on — see existingClients.
func (s *Service) Update(ctx context.Context, c Client) (Client, error) {
	defer s.invalidateSnapshot()
	if err := s.repo.Update(ctx, c); err != nil {
		return Client{}, err
	}
	s.forget(c.ID)
	// A changed URL or login is a different connection: its old failures say nothing
	// about the new one.
	if err := s.status.Reset(ctx, connstatus.KindDownloadClient, strconv.FormatInt(c.ID, 10)); err != nil {
		s.log.Warn("download client: couldn't clear its saved status", "id", c.ID, "err", err)
	}
	return s.repo.Get(ctx, c.ID)
}

// Delete removes a client and its recorded status. Its cached login goes with it: SQLite
// can hand the id to the next client added, which must not inherit a session for somebody
// else's WebUI.
func (s *Service) Delete(ctx context.Context, id int64) error {
	defer s.invalidateSnapshot()
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	s.forget(id)
	if err := s.status.Forget(ctx, connstatus.KindDownloadClient, strconv.FormatInt(id, 10)); err != nil {
		s.log.Warn("download client: couldn't clear its saved status", "id", id, "err", err)
	}
	return nil
}

// existingClients is every client, switched off or not, for work on torrents that are
// already downloading: reading the queue, pausing, resuming, removing. "Disabled" means
// "send it nothing new". Leaving a disabled client's torrents out of the queue made them
// look vanished to stall detection, which blocklisted healthy downloads and grabbed them
// again elsewhere; their imports and seed goals stalled too.
func (s *Service) existingClients(ctx context.Context) ([]Client, error) {
	return s.repo.List(ctx)
}

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
	start := time.Now()
	err = impl.Test(ctx, c)
	s.record(ctx, c, err, time.Since(start))
	return err
}

// Add dispatches a download to the first enabled client (later: route by
// protocol / user choice).
func (s *Service) Add(ctx context.Context, req AddRequest) error {
	defer s.invalidateSnapshot()
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

// Remove deletes a torrent (and optionally its data) from whichever client
// holds it, switched off or not.
func (s *Service) Remove(ctx context.Context, hash string, deleteData bool) error {
	defer s.invalidateSnapshot()
	// Checked here as well as at the HTTP edge: every caller that removes a torrent goes
	// through this, and a bad value here can empty the whole client.
	if !ValidHash(hash) {
		return ErrInvalidHash
	}
	clients, err := s.existingClients(ctx)
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

// savePathManager is implemented by clients whose default save path Arrmada manages.
type savePathManager interface {
	SetSavePath(ctx context.Context, dc Client, savePath string) error
}

// SetBundledSavePath points the client at url at the given downloads dir, so the
// client and Arrmada agree on where files land (and stay on the shared volume for
// hardlinking). No-op for client kinds without a managed save path.
func (s *Service) SetBundledSavePath(ctx context.Context, url, savePath string) error {
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
		if sp, ok := impl.(savePathManager); ok {
			return sp.SetSavePath(ctx, c, savePath)
		}
		return nil // client kind has no managed save path
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
	defer s.invalidateSnapshot()
	return s.onHash(ctx, func(impl Downloader, c Client) error { return impl.Pause(ctx, c, hash) })
}

// Resume restarts a stopped torrent on whichever enabled client holds it.
func (s *Service) Resume(ctx context.Context, hash string) error {
	defer s.invalidateSnapshot()
	return s.onHash(ctx, func(impl Downloader, c Client) error { return impl.Resume(ctx, c, hash) })
}

// ResumeMany restarts several torrents with one request per client, hashes joined
// the way qBittorrent's start/resume endpoint takes them. Every client gets the whole
// list: qBittorrent ignores hashes it doesn't have, so stopping at the first client
// that answers (as onHash does) would never reach a second client's torrents. Fails
// only when no client took the request.
func (s *Service) ResumeMany(ctx context.Context, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	defer s.invalidateSnapshot()
	clients, err := s.existingClients(ctx)
	if err != nil {
		return err
	}
	joined := strings.Join(hashes, "|")
	var lastErr error
	ok := false
	for _, c := range clients {
		impl, found := s.registry.For(c.Kind)
		if !found {
			continue
		}
		if err := impl.Resume(ctx, c, joined); err != nil {
			lastErr = err
			continue
		}
		ok = true
	}
	if ok {
		return nil
	}
	return lastErr
}

// Action runs a hash-scoped command (recheck/reannounce/prio_up/prio_down).
func (s *Service) Action(ctx context.Context, hash, action string) error {
	defer s.invalidateSnapshot()
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

// EnsureBundledQueue re-applies the client at url's current queue settings, which
// re-derives max_active_torrents from the per-kind limits — fixing torrents stuck
// "Queued" behind qBittorrent's default total-active cap of 5, without the user
// having to re-save anything.
func (s *Service) EnsureBundledQueue(ctx context.Context, url string) error {
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
			return nil
		}
		sm, ok := impl.(settingsManager)
		if !ok {
			return nil // client kind has no tunable queue settings
		}
		cur, err := sm.GetSettings(ctx, c)
		if err != nil {
			return err
		}
		return sm.SetSettings(ctx, c, cur)
	}
	return fmt.Errorf("client %q not found", url)
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

// onHash runs fn against each client holding torrents (see existingClients), returning on
// the first success.
func (s *Service) onHash(ctx context.Context, fn func(Downloader, Client) error) error {
	clients, err := s.existingClients(ctx)
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

// ClientState is whether one enabled download client answered just now.
type ClientState struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      Kind   `json:"kind"`
	OK        bool   `json:"ok"`
	Err       string `json:"error,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
}

// pinger is a client that can be checked without logging in again.
type pinger interface {
	Ping(ctx context.Context, dc Client) error
}

// clientStateTimeout bounds each client's answer, so one dead client can't hold up the rest.
const clientStateTimeout = 5 * time.Second

// ClientStates asks each enabled client, on its own and in parallel, whether it answers.
// It's for the health panel only: Queue can't say which client is down (it only fails
// when every one is), so a dead second client stayed invisible. Stall detection must
// never use this — it reads the queue fresh with QueueComplete.
func (s *Service) ClientStates(ctx context.Context) ([]ClientState, error) {
	clients, err := s.repo.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ClientState, len(clients))
	var wg sync.WaitGroup
	for i, c := range clients {
		out[i] = ClientState{ID: c.ID, Name: c.Name, Kind: c.Kind}
		impl, ok := s.registry.For(c.Kind)
		if !ok {
			out[i].Err = fmt.Sprintf("no downloader for kind %q", c.Kind)
			continue
		}
		wg.Add(1)
		go func(st *ClientState, c Client) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, clientStateTimeout)
			defer cancel()
			start := time.Now()
			// A panicking client counts as down rather than taking the app with it.
			err := safego.Call(s.log, "download client check "+c.Name, func() error {
				if p, ok := impl.(pinger); ok {
					return p.Ping(cctx, c)
				}
				return impl.Test(cctx, c)
			})
			st.LatencyMS = time.Since(start).Milliseconds()
			if err != nil {
				st.Err = err.Error()
				return
			}
			st.OK = true
		}(&out[i], c)
	}
	wg.Wait()
	return out, nil
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
		if it.Complete() && (category == "" || it.Category == category) {
			out = append(out, it)
		}
	}
	return out, nil
}

// Queue is the shared queue snapshot's torrents, across every client (see
// existingClients). A partial result — some clients answered, some didn't — is returned
// without error; callers that draw conclusions from a torrent's ABSENCE must use
// QueueComplete instead.
func (s *Service) Queue(ctx context.Context) ([]Item, error) {
	items, _, err := s.QueueComplete(ctx)
	return items, err
}

// QueueComplete is Queue that also reports whether every enabled client answered.
//
// It matters because "this torrent isn't in the queue" is treated as a stall, and that
// blocklists the release and grabs an alternate. With more than one client, a single
// unreachable one makes all of its torrents vanish from the list while Queue still returns
// nil error — so healthy downloads would be condemned for their client being down.
func (s *Service) QueueComplete(ctx context.Context) ([]Item, bool, error) {
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return nil, false, err
	}
	return snap.Items, snap.Complete, nil
}
