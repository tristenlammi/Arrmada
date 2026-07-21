package download

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// QBittorrent drives the qBittorrent WebUI API (v2). It logs in (cookie session,
// cached per client) and manages torrents.
type QBittorrent struct {
	mu       sync.Mutex
	sessions map[int64]*http.Client
	loginMu  map[int64]*sync.Mutex // per-client single-flight for login
}

// NewQBittorrent creates the downloader.
func NewQBittorrent() *QBittorrent {
	return &QBittorrent{
		sessions: map[int64]*http.Client{},
		loginMu:  map[int64]*sync.Mutex{},
	}
}

func base(dc Client) string { return strings.TrimRight(dc.URL, "/") }

// login authenticates and returns a client carrying the SID cookie. qBittorrent
// checks the Referer header for CSRF, so we always send it.
func (q *QBittorrent) login(ctx context.Context, dc Client) (*http.Client, error) {
	b := base(dc)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}

	form := url.Values{"username": {dc.Username}, "password": {dc.Password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", b)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qbittorrent: connect failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

	switch {
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("qbittorrent: refused (too many failed logins, or WebUI host/CSRF restriction)")
	case strings.TrimSpace(string(body)) == "Fails.":
		return nil, fmt.Errorf("qbittorrent: invalid username or password")
	// 200 "Ok." on normal login; 204 No Content when auth is bypassed (IP whitelist).
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return client, nil
	default:
		return nil, fmt.Errorf("qbittorrent: login HTTP %d", resp.StatusCode)
	}
}

func (q *QBittorrent) session(ctx context.Context, dc Client) (*http.Client, error) {
	q.mu.Lock()
	if c := q.sessions[dc.ID]; c != nil {
		q.mu.Unlock()
		return c, nil
	}
	// Single-flight logins per client: concurrent callers wait for one login
	// instead of stampeding qBittorrent (which bans after repeated auth attempts).
	lm := q.loginMu[dc.ID]
	if lm == nil {
		lm = &sync.Mutex{}
		q.loginMu[dc.ID] = lm
	}
	q.mu.Unlock()

	lm.Lock()
	defer lm.Unlock()
	// A concurrent caller may have logged in while we waited on lm.
	q.mu.Lock()
	if c := q.sessions[dc.ID]; c != nil {
		q.mu.Unlock()
		return c, nil
	}
	q.mu.Unlock()

	c, err := q.login(ctx, dc)
	if err != nil {
		return nil, err
	}
	q.mu.Lock()
	q.sessions[dc.ID] = c
	q.mu.Unlock()
	return c, nil
}

func (q *QBittorrent) drop(id int64) {
	q.mu.Lock()
	delete(q.sessions, id)
	q.mu.Unlock()
}

// doAuthed executes an authenticated request against qBittorrent. If the cached
// session has expired (HTTP 403), it drops the session, re-logs-in, and retries
// exactly once — a second 403 after a fresh login is a real error and the 403
// response is returned to the caller. newReq must build a fresh *http.Request
// on every call (request bodies are consumed by the transport, so a retry needs
// a rebuilt body, not a reused reader).
func (q *QBittorrent) doAuthed(ctx context.Context, dc Client, newReq func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		client, err := q.session(ctx, dc)
		if err != nil {
			return nil, err
		}
		req, err := newReq()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("qbittorrent: %w", err)
		}
		if resp.StatusCode == http.StatusForbidden {
			q.drop(dc.ID) // session expired (or auth revoked)
			if attempt == 0 {
				resp.Body.Close()
				continue // session() re-logs-in; retry the request once
			}
		}
		return resp, nil
	}
}

// getAuthed issues an authenticated GET (with the expired-session retry of
// doAuthed). The caller owns the response body.
func (q *QBittorrent) getAuthed(ctx context.Context, dc Client, path string) (*http.Response, error) {
	return q.doAuthed(ctx, dc, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base(dc)+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Referer", base(dc))
		return req, nil
	})
}

// Test logs in and reads the app version.
func (q *QBittorrent) Test(ctx context.Context, dc Client) error {
	client, err := q.login(ctx, dc)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base(dc)+"/api/v2/app/version", nil)
	req.Header.Set("Referer", base(dc))
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("qbittorrent: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qbittorrent: version check HTTP %d", resp.StatusCode)
	}
	return nil
}

