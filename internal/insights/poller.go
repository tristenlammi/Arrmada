package insights

import (
	"context"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/plex"
)

// liveSession tracks one in-flight stream across polls so we can record it accurately when it ends.
type liveSession struct {
	sess      plex.Session // latest snapshot (transcode decision can change mid-stream)
	started   time.Time
	lastTick  time.Time
	state     string
	pausedMS  int64
	buffering bool // currently inside a buffer spell (debounces events)
	bufCount  int
	bufEvents []bufEvent
}

type bufEvent struct {
	at     time.Time
	offset int64
}

// Run is the monitoring loop: while enabled + configured, poll Plex and record activity. Re-reads
// the interval + enabled flag each cycle so settings changes take effect without a restart.
func (s *Service) Run(ctx context.Context) {
	for {
		wait := time.Duration(s.pollSeconds(ctx)) * time.Second
		select {
		case <-ctx.Done():
			s.flushAll(context.Background())
			return
		case <-time.After(wait):
		}
		if !s.settings.GetBool(ctx, keyEnabled, false) {
			continue
		}
		if s.settings.Get(ctx, keyURL, "") == "" || s.settings.Get(ctx, keyToken, "") == "" {
			continue
		}
		s.poll(ctx)
	}
}

func (s *Service) poll(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	sessions, err := s.client(cctx).Sessions(cctx)
	cancel()
	if err != nil {
		s.log.Debug("insights: session poll failed", "err", err)
		return
	}
	now := time.Now()
	seen := make(map[string]bool, len(sessions))
	var total, lan, wan int64

	for _, sess := range sessions {
		seen[sess.SessionKey] = true
		ls := s.live[sess.SessionKey]
		if ls == nil {
			ls = &liveSession{started: now, lastTick: now, state: sess.State, sess: sess}
			s.live[sess.SessionKey] = ls
			_ = s.repo.upsertUser(ctx, sess.UserID, sess.UserName, sess.UserThumb, now.Unix())
			s.publish("plex.stream.started", sess)
		}
		if ls.observe(sess, now) { // a new buffer spell just started
			s.publish("plex.buffering", sess)
		}
		total += sess.Bandwidth
		if strings.EqualFold(sess.Location, "lan") || isLocalIP(s, sess) {
			lan += sess.Bandwidth
		} else {
			wan += sess.Bandwidth
		}
	}

	// Finalize sessions that vanished since the last poll.
	for key, ls := range s.live {
		if !seen[key] {
			s.finalize(ctx, ls, now)
			delete(s.live, key)
		}
	}

	_ = s.repo.insertBandwidth(ctx, now.Unix(), total, lan, wan)
}

// observe folds one poll's session snapshot into the tracked state. Returns true when a new
// buffering spell just began (so the caller can emit an event).
func (ls *liveSession) observe(sess plex.Session, now time.Time) bool {
	if ls.state == "paused" { // accrue paused time across the interval just elapsed
		ls.pausedMS += now.Sub(ls.lastTick).Milliseconds()
	}
	newSpell := false
	if sess.State == "buffering" {
		if !ls.buffering { // new spell
			ls.buffering = true
			ls.bufCount++
			ls.bufEvents = append(ls.bufEvents, bufEvent{at: now, offset: sess.OffsetMS})
			newSpell = true
		}
	} else {
		ls.buffering = false
	}
	ls.state = sess.State
	ls.lastTick = now
	ls.sess = sess // keep the freshest snapshot (offsets, decision, transcode specs)
	return newSpell
}

// publish emits a Plex watch event to the bus (for notifications). Safe if the bus is nil.
func (s *Service) publish(topic string, sess plex.Session) {
	if s.bus == nil {
		return
	}
	title := sess.Title
	if sess.Type == "episode" && sess.ShowTitle != "" {
		title = sess.ShowTitle + " · " + sess.Title
	}
	s.bus.Publish(topic, map[string]any{
		"user": sess.UserName, "title": title, "player": sess.PlayerName,
		"platform": sess.Platform, "decision": sess.Decision(),
	})
}

// record builds the persisted row from the tracked session state (pure — unit-tested).
func (ls *liveSession) record(now time.Time) sessionRecord {
	p := ls.sess
	rec := sessionRecord{
		SessionKey: p.SessionKey, UserID: p.UserID, UserName: p.UserName, RatingKey: p.RatingKey,
		MediaType: p.Type, Title: p.Title, GrandparentTitle: p.ShowTitle, ParentTitle: p.SeasonName,
		MediaIndex: p.Index, ParentIndex: p.SeasonNum, Year: p.Year, Thumb: p.Thumb,
		Player: p.PlayerName, Platform: p.Platform, Product: p.Product,
		IPAddress: streamIP(p), Location: p.Location, Decision: p.Decision(),
		StartedAt: ls.started.Unix(), StoppedAt: now.Unix(), PausedMS: ls.pausedMS,
		ViewOffsetMS: p.OffsetMS, DurationMS: p.DurationMS,
		VideoSrc: mediaLabel(p.SrcVideoCodec, p.SrcResolution), AudioSrc: strings.ToUpper(p.SrcAudioCodec),
		ContainerSrc: strings.ToUpper(p.SrcContainer), HWTranscode: p.TranscodeHW, BufferCount: ls.bufCount,
	}
	if p.Transcoding {
		if p.VideoDecision == "transcode" {
			rec.VideoStream = mediaLabel(p.StreamVideoCodec, streamRes(p.StreamHeight))
		}
		if p.AudioDecision == "transcode" {
			rec.AudioStream = strings.ToUpper(p.StreamAudioCodec)
		}
		rec.ContainerStream = strings.ToUpper(p.TranscodeCont)
	}
	return rec
}

// finalize writes a completed session + its buffer events. Blips shorter than a poll are dropped.
func (s *Service) finalize(ctx context.Context, ls *liveSession, now time.Time) {
	if now.Sub(ls.started) < 2*time.Second {
		return
	}
	rec := ls.record(now)
	id, err := s.repo.insertSession(ctx, rec)
	if err != nil {
		s.log.Warn("insights: could not record session", "title", rec.Title, "err", err)
		return
	}
	for _, be := range ls.bufEvents {
		_ = s.repo.insertBufferEvent(ctx, id, be.at.Unix(), be.offset)
	}
	s.log.Debug("insights: recorded session", "title", rec.Title, "user", rec.UserName, "buffers", ls.bufCount)
}

// flushAll finalizes every in-flight session (called on shutdown).
func (s *Service) flushAll(ctx context.Context) {
	now := time.Now()
	for key, ls := range s.live {
		s.finalize(ctx, ls, now)
		delete(s.live, key)
	}
}

func streamIP(p plex.Session) string {
	if p.PublicIP != "" {
		return p.PublicIP
	}
	return p.Address
}

func isLocalIP(s *Service, sess plex.Session) bool {
	return s.geo.Lookup(streamIP(sess)).Local
}
