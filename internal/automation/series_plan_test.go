package automation

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// planCase is one planner scenario: the eligible releases best-first, what's wanted, and
// the show around it. fail lists releases whose grab errors; pending ones read as in flight.
type planCase struct {
	name     string
	eligible []string
	wanted   []epKey
	counts   map[int]int // season → episode count; also the set of seasons the show has
	ended    bool
	fail     map[string]bool
}

func evals(names ...string) []quality.Evaluation {
	out := make([]quality.Evaluation, 0, len(names))
	for _, n := range names {
		out = append(out, quality.Evaluation{Candidate: quality.NewCandidate(n, 1, 10), Eligible: true})
	}
	return out
}

func seasonRange(from, to int) []epKey {
	var out []epKey
	for e := from; e <= to; e++ {
		out = append(out, epKey{3, e})
	}
	return out
}

func allSeasons(counts map[int]int) map[int]bool {
	out := map[int]bool{}
	for s := range counts {
		out[s] = true
	}
	return out
}

// runPlan runs the planner over a case and returns the grabs it attempted, in order, as
// "label:name" — a failed attempt is prefixed "x" — plus what it left uncovered.
func runPlan(c planCase, opts planOpts) ([]string, []epKey) {
	var calls []string
	left := planSeriesGrabs(planInput{
		eligible:      evals(c.eligible...),
		wanted:        c.wanted,
		seriesSeasons: allSeasons(c.counts),
		counts:        c.counts,
		ended:         c.ended,
		opts:          opts,
		cover:         coveredBy,
		try: func(name, label string) bool {
			if c.fail[name] {
				calls = append(calls, "x"+label+":"+name)
				return false
			}
			calls = append(calls, label+":"+name)
			return true
		},
	})
	return calls, left
}

// legacyPlan is the sweep's selection exactly as it stood inside grabSeriesLimited before
// the planner was extracted, kept verbatim (minus logging) so the replay test below can
// prove the extraction changed nothing the sweep does.
func legacyPlan(c planCase) ([]string, []epKey) {
	var calls []string
	eligible := evals(c.eligible...)
	seriesSeasons := allSeasons(c.counts)
	needed := map[epKey]bool{}
	for _, k := range c.wanted {
		needed[k] = true
	}
	grabFailed := map[string]bool{}
	grab := func(name, label string) bool {
		if c.fail[name] {
			calls = append(calls, "x"+label+":"+name)
			grabFailed[name] = true
			return false
		}
		calls = append(calls, label+":"+name)
		return true
	}
	ended := c.ended
	neededSeasons := seasonsOf(needed)
	if ended {
		for _, ev := range eligible {
			r := ev.Candidate.Release
			if r.Kind() != parser.KindCompleteShow {
				continue
			}
			if !coversAllSeasons(r, neededSeasons, seriesSeasons) {
				continue
			}
			if !packIsProportionate(neededSeasons, packSeasonsOf(r, seriesSeasons)) {
				continue
			}
			if !grab(ev.Candidate.Name, "complete series") {
				continue
			}
			return calls, sortedKeys(map[epKey]bool{})
		}
	}
	counts := c.counts
	takePacks := func(worthItOnly bool) {
		for {
			var best *quality.Evaluation
			var bestCover int
			for i := range eligible {
				if grabFailed[eligible[i].Candidate.Name] {
					continue
				}
				r := eligible[i].Candidate.Release
				if !isPackTier(r.Kind(), ended) {
					continue
				}
				n := len(coveredBy(r, needed))
				if n == 0 {
					continue
				}
				if worthItOnly && !packIsWorthIt(r, n, seriesSeasons, counts) {
					continue
				}
				if n > bestCover {
					bestCover, best = n, &eligible[i]
				}
			}
			if best == nil || bestCover == 0 {
				return
			}
			if !grab(best.Candidate.Name, "pack") {
				continue
			}
			for _, k := range coveredBy(best.Candidate.Release, needed) {
				delete(needed, k)
			}
		}
	}
	takePacks(true)
	for _, ev := range eligible {
		if len(needed) == 0 {
			break
		}
		r := ev.Candidate.Release
		if r.Kind() != parser.KindEpisode {
			continue
		}
		covered := coveredBy(r, needed)
		if len(covered) == 0 {
			continue
		}
		if !grab(ev.Candidate.Name, "episode") {
			continue
		}
		for _, k := range covered {
			delete(needed, k)
		}
	}
	if len(needed) > 0 {
		takePacks(false)
	}
	return calls, sortedKeys(needed)
}

