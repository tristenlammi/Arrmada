package insights

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/plex"
)

// Poll health counts failures in a row from the first one, notes a refused token, and
// resets on the next good poll; only the edges are reported, so the log gets one line
// per outage instead of one per poll.
func TestPollHealthRecord(t *testing.T) {
	var p pollHealth
	t0 := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)

	if failing, recovered := p.record(nil, t0); failing || recovered {
		t.Fatalf("first good poll reported a change")
	}
	if failing, _ := p.record(errors.New("connection refused"), t0.Add(5*time.Second)); !failing {
		t.Fatal("first failure not reported")
	}
	if failing, _ := p.record(fmt.Errorf("get sessions: %w", plex.ErrUnauthorized), t0.Add(10*time.Second)); failing {
		t.Fatal("second failure reported as a new outage")
	}
	h := p.get()
	if h.Consecutive != 2 || !h.Unauthorized || !h.FailingSince.Equal(t0.Add(5*time.Second)) || !h.LastOKAt.Equal(t0) {
		t.Errorf("while failing: %+v", h)
	}
	if _, recovered := p.record(nil, t0.Add(15*time.Second)); !recovered {
		t.Fatal("recovery not reported")
	}
	if h := p.get(); h.Consecutive != 0 || h.Unauthorized || !h.FailingSince.IsZero() || h.LastErr != "" {
		t.Errorf("after recovery: %+v", h)
	}
}
