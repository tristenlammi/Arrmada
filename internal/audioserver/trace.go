package audioserver

import (
	"context"
	"strconv"
	"time"
)

// Tracing: for a while, log every request an app makes, including the steady playing
// traffic (audio, covers, place syncs) that's normally only logged when it fails. It's
// how a new app that fails quietly mid-playback gets debugged without shipping a code
// change. Traced lines are the same redacted lines as always — the route pattern and the
// query parameter names, never an id, a search term, a username or a token — so the
// rule holds while tracing: admins see how much and when, never what. It switches itself
// off when the time runs out.

// KeyTraceUntil holds when tracing ends (unix ms; 0 or past means off).
const KeyTraceUntil = "audioserver_trace_until"

// MaxTrace is the longest an admin can switch tracing on for.
const MaxTrace = 24 * time.Hour

// loadTrace reads the saved end time once, at start-up; after that SetTrace keeps the
// in-memory copy current, so logging a request never reads the database.
func (s *Server) loadTrace(ctx context.Context) {
	if s.settings == nil {
		return
	}
	until, _ := strconv.ParseInt(s.settings.Get(ctx, KeyTraceUntil, "0"), 10, 64)
	s.traceUntil.Store(until)
}

// SetTrace switches tracing on for d (capped at MaxTrace), or off when d <= 0, and
// returns when it ends (0: off).
func (s *Server) SetTrace(ctx context.Context, d time.Duration) (int64, error) {
	var until int64
	if d > 0 {
		until = s.clock().Add(min(d, MaxTrace)).UnixMilli()
	}
	if err := s.settings.Set(ctx, KeyTraceUntil, strconv.FormatInt(until, 10)); err != nil {
		return 0, err
	}
	s.traceUntil.Store(until)
	if until > 0 {
		s.log.Info("audiobook server: tracing app requests", "until", time.UnixMilli(until).Format(time.RFC3339))
	} else {
		s.log.Info("audiobook server: tracing stopped")
	}
	return until, nil
}

// TraceUntil is when tracing ends, or 0 when it's off (including once the time has
// passed).
func (s *Server) TraceUntil() int64 {
	until := s.traceUntil.Load()
	if until <= s.clock().UnixMilli() {
		return 0
	}
	return until
}

func (s *Server) tracing() bool { return s.TraceUntil() > 0 }

func (s *Server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
