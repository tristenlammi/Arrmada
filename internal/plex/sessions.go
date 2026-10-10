package plex

import (
	"context"
	"fmt"
)

// Session is one active playback stream from /status/sessions, flattened into the fields
// Insights cares about. The raw Plex payload is far larger; we keep what drives the Activity
// view, the recorder, and the stream deep-dive.
type Session struct {
	SessionKey string
	RatingKey  string
	Type       string // movie | episode | track
	Title      string
	ShowTitle  string // grandparentTitle
	SeasonName string // parentTitle
	Index      int    // episode number
	SeasonNum  int    // parentIndex
	Year       int
	Thumb      string
	DurationMS int64
	OffsetMS   int64

	UserID    string
	UserName  string
	UserThumb string

	Address    string // player LAN/local address
	PublicIP   string // remotePublicAddress (for geolocation)
	Device     string
	Platform   string
	Product    string
	PlayerName string
	State      string // playing | paused | buffering
	Local      bool

	Bandwidth int64  // kbps
	Location  string // lan | wan

	// Transcode (zero-value when direct playing).
	Transcoding   bool
	VideoDecision string // copy | transcode
	AudioDecision string // copy | transcode
	SubDecision   string // "", copy, transcode, burn
	// TranscodeHW is whether the GPU is doing any of the work (decode or encode). The HW*
	// fields say which part: Plex reports what it actually uses, which can differ from
	// what was asked for — with hardware transcoding switched on, Plex still falls back to
	// the CPU when the GPU can't take a stream, and only HWRequested stays true.
	TranscodeHW    bool
	HWRequested    bool
	HWDecode       bool
	HWEncode       bool
	HWFullPipeline bool   // decode, filters and encode all on the GPU
	HWTitle        string // Plex's name for the hardware in use ("Intel (QuickSync)"), encoder first
	Throttled      bool
	TranscodeSpeed float64 // transcode throughput vs realtime; < 1.0 = falling behind
	TranscodeProto string
	TranscodeCont  string

	// Source media specs (from Media[0]).
	SrcResolution string
	SrcVideoCodec string
	SrcAudioCodec string
	SrcContainer  string
	SrcBitrate    int64

	// Transcode target specs (from TranscodeSession; empty when direct playing).
	StreamVideoCodec string
	StreamAudioCodec string
	StreamWidth      int
	StreamHeight     int
}

// BufferCause classifies the likely reason a stream stalled, from the transcode and
// network signals in the session. Plex reports no explicit reason, so this is a
// best-effort diagnosis returning (cause key, human-readable detail).
func (s Session) BufferCause() (cause, detail string) {
	// A transcoder that can't keep up (throughput < realtime) is the clearest, most
	// common culprit — and it tells you whether hardware transcoding is helping.
	// The encoder is what sets the pace, so it decides which story this is: a GPU encode
	// falling behind, a GPU Plex was asked to use but didn't, or a CPU-only setup.
	if s.Transcoding && s.TranscodeSpeed > 0 && s.TranscodeSpeed < 0.95 {
		switch {
		case s.HWEncode:
			return "transcode", fmt.Sprintf("hardware transcode falling behind (%.1f× realtime)", s.TranscodeSpeed)
		case s.HWRequested:
			return "transcode_fallback", fmt.Sprintf("Plex fell back to CPU — hardware transcoding was requested but not used (%.1f× realtime)", s.TranscodeSpeed)
		}
		return "transcode_cpu", fmt.Sprintf("CPU transcode too slow (%.1f× realtime) — hardware transcoding not in use", s.TranscodeSpeed)
	}
	// Remote direct play whose reserved bandwidth is under the source bitrate → network.
	if !s.Local && !s.Transcoding && s.Bandwidth > 0 && s.SrcBitrate > 0 && s.Bandwidth < s.SrcBitrate {
		return "bandwidth", fmt.Sprintf("remote direct play — ~%d kbps available vs %d kbps source", s.Bandwidth, s.SrcBitrate)
	}
	if !s.Local {
		return "bandwidth", "remote stream — likely limited by the client's connection"
	}
	if s.Transcoding {
		return "transcode", "transcoding — the server was momentarily busy"
	}
	return "unknown", "no clear cause — a brief source-read or network hiccup"
}

// Decision classifies the stream as direct_play / direct_stream / transcode.
func (s Session) Decision() string {
	if !s.Transcoding {
		return "direct_play"
	}
	if s.VideoDecision == "copy" && s.AudioDecision == "copy" {
		return "direct_stream"
	}
	return "transcode"
}