var fiveSeasons = map[int]int{1: 10, 2: 10, 3: 10, 4: 10, 5: 10}

// The sweep's cases, including the packsize fixtures: a box set for one gap, a season
// missing outright, the greedy multi-season pick, a failing best pack, and the last-resort
// oversized pack.
var sweepCases = []planCase{
	{name: "running show, whole season missing → season pack",
		eligible: []string{"Show.Complete.Series.1080p", "Show.S01-S05.1080p", "Show.S03.1080p", "Show.S03E01.1080p"},
		wanted:   seasonRange(1, 10), counts: fiveSeasons},
	{name: "running show, one gap → single, not the pack",
		eligible: []string{"Show.S03.1080p", "Show.S03E04.1080p"},
		wanted:   []epKey{{3, 4}}, counts: fiveSeasons},
	{name: "running show, one gap, no single → last-resort season pack",
		eligible: []string{"Show.S03.1080p"},
		wanted:   []epKey{{3, 4}}, counts: fiveSeasons},
	{name: "ended show, everything missing → complete pack",
		eligible: []string{"Show.S03.1080p", "Show.Complete.Series.1080p"},
		wanted:   append(append([]epKey{{1, 1}, {2, 1}}, seasonRange(1, 10)...), epKey{4, 1}, epKey{5, 1}), counts: fiveSeasons, ended: true},
	{name: "ended show, one gap → complete pack held back, last resort takes the first pack",
		eligible: []string{"Show.Complete.Series.1080p", "Show.S03.1080p"},
		wanted:   []epKey{{3, 4}}, counts: fiveSeasons, ended: true},
	{name: "ended show, two seasons missing → multi-season pack",
		eligible: []string{"Show.S03.1080p", "Show.S03-S04.1080p", "Show.S04.1080p"},
		wanted:   append(seasonRange(1, 10), epKey{4, 1}, epKey{4, 2}, epKey{4, 3}, epKey{4, 4}, epKey{4, 5}, epKey{4, 6}, epKey{4, 7}, epKey{4, 8}, epKey{4, 9}, epKey{4, 10}),
		counts:   fiveSeasons, ended: true},
	{name: "best pack fails → the next one, never the failed one again",
		eligible: []string{"Show.S03.2160p", "Show.S03.1080p", "Show.S03E01.1080p"},
		wanted:   seasonRange(1, 10), counts: fiveSeasons,
		fail: map[string]bool{"Show.S03.2160p": true}},
	{name: "every grab fails → everything still needed",
		eligible: []string{"Show.S03.1080p", "Show.S03E01.1080p"},
		wanted:   []epKey{{3, 1}}, counts: fiveSeasons,
		fail: map[string]bool{"Show.S03.1080p": true, "Show.S03E01.1080p": true}},
	{name: "complete pack fails → falls through to season packs",
		eligible: []string{"Show.Complete.Series.1080p", "Show.S01.1080p", "Show.S02.1080p"},
		wanted:   []epKey{{1, 1}, {1, 2}, {1, 3}, {1, 4}, {1, 5}, {1, 6}, {2, 1}, {2, 2}, {2, 3}, {2, 4}, {2, 5}, {2, 6}},
		counts:   map[int]int{1: 10, 2: 10}, ended: true,
		fail: map[string]bool{"Show.Complete.Series.1080p": true}},
	{name: "multi-episode single covers two gaps",
		eligible: []string{"Show.S03E04E05.1080p", "Show.S03E04.1080p", "Show.S03E05.1080p"},
		wanted:   []epKey{{3, 4}, {3, 5}}, counts: fiveSeasons},
	{name: "nothing eligible",
		eligible: nil, wanted: []epKey{{3, 4}}, counts: fiveSeasons},
}

