// Package download manages download clients — the programs that actually fetch
// releases (torrents/nzbs). Arrmada hands a grabbed release to a client and then
// tracks its progress. Clients sit behind one Downloader interface so qBittorrent,
// SABnzbd, etc. are interchangeable.
package download

import "context"

// Kind identifies a download-client implementation.
type Kind string

const (
	KindQbittorrent Kind = "qbittorrent"
)

// Client is a configured download client.
type Client struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Kind     Kind   `json:"kind"`
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	Password string `json:"-"` // secret
	Category string `json:"category,omitempty"`
	Enabled  bool   `json:"enabled"`
}

// AddRequest asks a client to start a download. Provide either URL (the client
// fetches it) or File bytes (Arrmada already fetched an auth-gated .torrent).
type AddRequest struct {
	Name     string // release title, for logging
	URL      string
	File     []byte
	Filename string
	Category string
	SavePath string // where the client should save (matches Arrmada's downloads dir)
	Paused   bool
}

// Item is a download's live status, normalized across clients.
type Item struct {
	Hash            string  `json:"hash"`
	Name            string  `json:"name"`
	State           string  `json:"state"`    // downloading|seeding|paused|completed|error|…
	Progress        float64 `json:"progress"` // 0..1
	SizeBytes       int64   `json:"size_bytes"`
	DownloadedBytes int64   `json:"downloaded_bytes"`
	DownSpeed       int64   `json:"down_speed"` // bytes/s
	UpSpeed         int64   `json:"up_speed"`   // bytes/s
	ETASeconds      int64   `json:"eta_seconds"`
	Ratio           float64 `json:"ratio"`
	SeedingTime     int64   `json:"seeding_time,omitempty"` // seconds seeded after completion
	Category        string  `json:"category,omitempty"`
	ContentPath     string  `json:"content_path,omitempty"` // path on disk (for import)
}

// ClientSettings is the tunable subset of a torrent client's global config.
// Speed limits are bytes/second (0 = unlimited).
type ClientSettings struct {
	DlLimit            int64 `json:"dl_limit"`
	UpLimit            int64 `json:"up_limit"`
	AltDlLimit         int64 `json:"alt_dl_limit"`
	AltUpLimit         int64 `json:"alt_up_limit"`
	ScheduleEnabled    bool  `json:"schedule_enabled"`
	FromHour           int   `json:"from_hour"`
	FromMin            int   `json:"from_min"`
	ToHour             int   `json:"to_hour"`
	ToMin              int   `json:"to_min"`
	Days               int   `json:"days"` // qBit: 0=every day, 1=weekdays, 2=weekends
	MaxActiveDownloads int   `json:"max_active_downloads"`
	MaxActiveUploads   int   `json:"max_active_uploads"`
}

// Downloader is one download-client implementation.
type Downloader interface {
	Add(ctx context.Context, dc Client, req AddRequest) error
	List(ctx context.Context, dc Client) ([]Item, error)
	Remove(ctx context.Context, dc Client, hash string, deleteData bool) error
	Pause(ctx context.Context, dc Client, hash string) error
	Resume(ctx context.Context, dc Client, hash string) error
	// TorrentAction runs a hash-scoped command: "recheck", "reannounce",
	// "prio_up", "prio_down".
	TorrentAction(ctx context.Context, dc Client, hash, action string) error
	Test(ctx context.Context, dc Client) error
}

// settingsManager is implemented by clients that expose tunable global settings.
type settingsManager interface {
	GetSettings(ctx context.Context, dc Client) (ClientSettings, error)
	SetSettings(ctx context.Context, dc Client, s ClientSettings) error
}

// Registry maps client kinds to their implementation.
type Registry struct {
	impls map[Kind]Downloader
}

// NewRegistry wires the built-in download clients.
func NewRegistry() *Registry {
	return &Registry{impls: map[Kind]Downloader{
		KindQbittorrent: NewQBittorrent(),
	}}
}

// For returns the downloader for a kind.
func (r *Registry) For(kind Kind) (Downloader, bool) {
	d, ok := r.impls[kind]
	return d, ok
}
