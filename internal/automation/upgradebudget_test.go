package automation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestUpgradeBudgetAccounting(t *testing.T) {
	b := &upgradeBudget{max: 2}
	if !b.allow() || b.spent() {
		t.Fatal("a fresh budget allows")
	}
	b.take()
	b.take()
	if b.allow() || !b.spent() {
		t.Fatal("two of two used: spent")
	}
	b.defer1()
	if b.used != 2 || b.deferred != 1 {
		t.Errorf("used=%d deferred=%d", b.used, b.deferred)
	}

	// 0 is no limit, and so is no budget at all (the manual single-title paths).
	unlimited := &upgradeBudget{max: 0}
	for i := 0; i < 100; i++ {
		unlimited.take()
	}
	var none *upgradeBudget
	none.take()
	none.defer1()
	if !unlimited.allow() || unlimited.spent() || !none.allow() || none.spent() {
		t.Error("max 0 and a nil budget never run out")
	}

	// Nothing wired (tests, a bare coordinator): the default.
	if b := (&Coordinator{}).newSweepBudget(context.Background()); b.max != DefaultUpgradeBudget || DefaultUpgradeBudget != 10 {
		t.Errorf("unwired budget = %d, want the default of 10", b.max)
	}
	for raw, want := range map[string]int{"": DefaultUpgradeBudget, "x": DefaultUpgradeBudget, "-3": DefaultUpgradeBudget, "0": 0, " 25 ": 25} {
		if got := ParseUpgradeBudget(raw); got != want {
			t.Errorf("ParseUpgradeBudget(%q) = %d, want %d", raw, got, want)
		}
	}
}

// With 15 upgrades found and a budget of 10, ten are grabbed and five deferred. A pick
// that couldn't be grabbed (low disk, a failed grab) costs nothing.
func TestTakeUpgradesHonoursTheBudget(t *testing.T) {
	picks := make([]int, 15)
	for i := range picks {
		picks[i] = i
	}
	b := &upgradeBudget{max: 10}
	got := takeUpgrades(picks, b, func(int) bool { return true })
	if len(got) != 10 || b.used != 10 || b.deferred != 5 || got[9] != 9 {
		t.Fatalf("grabbed %v, used %d, deferred %d", got, b.used, b.deferred)
	}

	b = &upgradeBudget{max: 3}
	got = takeUpgrades(picks[:6], b, func(i int) bool { return i%2 == 1 }) // evens fail
	if len(got) != 3 || got[0] != 1 || got[2] != 5 || b.deferred != 0 {
		t.Errorf("failed grabs must not spend the budget: grabbed %v, deferred %d", got, b.deferred)
	}
	if got := takeUpgrades(picks, nil, func(int) bool { return true }); len(got) != 15 {
		t.Errorf("no budget grabbed %d of 15", len(got))
	}
}

// End to end over the fake indexer and download client: five movies with an upgrade waiting,
// one sweep with a budget of 3 grabs 3, the next sweep picks up the rest, and 0 means
// no limit.
func TestMovieUpgradeSweepGrabsAtMostTheBudget(t *testing.T) {
	h := newStallHarness(t)
	if _, err := h.c.db.Exec(`UPDATE quality_profiles SET upgrades_enabled = 1`); err != nil {
		t.Fatal(err)
	}
	var offers []string
	for i := 0; i < 5; i++ {
		title := "Film " + string(rune('A'+i))
		id := h.addMovie(t, 1000+i, title, 2001)
		have := strings.ReplaceAll(title, " ", ".") + ".2001.720p.WEB-DL.x264-GRP"
		if _, err := h.c.db.Exec(`UPDATE movies SET has_file = 1, movie_file_path = ?, source_release = ? WHERE id = ?`,
			fmt.Sprintf("/lib/%s/%s.mkv", title, have), have, id); err != nil {
			t.Fatal(err)
		}
		offers = append(offers, strings.ReplaceAll(title, " ", ".")+".2001.1080p.BluRay.x264-GRP")
	}
	h.ix.offer(offers...)
	h.c.SetUpgradeBudget(func(context.Context) int { return 3 })
	adds := func() int {
		n := 0
		for _, c := range h.qbit.callLog() {
			if strings.HasPrefix(c, "add ") {
				n++
			}
		}
		return n
	}

	h.c.UpgradeMovies(h.ctx)
	if n := adds(); n != 3 {
		t.Fatalf("one sweep grabbed %d upgrades, want 3", n)
	}
	searches := h.ix.searchCount()

	// The next sweep, unlimited: the five left are grabbed, the ten in flight aren't again.
	h.c.SetUpgradeBudget(func(context.Context) int { return 0 })
	h.c.UpgradeMovies(h.ctx)
	if n := adds(); n != 5 {
		t.Errorf("after the second sweep %d upgrades grabbed in all, want 5", n)
	}
	if h.ix.searchCount() <= searches {
		t.Error("the second sweep should have searched")
	}
}

// A spent budget stops the sweep before its next indexer search.
func TestMovieUpgradeSweepStopsSearchingWhenSpent(t *testing.T) {
	h := newStallHarness(t)
	if _, err := h.c.db.Exec(`UPDATE quality_profiles SET upgrades_enabled = 1`); err != nil {
		t.Fatal(err)
	}
	var offers []string
	for i := 0; i < 3; i++ {
		title := "Show " + string(rune('A'+i))
		id := h.addMovie(t, 2000+i, title, 2001)
		have := strings.ReplaceAll(title, " ", ".") + ".2001.720p.WEB-DL.x264-GRP"
		if _, err := h.c.db.Exec(`UPDATE movies SET has_file = 1, movie_file_path = ?, source_release = ? WHERE id = ?`,
			"/lib/"+have+".mkv", have, id); err != nil {
			t.Fatal(err)
		}
		offers = append(offers, strings.ReplaceAll(title, " ", ".")+".2001.1080p.BluRay.x264-GRP")
	}
	h.ix.offer(offers...)
	h.c.SetUpgradeBudget(func(context.Context) int { return 1 })
	h.c.UpgradeMovies(h.ctx)
	one := h.ix.searchCount() // what searching one title costs
	if one == 0 {
		t.Fatal("the first title was never searched")
	}
	// Unlimited, every other title is searched. The first one's upgrade is already in
	// flight, and the acquisition record keeps it out of the sweep until it lands.
	h.c.SetUpgradeBudget(func(context.Context) int { return 0 })
	h.c.UpgradeMovies(h.ctx)
	if all := h.ix.searchCount() - one; all != 2*one {
		t.Errorf("a budget of 1 searched %d times and no budget %d: the spent sweep didn't stop early", one, all)
	}
}

// Wanted's upgrade Search all shares one budget between the searches the queue runs two at
// a time: however they interleave, no more upgrades are grabbed than the budget allows.
func TestUpgradeBudgetSharedAcrossSearches(t *testing.T) {
	b := &upgradeBudget{max: 5}
	var mu sync.Mutex
	grabbed := 0
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := takeUpgrades([]int{1, 2}, b, func(int) bool { return true })
			mu.Lock()
			grabbed += len(got)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if grabbed != 5 || !b.spent() {
		t.Fatalf("grabbed %d upgrades on a budget of 5", grabbed)
	}
	// A batch whose budget is spent searches nothing more.
	c := &Coordinator{}
	if err := c.UpgradeMovieIn(context.Background(), 1, &UpgradeBatch{b: b}); err != nil {
		t.Fatalf("spent batch: %v", err)
	}
}