// The extraction must not change one grab the sweep makes: same releases, same order,
// same leftovers.
func TestPlanUnscopedMatchesLegacyPasses(t *testing.T) {
	for _, c := range sweepCases {
		t.Run(c.name, func(t *testing.T) {
			gotCalls, gotLeft := runPlan(c, planOpts{})
			wantCalls, wantLeft := legacyPlan(c)
			if !reflect.DeepEqual(gotCalls, wantCalls) {
				t.Errorf("grabs = %v, legacy %v", gotCalls, wantCalls)
			}
			if len(gotLeft) != len(wantLeft) || (len(gotLeft) > 0 && !reflect.DeepEqual(gotLeft, wantLeft)) {
				t.Errorf("left = %v, legacy %v", gotLeft, wantLeft)
			}
		})
	}
}

// A grab that can't happen for any reason — not only an indexer error — must not be picked
// again, or the pack passes would loop on it forever.
func TestPlanNeverRetriesAFailedPack(t *testing.T) {
	c := planCase{
		eligible: []string{"Show.S03.1080p"},
		wanted:   seasonRange(1, 10), counts: fiveSeasons,
		fail: map[string]bool{"Show.S03.1080p": true},
	}
	calls, left := runPlan(c, planOpts{})
	if len(calls) != 1 {
		t.Fatalf("a failing pack was tried %d times: %v", len(calls), calls)
	}
	if len(left) != 10 {
		t.Errorf("a failed pack covered %d episodes; want none covered", 10-len(left))
	}
}

func seasonClick() planOpts {
	return planOpts{Scoped: true, AllowPackFallback: true}
}

func episodeClick() planOpts {
	return planOpts{Scoped: true, EpisodesOnly: true}
}

func grabbedNames(calls []string) string {
	var out []string
	for _, c := range calls {
		if !strings.HasPrefix(c, "x") {
			out = append(out, c[strings.Index(c, ":")+1:])
		}
	}
	return strings.Join(out, ",")
}

// On a running show a season click never takes a box set or a multi-season pack, however
// well it ranks; on an ended show it still won't, unless the pack is mostly that season.
func TestPlanScopedSeasonRunningShowRejectsCompletePack(t *testing.T) {
	ranked := []string{"Show.Complete.Series.2160p", "Show.S01-S05.2160p", "Show.S03.1080p", "Show.S03E01.1080p"}
	for _, ended := range []bool{false, true} {
		calls, _ := runPlan(planCase{eligible: ranked, wanted: seasonRange(1, 10), counts: fiveSeasons, ended: ended}, seasonClick())
		if got := grabbedNames(calls); got != "Show.S03.1080p" {
			t.Errorf("ended=%v: grabbed %q, want only the season pack", ended, got)
		}
	}
	// Even when the box set is the only release: the last resort must stay proportionate.
	calls, left := runPlan(planCase{eligible: ranked[:2], wanted: seasonRange(1, 10), counts: fiveSeasons, ended: true}, seasonClick())
	if len(calls) != 0 || len(left) != 10 {
		t.Errorf("a five-season pack was taken for one season: %v (left %d)", calls, len(left))
	}
}

// A season click on a partly filled season fetches singles for the gaps, and the season
// pack only when at least half the season is missing.
func TestPlanScopedSeasonPackOnlyWhenMostMissing(t *testing.T) {
	ranked := []string{"Show.S03.1080p", "Show.S03E01.1080p", "Show.S03E02.1080p", "Show.S03E03.1080p",
		"Show.S03E04.1080p", "Show.S03E05.1080p", "Show.S03E06.1080p"}
	calls, left := runPlan(planCase{eligible: ranked, wanted: seasonRange(1, 3), counts: fiveSeasons}, seasonClick())
	if got := grabbedNames(calls); got != "Show.S03E01.1080p,Show.S03E02.1080p,Show.S03E03.1080p" || len(left) != 0 {
		t.Errorf("3 of 10 missing: grabbed %q (left %v), want the three singles", got, left)
	}
	calls, _ = runPlan(planCase{eligible: ranked, wanted: seasonRange(1, 6), counts: fiveSeasons}, seasonClick())
	if got := grabbedNames(calls); got != "Show.S03.1080p" {
		t.Errorf("6 of 10 missing: grabbed %q, want the season pack", got)
	}
}

