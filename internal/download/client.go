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
	// UploadedBytes / TransferredBytes are the client's own transfer counters, kept so a
	// seed goal can be judged from real numbers instead of the client's Ratio field —
	// qBittorrent reports an unbounded ratio as a sentinel, which clears any target.
	UploadedBytes    int64 `json:"uploaded_bytes"`
	TransferredBytes int64 `json:"transferred_bytes"` // actually pulled from peers (may be 0 for pre-existing data)
	// RemainingBytes is what the client still owes on the files it was told to fetch, and
	// TotalSizeBytes covers every file in the torrent including ones excluded from the
	// download. TotalSizeBytes > SizeBytes therefore means files were deselected.
	RemainingBytes int64   `json:"remaining_bytes"`
	TotalSizeBytes int64   `json:"total_size_bytes"`
	DownSpeed      int64   `json:"down_speed"` // bytes/s
	UpSpeed        int64   `json:"up_speed"`   // bytes/s
	ETASeconds     int64   `json:"eta_seconds"`
	Ratio          float64 `json:"ratio"`
	SeedingTime    int64   `json:"seeding_time,omitempty"` // seconds seeded after completion
	Category       string  `json:"category,omitempty"`
	ContentPath    string  `json:"content_path,omitempty"` // path on disk (for import)
}

// Complete reports whether everything this torrent was told to fetch is on disk.
//
// Progress alone used to decide this everywhere, and it's a single float: rounding, a
// mid-recheck sample, or a client that reports it optimistically all read as "finished",
// and a download that wasn't there yet would be imported and later deleted for seeding.
// RemainingBytes is an independent second opinion from the same payload — it costs nothing
// and both must agree.
func (i Item) Complete() bool {
	return i.Progress >= 1.0 && i.RemainingBytes <= 0
}

// PartiallySelected reports whether files were excluded from the download, so a caller can
// say why a "complete" torrent didn't yield everything the release promised.
func (i Item) PartiallySelected() bool {
	return i.TotalSizeBytes > 0 && i.SizeBytes > 0 && i.TotalSizeBytes > i.SizeBytes
}

// SeedRatio is the torrent's real upload ratio, or -1 when it can't be worked out.
//
// The client's own Ratio field is NOT trustworthy for this. qBittorrent reports an
// unbounded ratio as a sentinel (MAX_RATIO, 9999) whenever its download counter reads zero
// — pre-existing data, a cross-seed, or counters lost across a restart. Compared straight
// against a target of 2 that sentinel clears instantly, and a season pack that had uploaded
// 4 MB of 3.65 GB was deleted a day into a 28-day seed goal, earning a hit-and-run on the
// tracker it came from.
//
// So the ratio is computed from the byte counters, and when there's no honest denominator
// this returns -1 so a caller can fall back to a time-based rule. Erring toward seeding too
// long costs disk; erring toward too short costs a ban.
func (i Item) SeedRatio() float64 {
	// What was actually pulled from peers is the right denominator. Data already on disk
	// falls back to the completed size — still real, and it can only make the ratio look
	// smaller, which keeps the torrent seeding.
	denom := i.TransferredBytes
	if denom <= 0 {
		denom = i.DownloadedBytes
	}
	if denom <= 0 || i.UploadedBytes < 0 {
		return -1
	}
	return float64(i.UploadedBytes) / float64(denom)
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
