package insights

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/jellyfin"
	"github.com/tristenlammi/arrmada/internal/jellyfinsvc"
	"github.com/tristenlammi/arrmada/internal/mediaserver"
)

// Watching Jellyfin. Jellyfin keeps no play history of its own, so Insights records it the
// way it records Plex: a poller asks /Sessions every few seconds (jellyfin_poll_seconds) while
// jellyfin_monitoring_enabled is on, and each play becomes a stream_sessions row with
// server = 'jellyfin'. History therefore starts when monitoring is switched on.
//
// What a Jellyfin session says, as checked against a real 12.2 server (internal/jellyfin's
// doc): no overall bitrate (the source's comes from the item's MediaSources, matched by
// PlayState.MediaSourceId, else the sum of its streams'); TranscodingInfo only while a
// transcode job runs, and then it is the truth over PlayMethod; HardwareAccelerationType is
// the configured hardware, not proof the GPU took the stream; no buffering at all.

// jfWiring is the Jellyfin connection and its session source.
type jfWiring struct {
	svc *jellyfinsvc.Service
	src *jellyfinSource
}

// SetJellyfin wires the Jellyfin connection. Call it before RunJellyfin; until then
// Insights is Plex only.
func (s *Service) SetJellyfin(jf *jellyfinsvc.Service) {
	if jf == nil {
		return
	}
	s.jfw.Store(&jfWiring{svc: jf, src: &jellyfinSource{s: s, jf: jf, rates: map[string]int64{}}})
}

// jellyfinMonitoring: Jellyfin is connected and its monitoring switch is on.
func (s *Service) jellyfinMonitoring(ctx context.Context) bool {
	w := s.jfw.Load()
	return w != nil && w.svc.MonitoringEnabled(ctx) && w.svc.Configured(ctx)
}

// JellyfinConfigured reports whether a Jellyfin server is connected (an address and a key).
func (s *Service) JellyfinConfigured(ctx context.Context) bool {
	w := s.jfw.Load()
	return w != nil && w.svc.Configured(ctx)
}

// ErrNoJellyfin is a Jellyfin setting saved before Jellyfin is wired.
var ErrNoJellyfin = errors.New("jellyfin isn't available")

// SetJellyfinMonitoring saves Jellyfin's "record what's played" switch and/or its poll
// interval (2–60 s; nil leaves a value as it is). Turning it off finalizes the streams in
// progress at the next cycle, as for Plex.
func (s *Service) SetJellyfinMonitoring(ctx context.Context, enabled *bool, pollSeconds *int) error {
	if s.jfw.Load() == nil {
		return ErrNoJellyfin
	}
	if enabled != nil {
		if err := s.settings.SetBool(ctx, jellyfinsvc.KeyMonitoring, *enabled); err != nil {
			return err
		}
	}
	if pollSeconds != nil {
		n := min(max(*pollSeconds, 2), 60)
		if err := s.settings.Set(ctx, jellyfinsvc.KeyPollSeconds, fmt.Sprint(n)); err != nil {
			return err
		}
	}
	return nil
}

// RunJellyfin is Jellyfin's monitoring loop (Run is Plex's). It returns at once when
// SetJellyfin wasn't called.
func (s *Service) RunJellyfin(ctx context.Context) {
	w := s.jfw.Load()
	if w == nil {
		return
	}
	// From here the poller records every exchange with Jellyfin, so its health panel and
	// connection card read the poller instead of probing on their own while it's on.
	w.svc.SetPolling(s.jellyfinMonitoring)
	s.restoreLive(ctx, s.jf)
	s.runMonitor(ctx, monitor{
		src: w.src, tr: s.jf,
		active: s.jellyfinMonitoring,
		every:  w.svc.PollSeconds,
		record: w.svc.Record,
		after:  w.src.syncUsers,
	})
}

// jellyfinSource reads Jellyfin's live sessions. The poller and the Activity view share it;
// rates is guarded by mu, lastUsers is the poller's alone.
type jellyfinSource struct {
	s  *Service
	jf *jellyfinsvc.Service

	mu sync.Mutex
	// rates is each playing version's source bitrate (kbps), by item|mediaSource: Jellyfin
	// only says it on the item, so it's read once per version, not every poll.
	rates map[string]int64

	lastUsers time.Time
}

func (*jellyfinSource) Kind() mediaserver.Kind { return mediaserver.Jellyfin }

