package insights

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tristenlammi/arrmada/internal/geoip"
	"github.com/tristenlammi/arrmada/internal/mediaserver"
	"github.com/tristenlammi/arrmada/internal/plex"
)

// Stream is one live playback session, enriched for the Activity view + deep-dive.
type Stream struct {
	Server      string         `json:"server"` // plex | jellyfin
	SessionKey  string         `json:"session_key"`
	User        string         `json:"user"`
	UserID      string         `json:"user_id"` // the Plex account id the People page is under
	Title       string         `json:"title"`
	Subtitle    string         `json:"subtitle"` // "Show · S1 · E2" or the year
	Type        string         `json:"type"`
	Thumb       string         `json:"thumb"`           // proxied poster URL (the show's, for an episode)
	Still       string         `json:"still,omitempty"` // an episode's own landscape still
	Art         string         `json:"art,omitempty"`   // background art
	ProgressPct int            `json:"progress_pct"`
	OffsetMS    int64          `json:"offset_ms"`
	DurationMS  int64          `json:"duration_ms"`
	State       string         `json:"state"` // playing | paused | buffering
	Player      string         `json:"player"`
	Platform    string         `json:"platform"`
	Product     string         `json:"product"`
	Decision    string         `json:"decision"` // direct_play | direct_stream | transcode
	Bandwidth   int64          `json:"bandwidth_kbps"`
	Location    string         `json:"location"` // lan | wan
	IP          string         `json:"ip"`
	Geo         geoip.Location `json:"geo"`

	// Deep-dive.
	Video       Detail `json:"video"`
	Audio       Detail `json:"audio"`
	Container   Detail `json:"container"`
	HWTranscode bool   `json:"hw_transcode"` // the GPU is doing some of the work (decode or encode)
	HWDecode    bool   `json:"hw_decode"`
	HWEncode    bool   `json:"hw_encode"`
	HWRequested bool   `json:"hw_requested"` // asked for; without HWEncode it means Plex fell back to CPU
	// HWTitle names the hardware: Plex's ("Intel (QuickSync)"), or for Jellyfin the
	// configured acceleration ("vaapi") with HWTranscode set and the three above false —
	// Jellyfin doesn't say which part the GPU did, or whether it took the stream at all.
	HWTitle   string   `json:"hw_title,omitempty"`
	Throttled bool     `json:"throttled"`
	Reasons   []string `json:"reasons"` // why it's transcoding (empty for direct play)
}

// Detail is a source→stream comparison line for the deep-dive.
type Detail struct {
	Src    string `json:"src"`
	Stream string `json:"stream,omitempty"`
}

// Activity holds the current streams plus aggregate bandwidth.
type Activity struct {
	Streams   []Stream  `json:"streams"`
	Bandwidth Bandwidth `json:"bandwidth"`
	GeoActive bool      `json:"geo_active"` // full city resolution available
	// Errors says which connected server didn't answer (by server), when another did: its
	// streams are missing from the list.
	Errors map[string]string `json:"errors,omitempty"`
}

// Bandwidth is the summed stream bandwidth, split by network location.
type Bandwidth struct {
	TotalKbps int64 `json:"total_kbps"`
	LANKbps   int64 `json:"lan_kbps"`
	WANKbps   int64 `json:"wan_kbps"`
}

// Activity returns the current live streams from the connected servers the filter allows
// (server: "" for both, "plex", "jellyfin"), Plex's first, enriched with geolocation, the
// stream decision, source→stream detail, and human "why it's transcoding" reasons. It asks
// the servers directly, so it works whether or not monitoring is on. One server failing
// leaves its streams out (Errors says so); it's an error only when none answered.
func (s *Service) Activity(ctx context.Context, server string) (Activity, error) {
	srcs := s.liveSources(ctx, server)
	if len(srcs) == 0 {
		return Activity{}, plex.ErrNotConfigured
	}
	out := Activity{Streams: []Stream{}, GeoActive: s.geo.Enabled()}
	var firstErr error
	for _, src := range srcs {
		sessions, err := src.Sessions(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if out.Errors == nil {
				out.Errors = map[string]string{}
			}
			out.Errors[string(src.Kind())] = err.Error()
			continue
		}
		for _, sess := range sessions {
			st := s.enrich(sess)
			st.UserID = sess.UserID
			if src.Kind() == mediaserver.Plex {
				st.UserID = s.knownUserID(ctx, sess.UserID)
			}
			out.Streams = append(out.Streams, st)
			out.Bandwidth.TotalKbps += st.Bandwidth
			if strings.EqualFold(sess.Location, "lan") || st.Geo.Local {
				out.Bandwidth.LANKbps += st.Bandwidth
			} else {
				out.Bandwidth.WANKbps += st.Bandwidth
			}
		}
	}
	if len(out.Errors) == len(srcs) {
		return Activity{}, firstErr
	}
	return out, nil
}

