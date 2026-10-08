package automation

import (
	"log/slog"

	"github.com/tristenlammi/arrmada/internal/indexer"
)

// sweepOutcome decides what a missing-sweep records for one title's search: clear its
// backoff (something was grabbed), count a miss (a search ran and found nothing usable),
// or neither.
//
// An error records nothing. That matters most for an indexer outage: it used to come back
// as an empty result and count as a miss, so a few hours of Prowlarr being down pushed
// every wanted title into the 12h backoff. A title the sweep didn't search (nothing
// wanted) isn't a miss either.
func sweepOutcome(err error, searched bool, grabbed int) (resetMisses, recordMiss bool) {
	switch {
	case err != nil, !searched:
		return false, false
	case grabbed > 0:
		return true, false
	default:
		return false, true
	}
}

// outageTally collects indexer-outage failures across one sweep, so an outage logs one
// warning per sweep instead of one line per title.
type outageTally struct {
	titles int
	first  error
}

// note records err if it's an indexer outage and reports whether it was one; the caller
// skips its own per-title log line when it was.
func (o *outageTally) note(err error) bool {
	if !indexer.IsOutage(err) {
		return false
	}
	if o.titles == 0 {
		o.first = err
	}
	o.titles++
	return true
}

// report logs the sweep's outage once, if it had one.
func (o *outageTally) report(log *slog.Logger, sweep string) {
	if o.titles == 0 {
		return
	}
	log.Warn(sweep+": every indexer failed; not counting misses", "titles", o.titles, "err", o.first)
}
