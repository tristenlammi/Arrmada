package httpapi

import (
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/attention"
)

// attentionItemsMax is how many items one answer carries. The counts and groups cover
// everything; the list is for the Dashboard card, worst first.
const attentionItemsMax = 50

// attentionPayload is GET /api/v1/attention.
type attentionPayload struct {
	At     *time.Time        `json:"at"`    // when the snapshot was taken; null before the first
	Stale  bool              `json:"stale"` // older than attention.StaleAfter, or none yet
	Counts attention.Counts  `json:"counts"`
	Groups []attention.Group `json:"groups"`
	Items  []attention.Item  `json:"items"`
}

// handleAttention is GET /api/v1/attention: the Needs-you snapshot for the sidebar badges
// and the Dashboard card. It serves what the last refresh found and makes no live call,
// so every open tab can poll it every 30 seconds for free.
func (a *api) handleAttention(w http.ResponseWriter, r *http.Request) {
	out := attentionPayload{Stale: true, Groups: []attention.Group{}, Items: []attention.Item{}}
	if snap := a.deps.Attention.Current(); snap != nil {
		at := snap.At
		out.At = &at
		out.Stale = time.Since(snap.At) > attention.StaleAfter
		out.Counts = snap.Counts
		out.Groups = append(out.Groups, snap.Groups...)
		items := snap.Items
		if len(items) > attentionItemsMax {
			items = items[:attentionItemsMax]
		}
		out.Items = append(out.Items, items...)
	}
	a.writeJSON(w, http.StatusOK, out)
}

// kickAttention asks the Needs-you feed to look again soon, after a change it reports on
// (a request made or decided, a review resolved). Nil-safe.
func (a *api) kickAttention() { a.deps.Attention.Kick() }
