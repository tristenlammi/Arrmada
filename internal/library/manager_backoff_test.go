package library

import (
	"errors"
	"testing"
	"time"
)

// A failing import is retried on a growing schedule and warned about only when the
// news changes; success forgets the history.
func TestImportFailureBackoff(t *testing.T) {
	m := &Manager{}
	if !m.retryDue("h") {
		t.Fatal("a never-failed hash must be due")
	}
	perm := errors.New("create library dir: permission denied")
	attempts, warn := m.noteFailure("h", perm)
	if attempts != 1 || !warn {
		t.Errorf("first failure: attempts=%d warn=%v, want 1 and a warning", attempts, warn)
	}
	if m.retryDue("h") {
		t.Error("just failed: must not be due again immediately")
	}
	// Force the clock past the first wait and fail again with the same error: no warning.
	m.failures["h"].next = time.Now().Add(-time.Second)
	if !m.retryDue("h") {
		t.Error("past its retry time it must be due")
	}
	if attempts, warn := m.noteFailure("h", perm); attempts != 2 || warn {
		t.Errorf("same error again: attempts=%d warn=%v, want 2 and no warning", attempts, warn)
	}
	// A different error is news.
	if _, warn := m.noteFailure("h", errors.New("no space left on device")); !warn {
		t.Error("a changed error must be warned about")
	}
	// The wait grows and is capped.
	for i := 0; i < 10; i++ {
		m.noteFailure("h", perm)
	}
	if wait := time.Until(m.failures["h"].next); wait > importRetryMax+time.Second || wait < importRetryMax-time.Minute {
		t.Errorf("wait after many failures = %v, want about the %v cap", wait, importRetryMax)
	}
	m.clearFailure("h")
	if !m.retryDue("h") {
		t.Error("cleared: must be due")
	}
}
