package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tristenlammi/arrmada/internal/connstatus"
	"github.com/tristenlammi/arrmada/internal/safego"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Service manages download clients and dispatches downloads to them.
type Service struct {
	repo     *Repo
	registry *Registry
	log      *slog.Logger
	// status records each client's last queue read and Test (see connstatus). Clients
	// are never paused — a dead one is only shown — so outcomes carry no backoff.
	status *connstatus.Tracker
	flags  flagStore // the shared settings service (SetFlags); nil = no bundled-removed flag
	// snap is the shared, briefly cached queue read every caller goes through (snapshot.go).
	snap snapshotCache
	// transports caches which transports have a client to send to (HasTransport).
	transports transportCache
	// ownNets is the networks Arrmada's container is on (qbitauth.go); nil reads its
	// interfaces. lookupHost resolves qBittorrent's host; nil uses DNS. Tests set them.
	ownNets    func() []*net.IPNet
	lookupHost func(host string) []net.IP
	// qbitHeld says why the last SecureBundledQbit left an open qBittorrent alone on
	// purpose ("" when it didn't), for the health line.
	qbitHeld atomic.Pointer[string]
}

// transportCache remembers HasTransport's answer until a client is created, edited or
// deleted. gen counts those changes, so an answer worked out across one isn't kept.
type transportCache struct {
	mu    sync.Mutex
	gen   uint64
	valid bool
	has   map[Transport]bool
}

// invalidateTransports makes the next HasTransport read the client list again.
func (s *Service) invalidateTransports() {
	s.transports.mu.Lock()
	s.transports.gen++
	s.transports.valid = false
	s.transports.mu.Unlock()
}

// HasTransport reports whether an enabled client of transport t exists that Arrmada can
// talk to (its kind has an implementation) — whether a release of that transport can be
// downloaded at all. The answer is cached until a client is created, edited or deleted.
// An unreadable client list answers false: nothing is offered that can't be sent.
func (s *Service) HasTransport(ctx context.Context, t Transport) bool {
	if s == nil || s.repo == nil {
		return false
	}
	tc := &s.transports
	tc.mu.Lock()
	if tc.valid {
		v := tc.has[t]
		tc.mu.Unlock()
		return v
	}
	gen := tc.gen
	tc.mu.Unlock()

	clients, err := s.repo.ListEnabled(ctx)
	if err != nil {
		if s.log != nil {
			s.log.Warn("download clients: couldn't read which networks have a client", "err", err)
		}
		return false
	}
	has := map[Transport]bool{}
	for _, c := range clients {
		if _, ok := s.registry.For(c.Kind); ok && c.Kind.Transport() != "" {
			has[c.Kind.Transport()] = true
		}
	}
	tc.mu.Lock()
	if tc.gen == gen {
		tc.has, tc.valid = has, true
	}
	tc.mu.Unlock()
	return has[t]
}

