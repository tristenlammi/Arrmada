package indexer

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrNoIndexers means no enabled indexer serves the media type searched for, so nothing
// was asked at all. Returned as an error, not an empty result, so the sweeps don't read
// "nobody was asked" as "nothing exists" and back the title off.
var ErrNoIndexers = errors.New("no enabled indexer serves this kind of search")

// AllFailedError means every indexer that should have answered a search errored. The
// sweeps used to see this as an empty result and count a search miss, so an outage of a
// few hours pushed titles into a 12h backoff — and a wanted book could drop out of
// automatic search for good. A caller that only needs releases can still read the
// partial SearchResult returned alongside it (its Errors map is filled in).
//
// A background search also lands here when the indexers it didn't ask were skipped for
// backing off after repeated failures: nobody answered either way, so it is an outage
// too, not "nothing found". Skipped holds those, with why.
type AllFailedError struct {
	Errors  map[string]string // indexer name -> its (already sanitized) error text
	Skipped map[string]string // indexer name -> why it is paused (see SearchResult.Skipped)
}

func (e *AllFailedError) Error() string {
	if e.AllPaused() {
		label := "indexers are"
		if len(e.Skipped) == 1 {
			label = "indexer is"
		}
		return fmt.Sprintf("every indexer is paused after repeated failures (%d %s backing off): %s",
			len(e.Skipped), label, joinReasons(e.Skipped))
	}
	label := "indexers"
	if len(e.Errors) == 1 {
		label = "indexer"
	}
	msg := fmt.Sprintf("all %d %s failed: %s", len(e.Errors), label, joinReasons(e.Errors))
	if len(e.Skipped) > 0 {
		msg += "; paused: " + joinReasons(e.Skipped)
	}
	return msg
}

// AllPaused reports whether no indexer was asked at all because every one is backing
// off — as opposed to some or all of them failing just now.
func (e *AllFailedError) AllPaused() bool { return len(e.Errors) == 0 && len(e.Skipped) > 0 }

// joinReasons lists "name: reason" pairs in name order.
func joinReasons(m map[string]string) string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, n+": "+m[n])
	}
	return strings.Join(parts, "; ")
}

// IsOutage reports whether err means no indexer could answer at all — every one
// errored or is paused, or none serves the search. Callers use it to tell "the search
// couldn't run" apart from "the search ran and found nothing".
func IsOutage(err error) bool {
	var af *AllFailedError
	return errors.As(err, &af) || errors.Is(err, ErrNoIndexers)
}

// IsPaused reports whether err is a search that asked nobody because every indexer it
// would have asked is backing off after repeated failures.
func IsPaused(err error) bool {
	var af *AllFailedError
	return errors.As(err, &af) && af.AllPaused()
}

// outcome turns a finished fan-out into the error the caller sees: nil when at least
// one eligible indexer answered (even with nothing), ErrNoIndexers when none was
// eligible, and AllFailedError when every eligible one errored or was skipped for
// backing off. failed and skipped are counted separately from the maps because two
// indexers sharing a name share one map entry.
func outcome(eligible, failed, skipped int, errs, skips map[string]string) error {
	if eligible == 0 {
		return ErrNoIndexers
	}
	if failed+skipped >= eligible {
		e := &AllFailedError{Errors: copyErrors(errs)}
		if len(skips) > 0 {
			e.Skipped = copyErrors(skips)
		}
		return e
	}
	return nil
}
