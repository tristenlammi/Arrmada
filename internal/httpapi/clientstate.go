package httpapi

import (
	"context"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
)

// clientsState is the Downloads page's "clients" block: whether there's a download client
// at all, and whether the last queue read reached every one that's switched on. A dead
// qBittorrent used to look exactly like an empty queue — a green "Live" dot, "Nothing
// downloading", and every wanted title "Searching".
type clientsState struct {
	Configured int  `json:"configured"` // clients set up, switched on or not
	Enabled    int  `json:"enabled"`    // clients that can take a download
	OK         bool `json:"ok"`         // false: the queue read failed or missed a client
	// The client that isn't answering, what it said, and since when (the first failure
	// of this outage). Set only when OK is false.
	Name  string     `json:"name,omitempty"`
	Error string     `json:"error,omitempty"`
	Since *time.Time `json:"since,omitempty"`
}

// queueHealth is the requester-safe form: only whether downloads can be checked right now.
// Which client is down and why is admin detail.
type queueHealth struct {
	OK bool `json:"ok"`
}

// queueSnapshot reads the shared download snapshot. known is false when what it says
// can't be trusted to be the whole queue: the read failed, or a client didn't answer.
func (a *api) queueSnapshot(ctx context.Context) (snap download.Snapshot, known bool, err error) {
	if a.deps.Downloads == nil {
		return download.Snapshot{Complete: true}, true, nil
	}
	snap, err = a.deps.Downloads.Snapshot(ctx)
	return snap, err == nil && snap.Complete, err
}

// clientsStateOf builds the clients block from the stored clients and a queue read.
func clientsStateOf(clients []download.Client, snap download.Snapshot, err error) clientsState {
	st := clientsState{Configured: len(clients), OK: err == nil && snap.Complete}
	for _, c := range clients {
		if c.Enabled {
			st.Enabled++
		}
	}
	if st.OK {
		return st
	}
	// Name the enabled client that's been down longest; a switched-off one isn't expected
	// to answer.
	for _, h := range snap.Health {
		if !h.Enabled || h.Reachable {
			continue
		}
		if st.Since == nil || (!h.Since.IsZero() && h.Since.Before(*st.Since)) {
			since := h.Since
			st.Name, st.Error, st.Since = h.Name, h.LastErr, &since
		}
	}
	if st.Error == "" {
		if err != nil {
			st.Error = err.Error()
		} else {
			st.Error = "a download client didn't answer"
		}
	}
	return st
}