// With no single episode for a gap, a season click falls back to the season pack — an
// inefficient grab beats none, as in the sweep.
func TestPlanScopedSeasonPackFallbackWhenNoEpisodes(t *testing.T) {
	calls, left := runPlan(planCase{eligible: []string{"Show.S03.1080p"}, wanted: []epKey{{3, 4}}, counts: fiveSeasons}, seasonClick())
	if got := grabbedNames(calls); got != "Show.S03.1080p" || len(left) != 0 {
		t.Errorf("grabbed %q (left %v), want the season pack as the last resort", got, left)
	}
	// Without the fallback (Replace), the gap stays a gap.
	calls, left = runPlan(planCase{eligible: []string{"Show.S03.1080p"}, wanted: []epKey{{3, 4}}, counts: fiveSeasons}, planOpts{Scoped: true})
	if len(calls) != 0 || len(left) != 1 {
		t.Errorf("no fallback: grabbed %v (left %v), want nothing", calls, left)
	}
}

func TestPlanScopedEpisodePrefersEpisodeOverPack(t *testing.T) {
	calls, left := runPlan(planCase{
		eligible: []string{"Show.S03.2160p", "Show.Complete.Series.2160p", "Show.S03E03E04.1080p", "Show.S03E04.720p"},
		wanted:   []epKey{{3, 4}}, counts: fiveSeasons, ended: true,
	}, episodeClick())
	if got := grabbedNames(calls); got != "Show.S03E03E04.1080p" || len(left) != 0 {
		t.Errorf("grabbed %q (left %v), want the best release that is episodes, not a pack", got, left)
	}
}

// An episode click never takes a pack — not a season pack of a two-episode season (which
// packIsWorthIt would allow), and not a one-season show's "Complete" release.
func TestPlanScopedEpisodeNeverTakesPack(t *testing.T) {
	cases := []planCase{
		{eligible: []string{"Show.S03.1080p"}, wanted: []epKey{{3, 1}}, counts: map[int]int{3: 2}},
		{eligible: []string{"Show.Complete.Series.1080p"}, wanted: []epKey{{1, 1}}, counts: map[int]int{1: 6}, ended: true},
		{eligible: []string{"Show.S01-S02.1080p", "Show.S01.1080p"}, wanted: []epKey{{1, 1}}, counts: map[int]int{1: 1, 2: 1}, ended: true},
	}
	for i, c := range cases {
		if calls, left := runPlan(c, episodeClick()); len(calls) != 0 || len(left) != 1 {
			t.Errorf("case %d: grabbed %v (left %v), want nothing", i, calls, left)
		}
	}
}

func TestOnlyPacksFit(t *testing.T) {
	if !onlyPacksFit(evals("Show.S03.1080p", "Show.Complete.Series.1080p")) {
		t.Error("packs only → true")
	}
	if onlyPacksFit(evals("Show.S03.1080p", "Show.S03E04.1080p")) {
		t.Error("an episode release fits → false")
	}
	if onlyPacksFit(nil) {
		t.Error("nothing fits → false (that's a different note)")
	}
}

func ExampleGrabOutcome_Summary() {
	o := GrabOutcome{Searched: true, Found: 37, WrongShow: 22,
		Rejected: map[string]int{"Over your 20 Mbps ceiling": 15}}
	fmt.Println(o.Summary())
	// Output: Grabbed nothing · 37 found · 0 fit: 22 other shows, 15 Over your 20 Mbps ceiling
}
