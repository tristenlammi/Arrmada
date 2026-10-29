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
	// KindSABnzbd is a SABnzbd usenet client (sabnzbd.go).
	KindSABnzbd Kind = "sabnzbd"
)

// Client is a configured download client.
type Client struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Kind     Kind   `json:"kind"`
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	Password string `json:"-"` // secret
	// Category is the retired per-client category, still stored but never read: Arrmada
	// chooses the category for every download (categories.go).
	Category string `json:"-"`
	Enabled  bool   `json:"enabled"`
	// Priority orders the clients for new downloads: the lowest number is tried first,
	// and the next only when the first can't be reached at all (Service.Add).
	Priority int `json:"priority"`
	// Bundled marks a packaged client, at most one per kind. Startup re-adds it only while
	// no row of its kind is marked and the owner hasn't removed it (Service.EnsureBundled).
	Bundled bool `json:"bundled"`
	// PathMaps translate the paths this client reports into the ones Arrmada reads, for a
	// client that sees its downloads under a different path (pathmap.go). Empty for most.
	PathMaps PathMaps `json:"path_maps,omitempty"`
}

// DefaultPriority is a new client's place in the order, the same default indexers use.
const DefaultPriority = 25

// AddRequest asks a client to start a download. Provide either URL (the client
// fetches it) or File bytes (Arrmada already fetched an auth-gated .torrent, or an .nzb).
type AddRequest struct {
	Name     string // release title, for logging
	URL      string
	File     []byte
	Filename string
	Category string
	SavePath string // where the client should save (matches Arrmada's downloads dir)
	Paused   bool
	// Transport picks which clients may take it (Service.AddDownload): a torrent never goes
	// to a usenet client, nor an NZB to a torrent client. Empty means torrent — all Add has
	// ever sent.
	Transport Transport
	// ClientFetches (usenet only) hands the usenet client URL to fetch the NZB itself, in
	// place of File. Arrmada fetches NZBs itself so the indexer's key never sits in the
	// client's history; this is for the one case it can't — the indexer blocked Arrmada's
	// download but admits the usenet client. Without it a usenet client refuses a link.
	ClientFetches bool
}

// Added is where a download went and the key it is known by there (key.go). Key may be
// empty for a torrent whose hash couldn't be read from its link, as before.
type Added struct {
	ClientID int64
	Key      string
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

	// RawState is the client's own state string ("stalledDL", "metaDL", "queuedDL", …).
	// State folds these together for its many existing callers, which hid a dead torrent
	// behind "downloading"; Phase() reads this to tell them apart again.
	RawState string `json:"raw_state,omitempty"`
	// Seeds/Peers are the peers this client is connected to right now; SwarmSeeds and
	// SwarmPeers are what the tracker reports for the whole swarm. A torrent with zero of
	// both has nobody to download from.
	Seeds      int `json:"seeds"`
	Peers      int `json:"peers"`
	SwarmSeeds int `json:"swarm_seeds"`
	SwarmPeers int `json:"swarm_peers"`
	// LastActivity is when a piece last moved in either direction, and AddedOn when the
	// torrent was added — both unix seconds, 0 when the client didn't say.
	LastActivity int64 `json:"last_activity,omitempty"`
	AddedOn      int64 `json:"added_on,omitempty"`
	// Availability is distributed copies of the torrent among connected peers; below 1
	// means no connected peer has every piece. qBittorrent reports -1 when unknown.
	Availability float64 `json:"availability"`
	// ClientID is the download client this torrent was read from (set by the queue read).
	ClientID int64 `json:"client_id,omitempty"`
	// Transport is the client's transport, set by the queue read from its kind. Hash is
	// the download key (key.go): an info hash for a torrent, "sab-…" for a usenet job.
	Transport Transport `json:"transport,omitempty"`
	// FailMessage is a usenet client's own reason for a failed job ("out of retention",
	// "CRC error"); empty otherwise.
	FailMessage string `json:"fail_message,omitempty"`
	// Labels are a usenet client's flags on a job (DUPLICATE, ENCRYPTED, …) as it gives them.
	Labels []string `json:"labels,omitempty"`
}

