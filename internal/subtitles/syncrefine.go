package subtitles

import (
	"math"
	"sort"
	"time"
)

// Refining a measured offset by where lines START.
//
// align() matches the whole on/off shape of the cues to the speech. That finds an offset
// of seconds reliably, but it leans by 100-200 ms with how the subtitle was timed: cues
// that linger after the last word (most do) pull the best overlap late, and the lean
// differs from one subtitle to the next. For the download gate's 300 ms tolerance that
// never mattered. For a fix of a few hundred milliseconds it is the whole question.
//
// A line's start doesn't have that problem: subtitlers cue a line in on its first word
// whatever they do with its end. So after align() has found the offset to within a few
// hundred milliseconds, the cue starts that follow a pause are compared with the places
// speech starts after a pause, and the offset that lines the most of them up wins. When
// too few line up to trust (wall-to-wall music, a subtitle of mostly continuing lines),
// there is no refinement and the caller keeps to the coarse tolerance.

const (
	refinePause   = 300 * time.Millisecond // silence before a start for it to count, cue or speech
	refineRadius  = 500                    // ms either side of the coarse offset searched
	refineKernel  = 60                     // ms: a start this close to a speech onset is a hit
	refineMinCues = 20                     // fewer fresh starts than this can't be judged
	refineMinHits = 0.35                   // share of fresh starts that must land on an onset
	refineMinPeak = 1.8                    // the best offset's score over the typical one
)

// onsets are the times (ms) speech starts after at least refinePause of quiet. The
// hangover in gateEnergy only lengthens a burst's tail, so a rising edge is a true start.
func (t *speechTrack) onsets() []int {
	quiet := int(refinePause / syncFrame)
	var out []int
	off := quiet // the file's first sound counts as a start
	for i, v := range t.env {
		if v == 0 {
			off++
			continue
		}
		if off >= quiet {
			out = append(out, i*int(syncFrame/time.Millisecond))
		}
		off = 0
	}
	return out
}

// freshStarts are the cue starts (ms) that follow at least refinePause without a cue: the
// first line after a pause, not the second half of a sentence split over two cues.
func freshStarts(cues []cue) []int {
	var out []int
	var lastEnd time.Duration = -time.Hour
	for _, c := range cues {
		if c.start-lastEnd >= refinePause {
			out = append(out, int(c.start/time.Millisecond))
		}
		if c.end > lastEnd {
			lastEnd = c.end
		}
	}
	return out
}

// refineByStarts sharpens a coarse result. It returns the result with OffsetMs replaced by
// the offset at which the most fresh cue starts sit on a speech onset, and ok=false (and
// the result unchanged) when the starts don't line up clearly enough to say.
func (t *speechTrack) refineByStarts(cues []cue, res SyncResult) (SyncResult, bool) {
	starts := freshStarts(cues)
	on := t.onsets()
	if len(starts) < refineMinCues || len(on) < refineMinCues {
		return res, false
	}
	// A cue at c is spoken at c×scale − offset. d = that − the onset near it: how much
	// later the corrected cue still runs than the words. One vote per (start, onset) pair
	// within reach, in 10 ms bins.
	const bin = 10
	reach := refineRadius + refineKernel
	nb := 2*reach/bin + 1
	hist := make([]float64, nb)
	scale := res.scale()
	for _, c := range starts {
		pred := int(math.Round(float64(c)*scale)) - res.OffsetMs
		i := sort.SearchInts(on, pred-reach)
		for ; i < len(on) && on[i] <= pred+reach; i++ {
			hist[(pred-on[i]+reach)/bin]++
		}
	}
	// Score each candidate δ with a triangle ±refineKernel wide.
	k := refineKernel / bin
	best, bestScore := 0, -1.0
	var scores []float64
	for b := k; b < nb-k; b++ {
		s := 0.0
		for j := -k; j <= k; j++ {
			s += hist[b+j] * (1 - math.Abs(float64(j))/float64(k+1))
		}
		scores = append(scores, s)
		if s > bestScore {
			best, bestScore = b, s
		}
	}
	// How many starts actually sit on an onset at the winning δ, and how far the winner
	// stands above an ordinary δ (onsets are everywhere in talky audio: chance hits are
	// the floor to beat).
	hits := 0.0
	for j := -k; j <= k; j++ {
		hits += hist[best+j]
	}
	sorted := append([]float64(nil), scores...)
	sort.Float64s(sorted)
	median := sorted[len(sorted)/2]
	if hits/float64(len(starts)) < refineMinHits || bestScore < refineMinPeak*math.Max(median, 1) {
		return res, false
	}
	delta := best*bin - reach
	res.OffsetMs += delta
	return res, true
}
