package jellyfin

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Session is one client connection from /Sessions. A session with a NowPlayingItem is a
// stream; the rest are idle apps (and Arrmada's own key shows up as one).
type Session struct {
	ID                 string    `json:"Id"`
	UserID             string    `json:"UserId"`
	UserName           string    `json:"UserName"`
	Client             string    `json:"Client"` // the app: "Jellyfin Web", "Infuse" …
	DeviceName         string    `json:"DeviceName"`
	DeviceID           string    `json:"DeviceId"`
	ApplicationVersion string    `json:"ApplicationVersion"`
	RemoteEndPoint     string    `json:"RemoteEndPoint"` // the client's IP as the server saw it
	LastActivityDate   time.Time `json:"LastActivityDate"`
	IsActive           bool      `json:"IsActive"`

	NowPlayingItem  *NowPlayingItem  `json:"NowPlayingItem"`
	PlayState       PlayState        `json:"PlayState"`
	TranscodingInfo *TranscodingInfo `json:"TranscodingInfo"` // nil when direct playing
}

// Playing reports whether the session is streaming something.
func (s Session) Playing() bool { return s.NowPlayingItem != nil }

// NowPlayingItem is the item a session is playing. MediaStreams are the source file's
// streams (checked against 12.2: present, with BitDepth and Profile on the video stream).
type NowPlayingItem struct {
	ID                string        `json:"Id"`
	Name              string        `json:"Name"`
	Type              string        `json:"Type"` // Movie | Episode | Audio …
	ProductionYear    int           `json:"ProductionYear"`
	RunTimeTicks      int64         `json:"RunTimeTicks"`
	Container         string        `json:"Container"`
	Path              string        `json:"Path"`
	ProviderIDs       ProviderIDs   `json:"ProviderIds"`
	SeriesID          string        `json:"SeriesId"`
	SeriesName        string        `json:"SeriesName"`
	SeasonID          string        `json:"SeasonId"`
	ParentIndexNumber int           `json:"ParentIndexNumber"`
	IndexNumber       int           `json:"IndexNumber"`
	MediaStreams      []MediaStream `json:"MediaStreams"`
}

// VideoStream returns the first video stream, or nil.
func (n *NowPlayingItem) VideoStream() *MediaStream {
	if n == nil {
		return nil
	}
	for i := range n.MediaStreams {
		if n.MediaStreams[i].Type == "Video" {
			return &n.MediaStreams[i]
		}
	}
	return nil
}

// PlayState is where a session is in its item.
type PlayState struct {
	PositionTicks int64  `json:"PositionTicks"`
	IsPaused      bool   `json:"IsPaused"`
	IsMuted       bool   `json:"IsMuted"`
	PlayMethod    string `json:"PlayMethod"` // DirectPlay | DirectStream | Transcode
	MediaSourceID string `json:"MediaSourceId"`
	CanSeek       bool   `json:"CanSeek"`
}

// Position converts PositionTicks.
func (p PlayState) Position() time.Duration { return TicksToDuration(p.PositionTicks) }

// TranscodingInfo is a transcode's state. Jellyfin reports the configured hardware type
// (vaapi, qsv, nvenc …; "none" for software), not whether the GPU actually took the stream.
type TranscodingInfo struct {
	AudioCodec               string   `json:"AudioCodec"`
	VideoCodec               string   `json:"VideoCodec"`
	Container                string   `json:"Container"`
	IsVideoDirect            bool     `json:"IsVideoDirect"`
	IsAudioDirect            bool     `json:"IsAudioDirect"`
	Bitrate                  int64    `json:"Bitrate"` // bits per second
	Framerate                float64  `json:"Framerate"`
	CompletionPercentage     float64  `json:"CompletionPercentage"`
	Width                    int      `json:"Width"`
	Height                   int      `json:"Height"`
	AudioChannels            int      `json:"AudioChannels"`
	HardwareAccelerationType string   `json:"HardwareAccelerationType"`
	TranscodeReasons         []string `json:"TranscodeReasons"`

	// Emby has no HardwareAccelerationType; it says per transcode whether the decoder and
	// the encoder ran on hardware and which kind. Sessions folds them into
	// HardwareAccelerationType for an Emby client, so readers see one field.
	VideoDecoderIsHardware bool   `json:"VideoDecoderIsHardware"`
	VideoDecoderHwAccel    string `json:"VideoDecoderHwAccel"`
	VideoEncoderIsHardware bool   `json:"VideoEncoderIsHardware"`
	VideoEncoderHwAccel    string `json:"VideoEncoderHwAccel"`
}

// embyHardware is the hardware an Emby transcode really ran on, in Jellyfin's terms: the
// encoder's kind ("qsv", "vaapi" …), else the decoder's, else "none".
func (t *TranscodingInfo) embyHardware() string {
	switch {
	case t.VideoEncoderIsHardware:
		return hwName(t.VideoEncoderHwAccel)
	case t.VideoDecoderIsHardware:
		return hwName(t.VideoDecoderHwAccel)
	}
	return "none"
}

func hwName(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return "hardware"
	}
	return s
}

// Sessions lists sessions seen within the last activeWithinSeconds (0 = Jellyfin's default,
// every session it still holds).
func (c *Client) Sessions(ctx context.Context, activeWithinSeconds int) ([]Session, error) {
	var q url.Values
	if activeWithinSeconds > 0 {
		q = url.Values{"activeWithinSeconds": {strconv.Itoa(activeWithinSeconds)}}
	}
	var out []Session
	err := c.call(ctx, http.MethodGet, "/Sessions", q, nil, &out, true)
	if c.emby {
		for i := range out {
			if ti := out[i].TranscodingInfo; ti != nil && ti.HardwareAccelerationType == "" {
				ti.HardwareAccelerationType = ti.embyHardware()
			}
		}
	}
	return out, err
}
