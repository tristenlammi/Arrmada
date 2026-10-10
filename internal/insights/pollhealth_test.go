package insights

import (
	"context"
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

// The header badge's four states come from the same poll record the health panel reads:
// nothing saved, saved but switched off, recording, and unreachable once polls have
// failed three times in a row (or Plex refused the token).
func TestConfigStatus(t *testing.T) {
	cases := []struct {
		name              string
		url, token        string
		enabled           bool
		fails             int
		unauthorized      bool
		want              Status
		wantErr, wantLast bool
	}{
		{name: "nothing saved", want: StatusUnconfigured},
		{name: "url without token", url: "http://plex:32400", enabled: true, want: StatusUnconfigured},
		{name: "saved, monitoring off", url: "http://plex:32400", token: "t", want: StatusOff, wantLast: true},
		{name: "off ignores failures", url: "http://plex:32400", token: "t", fails: 5, want: StatusOff, wantErr: true, wantLast: true},
		{name: "recording", url: "http://plex:32400", token: "t", enabled: true, want: StatusRecording, wantLast: true},
		{name: "one miss is a hiccup", url: "http://plex:32400", token: "t", enabled: true, fails: 1, want: StatusRecording, wantErr: true, wantLast: true},
		{name: "two misses still recording", url: "http://plex:32400", token: "t", enabled: true, fails: 2, want: StatusRecording, wantErr: true, wantLast: true},
		{name: "three misses is unreachable", url: "http://plex:32400", token: "t", enabled: true, fails: 3, want: StatusUnreachable, wantErr: true, wantLast: true},
		{name: "refused token at once", url: "http://plex:32400", token: "t", enabled: true, fails: 1, unauthorized: true, want: StatusUnreachable, wantErr: true, wantLast: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestService(t)
			ctx := context.Background()
			if c.url != "" || c.token != "" {
				tok := c.token
				if err := s.SetConfig(ctx, c.url, &tok, &c.enabled, nil); err != nil {
					t.Fatal(err)
				}
			}
			t0 := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
			s.health.record(nil, t0) // one good answer, then the failures
			for i := 0; i < c.fails; i++ {
				err := errors.New("connection refused")
				if c.unauthorized {
					err = fmt.Errorf("get sessions: %w", plex.ErrUnauthorized)
				}
				s.health.record(err, t0.Add(time.Duration(i+1)*5*time.Second))
			}
			cfg := s.Config(ctx)
			if cfg.Status != c.want || s.Status(ctx) != c.want {
				t.Errorf("status = %q (Status() %q), want %q", cfg.Status, s.Status(ctx), c.want)
			}
			if (cfg.LastError != "") != c.wantErr {
				t.Errorf("last_error = %q, want set=%v", cfg.LastError, c.wantErr)
			}
			if c.wantLast && cfg.LastPollAt != t0.Unix() {
				t.Errorf("last_poll_at = %d, want %d", cfg.LastPollAt, t0.Unix())
			}
			if got, want := s.MonitoringActive(ctx), c.want == StatusRecording || c.want == StatusUnreachable; got != want {
				t.Errorf("MonitoringActive = %v, want %v", got, want)
			}
		})
	}
}
