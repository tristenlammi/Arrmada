// Package applog captures Arrmada's structured logs into an in-memory ring buffer so
// they can be viewed inside the app (the Logs page), in addition to stdout/docker logs.
package applog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// Entry is one captured log record, flattened for the API.
type Entry struct {
	TimeMS  int64  `json:"time_ms"`
	Level   string `json:"level"` // DEBUG | INFO | WARN | ERROR
	Message string `json:"msg"`
	Attrs   string `json:"attrs,omitempty"` // "key=value key2=value2"
}

// Ring is a fixed-capacity, concurrency-safe buffer of the most recent log entries.
type Ring struct {
	mu   sync.RWMutex
	buf  []Entry
	next int
	full bool
	max  int
}

// NewRing builds a ring holding up to max entries (min 100).
func NewRing(max int) *Ring {
	if max < 100 {
		max = 100
	}
	return &Ring{buf: make([]Entry, max), max: max}
}

func (r *Ring) add(e Entry) {
	r.mu.Lock()
	r.buf[r.next] = e
	r.next = (r.next + 1) % r.max
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

// Snapshot returns the most-recent entries (oldest first) at or above minLevel, whose
// message/attrs contain query (case-insensitive). limit caps the newest N returned.
func (r *Ring) Snapshot(limit int, minLevel slog.Level, query string) []Entry {
	r.mu.RLock()
	// Reconstruct chronological order.
	var ordered []Entry
	if r.full {
		ordered = append(ordered, r.buf[r.next:]...)
	}
	ordered = append(ordered, r.buf[:r.next]...)
	r.mu.RUnlock()

	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]Entry, 0, len(ordered))
	for _, e := range ordered {
		if levelValue(e.Level) < minLevel {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Message), q) && !strings.Contains(strings.ToLower(e.Attrs), q) {
			continue
		}
		out = append(out, e)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func levelValue(s string) slog.Level {
	switch s {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Handler is an slog.Handler that tees each record into the ring while delegating to a
// base handler (stdout). Install it via NewHandler around a text/JSON handler.
type Handler struct {
	base  slog.Handler
	ring  *Ring
	attrs []slog.Attr
	group string
}

// NewHandler wraps base so every emitted record is also appended to ring.
func NewHandler(base slog.Handler, ring *Ring) *Handler {
	return &Handler{base: base, ring: ring}
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.base.Enabled(ctx, l) }

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	var sb strings.Builder
	for _, a := range h.attrs {
		appendAttr(&sb, h.group, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(&sb, h.group, a)
		return true
	})
	h.ring.add(Entry{TimeMS: r.Time.UnixMilli(), Level: r.Level.String(), Message: r.Message, Attrs: strings.TrimSpace(sb.String())})
	return h.base.Handle(ctx, r)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cp := *h
	cp.base = h.base.WithAttrs(attrs)
	cp.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &cp
}

func (h *Handler) WithGroup(name string) slog.Handler {
	cp := *h
	cp.base = h.base.WithGroup(name)
	if name != "" {
		if cp.group == "" {
			cp.group = name
		} else {
			cp.group = cp.group + "." + name
		}
	}
	return &cp
}

func appendAttr(sb *strings.Builder, group string, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	key := a.Key
	if group != "" {
		key = group + "." + key
	}
	fmt.Fprintf(sb, "%s=%v ", key, a.Value.Any())
}