// Phase is a finer reading of the torrent's state than State, for the places that need to
// tell a dead download from a live one: "stalled" (no peers sending), "metadata" (a magnet
// still fetching its file list), "queued" (held by the client's active-download limit),
// "checking", "moving", "allocating", "downloading", "seeding", "paused" or "error" — and,
// for a usenet job, "unpacking" (being verified, repaired or extracted after the download:
// the files aren't ready yet, but nothing is stuck).
//
// State keeps its old meaning for every existing caller; this only adds information. A
// raw state this doesn't know (a future client version, or a client that doesn't report
// one) falls back to State, so nothing reads as a phase it isn't.
func (i Item) Phase() string {
	if i.State == PhaseUnpacking {
		return PhaseUnpacking
	}
	if i.Transport == TransportUsenet {
		// SABnzbd's post-processing stages. Read only for usenet: qBittorrent's states are
		// lowercase ("moving") and keep their own meaning.
		switch i.RawState {
		case "QuickCheck", "Verifying", "Repairing", "Extracting", "Moving", "Running":
			return PhaseUnpacking
		}
		// A job waiting its turn, for its articles to propagate, or for its NZB to be
		// fetched is held, not stuck. A paused queue reads as paused whatever each job says.
		switch {
		case i.State == "paused" || i.RawState == "Paused":
			return "paused"
		case i.RawState == "Queued" || i.RawState == "Propagating" || i.RawState == "Fetching" || i.RawState == "Grabbing":
			return "queued"
		case i.RawState == "Downloading":
			return "downloading"
		case i.RawState == "Failed":
			return "error"
		}
	}
	switch i.RawState {
	case "metaDL", "forcedMetaDL":
		return "metadata"
	case "queuedDL", "queuedUP":
		return "queued"
	case "stalledDL":
		return "stalled"
	case "checkingDL", "checkingUP", "checkingResumeData":
		return "checking"
	case "moving":
		return "moving"
	case "allocating":
		return "allocating"
	case "downloading", "forcedDL":
		return "downloading"
	case "uploading", "stalledUP", "forcedUP":
		return "seeding"
	case "pausedDL", "pausedUP", "stoppedDL", "stoppedUP":
		return "paused"
	case "error", "missingFiles":
		return "error"
	}
	return i.State
}

// PhaseUnpacking is a usenet job being verified, repaired or extracted (see Phase).
const PhaseUnpacking = "unpacking"

// Complete reports whether everything this torrent was told to fetch is on disk.
//
// Progress alone used to decide this everywhere, and it's a single float: rounding, a
// mid-recheck sample, or a client that reports it optimistically all read as "finished",
// and a download that wasn't there yet would be imported and later deleted for seeding.
// RemainingBytes is an independent second opinion from the same payload — it costs nothing
// and both must agree.
//
// A usenet job is complete only when its client says so (State "completed": SABnzbd's
// history reads Completed). Its bytes are all down long before that — while it is verified,
// repaired and unpacked — and importing then would take half-extracted files.
func (i Item) Complete() bool {
	if i.Transport == TransportUsenet && i.State != StateCompleted {
		return false
	}
	return i.Progress >= 1.0 && i.RemainingBytes <= 0
}

// ReadyToImport reports whether a finished download's files are where they'll stay, so an
// import can read them: Complete, and not being moved by its client.
//
// A qBittorrent with "Default Download Path (incomplete torrents)" on downloads into that
// folder and moves each torrent to its save folder once the last byte lands. During the
// move the torrent is already 100% (Complete is true) but its state is "moving" and its
// content path still names the incomplete folder — one Arrmada usually has no reason to
// mount. An import that read it then found no folder at all, and the series import took
// "no folder" for "no video": it blocklisted a good release, failed its grab, and the
// next search grabbed another copy of the same season. Across two mounts the move is a
// full copy, so for a season pack this window is minutes long, not a moment.
//
// Complete keeps its meaning — every byte is down — for the stall check, the in-flight
// guards and the queue pages; only the importers wait for the move.
func (i Item) ReadyToImport() bool {
	return i.Complete() && !i.Moving()
}

// Moving reports whether the client is moving the download's files to another folder
// (qBittorrent's "moving" state: its temp-path move on completion, or a save-path change).
// A usenet job's own post-processing move reads as unpacking (Phase), and such a job isn't
// Complete until its client has finished with it, so this is only ever true for a torrent.
func (i Item) Moving() bool {
	return i.Transport != TransportUsenet && i.RawState == "moving"
}

// StateCompleted is a usenet job its client has finished with: downloaded, verified and
// unpacked into its final folder (Item.ContentPath).
const StateCompleted = "completed"

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

// Downloader is one download-client implementation. "hash" is the download key (key.go)
// the client's List reports as Item.Hash. Pause and Resume also take PauseAll, meaning
// every download in that client; nothing else may receive it.
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

// keyedAdder is a client that says which key the download it took is known by (a usenet
// client's job id, as UsenetKey). A client without it is a torrent client, whose key is
// the info hash read from the request (torrentKey).
type keyedAdder interface {
	AddKeyed(ctx context.Context, dc Client, req AddRequest) (string, error)
}

// categorySetter is implemented by clients that can re-file a torrent under another
// category (Needs you's "Move to TV" for a misfiled download).
type categorySetter interface {
	SetCategory(ctx context.Context, dc Client, hash, category string) error
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
		KindSABnzbd:     NewSABnzbd(),
	}}
}

// Register sets the implementation for a kind (the usenet client, and fakes in tests).
func (r *Registry) Register(kind Kind, d Downloader) { r.impls[kind] = d }

// For returns the downloader for a kind.
func (r *Registry) For(kind Kind) (Downloader, bool) {
	d, ok := r.impls[kind]
	return d, ok
}