// Sessions lists what's playing on Jellyfin as neutral sessions. Idle apps (no
// NowPlayingItem) and things Insights doesn't record (books, photos, live TV) are left out.
func (j *jellyfinSource) Sessions(ctx context.Context) ([]mediaserver.Session, error) {
	c := j.jf.Client(ctx)
	raw, err := c.Sessions(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]mediaserver.Session, 0, len(raw))
	for _, js := range raw {
		if !js.Playing() || jellyfinMediaType(js.NowPlayingItem.Type) == "" {
			continue
		}
		sess, ok := jellyfinSession(js, j.sourceKbps(ctx, c, js))
		if !ok {
			continue
		}
		if j.s.geo.Lookup(sess.Address).Local {
			sess.Local, sess.Location = true, "lan"
		} else {
			sess.Location = "wan"
		}
		out = append(out, sess)
	}
	return out, nil
}

// sourceKbps is the playing version's bitrate: the item's MediaSource the session names
// (its only one when it names none), else the sum of the streams the session lists. A
// failed lookup isn't remembered, so the next poll asks again.
func (j *jellyfinSource) sourceKbps(ctx context.Context, c *jellyfin.Client, js jellyfin.Session) int64 {
	np := js.NowPlayingItem
	key := np.ID + "|" + js.PlayState.MediaSourceID
	j.mu.Lock()
	kbps, ok := j.rates[key]
	j.mu.Unlock()
	if ok {
		return kbps
	}
	page, err := c.Items(ctx, jellyfin.ItemsQuery{IDs: []string{np.ID}, Fields: []string{"MediaSources"}, Limit: 1})
	if err == nil && len(page.Items) > 0 {
		kbps = mediaSourceKbps(page.Items[0].MediaSources, js.PlayState.MediaSourceID)
	}
	if kbps == 0 {
		kbps = streamsKbps(np.MediaStreams)
	}
	if err == nil {
		j.mu.Lock()
		if len(j.rates) >= 512 { // a long-running install plays thousands of things
			clear(j.rates)
		}
		j.rates[key] = kbps
		j.mu.Unlock()
	}
	return kbps
}

// mediaSourceKbps picks the playing version's bitrate out of an item's versions.
func mediaSourceKbps(sources []jellyfin.MediaSource, id string) int64 {
	for _, ms := range sources {
		if ms.ID == id && ms.Bitrate > 0 {
			return ms.Bitrate / 1000
		}
	}
	if id == "" && len(sources) == 1 {
		return sources[0].Bitrate / 1000
	}
	return 0
}

// streamsKbps sums a file's own streams (not external subtitles).
func streamsKbps(streams []jellyfin.MediaStream) int64 {
	var bps int64
	for _, st := range streams {
		if !st.IsExternal {
			bps += st.BitRate
		}
	}
	return bps / 1000
}

// jellyfinMediaType is Insights' word for a Jellyfin item type, "" for one it doesn't
// record. Books and audiobooks are left out on purpose: listening is Arrmada's audiobook
// server's to report, and staff never see what someone reads.
func jellyfinMediaType(t string) string {
	switch t {
	case jellyfin.TypeMovie, "Video", "MusicVideo":
		return "movie"
	case jellyfin.TypeEpisode:
		return "episode"
	case "Audio":
		return "track"
	}
	return ""
}

// JellyfinSessionKey is the session key a Jellyfin session is recorded under: its id,
// prefixed so it can never be taken for a Plex session key.
func JellyfinSessionKey(id string) string { return "jellyfin:" + id }

