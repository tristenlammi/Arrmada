package scheduler

import (
	"context"
	"time"
)

// A run's result.
const (
	StatusOK       = "ok"
	StatusFailed   = "failed"
	StatusPanicked = "panicked"
)

// state is one task's run history. Guarded by task.mu.
type state struct {
	LastStart           time.Time
	LastEnd             time.Time
	LastDuration        time.Duration
	LastStatus          string // "" until the first run, then ok | failed | panicked
	LastTrigger         string
	LastError           string // the latest run's error; empty once a run succeeds
	LastErrorAt         time.Time
	Runs                uint64
	Failures            uint64 // errors and panics both count
	ConsecutiveFailures uint64
	Skipped             uint64 // ticks dropped because the task was still running
	NextRun             time.Time
	JobID               int64 // the Run now job in progress, 0 for a scheduled run
}

// TaskStatus is one task as the Tasks page sees it. Times are null until they happen.
// This shape is the public contract of GET /api/v1/system/tasks: fields may be added,
// never renamed or removed.
type TaskStatus struct {
	Name                string     `json:"name"`
	Label               string     `json:"label"`
	Description         string     `json:"description"`
	IntervalSeconds     int64      `json:"interval_seconds"`
	Running             bool       `json:"running"`
	LastStart           *time.Time `json:"last_start"`
	LastEnd             *time.Time `json:"last_end"`
	LastDurationMS      int64      `json:"last_duration_ms"`
	LastStatus          string     `json:"last_status"` // "" (never run) | ok | failed | panicked
	LastOK              bool       `json:"last_ok"`
	LastError           string     `json:"last_error"`
	LastErrorAt         *time.Time `json:"last_error_at"`
	Runs                uint64     `json:"runs"`
	Failures            uint64     `json:"failures"`
	ConsecutiveFailures uint64     `json:"consecutive_failures"`
	Skipped             uint64     `json:"skipped"`
	NextRun             *time.Time `json:"next_run"`
	JobID               int64      `json:"job_id,omitempty"` // the running Run now's job
	Hidden              bool       `json:"-"`
}

// Persisted is the part of a task's history kept across restarts.
type Persisted struct {
	Every               time.Duration
	LastStart           time.Time
	LastEnd             time.Time
	LastDuration        time.Duration
	LastStatus          string
	LastError           string
	LastErrorAt         time.Time
	Runs                uint64
	Failures            uint64
	ConsecutiveFailures uint64
}

// Store keeps task history between runs of the app.
type Store interface {
	Load(ctx context.Context) (map[string]Persisted, error)
	Save(ctx context.Context, name string, p Persisted) error
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func (t *task) status() TaskStatus {
	t.mu.Lock()
	st := t.st
	t.mu.Unlock()
	return TaskStatus{
		Name:                t.name,
		Label:               t.label,
		Description:         t.description,
		IntervalSeconds:     int64(t.every / time.Second),
		Running:             t.running.Load(),
		LastStart:           timePtr(st.LastStart),
		LastEnd:             timePtr(st.LastEnd),
		LastDurationMS:      st.LastDuration.Milliseconds(),
		LastStatus:          st.LastStatus,
		LastOK:              st.LastStatus == StatusOK,
		LastError:           st.LastError,
		LastErrorAt:         timePtr(st.LastErrorAt),
		Runs:                st.Runs,
		Failures:            st.Failures,
		ConsecutiveFailures: st.ConsecutiveFailures,
		Skipped:             st.Skipped,
		NextRun:             timePtr(st.NextRun),
		JobID:               st.JobID,
		Hidden:              t.hidden,
	}
}

func (t *task) setNext(at time.Time) {
	t.mu.Lock()
	t.st.NextRun = at
	t.mu.Unlock()
}

// restore applies saved history to a task that hasn't run yet this process.
func (t *task) restore(p Persisted) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.st.Runs > 0 {
		return // already ran here; what it did now is newer than what was saved
	}
	t.st.LastStart, t.st.LastEnd, t.st.LastDuration = p.LastStart, p.LastEnd, p.LastDuration
	t.st.LastStatus, t.st.LastError, t.st.LastErrorAt = p.LastStatus, p.LastError, p.LastErrorAt
	t.st.Runs, t.st.Failures, t.st.ConsecutiveFailures = p.Runs, p.Failures, p.ConsecutiveFailures
	t.saved, t.savedOK = true, p.LastStatus == StatusOK || p.LastStatus == ""
}

// persisted is the task's history to save. Called with t.mu held.
func (t *task) persisted() Persisted {
	return Persisted{
		Every:               t.every,
		LastStart:           t.st.LastStart,
		LastEnd:             t.st.LastEnd,
		LastDuration:        t.st.LastDuration,
		LastStatus:          t.st.LastStatus,
		LastError:           t.st.LastError,
		LastErrorAt:         t.st.LastErrorAt,
		Runs:                t.st.Runs,
		Failures:            t.st.Failures,
		ConsecutiveFailures: t.st.ConsecutiveFailures,
	}
}
