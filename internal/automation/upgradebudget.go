package automation

import (
	"context"
	"strconv"
	"strings"
)

// The per-sweep upgrade budget. Each upgrade sweep used to grab every upgrade it found,
// held back only by free disk, so one profile edit could queue hundreds of downloads in a
// single pass. A sweep now grabs at most N upgrades; the rest are logged and wait for the
// next sweep (an upgraded title drops out of the candidates, so each sweep makes progress).

// KeyUpgradeBudget is the setting (Settings → Downloads) holding how many upgrades one
// sweep may grab; 0 means no limit.
const KeyUpgradeBudget = "upgrade_max_grabs_per_sweep"

// DefaultUpgradeBudget is the limit when nothing is saved: enough to keep a library moving
// towards its profiles without a profile edit turning into a download storm.
const DefaultUpgradeBudget = 10

// ParseUpgradeBudget reads a stored budget, falling back to the default for anything
// unreadable or negative.
func ParseUpgradeBudget(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return DefaultUpgradeBudget
	}
	return n
}

// SetUpgradeBudget wires the budget setting. Asked at the start of each sweep, so a change
// in Settings applies on the next one. Unset (tests) means the default.
func (c *Coordinator) SetUpgradeBudget(fn func(ctx context.Context) int) { c.upgradeBudgetFn = fn }

// newSweepBudget is the budget for one sweep.
func (c *Coordinator) newSweepBudget(ctx context.Context) *upgradeBudget {
	max := DefaultUpgradeBudget
	if c.upgradeBudgetFn != nil {
		max = c.upgradeBudgetFn(ctx)
	}
	return &upgradeBudget{max: max}
}

// upgradeBudget counts one sweep's upgrade grabs. A nil budget is unlimited: the manual
// single-title upgrades (right after a profile change) are never held back by it.
type upgradeBudget struct {
	max      int // 0 = no limit
	used     int // upgrades grabbed
	deferred int // upgrades found but left for the next sweep
}

// allow reports whether another upgrade may be grabbed.
func (b *upgradeBudget) allow() bool { return b == nil || b.max <= 0 || b.used < b.max }

// spent reports a limited budget with nothing left, so the sweep can stop searching.
func (b *upgradeBudget) spent() bool { return b != nil && b.max > 0 && b.used >= b.max }

func (b *upgradeBudget) take() {
	if b != nil {
		b.used++
	}
}

func (b *upgradeBudget) defer1() {
	if b != nil {
		b.deferred++
	}
}

// takeUpgrades walks one title's upgrade picks in order and grabs each while the budget
// allows. grab reports whether the pick was actually grabbed; one that wasn't (low disk, a
// failed grab, already in flight) costs nothing. Picks past the budget are counted as
// deferred and left alone. It returns the picks that were grabbed.
func takeUpgrades[T any](picks []T, b *upgradeBudget, grab func(T) bool) []T {
	var grabbed []T
	for _, p := range picks {
		if !b.allow() {
			b.defer1()
			continue
		}
		if grab(p) {
			b.take()
			grabbed = append(grabbed, p)
		}
	}
	return grabbed
}

// logBudget says, at the end of a sweep, when the budget held upgrades back.
func (c *Coordinator) logBudget(module string, b *upgradeBudget, titlesLeft int) {
	if b == nil || c.log == nil || (b.deferred == 0 && titlesLeft == 0) {
		return
	}
	c.log.Info("automation: upgrade budget reached — "+strconv.Itoa(b.used)+" grabbed; remaining titles wait for the next sweep",
		"module", module, "grabbed", b.used, "limit", b.max, "deferred_upgrades", b.deferred, "titles_not_searched", titlesLeft)
}
