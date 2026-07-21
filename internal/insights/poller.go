package insights

import (
	"context"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/plex"
)

// liveSession tracks one in-flight stream across polls so we can record it accurately when it ends.
type liveSession struct {
	sess plex.Session // latest snapshot (transcode decision can change mid-stream)
	// lastSeen is the timestamp of the last poll in which we actually observed this session
	// playing. It bounds how much wall-clock time the session can accrue: on finalize we credit
	// only up to lastSeen, never to "now" — so a Plex outage, a disable, or a slow poll cannot
	// backfill phantom watch time for a session we haven't seen since.
	started   time.Time
	lastSeen  time.Time
	state     string
	pausedMS  int64
	buffering bool // currently inside a buffer spell (debounces events)
	bufCount  int
	bufEvents []bufEvent
}

type bufEvent struct {
	at     time.Time
	offset int64
	cause  string // classified likely cause, captured at the moment the spell began
	detail string
}

// Run is the monitoring loop: while enabled + configured, poll Plex and record activity. Re-reads
// the interval + enabled flag each cycle so settings changes take effect without a restart. When
// Insights is disabled or unconfigured we flush any in-flight sessions immediately (capped at their
// lastSeen) rather than stranding them until a later re-enable would backfill days of phantom time.
func (s *Service) Run(ctx context.Context) {
	wait := time.Duration(s.pollSeconds(ctx)) * time.Second
	for {
		select {
		case <-ctx.Done():
			s.flushAll(context.Background())
			return
		case <-time.After(wait):
		}
		start := time.Now()
		interval := time.Duration(s.pollSeconds(ctx)) * time.Second
		enabled := s.settings.GetBool(ctx, keyEnabled, false)
		configured := s.settings.Get(ctx, keyURL, "") != "" && s.settings.Get(ctx, keyToken, "") != ""
		if enabled && configured {
			s.poll(ctx)
		} else {
			// Disabled or unconfigured: finalize live sessions now (first cycle only; s.live is
			// then empty, so re-enabling starts fresh instead of continuing a stale entry).
			s.flushAll(ctx)
		}
		// Subtract the work we just did from the interval so a slow Plex (up to the 15s timeout)
		// doesn't widen the real sampling period. Never negative; polls stay strictly sequential.
		if wait = interval - time.Since(start); wait < 0 {
			wait = 0
		}
	}
}

func (s *Service) poll(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	sessions, err := s.client(cctx).Sessions(cctx)
	cancel()
	if err != nil {
		// A failed poll (Plex unreachable) leaves live sessions untouched: they keep their prior
		// lastSeen, so when they eventually vanish they're credited only up to the last poll that
		// actually saw them — not through the outage.
		s.log.Debug("insights: session poll failed", "err", err)
		return
	}
	s.reconcile(ctx, sessions, time.Now())
}

// reconcile folds one poll's set of active sessions into the tracked live state: it starts newly
// seen streams, updates ongoing ones, splits a reused SessionKey that now carries different
// content/user, and finalizes any that vanished. Split out from poll so it's testable with a fixed
// clock and canned sessions.
func (s *Service) reconcile(ctx context.Context, sessions []plex.Session, now time.Time) {
	seen := make(map[string]bool, len(sessions))
	var total, lan, wan int64

	for _, sess := range sessions {
		seen[sess.SessionKey] = true
		ls := s.live[sess.SessionKey]
		// A client can keep the same sessionKey across autoplayed episodes, and a Plex restart
		// resets sessionKey numbering so a fresh stream can land on a stale key. If the key now
		// reports a different item or user, the old stream ended: finalize it (capped at its
		// lastSeen) and start a new one so durations and user attribution stay correct.
		if ls != nil && (ls.sess.RatingKey != sess.RatingKey || ls.sess.UserID != sess.UserID) {
			s.finalize(ctx, ls)
			delete(s.live, sess.SessionKey)
			ls = nil
		}
		if ls == nil {
			ls = &liveSession{started: now, lastSeen: now, state: sess.State, sess: sess}
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
			s.finalize(ctx, ls)
			delete(s.live, key)
		}
	}

	_ = s.repo.insertBandwidth(ctx, now.Unix(), total, lan, wan)
}

// observe folds one poll's session snapshot into the tracked state. Returns true when a new
// buffering spell just began (so the caller can emit an event).
func (ls *liveSession) observe(sess plex.Session, now time.Time) bool {
	if ls.state == "paused" { // accrue paused time across the interval just elapsed
		ls.pausedMS += now.Sub(ls.lastSeen).Milliseconds()
	}
	newSpell := false
	if sess.State == "buffering" {
		if !ls.buffering { // new spell
			ls.buffering = true
			ls.bufCount++
			cause, detail := sess.BufferCause() // diagnose from this snapshot's transcode/network signals
			ls.bufEvents = append(ls.bufEvents, bufEvent{at: now, offset: sess.OffsetMS, cause: cause, detail: detail})
			newSpell = true
		}
	} else {
		ls.buffering = false
	}
	ls.state = sess.State
	ls.lastSeen = now
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

// finalize writes a completed session + its buffer events. The session is credited only up to
// lastSeen — the last poll that actually observed it playing — so wall-clock time during an outage,
// a disable, or between the last sighting and "now" is never counted. Blips shorter than a poll are
// dropped.
func (s *Service) finalize(ctx context.Context, ls *liveSession) {
	if ls.lastSeen.Sub(ls.started) < 2*time.Second {
		return
	}
	rec := ls.record(ls.lastSeen)
	id, err := s.repo.insertSession(ctx, rec)
	if err != nil {
		s.log.Warn("insights: could not record session", "title", rec.Title, "err", err)
		return
	}
	for _, be := range ls.bufEvents {
		_ = s.repo.insertBufferEvent(ctx, id, be.at.Unix(), be.offset, be.cause, be.detail)
	}
	s.log.Debug("insights: recorded session", "title", rec.Title, "user", rec.UserName, "buffers", ls.bufCount)
}

// flushAll finalizes every in-flight session (called on shutdown, and when Insights is disabled or
// unconfigured). Each is capped at its lastSeen, so flushing a week after a stream was last observed
// records only the watched portion, not the gap.
func (s *Service) flushAll(ctx context.Context) {
	for key, ls := range s.live {
		s.finalize(ctx, ls)
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