// Add starts a download from a URL or an already-fetched .torrent file.
func (q *QBittorrent) Add(ctx context.Context, dc Client, req AddRequest) error {
	if len(req.File) == 0 && req.URL == "" {
		return fmt.Errorf("qbittorrent: add requires a URL or file")
	}

	// Built per attempt: a multipart body is consumed on send, so the re-login
	// retry needs a freshly rebuilt request.
	newReq := func() (*http.Request, error) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		if len(req.File) > 0 {
			name := req.Filename
			if name == "" {
				name = "arrmada.torrent"
			}
			part, err := w.CreateFormFile("torrents", name)
			if err != nil {
				return nil, err
			}
			if _, err := part.Write(req.File); err != nil {
				return nil, err
			}
		} else {
			_ = w.WriteField("urls", req.URL)
		}

		cat := req.Category
		if cat == "" {
			cat = dc.Category
		}
		if cat != "" {
			_ = w.WriteField("category", cat)
		}
		// Pin the save path to Arrmada's downloads dir so the client and Arrmada agree
		// on where the file lands — otherwise a stale client default breaks import
		// (and hardlinking, if it points off the shared volume).
		if req.SavePath != "" {
			_ = w.WriteField("savepath", req.SavePath)
		}
		_ = w.WriteField("paused", strconv.FormatBool(req.Paused))
		_ = w.Close()

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base(dc)+"/api/v2/torrents/add", &buf)
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", w.FormDataContentType())
		httpReq.Header.Set("Referer", base(dc))
		return httpReq, nil
	}

	resp, err := q.doAuthed(ctx, dc, newReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("qbittorrent: add unauthorized after re-login (HTTP 403)")
	}
	// qBittorrent returns 200 "Ok." on a normal add, but also 202 Accepted when it
	// queues a URL/magnet to fetch asynchronously — both are success. Only a
	// non-2xx status or an explicit "Fails." body is a real rejection.
	if resp.StatusCode/100 != 2 || strings.TrimSpace(string(body)) == "Fails." {
		return fmt.Errorf("qbittorrent: add rejected (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// Remove deletes a torrent (optionally with its data) from qBittorrent.
func (q *QBittorrent) Remove(ctx context.Context, dc Client, hash string, deleteData bool) error {
	form := url.Values{}
	form.Set("hashes", hash)
	form.Set("deleteFiles", strconv.FormatBool(deleteData))
	return q.postForm(ctx, dc, "/api/v2/torrents/delete", form)
}

// Pause stops a torrent. qBittorrent 5.x renamed pause→stop; try the new name
// first and fall back to the old one for older servers.
func (q *QBittorrent) Pause(ctx context.Context, dc Client, hash string) error {
	return q.action(ctx, dc, hash, "stop", "pause")
}

// Resume starts a stopped torrent (start in 5.x, resume pre-5.x).
func (q *QBittorrent) Resume(ctx context.Context, dc Client, hash string) error {
	return q.action(ctx, dc, hash, "start", "resume")
}

// action posts a torrent command, retrying with a legacy endpoint name on failure.
func (q *QBittorrent) action(ctx context.Context, dc Client, hash, primary, legacy string) error {
	form := url.Values{"hashes": {hash}}
	if err := q.postForm(ctx, dc, "/api/v2/torrents/"+primary, form); err != nil {
		if q.postForm(ctx, dc, "/api/v2/torrents/"+legacy, form) == nil {
			return nil
		}
		return err
	}
	return nil
}

// postForm posts a urlencoded form to qBittorrent, transparently re-logging-in
// once if the session has expired.
func (q *QBittorrent) postForm(ctx context.Context, dc Client, path string, form url.Values) error {
	body := form.Encode()
	newReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base(dc)+path, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Referer", base(dc))
		return req, nil
	}
	resp, err := q.doAuthed(ctx, dc, newReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("qbittorrent: %s unauthorized after re-login (HTTP 403)", path)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("qbittorrent: %s HTTP %d", path, resp.StatusCode)
	}
	return nil
}

// TorrentAction runs a hash-scoped command (recheck/reannounce/priority).
func (q *QBittorrent) TorrentAction(ctx context.Context, dc Client, hash, action string) error {
	ep := map[string]string{
		"recheck":    "recheck",
		"reannounce": "reannounce",
		"prio_up":    "increasePrio",
		"prio_down":  "decreasePrio",
	}[action]
	if ep == "" {
		return fmt.Errorf("qbittorrent: unknown action %q", action)
	}
	return q.postForm(ctx, dc, "/api/v2/torrents/"+ep, url.Values{"hashes": {hash}})
}

// GetSettings reads the tunable global preferences.
func (q *QBittorrent) GetSettings(ctx context.Context, dc Client) (ClientSettings, error) {
	resp, err := q.getAuthed(ctx, dc, "/api/v2/app/preferences")
	if err != nil {
		return ClientSettings{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ClientSettings{}, fmt.Errorf("qbittorrent: preferences HTTP %d", resp.StatusCode)
	}
	var p struct {
		DlLimit            int64 `json:"dl_limit"`
		UpLimit            int64 `json:"up_limit"`
		AltDlLimit         int64 `json:"alt_dl_limit"`
		AltUpLimit         int64 `json:"alt_up_limit"`
		SchedulerEnabled   bool  `json:"scheduler_enabled"`
		FromHour           int   `json:"schedule_from_hour"`
		FromMin            int   `json:"schedule_from_min"`
		ToHour             int   `json:"schedule_to_hour"`
		ToMin              int   `json:"schedule_to_min"`
		Days               int   `json:"scheduler_days"`
		MaxActiveDownloads int   `json:"max_active_downloads"`
		MaxActiveUploads   int   `json:"max_active_uploads"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return ClientSettings{}, err
	}
	return ClientSettings{
		DlLimit: p.DlLimit, UpLimit: p.UpLimit, AltDlLimit: p.AltDlLimit, AltUpLimit: p.AltUpLimit,
		ScheduleEnabled: p.SchedulerEnabled, FromHour: p.FromHour, FromMin: p.FromMin,
		ToHour: p.ToHour, ToMin: p.ToMin, Days: p.Days,
		MaxActiveDownloads: p.MaxActiveDownloads, MaxActiveUploads: p.MaxActiveUploads,
	}, nil
}

// SetSettings writes the tunable global preferences. Max-active values require
// queueing, so it's enabled alongside them.
// maxActiveTorrents sizes qBittorrent's total-active cap so the per-kind limits
// (downloads + seeding) are the real constraint. A negative (unlimited) on either
// makes the total unlimited too.
func maxActiveTorrents(downloads, uploads int) int {
	if downloads < 0 || uploads < 0 {
		return -1 // unlimited
	}
	return downloads + uploads
}

func (q *QBittorrent) SetSettings(ctx context.Context, dc Client, s ClientSettings) error {
	prefs, _ := json.Marshal(map[string]any{
		"dl_limit": s.DlLimit, "up_limit": s.UpLimit,
		"alt_dl_limit": s.AltDlLimit, "alt_up_limit": s.AltUpLimit,
		"scheduler_enabled":  s.ScheduleEnabled,
		"schedule_from_hour": s.FromHour, "schedule_from_min": s.FromMin,
		"schedule_to_hour": s.ToHour, "schedule_to_min": s.ToMin,
		"scheduler_days":       s.Days,
		"queueing_enabled":     true,
		"max_active_downloads": s.MaxActiveDownloads,
		"max_active_uploads":   s.MaxActiveUploads,
		// The TOTAL active cap (defaults to 5 in qBittorrent) otherwise queues
		// torrents regardless of the per-kind limits — the cause of "Queued"
		// torrents despite a high seeding limit. Sized to cover both.
		"max_active_torrents": maxActiveTorrents(s.MaxActiveDownloads, s.MaxActiveUploads),
	})
	return q.postForm(ctx, dc, "/api/v2/app/setPreferences", url.Values{"json": {string(prefs)}})
}

// SetListenPort fixes qBittorrent's incoming-connection port (and disables random
// port + UPnP, which is useless inside Docker's bridge network) so it matches the
// Docker-published/port-forwarded port.
func (q *QBittorrent) SetListenPort(ctx context.Context, dc Client, port int) error {
	prefs, _ := json.Marshal(map[string]any{"listen_port": port, "random_port": false, "upnp": false})
	return q.postForm(ctx, dc, "/api/v2/app/setPreferences", url.Values{"json": {string(prefs)}})
}

// SetSavePath points qBittorrent's default save path and incomplete (temp) path
// at Arrmada's downloads dir. The seed config only applies on first run, so this
// fixes an existing client whose path still points at the old managed volume.
func (q *QBittorrent) SetSavePath(ctx context.Context, dc Client, savePath string) error {
	// Download straight to the final folder — NO separate "incomplete" path. The
	// move-on-completion that a temp path triggers breaks hardlinks and can leave
	// 0-byte files on Unraid's /mnt/user FUSE, which kills seeding. Downloading in
	// place keeps every file stable so imports hardlink the real data.
	prefs, _ := json.Marshal(map[string]any{
		"save_path":         savePath,
		"temp_path_enabled": false,
	})
	return q.postForm(ctx, dc, "/api/v2/app/setPreferences", url.Values{"json": {string(prefs)}})
}

// ListenPort reports qBittorrent's current incoming-connection port.
func (q *QBittorrent) ListenPort(ctx context.Context, dc Client) (int, error) {
	resp, err := q.getAuthed(ctx, dc, "/api/v2/app/preferences")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("qbittorrent: preferences HTTP %d", resp.StatusCode)
	}
	var p struct {
		ListenPort int `json:"listen_port"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return 0, err
	}
	return p.ListenPort, nil
}

// List returns the current torrents. It intentionally does NOT filter by the
// client's category: Arrmada manages several internal categories on the bundled
// client (movies use "arrmada", series use "arrmada-tv"), and callers that need a
// specific category filter in Go (see CompletedInCategory). Filtering server-side
// to a single category here would hide series torrents from imports and the feed.
func (q *QBittorrent) List(ctx context.Context, dc Client) ([]Item, error) {
	resp, err := q.getAuthed(ctx, dc, "/api/v2/torrents/info")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qbittorrent: list HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	return parseTorrentsInfo(body)
}

type qbitTorrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	Size        int64   `json:"size"`
	Progress    float64 `json:"progress"`
	DLSpeed     int64   `json:"dlspeed"`
	UPSpeed     int64   `json:"upspeed"`
	ETA         int64   `json:"eta"`
	State       string  `json:"state"`
	Ratio       float64 `json:"ratio"`
	Category    string  `json:"category"`
	Completed   int64   `json:"completed"`
	AmountLeft  int64   `json:"amount_left"`
	ContentPath string  `json:"content_path"`
	SeedingTime int64   `json:"seeding_time"` // seconds spent seeding after completion
}

func parseTorrentsInfo(body []byte) ([]Item, error) {
	var raw []qbitTorrent
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("qbittorrent: parse torrents: %w", err)
	}
	items := make([]Item, 0, len(raw))
	for _, t := range raw {
		items = append(items, Item{
			Hash:            t.Hash,
			Name:            t.Name,
			State:           normalizeState(t.State),
			Progress:        t.Progress,
			SizeBytes:       t.Size,
			DownloadedBytes: t.Completed,
			DownSpeed:       t.DLSpeed,
			UpSpeed:         t.UPSpeed,
			ETASeconds:      t.ETA,
			Ratio:           t.Ratio,
			Category:        t.Category,
			ContentPath:     t.ContentPath,
			SeedingTime:     t.SeedingTime,
		})
	}
	return items, nil
}

// normalizeState collapses qBittorrent's many states into a small set.
func normalizeState(s string) string {
	switch s {
	case "downloading", "metaDL", "stalledDL", "forcedDL", "queuedDL", "allocating", "checkingDL":
		return "downloading"
	case "uploading", "stalledUP", "forcedUP", "queuedUP", "checkingUP":
		return "seeding"
	case "pausedDL", "pausedUP", "stoppedDL", "stoppedUP":
		return "paused"
	case "error", "missingFiles":
		return "error"
	case "checkingResumeData", "moving":
		return "checking"
	default:
		return s
	}
}
