package requests

import (
	"errors"
	"fmt"
	"sort"

	"github.com/tristenlammi/arrmada/internal/series"
)

// Errors a season-scoped series request can be refused with.
var (
	// ErrAlreadyAvailable: every season asked for is already on disk, and no request
	// covers it to follow instead.
	ErrAlreadyAvailable = errors.New("those seasons are already in the library")
	// ErrNoSuchSeasons: none of the seasons asked for exist (or only specials were).
	ErrNoSuchSeasons = errors.New("none of those seasons exist")
	// ErrSeasonsNotRequested: an approval named seasons the request didn't ask for.
	ErrSeasonsNotRequested = errors.New("you can only approve seasons the request asked for")
)

// maxSeason bounds a season number a request may name. Without a catalogue to check
// against, the list is taken as asked, and nothing real has a thousand seasons.
const maxSeason = 999

// seasonRow is one existing request for a show, as the overlap planner sees it.
type seasonRow struct {
	id          int64
	status      string
	seasons     []int // nil: the whole show
	done        bool  // its "ready" notice has gone out: it covers nothing any more
	requestedBy int64
}

// active is whether a row still stands for its seasons: pending, or approved and not
// yet delivered. A declined row covers nothing, and neither does a delivered one — a
// season that went missing or aired since has to be asked for again.
func (r seasonRow) active() bool {
	return !r.done && (r.status == StatusPending || r.status == StatusApproved)
}

// covers reports whether the row covers season n.
func (r seasonRow) covers(n int) bool {
	if r.seasons == nil {
		return true
	}
	for _, s := range r.seasons {
		if s == n {
			return true
		}
	}
	return false
}

// seasonPlan is what creating a series request should do.
type seasonPlan struct {
	// insert a new row for seasons (nil: the whole show) — or, with reopen set, re-open
	// that declined row instead, which asked for exactly the same.
	insert  bool
	seasons []int
	reopen  int64
	// follow are the rows the caller joins as a subscriber: those covering the seasons
	// asked for that the new row (if any) doesn't.
	follow []int64
	// declinedBy are the requesters of declined rows that asked for some of the new
	// row's seasons: they're added to it as subscribers, so they hear the outcome.
	declinedBy []int64
	// units is how many seasons the new row counts against a request limit: the seasons
	// nobody has covered yet (on a whole-show row too), or one for a whole show with no
	// season list to count.
	units int
}