// SetDownloader registers the implementation for a client kind (the usenet client; fakes
// in tests). Call it while wiring, before the service is in use.
func (s *Service) SetDownloader(kind Kind, d Downloader) {
	s.registry.Register(kind, d)
	s.invalidateTransports()
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
func NewService(db store.Pool, log *slog.Logger) *Service {
	return &Service{repo: NewRepo(db), registry: NewRegistry(), log: log}
}

// List returns all configured clients.
func (s *Service) List(ctx context.Context) ([]Client, error) { return s.repo.List(ctx) }

// KeyBundledRemoved is the setting that says the owner deleted the bundled qBittorrent,
// so startup must not add it back. Restore clears it.
const KeyBundledRemoved = "download_bundled_removed"

// KeyBundledRemovedSABnzbd is KeyBundledRemoved for the optional bundled SABnzbd.
const KeyBundledRemovedSABnzbd = "download_bundled_removed_sabnzbd"

// bundledRemovedKey is the removed flag for one kind's bundled client. qBittorrent keeps
// the key it has always had, so an install that removed it before this stays removed.
func bundledRemovedKey(kind Kind) string {
	if kind == KindQbittorrent {
		return KeyBundledRemoved
	}
	return KeyBundledRemoved + "_" + string(kind)
}

// bundledName is the name a bundled client is registered under.
func bundledName(kind Kind) string {
	switch kind {
	case KindQbittorrent:
		return "qBittorrent (bundled)"
	case KindSABnzbd:
		return "SABnzbd (bundled)"
	}
	return string(kind) + " (bundled)"
}

// flagStore is the slice of the shared settings service this package uses.
type flagStore interface {
	Get(ctx context.Context, key, def string) string
	Set(ctx context.Context, key, value string) error
}

// SetFlags wires the shared settings service, where the bundled-removed flag lives. nil
// (tests) means the bundled client is never treated as removed.
func (s *Service) SetFlags(f flagStore) { s.flags = f }

func (s *Service) bundledRemoved(ctx context.Context, kind Kind) bool {
	return s.flags != nil && s.flags.Get(ctx, bundledRemovedKey(kind), "") == "1"
}

// EnsureBundled registers the packaged qBittorrent companion as a download client
// (idempotent; run at startup) without a login: the Compose bundle's first config lets the
// private Docker network in until SecureBundledQbit gives it a password, and from then on
// EnsureBundledLogin registers it with that.
//
// It used to re-create a row whenever none had the bundled URL, so the bundled client
// couldn't be removed and an edit of its URL spawned a duplicate. Now the row is found by
// its bundled flag: once one is marked — enabled or not, whatever its URL — this does
// nothing, and nothing is added while the owner has deleted it (KeyBundledRemoved). An
// install from before the flag has its row recognised by URL and marked here.
func (s *Service) EnsureBundled(ctx context.Context, url string) error {
	return s.EnsureBundledOf(ctx, KindQbittorrent, url)
}

// EnsureBundledOf is EnsureBundled for one kind of packaged client: each kind has its own
// bundled row and its own removed flag, so the optional bundled SABnzbd neither stands in
// for the bundled qBittorrent nor brings it back. An existing row is recognised by URL
// only among clients of the same kind.
func (s *Service) EnsureBundledOf(ctx context.Context, kind Kind, url string) error {
	_, err := s.ensureBundled(ctx, Client{Kind: kind, URL: url}, false)
	return err
}

// ensureBundled is EnsureBundledOf for a template carrying the kind, the URL and, for a
// client that needs one, its key (Password). added says a new row was made.
//
// An existing row at the URL is adopted as the bundled client (installs from before the
// bundled flag). sameLogin limits that to a row that already has want's login: the
// all-in-one image's qBittorrent answers at 127.0.0.1:8080, which on host networking is
// also where an owner's own qBittorrent can be, and their row (and its password) must not
// be taken over. A row with another login is left as it is and the bundled one is added
// beside it.
func (s *Service) ensureBundled(ctx context.Context, want Client, sameLogin bool) (added bool, err error) {
	kind, url := want.Kind, want.URL
	if _, ok, err := s.repo.BundledOf(ctx, kind); err != nil || ok {
		return false, err
	}
	if s.bundledRemoved(ctx, kind) {
		return false, nil
	}
	clients, err := s.repo.List(ctx)
	if err != nil {
		return false, err
	}
	for _, c := range clients {
		if c.URL == url && c.Kind == kind {
			if sameLogin && (c.Username != want.Username || c.Password != want.Password) {
				s.log.Info("a download client of yours is at the bundled qBittorrent's address with another login; leaving it as it is and adding the bundled one beside it",
					"name", c.Name, "url", url)
				continue
			}
			if err := s.repo.MarkBundled(ctx, c.ID); err != nil {
				return false, err
			}
			if want.Password != "" && (c.Password != want.Password || c.Username != want.Username) {
				c.Username, c.Password = want.Username, want.Password
				_, err = s.update(ctx, c)
			}
			return false, err
		}
	}
	defer s.invalidateSnapshot() // a new client: the next queue read must ask it too
	defer s.invalidateTransports()
	name := bundledName(kind)
	_, err = s.repo.Create(ctx, Client{
		Name:     name,
		Kind:     kind,
		URL:      url,
		Username: want.Username,
		Password: want.Password,
		Enabled:  true,
		Bundled:  true,
	})
	if err != nil {
		return false, err
	}
	s.log.Info("registered bundled download client", "name", name, "url", url)
	return true, nil
}

// EnsureBundledSAB registers the optional bundled SABnzbd (docker-compose.yml's "sabnzbd"
// profile) with the API key read from its config (ReadSABAPIKey), and keeps a registered
// one's key in step when SABnzbd's own changes. A bundled row its owner pointed at another
// address is left alone: that key isn't this one. Nothing is added while the owner has
// deleted it. added says a new row was made, so the caller can run the Prowlarr sync.
func (s *Service) EnsureBundledSAB(ctx context.Context, url, key string) (added bool, err error) {
	if key == "" {
		return false, errNoSABKey
	}
	c, ok, err := s.repo.BundledOf(ctx, KindSABnzbd)
	if err != nil {
		return false, err
	}
	if !ok {
		return s.ensureBundled(ctx, Client{Kind: KindSABnzbd, URL: url, Password: key}, false)
	}
	if c.URL != url || c.Password == key {
		return false, nil
	}
	c.Password = key
	if _, err := s.update(ctx, c); err != nil {
		return false, err
	}
	s.log.Info("bundled SABnzbd's API key changed in SABnzbd; using the new one", "name", c.Name)
	return false, nil
}

// RestoreBundledSAB brings back a deleted bundled SABnzbd, with the key from its config.
func (s *Service) RestoreBundledSAB(ctx context.Context, url, key string) error {
	if key == "" {
		return errNoSABKey
	}
	if s.flags != nil {
		if err := s.flags.Set(ctx, bundledRemovedKey(KindSABnzbd), ""); err != nil {
			return err
		}
	}
	_, err := s.EnsureBundledSAB(ctx, url, key)
	return err
}

// BundledClient is the bundled client of a kind, switched off or not; false when none.
func (s *Service) BundledClient(ctx context.Context, kind Kind) (Client, bool, error) {
	return s.repo.BundledOf(ctx, kind)
}

// RestoreBundled brings back a deleted bundled qBittorrent: it clears the removed flag
// and registers it again (or marks an existing row with its URL).
func (s *Service) RestoreBundled(ctx context.Context, url string) error {
	return s.RestoreBundledOf(ctx, KindQbittorrent, url)
}

// RestoreBundledOf is RestoreBundled for one kind of packaged client.
func (s *Service) RestoreBundledOf(ctx context.Context, kind Kind, url string) error {
	if s.flags != nil {
		if err := s.flags.Set(ctx, bundledRemovedKey(kind), ""); err != nil {
			return err
		}
	}
	return s.EnsureBundledOf(ctx, kind, url)
}

// KeyBundledLogin is a hash of the bundled qBittorrent login last given to its client
// (EnsureBundledLogin), so a login the owner typed on the card isn't overwritten at every
// start, while a new one (a fresh qBittorrent config) still reaches the client.
const KeyBundledLogin = "download_bundled_login"

// EnsureBundledLogin is EnsureBundled for the Unraid all-in-one image's qBittorrent, which
// takes a login (Arrmada made it on first start, internal/aio): the client is registered
// with it, and an existing bundled client still pointing at url is given it whenever it's
// a different login from the one it was last given. A client the owner pointed elsewhere
// keeps its own.
func (s *Service) EnsureBundledLogin(ctx context.Context, url, username, password string) error {
	h := sha256.Sum256([]byte(username + "\x00" + password))
	mark := hex.EncodeToString(h[:])
	c, ok, err := s.repo.BundledOf(ctx, KindQbittorrent)
	if err != nil {
		return err
	}
	if !ok {
		if _, err := s.ensureBundled(ctx, Client{Kind: KindQbittorrent, URL: url, Username: username, Password: password}, true); err != nil {
			return err
		}
		if _, ok, err := s.repo.BundledOf(ctx, KindQbittorrent); err != nil || !ok {
			return err // removed by the owner: nothing was registered
		}
		return s.setFlag(ctx, KeyBundledLogin, mark)
	}
	if c.URL != url || (s.flags != nil && s.flags.Get(ctx, KeyBundledLogin, "") == mark) {
		return nil
	}
	if c.Username != username || c.Password != password {
		c.Username, c.Password = username, password
		if _, err := s.update(ctx, c); err != nil {
			return err
		}
		s.log.Info("bundled qBittorrent has a new login; its client uses it now", "name", c.Name)
	}
	return s.setFlag(ctx, KeyBundledLogin, mark)
}

// RestoreBundledLogin is RestoreBundled for the all-in-one image's qBittorrent.
func (s *Service) RestoreBundledLogin(ctx context.Context, url, username, password string) error {
	if s.flags != nil {
		if err := s.flags.Set(ctx, bundledRemovedKey(KindQbittorrent), ""); err != nil {
			return err
		}
	}
	return s.EnsureBundledLogin(ctx, url, username, password)
}

func (s *Service) setFlag(ctx context.Context, key, value string) error {
	if s.flags == nil {
		return nil
	}
	return s.flags.Set(ctx, key, value)
}

// KeyBundledSwitchedOff says the all-in-one image's built-in qBittorrent was switched off
// in the container's settings and Arrmada switched its client off to match, so switching
// it back on there switches the client back on. A client the owner switched off in
// Arrmada doesn't carry it, and stays off. The optional built-in SABnzbd has its own
// (switchedOffKey).
const KeyBundledSwitchedOff = "download_bundled_switched_off"

// switchedOffKey is KeyBundledSwitchedOff for one kind's bundled client. qBittorrent keeps
// the key it has always had, as bundledRemovedKey does.
func switchedOffKey(kind Kind) string {
	if kind == KindQbittorrent {
		return KeyBundledSwitchedOff
	}
	return KeyBundledSwitchedOff + "_" + string(kind)
}

// SyncBundledSwitch keeps the bundled qBittorrent's client in step with the all-in-one
// image's switch for it (SyncBundledSwitchOf).
func (s *Service) SyncBundledSwitch(ctx context.Context, url string, on bool) error {
	return s.SyncBundledSwitchOf(ctx, KindQbittorrent, url, on)
}

// SyncBundledSwitchOf keeps a bundled client in step with the all-in-one image's switch
// for it (its qBittorrent, or its optional SABnzbd): url is where the built-in one answers,
// on whether it's running. Off, an enabled bundled client still pointing there is switched
// off (not removed: what's in it is still read, and the owner can switch it back on), so
// nothing is sent to a client that isn't running. On again, a client switched off that way
// is switched back on. A bundled client the owner pointed somewhere else is left alone.
func (s *Service) SyncBundledSwitchOf(ctx context.Context, kind Kind, url string, on bool) error {
	if s.flags == nil {
		return nil
	}
	c, ok, err := s.repo.BundledOf(ctx, kind)
	if err != nil {
		return err
	}
	if !ok || c.URL != url {
		// Nothing of the switch's to undo. A note left from before (the row deleted, or
		// pointed elsewhere, outside Update) mustn't wake a later client up.
		if on {
			return s.clearSwitchedOff(ctx, kind)
		}
		return nil
	}
	key, app := switchedOffKey(kind), builtInName(kind)
	paused := s.flags.Get(ctx, key, "") == "1"
	switch {
	case on && paused:
		if !c.Enabled {
			c.Enabled = true
			if _, err := s.update(ctx, c); err != nil {
				return err
			}
			s.log.Info("built-in "+app+" switched back on; sending downloads to it again", "name", c.Name)
		}
		return s.flags.Set(ctx, key, "")
	case !on && c.Enabled:
		// The flag first: if the client is switched off without it, it would stay off
		// when the owner switches it back on.
		if err := s.flags.Set(ctx, key, "1"); err != nil {
			return err
		}
		c.Enabled = false
		if _, err := s.update(ctx, c); err != nil {
			return err
		}
		s.log.Info("built-in "+app+" is switched off in the container settings; its client is switched off too (what's in it is kept)", "name", c.Name)
	}
	return nil
}

// builtInName is how the log names a kind's built-in client.
func builtInName(kind Kind) string {
	switch kind {
	case KindQbittorrent:
		return "qBittorrent"
	case KindSABnzbd:
		return "SABnzbd"
	}
	return string(kind)
}

// ErrBundledInactive means there's no bundled qBittorrent to tune: it was removed, or it
// is switched off and so may not be running at all.
var ErrBundledInactive = errors.New("the bundled qBittorrent is switched off or removed")

// activeBundled is the bundled qBittorrent when it exists and is switched on. It is pinned
// to qBittorrent: the port, save path and queue tuning it serves are torrent settings.
func (s *Service) activeBundled(ctx context.Context) (Client, Downloader, error) {
	c, ok, err := s.repo.BundledOf(ctx, KindQbittorrent)
	if err != nil {
		return Client{}, nil, err
	}
	if !ok || !c.Enabled {
		return Client{}, nil, ErrBundledInactive
	}
	impl, found := s.registry.For(c.Kind)
	if !found {
		return Client{}, nil, fmt.Errorf("no downloader for kind %q", c.Kind)
	}
	return c, impl, nil
}

// Status is a client's recorded health (see connstatus), false when nothing is known.
func (s *Service) Status(id int64) (connstatus.State, bool) {
	return s.status.Get(connstatus.KindDownloadClient, strconv.FormatInt(id, 10))
}

// Create stores a new client.
func (s *Service) Create(ctx context.Context, c Client) (Client, error) {
	defer s.invalidateSnapshot()
	defer s.invalidateTransports()
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
//
// This is the owner's edit. An edit of a bundled client drops the note that the all-in-one
// image's switch turned it off (KeyBundledSwitchedOff): from then on its on/off is the
// owner's, and switching it back on in the container won't undo it. Arrmada's own changes
// go through update, which leaves the note alone.
func (s *Service) Update(ctx context.Context, c Client) (Client, error) {
	saved, err := s.update(ctx, c)
	if err == nil && saved.Bundled {
		err = s.clearSwitchedOff(ctx, saved.Kind)
	}
	return saved, err
}

// clearSwitchedOff forgets that the all-in-one switch turned a kind's bundled client off.
func (s *Service) clearSwitchedOff(ctx context.Context, kind Kind) error {
	key := switchedOffKey(kind)
	if s.flags == nil || s.flags.Get(ctx, key, "") == "" {
		return nil
	}
	return s.flags.Set(ctx, key, "")
}

// update is Update without the owner's-edit bookkeeping, for Arrmada's own changes.
func (s *Service) update(ctx context.Context, c Client) (Client, error) {
	defer s.invalidateSnapshot()
	defer s.invalidateTransports()
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
//
// Deleting the bundled client records that, so startup doesn't add it back. The flag is
// written first: if it can't be, the delete is refused rather than left to undo itself at
// the next restart.
func (s *Service) Delete(ctx context.Context, id int64) error {
	c, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.Bundled && s.flags != nil {
		if err := s.flags.Set(ctx, bundledRemovedKey(c.Kind), "1"); err != nil {
			return fmt.Errorf("couldn't record that the bundled client was removed: %w", err)
		}
		// A restored client starts fresh: the switch's note doesn't outlive its row.
		if err := s.clearSwitchedOff(ctx, c.Kind); err != nil {
			return err
		}
	}
	defer s.invalidateSnapshot()
	defer s.invalidateTransports()
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

// Add hands a torrent to the torrent clients (AddDownload). Kept for the callers that only
// ever add torrents and don't need to know where it went.
func (s *Service) Add(ctx context.Context, req AddRequest) error {
	_, err := s.AddDownload(ctx, req)
	return err
}

// ErrNoUsenetClient: a usenet release with no enabled usenet client to send it to.
var ErrNoUsenetClient = errors.New("no enabled usenet download client configured")

// AddDownload hands a download to the enabled clients of its transport (req.Transport;
// empty is torrent) in priority order (ListEnabled), moving on to the next only when a
// client certainly never received the request (NeverReached: it couldn't be connected to).
// A client that answered — even with a rejection or an HTTP error — ends it there: it may
// have taken the download, and adding it to a second client as well would fetch it twice.
//
// Fail-over never crosses transports: a torrent is never sent to a usenet client nor an
// NZB to a torrent client, whatever is or isn't reachable.
//
// The key is the download's name in that client (key.go): a usenet client's own job key,
// or for a torrent client the info hash read from the request — empty when the link
// doesn't carry one, as before.
func (s *Service) AddDownload(ctx context.Context, req AddRequest) (Added, error) {
	defer s.invalidateSnapshot()
	if req.Transport == "" {
		req.Transport = TransportTorrent
	}
	enabled, err := s.repo.ListEnabled(ctx)
	if err != nil {
		return Added{}, err
	}
	var clients []Client
	for _, c := range enabled {
		if c.Kind.Transport() == req.Transport {
			clients = append(clients, c)
		}
	}
	if len(clients) == 0 {
		if req.Transport == TransportUsenet {
			return Added{}, ErrNoUsenetClient
		}
		return Added{}, fmt.Errorf("no enabled download client configured")
	}
	var unreachable []error
	for _, c := range clients {
		impl, ok := s.registry.For(c.Kind)
		if !ok {
			unreachable = append(unreachable, fmt.Errorf("%s: no downloader for kind %q", c.Name, c.Kind))
			continue
		}
		// The save path is Arrmada's downloads folder; a client that sees it under another
		// path is sent its own name for it (no mappings: unchanged).
		creq := req
		creq.SavePath = c.PathMaps.ToRemote(req.SavePath)
		start := time.Now()
		key, err := s.addTo(ctx, impl, c, creq)
		if err == nil {
			s.record(ctx, c, nil, time.Since(start))
			if len(unreachable) > 0 {
				s.log.Warn("download added to a later client because earlier ones couldn't be reached",
					"client", c.Name, "release", req.Name, "skipped", len(unreachable))
			} else {
				s.log.Info("download added", "client", c.Name, "release", req.Name)
			}
			return Added{ClientID: c.ID, Key: key}, nil
		}
		if !NeverReached(err) || ctx.Err() != nil {
			return Added{}, err
		}
		s.record(ctx, c, err, time.Since(start))
		s.log.Warn("download client unreachable; trying the next one", "client", c.Name, "release", req.Name, "err", err)
		unreachable = append(unreachable, fmt.Errorf("%s: %w", c.Name, err))
	}
	return Added{}, fmt.Errorf("no download client could be reached: %w", errors.Join(unreachable...))
}

// addTo sends one add to one client and returns the download's key there. A key that
// isn't a valid key of the request's transport is dropped with a warning rather than
// failing the add: the client has the download, and refusing now would only grab it again.
func (s *Service) addTo(ctx context.Context, impl Downloader, c Client, req AddRequest) (string, error) {
	var key string
	if ka, ok := impl.(keyedAdder); ok {
		k, err := ka.AddKeyed(ctx, c, req)
		if err != nil {
			return "", err
		}
		key = k
	} else {
		if err := impl.Add(ctx, c, req); err != nil {
			return "", err
		}
		if req.Transport == TransportTorrent {
			key = torrentKey(req)
		}
	}
	if key != "" && KeyTransport(key) != req.Transport {
		s.log.Warn("download client answered with an unusable download id; it will be matched later",
			"client", c.Name, "release", req.Name)
		key = ""
	}
	return key, nil
}

// torrentKey is a torrent request's info hash: from the .torrent file's info dictionary,
// else from a magnet link; "" when neither says.
func torrentKey(req AddRequest) string {
	if len(req.File) > 0 {
		if h, err := InfoHashFromFile(req.File); err == nil {
			return h
		}
		return ""
	}
	if h, err := InfoHashFromMagnet(req.URL); err == nil {
		return h
	}
	return ""
}

// Remove deletes a download (and optionally its data) from the client holding it,
// switched off or not.
func (s *Service) Remove(ctx context.Context, key string, deleteData bool) error {
	defer s.invalidateSnapshot()
	// Checked here as well as at the HTTP edge: every caller that removes a download goes
	// through this, and a bad value here can empty the whole client.
	if !ValidKey(key) {
		return ErrInvalidHash
	}
	return s.onKey(ctx, key, func(impl Downloader, c Client) error { return impl.Remove(ctx, c, key, deleteData) })
}

// portManager is implemented by clients whose incoming port Arrmada manages.
type portManager interface {
	SetListenPort(ctx context.Context, dc Client, port int) error
	ListenPort(ctx context.Context, dc Client) (int, error)
}

// SetBundledPort pins the bundled client's incoming-connection port. ErrBundledInactive
// when it's switched off or removed: a client that may not be running is left alone.
func (s *Service) SetBundledPort(ctx context.Context, port int) error {
	c, impl, err := s.activeBundled(ctx)
	if err != nil {
		return err
	}
	if pm, ok := impl.(portManager); ok {
		return pm.SetListenPort(ctx, c, port)
	}
	return nil // client kind has no managed port
}

// savePathManager is implemented by clients whose default save path Arrmada manages.
type savePathManager interface {
	SetSavePath(ctx context.Context, dc Client, savePath string) error
}

// SetBundledSavePath points the bundled client at the given downloads dir, so the client
// and Arrmada agree on where files land (and stay on the shared volume for hardlinking).
// No-op for client kinds without a managed save path; ErrBundledInactive when it's
// switched off or removed.
func (s *Service) SetBundledSavePath(ctx context.Context, savePath string) error {
	c, impl, err := s.activeBundled(ctx)
	if err != nil {
		return err
	}
	if sp, ok := impl.(savePathManager); ok {
		return sp.SetSavePath(ctx, c, savePath)
	}
	return nil // client kind has no managed save path
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

// PauseAll is the key Pause and Resume take for "every download in every client" (the
// Downloads page's Pause all / Resume all). Nothing else accepts it.
const PauseAll = "all"

// Pause stops a download on the client holding it, or — for PauseAll — every download on
// every client.
func (s *Service) Pause(ctx context.Context, key string) error {
	defer s.invalidateSnapshot()
	if key == PauseAll {
		return s.onEvery(ctx, func(impl Downloader, c Client) error { return impl.Pause(ctx, c, PauseAll) })
	}
	return s.onKey(ctx, key, func(impl Downloader, c Client) error { return impl.Pause(ctx, c, key) })
}

// ErrNoCategories: no download client holding torrents can change one's category.
var ErrNoCategories = errors.New("the download client can't change a torrent's category")

// SetCategory files a download under another category on the client holding it. The
// caller decides which categories are allowed (Arrmada's own); this only moves it.
func (s *Service) SetCategory(ctx context.Context, hash, category string) error {
	defer s.invalidateSnapshot()
	tried := false
	err := s.onKey(ctx, hash, func(impl Downloader, c Client) error {
		cs, ok := impl.(categorySetter)
		if !ok {
			return ErrNoCategories
		}
		tried = true
		return cs.SetCategory(ctx, c, hash, category)
	})
	if err == nil && !tried {
		return ErrNoCategories
	}
	return err
}

// Resume restarts a stopped download on the client holding it, or — for PauseAll — every
// download on every client.
func (s *Service) Resume(ctx context.Context, key string) error {
	defer s.invalidateSnapshot()
	if key == PauseAll {
		return s.onEvery(ctx, func(impl Downloader, c Client) error { return impl.Resume(ctx, c, PauseAll) })
	}
	return s.onKey(ctx, key, func(impl Downloader, c Client) error { return impl.Resume(ctx, c, key) })
}

// ResumeMany restarts several downloads. The torrents go in one request per torrent
// client, hashes joined the way qBittorrent's start/resume endpoint takes them, and every
// torrent client gets the whole list: qBittorrent ignores hashes it doesn't have, so
// stopping at the first client that answers would never reach a second client's torrents.
// That part fails only when no client took the request. Usenet jobs are resumed one by one
// on the client holding each — a joined list means nothing to a usenet client, and a
// torrent hash must never reach one.
func (s *Service) ResumeMany(ctx context.Context, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	defer s.invalidateSnapshot()
	var torrents, usenet []string
	for _, h := range hashes {
		if routeOf(h) == TransportUsenet {
			usenet = append(usenet, h)
		} else {
			torrents = append(torrents, h)
		}
	}
	var firstErr error
	if len(torrents) > 0 {
		clients, err := s.existingClients(ctx)
		if err != nil {
			return err
		}
		joined := strings.Join(torrents, "|")
		var lastErr error
		ok := false
		for _, c := range clients {
			if c.Kind.Transport() != TransportTorrent {
				continue
			}
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
		if !ok {
			firstErr = lastErr
		}
	}
	for _, key := range usenet {
		err := s.onKey(ctx, key, func(impl Downloader, c Client) error { return impl.Resume(ctx, c, key) })
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Action runs a download-scoped command (recheck/reannounce/prio_up/prio_down) on the
// client holding it.
func (s *Service) Action(ctx context.Context, hash, action string) error {
	defer s.invalidateSnapshot()
	return s.onKey(ctx, hash, func(impl Downloader, c Client) error { return impl.TorrentAction(ctx, c, hash, action) })
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

// EnsureBundledQueue re-applies the bundled client's current queue settings, which
// re-derives max_active_torrents from the per-kind limits — fixing torrents stuck
// "Queued" behind qBittorrent's default total-active cap of 5, without the user
// having to re-save anything. ErrBundledInactive when it's switched off or removed.
func (s *Service) EnsureBundledQueue(ctx context.Context) error {
	c, impl, err := s.activeBundled(ctx)
	if err != nil {
		return err
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

// routeOf is the transport a command for key may go to. Anything that isn't a usenet key
// goes to torrent clients, as every command did before usenet existed — the HTTP edge and
// Remove have already refused what isn't one download's key.
func routeOf(key string) Transport {
	if strings.HasPrefix(key, usenetKeyPrefix) {
		return TransportUsenet
	}
	return TransportTorrent
}

// lastHolder is the client the most recent finished queue read saw key in, 0 when it
// didn't. Any age will do: a download never moves between clients, and reading the cached
// copy keeps a burst of commands (seed cleanup removing a dozen torrents) from re-reading
// every client once per command.
func (s *Service) lastHolder(key string) int64 {
	s.snap.mu.Lock()
	cur := s.snap.cur
	s.snap.mu.Unlock()
	if cur == nil {
		return 0
	}
	for _, it := range cur.Items {
		if strings.EqualFold(it.Hash, key) {
			return it.ClientID
		}
	}
	return 0
}

// onKey runs fn against the client holding key — only that one. It used to try every
// client until one answered, and qBittorrent answers "Ok." for a hash it doesn't hold, so
// a command for a second client's torrent stopped at the first client and did nothing.
//
// The holder is the client the last queue read saw the download in. A download it didn't
// see — added since, or in a client that didn't answer — goes to the clients of its own
// transport: the only one when there's one, else after a fresh read, every one of them
// (each ignores a key it doesn't hold). A usenet key never reaches a torrent client, nor a
// torrent hash a usenet client.
func (s *Service) onKey(ctx context.Context, key string, fn func(Downloader, Client) error) error {
	clients, err := s.existingClients(ctx)
	if err != nil {
		return err
	}
	route := routeOf(key)
	var cands []Client
	for _, c := range clients {
		if c.Kind.Transport() != route {
			continue
		}
		if _, ok := s.registry.For(c.Kind); ok {
			cands = append(cands, c)
		}
	}
	holder := s.lastHolder(key)
	if holder == 0 && len(cands) > 1 {
		// Several clients could hold it: read them now rather than guess. A failed read
		// leaves holder 0 and every candidate is asked.
		_, _ = s.Snapshot(ctx)
		holder = s.lastHolder(key)
	}
	if holder != 0 {
		for _, c := range cands {
			if c.ID == holder {
				impl, _ := s.registry.For(c.Kind)
				return fn(impl, c)
			}
		}
	}
	var lastErr error
	ok := false
	for _, c := range cands {
		impl, _ := s.registry.For(c.Kind)
		if err := fn(impl, c); err != nil {
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

// onEvery runs fn against every client (see existingClients), succeeding when at least
// one took it — Pause all and Resume all, which must reach every client, not stop at the
// first that answers.
func (s *Service) onEvery(ctx context.Context, fn func(Downloader, Client) error) error {
	clients, err := s.existingClients(ctx)
	if err != nil {
		return err
	}
	var lastErr error
	ok := false
	for _, c := range clients {
		impl, found := s.registry.For(c.Kind)
		if !found {
			continue
		}
		if err := fn(impl, c); err != nil {
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
