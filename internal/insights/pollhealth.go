package insights

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/plex"
)

// PlexHealth is how the Plex connection has been answering, for the health panel.
// Monitoring means the poller is talking to Plex every few seconds; when it isn't, the
// numbers come from ProbeIdentity instead.
type PlexHealth struct {
	Configured   bool
	Monitoring   bool
	LastOKAt     time.Time
	LastErrAt    time.Time
	FailingSince time.Time // first failure of the current run of failures; zero when OK
	LastErr      string
	Unauthorized bool // the last failure was Plex refusing the token
	Consecutive  int  // failures in a row
}

// pollHealth records each poll's outcome. The poller goroutine writes it, the health check
// reads it.
type pollHealth struct {
	mu sync.Mutex
	h  PlexHealth
}

// record notes one exchange with Plex and reports whether it changed between working and
// failing, so the poller can log the change once instead of every few seconds.
func (p *pollHealth) record(err error, now time.Time) (startedFailing, recovered bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		recovered = p.h.Consecutive > 0
		p.h.LastOKAt = now
		p.h.FailingSince = time.Time{}
		p.h.Consecutive = 0
		p.h.LastErr = ""
		p.h.Unauthorized = false
		return false, recovered
	}
	if p.h.Consecutive == 0 {
		p.h.FailingSince = now
		startedFailing = true
	}
	p.h.Consecutive++
	p.h.LastErrAt = now
	p.h.LastErr = err.Error()
	p.h.Unauthorized = errors.Is(err, plex.ErrUnauthorized)
	return startedFailing, false
}

func (p *pollHealth) get() PlexHealth {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.h
}

// observePoll records a session poll and logs the transitions: a Warn when Plex starts
// failing and an Info when it answers again (each failure on its own stays at Debug).
func (s *Service) observePoll(err error) {
	failing, recovered := s.health.record(err, time.Now())
	switch {
	case failing:
		s.log.Warn("insights: Plex isn't answering", "err", err)
	case recovered:
		s.log.Info("insights: Plex is answering again")
	case err != nil:
		s.log.Debug("insights: session poll failed", "err", err)
	}
}

// PollHealth reports how the Plex connection has been doing, without calling Plex.
func (s *Service) PollHealth(ctx context.Context) PlexHealth {
	h := s.health.get()
	h.Configured = s.settings.Get(ctx, keyURL, "") != "" && s.settings.Get(ctx, keyToken, "") != ""
	h.Monitoring = h.Configured && s.settings.GetBool(ctx, keyEnabled, false)
	return h
}

// ProbeIdentity asks Plex who it is and records the answer like a poll. The health check
// uses it when monitoring is off, so a revoked token still shows up — Plex sign-in and
// recommendations depend on the connection too.
func (s *Service) ProbeIdentity(ctx context.Context) error {
	_, err := s.client(ctx).Identity(ctx)
	s.observePoll(err)
	return err
}