func (s *Service) enrich(p plex.Session) Stream {
	ip := p.PublicIP
	if ip == "" {
		ip = p.Address
	}
	st := Stream{
		Server: string(serverOf(p.Server)), SessionKey: p.SessionKey, User: p.UserName, Title: p.Title, Type: p.Type,
		Thumb: proxyImage(posterPath(p.Type, p.Thumb, p.ShowThumb)), Art: proxyImage(p.Art), OffsetMS: p.OffsetMS, DurationMS: p.DurationMS,
		State: p.State, Player: p.PlayerName, Platform: p.Platform, Product: p.Product,
		Decision: p.Decision(), Bandwidth: p.Bandwidth, Location: p.Location,
		IP: ip, Geo: s.geo.Lookup(ip),
		HWTranscode: p.TranscodeHW, HWDecode: p.HWDecode, HWEncode: p.HWEncode, HWRequested: p.HWRequested,
		HWTitle: p.HWTitle, Throttled: p.Throttled,
	}
	if p.DurationMS > 0 {
		st.ProgressPct = int(p.OffsetMS * 100 / p.DurationMS)
	}
	if p.Type == "episode" && p.ShowThumb != "" {
		st.Still = proxyImage(p.Thumb)
	}
	st.Subtitle = subtitleFor(p)
	st.Video = Detail{Src: mediaLabel(p.SrcVideoCodec, p.SrcResolution)}
	st.Audio = Detail{Src: strings.ToUpper(p.SrcAudioCodec)}
	st.Container = Detail{Src: strings.ToUpper(p.SrcContainer)}
	if p.Transcoding {
		if p.VideoDecision == "transcode" {
			st.Video.Stream = mediaLabel(p.StreamVideoCodec, streamRes(p.StreamHeight))
		}
		if p.AudioDecision == "transcode" {
			st.Audio.Stream = strings.ToUpper(p.StreamAudioCodec)
		}
		if p.TranscodeCont != "" {
			st.Container.Stream = strings.ToUpper(p.TranscodeCont)
		}
	}
	st.Reasons = transcodeReasons(p)
	return st
}

// subtitleFor builds the secondary line: "Show · S1 · E3" for episodes, else the year.
func subtitleFor(p plex.Session) string {
	if p.Type == "episode" && p.ShowTitle != "" {
		return fmt.Sprintf("%s · S%d · E%d", p.ShowTitle, p.SeasonNum, p.Index)
	}
	if p.Year > 0 {
		return fmt.Sprintf("%d", p.Year)
	}
	return p.SeasonName
}

// transcodeReasons explains, in plain English, why a stream is being transcoded.
func transcodeReasons(p plex.Session) []string {
	if !p.Transcoding {
		return nil
	}
	var r []string
	if p.VideoDecision == "transcode" {
		r = append(r, fmt.Sprintf("Converting video (%s → %s)", mediaLabel(p.SrcVideoCodec, p.SrcResolution), mediaLabel(p.StreamVideoCodec, streamRes(p.StreamHeight))))
	}
	if p.AudioDecision == "transcode" {
		r = append(r, fmt.Sprintf("Converting audio (%s → %s)", strings.ToUpper(p.SrcAudioCodec), strings.ToUpper(p.StreamAudioCodec)))
	}
	switch p.SubDecision {
	case "burn":
		r = append(r, "Burning in subtitles")
	case "transcode":
		r = append(r, "Converting subtitles")
	}
	if len(r) == 0 { // both streams copied but still a TranscodeSession → container/protocol remux
		r = append(r, "Remuxing the container (video & audio copied)")
	}
	return r
}

func mediaLabel(codec, res string) string {
	codec = strings.ToUpper(strings.TrimSpace(codec))
	res = strings.TrimSpace(res)
	switch {
	case codec != "" && res != "":
		return codec + " " + normRes(res)
	case codec != "":
		return codec
	case res != "":
		return normRes(res)
	}
	return "—"
}

func normRes(res string) string {
	switch strings.ToLower(res) {
	case "4k", "2160":
		return "4K"
	case "1080":
		return "1080p"
	case "720":
		return "720p"
	case "480", "sd":
		return "SD"
	}
	return res
}

func streamRes(h int) string {
	switch {
	case h >= 2000:
		return "4K"
	case h >= 1000:
		return "1080p"
	case h >= 700:
		return "720p"
	case h > 0:
		return "SD"
	}
	return ""
}

// posterPath picks the portrait image for a play: an episode's own thumb is a landscape
// still, so its show's poster wins when there is one.
func posterPath(mediaType, thumb, showThumb string) string {
	if mediaType == "episode" && showThumb != "" {
		return showThumb
	}
	return thumb
}

// proxyImage turns a Plex image path (or a Jellyfin one, "jellyfin:/Items/…") into an
// Arrmada-proxied URL (the token or key stays server-side).
func proxyImage(path string) string {
	if path == "" {
		return ""
	}
	return "/api/v1/insights/image?path=" + url.QueryEscape(path)
}

