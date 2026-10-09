package automation

import (
	"context"
	"errors"

	"github.com/tristenlammi/arrmada/internal/download"
)

// clientDownSkip is the line a search sweep logs when it sits a cycle out because the
// download client isn't answering.
const clientDownSkip = "download client unreachable — skipping search so nothing is grabbed twice"

// errClientPartial is a queue read that some enabled client didn't answer: what it holds
// is missing from the list, so the list can't say what's already downloading.
var errClientPartial = errors.New("a download client didn't answer")

// sweepQueue reads the shared download snapshot for a search sweep. ok is false when the
// sweep must skip this cycle: the read failed, or an enabled client didn't answer.
//
// Searching while the client is down spends indexer queries on grabs that can't be handed
// over, and the "already downloading" checks can't see what that client holds — so the
// next sweep would grab it a second time. During a qBittorrent or VPN outage the sweeps
// used to carry on regardless (one of them with a nil queue, which switched its in-flight
// check off altogether).
func (c *Coordinator) sweepQueue(ctx context.Context, sweep string) ([]download.Item, bool) {
	items, whole, err := c.downloads.QueueComplete(ctx)
	if err == nil && !whole {
		err = errClientPartial
	}
	if err != nil {
		c.log.Info(clientDownSkip, "sweep", sweep, "err", err)
		return nil, false
	}
	return items, true
}
