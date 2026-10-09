package httpapi

import (
	"net"
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/convert"
	"github.com/tristenlammi/arrmada/internal/subtitles"
)

// healthBusy is the long-running work a restart would throw away. update.sh reads it
// from /api/health inside the container and warns before restarting in the middle of a
// day-long encode. Counts and times only: never what is being converted.
type healthBusy struct {
	ConvertRunning    int   `json:"convert_running"`
	ConvertRunningSec int64 `json:"convert_running_sec"` // how long the longest-running conversion has run
	ConvertProgress   int   `json:"convert_progress"`    // that conversion's percent done, 0-100
	SubtitlesRunning  int   `json:"subtitles_running"`
}

func (a *api) healthBusy() healthBusy {
	var cj []convert.Job
	if a.deps.Convert != nil {
		cj = a.deps.Convert.Jobs()
	}
	var sj []subtitles.Job
	if a.deps.Subtitles != nil {
		sj = a.deps.Subtitles.Jobs()
	}
	return summarizeBusy(cj, sj, time.Now())
}

func summarizeBusy(cj []convert.Job, sj []subtitles.Job, now time.Time) healthBusy {
	var b healthBusy
	for _, j := range cj {
		if !j.State.Active() {
			continue
		}
		b.ConvertRunning++
		sec := now.Unix() - j.StartedAt
		if j.StartedAt == 0 || sec < 0 {
			sec = 0
		}
		if b.ConvertRunning == 1 || sec > b.ConvertRunningSec {
			b.ConvertRunningSec = sec
			b.ConvertProgress = int(j.Progress*100 + 0.5)
		}
	}
	for _, j := range sj {
		if j.State == subtitles.StateRunning {
			b.SubtitlesRunning++
		}
	}
	return b
}

// isLocalCaller reports whether a request came from inside the container itself (the
// healthcheck, update.sh's docker exec): a loopback peer with no sign of a proxy in
// front of it. Anything forwarded, tunnelled or from the LAN is not.
func (a *api) isLocalCaller(r *http.Request) bool {
	if h := a.deps.Config.ExternalHeader; h != "" && r.Header.Get(h) != "" {
		return false
	}
	for _, h := range []string{"Cf-Connecting-Ip", "X-Forwarded-For", "Forwarded", "X-Real-Ip"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