// jellyfinSession maps one playing Jellyfin session onto the neutral session (pure, for
// tests). srcKbps is the playing version's bitrate (sourceKbps).
func jellyfinSession(js jellyfin.Session, srcKbps int64) (mediaserver.Session, bool) {
	np := js.NowPlayingItem
	mt := ""
	if np != nil {
		mt = jellyfinMediaType(np.Type)
	}
	if np == nil || np.ID == "" || js.ID == "" || mt == "" {
		return mediaserver.Session{}, false
	}
	state := "playing"
	if js.PlayState.IsPaused {
		state = "paused"
	}
	out := mediaserver.Session{
		Server: mediaserver.Jellyfin, SessionKey: JellyfinSessionKey(js.ID), RatingKey: jellyfinRatingKey(np.ID), Type: mt,
		Title: np.Name, Year: np.ProductionYear, Thumb: serverItemImage(np.ID),
		DurationMS: jellyfin.TicksToDuration(np.RunTimeTicks).Milliseconds(),
		OffsetMS:   js.PlayState.Position().Milliseconds(),
		UserID:     js.UserID, UserName: js.UserName,
		Address: clientIP(js.RemoteEndPoint), Device: js.DeviceName, PlayerName: js.DeviceName,
		Product: js.Client, Platform: jellyfinPlatform(js.Client), State: state,
		SrcContainer: np.Container, SrcBitrate: srcKbps, Bandwidth: srcKbps,
	}
	if mt == "episode" {
		out.ShowTitle, out.Index, out.SeasonNum = np.SeriesName, np.IndexNumber, np.ParentIndexNumber
		if np.ParentIndexNumber > 0 {
			out.SeasonName = fmt.Sprintf("Season %d", np.ParentIndexNumber)
		}
		if isGUID(np.SeriesID) {
			out.ShowRatingKey, out.ShowThumb = np.SeriesID, JellyfinItemImage(np.SeriesID)
		} else if jellyfin.IsEmbyItemID(np.SeriesID) {
			out.ShowRatingKey, out.ShowThumb = jellyfinRatingKey(np.SeriesID), EmbyItemImage(np.SeriesID)
		}
	}
	if v := np.VideoStream(); v != nil {
		out.SrcVideoCodec, out.SrcResolution = v.Codec, streamRes(v.Height)
		out.SrcVideoBitDepth, out.SrcVideoProfile = v.BitDepth, strings.ToLower(v.Profile)
	}
	if a := audioStream(np.MediaStreams); a != nil {
		out.SrcAudioCodec = a.Codec
	}
	// TranscodingInfo only exists while a transcode job runs, and then it's what is really
	// happening, whatever the player said PlayMethod was.
	if ti := js.TranscodingInfo; ti != nil {
		out.Transcoding = true
		out.VideoDecision, out.AudioDecision = "transcode", "transcode"
		if ti.IsVideoDirect {
			out.VideoDecision = "copy"
		}
		if ti.IsAudioDirect {
			out.AudioDecision = "copy"
		}
		out.StreamVideoCodec, out.StreamAudioCodec = ti.VideoCodec, ti.AudioCodec
		out.StreamWidth, out.StreamHeight, out.TranscodeCont = ti.Width, ti.Height, ti.Container
		// The configured hardware: Jellyfin doesn't say whether the GPU took this stream,
		// so it's shown as "HW (vaapi)", never as Plex's "fell back to CPU".
		if hw := strings.ToLower(strings.TrimSpace(ti.HardwareAccelerationType)); hw != "" && hw != "none" {
			out.TranscodeHW, out.HWTitle = true, hw
		}
		if ti.Bitrate > 0 {
			out.Bandwidth = ti.Bitrate / 1000
		}
	}
	return out, true
}

// audioStream is the file's default audio stream, else its first.
func audioStream(streams []jellyfin.MediaStream) *jellyfin.MediaStream {
	var first *jellyfin.MediaStream
	for i := range streams {
		if streams[i].Type != "Audio" {
			continue
		}
		if streams[i].IsDefault {
			return &streams[i]
		}
		if first == nil {
			first = &streams[i]
		}
	}
	return first
}

// jellyfinPlatform is the app without the brand ("Jellyfin Android TV" → "Android TV",
// "Emby for Android" → "Android").
func jellyfinPlatform(client string) string {
	p := strings.TrimPrefix(client, "Jellyfin ")
	if p == client {
		p = strings.TrimPrefix(strings.TrimPrefix(client, "Emby "), "for ")
	}
	if p = strings.TrimSpace(p); p != "" {
		return p
	}
	return client
}

// jellyfinRatingKey is the key a Jellyfin or Emby item's plays are recorded under. A
// Jellyfin id (32 hex digits) is kept as is; an Emby item number gets an "emby-" prefix,
// because a bare number could be mistaken for a Plex rating key (Plex numbers its items
// too) and link a play to the wrong Plex title.
func jellyfinRatingKey(id string) string {
	if jellyfin.IsEmbyItemID(id) {
		return embyKeyPrefix + id
	}
	return id
}

const embyKeyPrefix = "emby-"

// isServerKey reports whether a recorded rating key is a Jellyfin or Emby one, never Plex's.
func isServerKey(k string) bool { return isGUID(k) || strings.HasPrefix(k, embyKeyPrefix) }

// clientIP is a RemoteEndPoint as a bare address: no port, no IPv4-mapped prefix.
func clientIP(ep string) string {
	ep = strings.TrimSpace(ep)
	if host, _, err := net.SplitHostPort(ep); err == nil {
		ep = host
	}
	ep = strings.Trim(ep, "[]")
	if ip := net.ParseIP(ep); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
		return ip.String()
	}
	return ep
}

