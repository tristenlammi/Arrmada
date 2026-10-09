package automation

import (
	"errors"
	"log/slog"
	"sync"

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

// outageStopAfter is how many searches in a row may hit an indexer outage before a sweep
// gives up for this cycle. One is tolerated so a single query that times out on its own
// doesn't stall every title queued behind it; two in a row means the indexers are down.
const outageStopAfter = 2

// noIndexerSaid remembers which sweeps have already said they have no indexer to ask, so
// a fresh install (or Books turned on with only TV/movie indexers) says it once per run
// rather than every five minutes forever.
var noIndexerSaid sync.Map

// outageTally collects indexer-outage failures across one sweep and decides when the
// sweep should stop. Skipping the misses alone wasn't enough: with nothing recorded and
// no backoff, every sweep went on to search every wanted title against the dead
// indexers — an endless run of failed logins for a revoked key or bad tracker
// credentials, which can get the account banned. Stopping early costs nothing: no miss
// is recorded, so the skipped titles are simply searched on the next sweep.
type outageTally struct {
	titles     int  // searches that hit an outage this sweep
	streak     int  // of those, how many in a row just now
	noIndexers bool // nothing serves this media type at all
	paused     bool // every indexer is backing off after repeated failures
	first      error
}

// note records one title's search error (nil included, which ends a streak) and reports
// whether it was an outage; the caller then skips that title's own log line and outcome.
func (o *outageTally) note(err error) bool {
	if !indexer.IsOutage(err) {
		o.streak = 0
		return false
	}
	if o.titles == 0 {
		o.first = err
	}
	o.titles++
	o.streak++
	if errors.Is(err, indexer.ErrNoIndexers) {
		o.noIndexers = true
	}
	if indexer.IsPaused(err) {
		o.paused = true
	}
	return true
}

// stop reports whether the sweep should end now. "No indexer serves this" is true of
// every title of the media type, so that stops at once; so is "every indexer is paused",
// which holds until the first pause runs out.
func (o *outageTally) stop() bool {
	return o.noIndexers || o.paused || o.streak >= outageStopAfter
}

// report logs the sweep's outage once, if it had one.
func (o *outageTally) report(log *slog.Logger, sweep string) {
	switch {
	case o.titles == 0:
		return
	case o.noIndexers:
		// Not a fault — just nothing set up for this kind of search yet. Calling it
		// "every indexer failed" sent people hunting for a broken indexer.
		if _, said := noIndexerSaid.LoadOrStore(sweep, true); said {
			log.Debug(sweep + ": no enabled indexer serves this media type; skipping")
			return
		}
		log.Info(sweep + ": no enabled indexer serves this media type; skipping")
	case o.paused:
		// Each indexer said so when it began backing off; this is the sweep visibly
		// standing down rather than going quiet.
		log.Info(sweep+": every indexer is paused after repeated failures; skipping this run", "err", o.first)
	default:
		log.Warn(sweep+": every indexer failed; not counting misses", "titles", o.titles, "stopped", o.stop(), "err", o.first)
	}
}
