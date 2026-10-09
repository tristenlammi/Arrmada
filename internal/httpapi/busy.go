package httpapi

import (
	"net/http"

	"github.com/tristenlammi/arrmada/internal/subtitles"
)

// busy is the work a restart would interrupt, so the restart confirm can say what it
// costs ("a conversion has been running 3h 12m (41%) and will start over"). Counts and
// times only: no titles, so it's safe on any staff page.
type busy struct {
	ConvertRunning    int     `json:"convert_running"`
	ConvertLongestSec int64   `json:"convert_longest_sec"`
	ConvertProgress   float64 `json:"convert_progress"` // 0..1, of the longest-running one
	SubtitlesRunning  int     `json:"subtitles_running"`
	SubtitlesQueued   int     `json:"subtitles_queued"`
}

// busySummary reads the Convert and Subtitles queues; either may be absent.
func (a *api) busySummary() busy {
	var b busy
	if a.deps.Convert != nil {
		b.ConvertRunning, b.ConvertLongestSec, b.ConvertProgress = a.deps.Convert.ActiveSummary()
	}
	if a.deps.Subtitles != nil {
		// The same list the Subtitles page shows.
		for _, j := range a.deps.Subtitles.Jobs() {
			switch j.State {
			case subtitles.StateRunning:
				b.SubtitlesRunning++
			case subtitles.StateQueued:
				b.SubtitlesQueued++
			}
		}
	}
	return b
}

// handlePendingRestart — GET /api/v1/system/pending-restart. Folders saved but not in use
// until a restart, whether the app can restart itself, and what a restart would
// interrupt. A separate endpoint, so /system/library's flat folder map (which the
// Settings form diffs field by field) stays as it is.
func (a *api) handlePendingRestart(w http.ResponseWriter, r *http.Request) {
	st := a.folderRestartState(r.Context())
	a.writeJSON(w, http.StatusOK, map[string]any{
		"restart_needed": st.Needed,
		"can_restart":    st.CanRestart,
		"changed":        st.Changed,
		"busy":           a.busySummary(),
	})
}
