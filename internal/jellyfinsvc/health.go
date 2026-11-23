package jellyfinsvc

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/jellyfin"
)

// Health is how the Jellyfin connection has been answering: the one record the health
// panel and the Connections card read, so they never disagree. Monitoring means a poller
// talks to Jellyfin every few seconds; otherwise the numbers come from Probe.
type Health struct {
	Configured   bool
	Monitoring   bool
	LastOKAt     time.Time
	LastErrAt    time.Time
	FailingSince time.Time // first failure of the current run; zero when OK
	LastErr      string
	Unauthorized bool // the last failure was Jellyfin refusing the key
	Consecutive  int  // failures in a row
}

type pollHealth struct {
	mu sync.Mutex
	h  Health
	// describe words an error for the health panel (Describe when nil); the service sets
	// it so an Emby connection's errors name Emby.
	describe func(error) string
}

// record notes one exchange and reports whether it changed between working and failing.
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
	if p.describe != nil {
		p.h.LastErr = p.describe(err)
	} else {
		p.h.LastErr = Describe(err)
	}
	p.h.Unauthorized = errors.Is(err, jellyfin.ErrUnauthorized)
	return startedFailing, false
}

func (p *pollHealth) get() Health {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.h
}

// reset forgets the record: a new server or key starts with a clean slate.
func (p *pollHealth) reset() {
	p.mu.Lock()
	p.h = Health{}
	p.mu.Unlock()
}

// Health reports how the connection has been doing, without calling Jellyfin.
func (s *Service) Health(ctx context.Context) Health {
	h := s.health.get()
	h.Configured = s.Configured(ctx)
	s.pollingMu.RLock()
	polling := s.polling
	s.pollingMu.RUnlock()
	h.Monitoring = h.Configured && polling != nil && polling(ctx)
	return h
}
