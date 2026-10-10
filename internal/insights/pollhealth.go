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
	Status       Status
}

// Status is what the Insights header badge says about monitoring. It is worked out from the
// same poll record the health panel reads, so the two never disagree.
type Status string

const (
	StatusUnconfigured Status = "unconfigured" // no server URL or token yet
	StatusOff          Status = "off"          // connected, but monitoring is switched off: nothing new is recorded
	StatusRecording    Status = "recording"    // the poller is talking to Plex
	StatusUnreachable  Status = "unreachable"  // monitoring is on but Plex has stopped answering
)

// unreachableAfter is how many failed polls in a row turn the badge to "Plex unreachable".
// One miss is a hiccup (a Plex restart, a slow answer); three is a real outage.
const unreachableAfter = 3

// statusOf reduces the poll record to the badge's four states. A refused token can't fix
// itself, so it counts as unreachable straight away.
func statusOf(h PlexHealth) Status {
	switch {
	case !h.Configured:
		return StatusUnconfigured
	case !h.Monitoring:
		return StatusOff
	case h.Unauthorized || h.Consecutive >= unreachableAfter:
		return StatusUnreachable
	default:
		return StatusRecording
	}
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

// PollHealth reports how the Plex connection has been doing, without calling Plex. It is the
// one source for the health panel, the Insights badge and Convert's pause hint.
func (s *Service) PollHealth(ctx context.Context) PlexHealth {
	h := s.health.get()
	h.Configured = s.configured(ctx)
	h.Monitoring = h.Configured && s.settings.GetBool(ctx, keyEnabled, false)
	h.Status = statusOf(h)
	return h
}

// MonitoringActive reports whether the poller is meant to be recording: monitoring switched
// on with a server to talk to. Convert's "pause while someone is watching" only works then.
func (s *Service) MonitoringActive(ctx context.Context) bool {
	return s.PollHealth(ctx).Monitoring
}

// Status is the monitoring state the Insights header shows.
func (s *Service) Status(ctx context.Context) Status {
	return s.PollHealth(ctx).Status
}

// ProbeIdentity asks Plex who it is and records the answer like a poll. The health check
// uses it when monitoring is off, so a revoked token still shows up — Plex sign-in and
// recommendations depend on the connection too.
func (s *Service) ProbeIdentity(ctx context.Context) error {
	_, err := s.client(ctx).Identity(ctx)
	s.observePoll(err)
	return err
}
