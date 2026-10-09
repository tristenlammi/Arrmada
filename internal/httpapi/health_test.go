package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/convert"
	"github.com/tristenlammi/arrmada/internal/subtitles"
)

// The busy block is for update.sh inside the container only. Anything from the LAN, the
// Docker bridge, or carrying a proxy's headers gets today's payload exactly.
func TestHealthBusyOnlyForLoopback(t *testing.T) {
	s := newRouteServer(t, nil)
	s.deps.Config.ExternalHeader = "Cf-Connecting-Ip"
	h := New(s.deps).Handler

	cases := []struct {
		name    string
		remote  string
		headers map[string]string
		busy    bool
	}{
		{"ipv4 loopback", "127.0.0.1:1234", nil, true},
		{"ipv6 loopback", "[::1]:1234", nil, true},
		{"docker bridge", "172.17.0.1:1234", nil, false},
		{"lan", "192.168.1.20:5000", nil, false},
		{"loopback through the tunnel", "127.0.0.1:1234", map[string]string{"Cf-Connecting-Ip": "203.0.113.9"}, false},
		{"loopback behind a proxy", "127.0.0.1:1234", map[string]string{"X-Forwarded-For": "192.168.1.5"}, false},
		{"loopback with Forwarded", "[::1]:1234", map[string]string{"Forwarded": "for=192.168.1.5"}, false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "http://arrmada.local/api/health", nil)
		r.RemoteAddr = c.remote
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d", c.name, rec.Code)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if _, ok := body["busy"]; ok != c.busy {
			t.Errorf("%s: busy present = %v, want %v (%s)", c.name, ok, c.busy, rec.Body)
		}
		if _, ok := body["commit"]; !ok {
			t.Errorf("%s: commit missing", c.name)
		}
	}
}

func TestSummarizeBusy(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	b := summarizeBusy([]convert.Job{
		{State: convert.StateEncoding, StartedAt: now.Unix() - 3*3600, Progress: 0.62, Title: "secret"},
		{State: convert.StateTesting, StartedAt: now.Unix() - 600, Progress: 0.9},
		{State: convert.StateDone, StartedAt: now.Unix() - 99*3600},
	}, []subtitles.Job{
		{State: subtitles.StateRunning}, {State: subtitles.StateQueued}, {State: subtitles.StateDone},
	}, now)
	want := healthBusy{ConvertRunning: 2, ConvertRunningSec: 3 * 3600, ConvertProgress: 62, SubtitlesRunning: 1}
	if b != want {
		t.Fatalf("busy = %+v, want %+v", b, want)
	}
	if (summarizeBusy(nil, nil, now) != healthBusy{}) {
		t.Fatal("idle isn't all zero")
	}
}
