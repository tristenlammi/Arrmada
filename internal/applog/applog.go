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

// Filter selects which captured entries a Snapshot returns.
type Filter struct {
	Limit int        // newest N after filtering; 0 = no cap
	Min   slog.Level // entries below this level are dropped
	Query string     // keep only entries whose message/attrs contain this
	// Hide drops entries whose message/attrs contain any of these comma-separated
	// terms. The routine chatter — per-page indexer tracing especially — can outnumber
	// everything else by an order of magnitude, and a keep-only filter can't help when
	// you don't yet know what you're looking for. Applied after Query, so a term can
	// narrow first and then subtract the noise from what's left.
	Hide string
}

// Snapshot returns the most-recent matching entries, oldest first.
func (r *Ring) Snapshot(f Filter) []Entry {
	r.mu.RLock()
	// Reconstruct chronological order.
	var ordered []Entry
	if r.full {
		ordered = append(ordered, r.buf[r.next:]...)
	}
	ordered = append(ordered, r.buf[:r.next]...)
	r.mu.RUnlock()

	q := strings.ToLower(strings.TrimSpace(f.Query))
	hide := hideTerms(f.Hide)
	out := make([]Entry, 0, len(ordered))
	for _, e := range ordered {
		if levelValue(e.Level) < f.Min {
			continue
		}
		if q != "" && !matches(e, q) {
			continue
		}
		if hidden(e, hide) {
			continue
		}
		out = append(out, e)
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[len(out)-f.Limit:]
	}
	return out
}

func matches(e Entry, lowerTerm string) bool {
	return strings.Contains(strings.ToLower(e.Message), lowerTerm) ||
		strings.Contains(strings.ToLower(e.Attrs), lowerTerm)
}

// hideTerms splits a comma-separated exclude list into lowercase terms, dropping blanks
// so a trailing comma doesn't turn into a term that matches everything.
func hideTerms(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if t := strings.ToLower(strings.TrimSpace(part)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func hidden(e Entry, terms []string) bool {
	for _, t := range terms {
		if matches(e, t) {
			return true
		}
	}
	return false
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
