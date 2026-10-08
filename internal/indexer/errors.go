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
type AllFailedError struct {
	Errors map[string]string // indexer name -> its (already sanitized) error text
}

func (e *AllFailedError) Error() string {
	names := make([]string, 0, len(e.Errors))
	for n := range e.Errors {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, n+": "+e.Errors[n])
	}
	label := "indexers"
	if len(names) == 1 {
		label = "indexer"
	}
	return fmt.Sprintf("all %d %s failed: %s", len(names), label, strings.Join(parts, "; "))
}

// IsOutage reports whether err means no indexer could answer at all — every one
// errored, or none serves the search. Callers use it to tell "the search couldn't run"
// apart from "the search ran and found nothing".
func IsOutage(err error) bool {
	var af *AllFailedError
	return errors.As(err, &af) || errors.Is(err, ErrNoIndexers)
}

// outcome turns a finished fan-out into the error the caller sees: nil when at least
// one eligible indexer answered (even with nothing), ErrNoIndexers when none was
// eligible, and AllFailedError when every eligible one errored. failed is counted
// separately from errs because two indexers sharing a name share one map entry.
func outcome(eligible, failed int, errs map[string]string) error {
	if eligible == 0 {
		return ErrNoIndexers
	}
	if failed >= eligible {
		return &AllFailedError{Errors: copyErrors(errs)}
	}
	return nil
}