// Image fetches a poster or art for the caller, keeping the server's credential
// server-side: a Plex image path, or a Jellyfin one in its strict form (IsJellyfinImage).
// The image proxy and alert posters both come through here.
func (s *Service) Image(ctx context.Context, path string) (*http.Response, error) {
	if strings.HasPrefix(path, jellyfinImagePrefix) || strings.HasPrefix(path, embyImagePrefix) {
		return s.jellyfinImage(ctx, path)
	}
	return s.client(ctx).Image(ctx, path)
}

// HistoryResult is a page of recorded plays with the total for pagination.
type HistoryResult struct {
	Rows  []HistoryEntry `json:"rows"`
	Total int            `json:"total"`
}

// watchedSecs is the Go twin of watchedExpr — the two must agree, or a row's watch time in
// the History list contradicts the totals on Users and Graphs, which are summed in SQL.
// Prefer what the source reported; fall back to wall time minus paused, which is only
// trustworthy for a live-tracked session where the poller saw the start AND the stop.
func watchedSecs(r HistoryRow) int64 {
	span := r.StoppedAt - r.StartedAt
	if span < 0 {
		span = 0
	}
	if r.WatchedMS > 0 {
		if w := r.WatchedMS / 1000; w < span {
			return w
		}
		return span // reported longer than the session existed — impossible, so cap it
	}
	if w := span - r.PausedMS/1000; w > 0 {
		return w
	}
	return 0
}

// HistoryEntry is a recorded play enriched for display (proxied thumb, geolocation, subtitle line).
type HistoryEntry struct {
	HistoryRow
	Subtitle    string         `json:"subtitle"`
	ThumbURL    string         `json:"thumb_url"`
	Geo         geoip.Location `json:"geo"`
	WatchedSecs int64          `json:"watched_secs"` // see watchedSecs
	ProgressPct int            `json:"progress_pct"`
	// Sessions is how many sittings a grouped row folds together (HistoryFilter.Group);
	// 0 on an ungrouped row.
	Sessions int `json:"sessions,omitempty"`
}

// History returns a page of recorded plays.
func (s *Service) History(ctx context.Context, f HistoryFilter) (HistoryResult, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	out := HistoryResult{Rows: []HistoryEntry{}}
	total, err := s.historyEach(ctx, f, func(e HistoryEntry) error { out.Rows = append(out.Rows, e); return nil })
	if err != nil {
		return HistoryResult{}, err
	}
	out.Total = total
	return out, nil
}

// HistoryExportMax caps one CSV export.
const HistoryExportMax = 50_000

// HistoryExport hands every play the filters match (newest first, at most
// HistoryExportMax) to fn, enriched as History lists them; f's paging is ignored.
func (s *Service) HistoryExport(ctx context.Context, f HistoryFilter, fn func(HistoryEntry) error) error {
	f.Limit, f.Offset = HistoryExportMax, 0
	_, err := s.historyEach(ctx, f, fn)
	return err
}

// historyEach runs one page of the history query, grouped or not, through toHistoryEntry.
func (s *Service) historyEach(ctx context.Context, f HistoryFilter, fn func(HistoryEntry) error) (int, error) {
	if f.Group {
		return s.repo.historyGrouped(ctx, f, func(g GroupedRow) error {
			e := s.toHistoryEntry(g.HistoryRow)
			// A viewing's watch time is its sittings' summed: its start-to-stop span also
			// covers the gaps between them.
			e.WatchedSecs, e.Sessions = g.WatchedSecs, g.Sessions
			return fn(e)
		})
	}
	return s.repo.historyEach(ctx, f, func(r HistoryRow) error { return fn(s.toHistoryEntry(r)) })
}

// toHistoryEntry enriches a recorded play for display. History and the Recently watched card
// both go through it, so a play's watch time and progress can't differ between the two — or
// from the Users and Graphs totals, which watchedSecs mirrors in SQL.
func (s *Service) toHistoryEntry(r HistoryRow) HistoryEntry {
	e := HistoryEntry{HistoryRow: r, ThumbURL: proxyImage(posterPath(r.MediaType, r.Thumb, r.GrandparentThumb)), Subtitle: historySubtitle(r), WatchedSecs: watchedSecs(r)}
	if s.geo != nil {
		e.Geo = s.geo.Lookup(r.IPAddress)
	}
	if r.DurationMS > 0 {
		pct := r.ViewOffsetMS * 100 / r.DurationMS
		e.ProgressPct = int(min(max(pct, 0), 100))
	}
	return e
}

func historySubtitle(r HistoryRow) string {
	if r.MediaType == "episode" && r.GrandparentTitle != "" {
		return fmt.Sprintf("%s · S%d · E%d", r.GrandparentTitle, r.ParentIndex, r.MediaIndex)
	}
	if r.Year > 0 {
		return fmt.Sprintf("%d", r.Year)
	}
	return r.ParentTitle
}