// syncUsers refreshes Jellyfin's people (GET /Users) every few hours, so the People tab
// lists everyone who can watch with their names and pictures, not only who has streamed.
func (j *jellyfinSource) syncUsers(ctx context.Context, now time.Time) {
	if !j.lastUsers.IsZero() && now.Sub(j.lastUsers) < 6*time.Hour {
		return
	}
	j.lastUsers = now
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	users, err := j.jf.Client(cctx).Users(cctx)
	if err != nil {
		j.s.log.Debug("insights: couldn't list Jellyfin's users", "err", err)
		return
	}
	for _, u := range users {
		if !isGUID(u.ID) && !jellyfin.IsEmbyItemID(u.ID) {
			continue
		}
		thumb := ""
		if u.PrimaryImageTag != "" {
			thumb = serverUserImage(u.ID)
		}
		if err := j.s.repo.upsertUser(ctx, mediaserver.Jellyfin, u.ID, u.Name, thumb, 0); err != nil {
			j.s.log.Debug("insights: couldn't save a Jellyfin user", "err", err)
		}
	}
}

// Jellyfin images. A Jellyfin image travels as "jellyfin:/Items/<id>/Images/Primary" (an
// item's poster) or "jellyfin:/Users/<id>/Images/Primary" (a person's picture), with the id
// 32 hex digits — the only two shapes accepted anywhere, so a stored or requested path can
// never point the keyed request at another endpoint.
const jellyfinImagePrefix = "jellyfin:"

var jellyfinImageRe = regexp.MustCompile(`^jellyfin:/(Items|Users)/([0-9a-f]{32})/Images/Primary$`)

// JellyfinItemImage is the image path of an item's poster.
func JellyfinItemImage(id string) string {
	return jellyfinImagePrefix + "/Items/" + strings.ToLower(id) + "/Images/Primary"
}

// JellyfinUserImage is the image path of a person's picture.
func JellyfinUserImage(id string) string {
	return jellyfinImagePrefix + "/Users/" + strings.ToLower(id) + "/Images/Primary"
}

// IsJellyfinImage reports whether path names a Jellyfin image (strictly).
func IsJellyfinImage(path string) bool { return jellyfinImageRe.MatchString(path) }

// Emby images. Emby numbers its items, so an Emby poster travels as
// "emby:/Items/<digits>/Images/Primary" (and a person's picture, when Emby gives a numeric
// user id, as "emby:/Users/<digits>/Images/Primary"); GUID ids use the Jellyfin form above.
// Either is fetched from the connected server, which is Jellyfin or Emby, never both.
const embyImagePrefix = "emby:"

var embyImageRe = regexp.MustCompile(`^emby:/(Items|Users)/([0-9]{1,19})/Images/Primary$`)

// EmbyItemImage is the image path of an Emby item's poster.
func EmbyItemImage(id string) string { return embyImagePrefix + "/Items/" + id + "/Images/Primary" }

// IsEmbyImage reports whether path names an Emby image (strictly).
func IsEmbyImage(path string) bool { return embyImageRe.MatchString(path) }

// IsServerImage reports whether path names a Jellyfin or an Emby image (strictly).
func IsServerImage(path string) bool { return IsJellyfinImage(path) || IsEmbyImage(path) }

// serverItemImage is an item's poster path in the form its id calls for: the Emby form for
// an Emby item number, the Jellyfin form otherwise.
func serverItemImage(id string) string {
	if jellyfin.IsEmbyItemID(id) {
		return EmbyItemImage(id)
	}
	return JellyfinItemImage(id)
}

// serverUserImage is serverItemImage for a person's picture.
func serverUserImage(id string) string {
	if jellyfin.IsEmbyItemID(id) {
		return embyImagePrefix + "/Users/" + id + "/Images/Primary"
	}
	return JellyfinUserImage(id)
}

// isGUID: 32 hex digits, as Jellyfin writes its ids.
func isGUID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// jellyfinImage fetches a Jellyfin image path (IsJellyfinImage) with the key server-side.
func (s *Service) jellyfinImage(ctx context.Context, path string) (*http.Response, error) {
	m := jellyfinImageRe.FindStringSubmatch(path)
	if m == nil {
		m = embyImageRe.FindStringSubmatch(path)
	}
	if m == nil {
		return nil, errors.New("unsupported image path")
	}
	w := s.jfw.Load()
	if w == nil || !w.svc.Configured(ctx) {
		return nil, ErrNoJellyfin
	}
	c := w.svc.Client(ctx)
	if m[1] == "Users" {
		return c.UserImage(ctx, m[2])
	}
	return c.Image(ctx, m[2], jellyfin.ImagePrimary, 600)
}