// planSeasons works out a series request. asked is what the caller wants (nil or empty:
// the whole show); known is every season the catalogue and the library know (nil: no
// catalogue, see below); onDisk are the seasons already complete on disk; rows are the
// show's existing requests.
//
//   - requested is asked minus unknown seasons and specials, or every known season for
//     the whole show.
//   - covered is the union of the active rows' seasons (a whole-show row covers
//     everything) and the seasons on disk; the remainder is requested minus covered.
//   - Nothing left: the caller follows every row covering a season they asked for (or is
//     told it's all here already).
//   - Otherwise a row is inserted for the remainder. It is whole-show only when the show was
//     asked for, nothing was covered and no other whole-show row exists; a declined row
//     asking for exactly the remainder is re-opened instead of a new one.
//
// With no catalogue (TMDB unreachable, or an import), a whole-show ask can't be split
// into seasons, so it is handled as before seasons existed: follow an active whole-show
// row, else re-open a declined one, else insert a whole-show row.
func planSeasons(asked, known []int, onDisk map[int]bool, rows []seasonRow) (seasonPlan, error) {
	whole := len(asked) == 0
	knownSet := map[int]bool{}
	for _, n := range known {
		if n > 0 {
			knownSet[n] = true
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })

	if whole && len(knownSet) == 0 {
		for _, r := range rows {
			if r.active() && r.seasons == nil {
				return seasonPlan{follow: []int64{r.id}}, nil
			}
		}
		p := seasonPlan{insert: true, units: 1}
		for i := len(rows) - 1; i >= 0; i-- {
			if r := rows[i]; r.status == StatusDeclined && r.seasons == nil {
				p.reopen = r.id
				break
			}
		}
		p.declinedBy = declinedOverlapping(rows, nil, p.reopen)
		return p, nil
	}

	var requested []int
	if whole {
		for n := range knownSet {
			requested = append(requested, n)
		}
	} else {
		seen := map[int]bool{}
		for _, n := range asked {
			if n <= 0 || n > maxSeason || seen[n] || (len(knownSet) > 0 && !knownSet[n]) {
				continue
			}
			seen[n] = true
			requested = append(requested, n)
		}
	}
	sort.Ints(requested)
	if len(requested) == 0 {
		return seasonPlan{}, ErrNoSuchSeasons
	}

	var remainder []int
	followSet := map[int64]bool{}
	someCovered := false
	for _, n := range requested {
		covered := onDisk[n]
		for _, r := range rows {
			if r.active() && r.covers(n) {
				covered = true
				followSet[r.id] = true
			}
		}
		if covered {
			someCovered = true
		} else {
			remainder = append(remainder, n)
		}
	}
	var follow []int64
	for _, r := range rows {
		if followSet[r.id] {
			follow = append(follow, r.id)
		}
	}
	if len(remainder) == 0 {
		if len(follow) == 0 {
			return seasonPlan{}, ErrAlreadyAvailable
		}
		return seasonPlan{follow: follow}, nil
	}

	p := seasonPlan{insert: true, seasons: remainder, follow: follow, units: len(remainder)}
	if whole && !someCovered {
		p.seasons = nil
		for _, r := range rows {
			if r.seasons == nil && r.status != StatusDeclined {
				// A whole-show row that's been delivered: a second whole-show row would share its
				// "ready" reference and its requester would never hear again. Ask by list.
				p.seasons = remainder
				break
			}
		}
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if r := rows[i]; r.status == StatusDeclined && sameSeasons(r.seasons, p.seasons) {
			p.reopen = r.id
			break
		}
	}
	p.declinedBy = declinedOverlapping(rows, p.seasons, p.reopen)
	return p, nil
}

// trimSeasons checks an approver's season choice against what a request asked for (nil:
// the whole show) and returns the seasons to approve plus a label for the rest ("S3", or
// "the rest of the show" for a whole-show request). A season the request didn't ask for
// is refused.
func trimSeasons(asked, approve []int) (keep []int, dropped string, err error) {
	seen := map[int]bool{}
	for _, n := range approve {
		if n <= 0 || n > maxSeason || seen[n] {
			continue
		}
		if asked != nil && !(seasonRow{seasons: asked}).covers(n) {
			return nil, "", ErrSeasonsNotRequested
		}
		seen[n] = true
		keep = append(keep, n)
	}
	sort.Ints(keep)
	if len(keep) == 0 {
		return asked, "", nil // nothing usable: approve as asked
	}
	if asked == nil {
		return keep, "The rest of the show wasn't approved.", nil
	}
	var rest []int
	for _, n := range asked {
		if !seen[n] {
			rest = append(rest, n)
		}
	}
	switch {
	case len(rest) == 1:
		dropped = fmt.Sprintf("Season %d wasn't approved.", rest[0])
	case len(rest) > 1:
		dropped = series.SeasonsLabel(rest) + " weren't approved."
	}
	return keep, dropped, nil
}

// declinedOverlapping are the requesters of declined rows (other than skip) that asked
// for any of seasons (nil: the whole show), oldest first, each once.
func declinedOverlapping(rows []seasonRow, seasons []int, skip int64) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, r := range rows {
		if r.status != StatusDeclined || r.id == skip || r.requestedBy <= 0 || seen[r.requestedBy] {
			continue
		}
		if overlaps(r.seasons, seasons) {
			seen[r.requestedBy] = true
			out = append(out, r.requestedBy)
		}
	}
	return out
}

// overlaps reports whether two season lists share a season (nil: the whole show).
func overlaps(a, b []int) bool {
	if a == nil || b == nil {
		return true
	}
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// sameSeasons reports whether two season lists are the same ask (nil: the whole show).
func sameSeasons(a, b []int) bool {
	if (a == nil) != (b == nil) || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