// Sessions returns the server's current active streams.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var r struct {
		MediaContainer struct {
			Metadata []rawSession `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := c.get(ctx, "/status/sessions", &r); err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(r.MediaContainer.Metadata))
	for _, m := range r.MediaContainer.Metadata {
		out = append(out, m.flatten())
	}
	return out, nil
}

// rawSession mirrors the nested Plex JSON so we can flatten it.
type rawSession struct {
	SessionKey       string  `json:"sessionKey"`
	RatingKey        string  `json:"ratingKey"`
	Type             string  `json:"type"`
	Title            string  `json:"title"`
	GrandparentTitle string  `json:"grandparentTitle"`
	ParentTitle      string  `json:"parentTitle"`
	Index            flexInt `json:"index"`
	ParentIndex      flexInt `json:"parentIndex"`
	Year             flexInt `json:"year"`
	Thumb            string  `json:"thumb"`
	Duration         flexInt `json:"duration"`
	ViewOffset       flexInt `json:"viewOffset"`
	User             struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Thumb string `json:"thumb"`
	} `json:"User"`
	Player struct {
		Address             string `json:"address"`
		RemotePublicAddress string `json:"remotePublicAddress"`
		Device              string `json:"device"`
		Platform            string `json:"platform"`
		Product             string `json:"product"`
		Title               string `json:"title"`
		State               string `json:"state"`
		Local               bool   `json:"local"`
	} `json:"Player"`
	Session struct {
		Bandwidth flexInt `json:"bandwidth"`
		Location  string  `json:"location"`
	} `json:"Session"`
	TranscodeSession *struct {
		VideoDecision string  `json:"videoDecision"`
		AudioDecision string  `json:"audioDecision"`
		SubDecision   string  `json:"subtitleDecision"`
		Protocol      string  `json:"protocol"`
		Container     string  `json:"container"`
		Throttled     bool    `json:"throttled"`
		Speed         float64 `json:"speed"`
		// What hardware Plex asked for and what it is really using. Plex doesn't document
		// the types: decoding/encoding name the API ("qsv", "vaapi", "dxva2"), the others
		// are flags, and any of them may arrive as a string or a bool — or be missing when
		// the CPU does the work — so all of them are read tolerantly.
		HwRequested     flexStr `json:"transcodeHwRequested"`
		HwDecoding      flexStr `json:"transcodeHwDecoding"`
		HwDecodingTitle flexStr `json:"transcodeHwDecodingTitle"`
		HwEncoding      flexStr `json:"transcodeHwEncoding"`
		HwEncodingTitle flexStr `json:"transcodeHwEncodingTitle"`
		HwFullPipeline  flexStr `json:"transcodeHwFullPipeline"`
		VideoCodec      string  `json:"videoCodec"`
		AudioCodec      string  `json:"audioCodec"`
		Width           flexInt `json:"width"`
		Height          flexInt `json:"height"`
	} `json:"TranscodeSession"`
	Media []struct {
		VideoResolution string  `json:"videoResolution"`
		Bitrate         flexInt `json:"bitrate"`
		Container       string  `json:"container"`
		VideoCodec      string  `json:"videoCodec"`
		AudioCodec      string  `json:"audioCodec"`
	} `json:"Media"`
}

func (m rawSession) flatten() Session {
	s := Session{
		SessionKey: m.SessionKey, RatingKey: m.RatingKey, Type: m.Type, Title: m.Title,
		ShowTitle: m.GrandparentTitle, SeasonName: m.ParentTitle,
		Index: int(m.Index), SeasonNum: int(m.ParentIndex), Year: int(m.Year), Thumb: m.Thumb,
		DurationMS: int64(m.Duration), OffsetMS: int64(m.ViewOffset),
		UserID: m.User.ID, UserName: m.User.Title, UserThumb: m.User.Thumb,
		Address: m.Player.Address, PublicIP: m.Player.RemotePublicAddress,
		Device: m.Player.Device, Platform: m.Player.Platform, Product: m.Player.Product,
		PlayerName: m.Player.Title, State: m.Player.State, Local: m.Player.Local,
		Bandwidth: int64(m.Session.Bandwidth), Location: m.Session.Location,
	}
	if t := m.TranscodeSession; t != nil {
		s.Transcoding = true
		s.VideoDecision, s.AudioDecision, s.SubDecision = t.VideoDecision, t.AudioDecision, t.SubDecision
		s.HWRequested, s.HWDecode, s.HWEncode = t.HwRequested.on(), t.HwDecoding.on(), t.HwEncoding.on()
		s.HWFullPipeline = t.HwFullPipeline.on()
		s.TranscodeHW = s.HWDecode || s.HWEncode
		switch {
		case s.HWEncode && t.HwEncodingTitle.on():
			s.HWTitle = string(t.HwEncodingTitle)
		case s.HWDecode && t.HwDecodingTitle.on():
			s.HWTitle = string(t.HwDecodingTitle)
		}
		s.Throttled = t.Throttled
		s.TranscodeSpeed = t.Speed
		s.TranscodeProto, s.TranscodeCont = t.Protocol, t.Container
		s.StreamVideoCodec, s.StreamAudioCodec = t.VideoCodec, t.AudioCodec
		s.StreamWidth, s.StreamHeight = int(t.Width), int(t.Height)
	}
	if len(m.Media) > 0 {
		md := m.Media[0]
		s.SrcResolution, s.SrcVideoCodec, s.SrcAudioCodec = md.VideoResolution, md.VideoCodec, md.AudioCodec
		s.SrcContainer, s.SrcBitrate = md.Container, int64(md.Bitrate)
	}
	return s
}
