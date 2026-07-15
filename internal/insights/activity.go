package insights

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tristenlammi/arrmada/internal/geoip"
	"github.com/tristenlammi/arrmada/internal/plex"
)

// Stream is one live playback session, enriched for the Activity view + deep-dive.
type Stream struct {
	SessionKey  string         `json:"session_key"`
	User        string         `json:"user"`
	Title       string         `json:"title"`
	Subtitle    string         `json:"subtitle"` // "Show · S1 · E2" or the year
	Type        string         `json:"type"`
	Thumb       string         `json:"thumb"` // proxied poster URL
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
	Video       Detail   `json:"video"`
	Audio       Detail   `json:"audio"`
	Container   Detail   `json:"container"`
	HWTranscode bool     `json:"hw_transcode"`
	Throttled   bool     `json:"throttled"`
	Reasons     []string `json:"reasons"` // why it's transcoding (empty for direct play)
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
}

// Bandwidth is the summed stream bandwidth, split by network location.
type Bandwidth struct {
	TotalKbps int64 `json:"total_kbps"`
	LANKbps   int64 `json:"lan_kbps"`
	WANKbps   int64 `json:"wan_kbps"`
}

// Activity returns the current live streams from Plex, enriched with geolocation, the stream
// decision, source→stream detail, and human "why it's transcoding" reasons.
func (s *Service) Activity(ctx context.Context) (Activity, error) {
	sessions, err := s.client(ctx).Sessions(ctx)
	if err != nil {
		return Activity{}, err
	}
	out := Activity{Streams: make([]Stream, 0, len(sessions)), GeoActive: s.geo.Enabled()}
	for _, sess := range sessions {
		st := s.enrich(sess)
		out.Streams = append(out.Streams, st)
		out.Bandwidth.TotalKbps += st.Bandwidth
		if strings.EqualFold(sess.Location, "lan") || st.Geo.Local {
			out.Bandwidth.LANKbps += st.Bandwidth
		} else {
			out.Bandwidth.WANKbps += st.Bandwidth
		}
	}
	return out, nil
}

func (s *Service) enrich(p plex.Session) Stream {
	ip := p.PublicIP
	if ip == "" {
		ip = p.Address
	}
	st := Stream{
		SessionKey: p.SessionKey, User: p.UserName, Title: p.Title, Type: p.Type,
		Thumb: proxyImage(p.Thumb), OffsetMS: p.OffsetMS, DurationMS: p.DurationMS,
		State: p.State, Player: p.PlayerName, Platform: p.Platform, Product: p.Product,
		Decision: p.Decision(), Bandwidth: p.Bandwidth, Location: p.Location,
		IP: ip, Geo: s.geo.Lookup(ip),
		HWTranscode: p.TranscodeHW, Throttled: p.Throttled,
	}
	if p.DurationMS > 0 {
		st.ProgressPct = int(p.OffsetMS * 100 / p.DurationMS)
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

// proxyImage turns a Plex image path into an Arrmada-proxied URL (token stays server-side).
func proxyImage(path string) string {
	if path == "" {
		return ""
	}
	return "/api/v1/insights/image?path=" + url.QueryEscape(path)
}

// Image proxies a Plex image (poster/art) to the caller, keeping the token server-side.
func (s *Service) Image(ctx context.Context, path string) (*http.Response, error) {
	return s.client(ctx).Image(ctx, path)
}
