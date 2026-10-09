package series

import (
	"errors"
	"testing"
)

// renumberFixture is anime stored on TMDB's one long season (S01E01-E04, a file on E03 and
// E04) and the TVDB listing that splits it in two (absolute 3-4 become S02E01-E02).
func renumberFixture(t *testing.T) (*Service, *fakeMeta, int64) {
	t.Helper()
	a := animeDetails()
	a.NumberingSource, a.Seasons = "tmdb", listing(4)
	svc, fm, ctx := refreshTestService(t, a)
	sr, err := svc.Add(ctx, a.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	_ = svc.repo.SetEpisodeFile(ctx, sr.ID, 1, 3, "/tv/A/Season 1/A - S01E03.mkv", 3)
	_ = svc.repo.SetEpisodeFile(ctx, sr.ID, 1, 4, "/tv/A/Season 1/A - S01E04.mkv", 4)
	fm.d.NumberingSource = "tvdb"
	fm.d.Seasons = withAbsolutes(listing(2, 2))
	return svc, fm, sr.ID
}

// What the owner reviews must be exactly what Apply does: one planner behind both.
func TestPlanRebuildMatchesRebuildRemaps(t *testing.T) {
	svc, fm, id := renumberFixture(t)
	ctx := t.Context()
	seasons := seasonsFromDetails(&fm.d, func(int) bool { return true })
	plan, err := svc.repo.PlanRebuild(ctx, id, seasons)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 {
		t.Fatalf("plan = %+v, want two moves", plan)
	}
	if got := mustFile(t, svc, ctx, id, 1, 3); got == "" {
		t.Fatal("planning must not write anything")
	}
	done, err := svc.repo.RebuildEpisodes(ctx, id, seasons)
	if err != nil {
		t.Fatal(err)
	}
	if PlanHash(done) != PlanHash(plan) || len(done) != len(plan) {
		t.Errorf("rebuild did %+v, the plan said %+v", done, plan)
	}
	for _, r := range plan {
		if got := mustFile(t, svc, ctx, id, r.NewSeason, r.NewEpisode); got != r.FilePath {
			t.Errorf("S%02dE%02d holds %q, the plan put %q there", r.NewSeason, r.NewEpisode, got, r.FilePath)
		}
	}
}

func TestPlanHashDeterministic(t *testing.T) {
	a := []EpisodeRemap{
		{Absolute: 3, OldSeason: 1, OldEpisode: 3, NewSeason: 2, NewEpisode: 1, FilePath: "/a"},
		{Absolute: 4, OldSeason: 1, OldEpisode: 4, NewSeason: 2, NewEpisode: 2, FilePath: "/b"},
		{Absolute: 9, OldSeason: 1, OldEpisode: 9, FilePath: "/c", Unplaced: true},
	}
	b := []EpisodeRemap{a[2], a[0], a[1]}
	if PlanHash(a) != PlanHash(b) {
		t.Error("the same plan in another order must hash the same, or Apply returns spurious 409s")
	}
	c := append([]EpisodeRemap(nil), a...)
	c[1].NewEpisode = 3
	if PlanHash(a) == PlanHash(c) {
		t.Error("a different plan must hash differently")
	}
	if PlanHash(a) != PlanHash(append([]EpisodeRemap(nil), a...)) || a[0].Absolute != 3 {
		t.Error("hashing must not reorder the caller's slice")
	}
}

// Two files never land on one slot, and a file with nowhere to go is shown, not hidden.
func TestPlanPlacementsConflictsAndUnplaced(t *testing.T) {
	current := []placementRow{
		{Absolute: 1, Season: 1, Episode: 1, HasFile: true, Path: "/one"},
		{Absolute: 7, Season: 1, Episode: 2, HasFile: true, Path: "/seven"}, // the new listing has no 7
		{Absolute: 8, Season: 3, Episode: 1, HasFile: true, Path: "/eight"}, // no 8 and no S03E01
		{Absolute: 2, Season: 1, Episode: 3, HasFile: true, Path: "/two"},
	}
	desired := []Season{{SeasonNumber: 1, Episodes: []Episode{
		{SeasonNumber: 1, EpisodeNumber: 1, AbsoluteNumber: 1},
		{SeasonNumber: 1, EpisodeNumber: 2, AbsoluteNumber: 2},
	}}}
	placed, remaps := planPlacements(current, desired)
	at := map[string][2]int{}
	for _, p := range placed {
		at[p.row.Path] = [2]int{p.season, p.episode}
	}
	if at["/two"] != [2]int{1, 2} {
		t.Errorf("absolute 2 should take S01E02 (by absolute beats staying put): %+v", at)
	}
	if _, ok := at["/seven"]; ok {
		t.Errorf("/seven's old slot is taken by absolute 2; it must not share it: %+v", at)
	}
	unplaced := 0
	for _, r := range remaps {
		if r.Unplaced {
			unplaced++
		}
	}
	if unplaced != 2 {
		t.Errorf("remaps = %+v, want /seven and /eight reported unplaced", remaps)
	}
}

// A refresh whose numbering would move files stores one proposal and moves nothing; the
// same plan seen again adds no second History line.
func TestRefreshStoresPendingInsteadOfRebuilding(t *testing.T) {
	svc, _, id := renumberFixture(t)
	ctx := t.Context()
	for i := 0; i < 2; i++ {
		_, res, err := svc.Refresh(ctx, id, RefreshOptions{})
		if err != nil || !res.Proposed || res.Rebuilt {
			t.Fatalf("refresh %d: %+v, %v", i, res, err)
		}
	}
	if mustFile(t, svc, ctx, id, 1, 3) != "/tv/A/Season 1/A - S01E03.mkv" {
		t.Error("a proposal moved a file")
	}
	n, err := svc.NumberingFor(ctx, id)
	if err != nil || n.Pending == nil {
		t.Fatalf("want a pending proposal, got %+v (%v)", n, err)
	}
	if n.Source != "tmdb" || n.Pending.From != "tmdb" || n.Pending.To != "tvdb" || len(n.Pending.Remaps) != 2 {
		t.Errorf("numbering = %+v / %+v", n, n.Pending)
	}
	events := 0
	evs, _ := svc.Events(ctx, id, 50)
	for _, e := range evs {
		if e.Event == "numbering" {
			events++
		}
	}
	if events != 1 {
		t.Errorf("%d numbering events, want one per distinct plan", events)
	}
}

// After Dismiss the same plan isn't proposed again; a different plan is.
func TestDismissedPlanNotReproposed(t *testing.T) {
	svc, fm, id := renumberFixture(t)
	ctx := t.Context()
	if _, _, err := svc.Refresh(ctx, id, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DismissNumbering(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Refresh(ctx, id, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.NumberingFor(ctx, id); n.Pending != nil {
		t.Fatalf("a dismissed plan came back: %+v", n.Pending)
	}
	if mustFile(t, svc, ctx, id, 1, 4) != "/tv/A/Season 1/A - S01E04.mkv" {
		t.Error("Dismiss must leave every file where it is")
	}
	if err := svc.DismissNumbering(ctx, id); !errors.Is(err, ErrNoPendingNumbering) {
		t.Errorf("dismissing nothing = %v, want ErrNoPendingNumbering", err)
	}

	// TVDB changes its split: a different plan, so it's proposed.
	fm.d.Seasons = withAbsolutes(listing(3, 1))
	if _, _, err := svc.Refresh(ctx, id, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.NumberingFor(ctx, id); n.Pending == nil || len(n.Pending.Remaps) != 1 {
		t.Fatalf("a different plan should be proposed, got %+v", n.Pending)
	}
}

// Applying a plan the metadata no longer produces moves nothing and says so, and the
// current plan is what's pending afterwards.
func TestApplyPendingRejectsStalePlan(t *testing.T) {
	svc, fm, id := renumberFixture(t)
	ctx := t.Context()
	if _, _, err := svc.Refresh(ctx, id, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	shown := pendingHash(t, svc, id)

	fm.d.Seasons = withAbsolutes(listing(3, 1)) // changed since it was shown
	if _, err := svc.ApplyNumbering(ctx, id, shown); !errors.Is(err, ErrStalePlan) {
		t.Fatalf("stale apply = %v, want ErrStalePlan", err)
	}
	if mustFile(t, svc, ctx, id, 1, 3) != "/tv/A/Season 1/A - S01E03.mkv" || mustFile(t, svc, ctx, id, 1, 4) != "/tv/A/Season 1/A - S01E04.mkv" {
		t.Error("a stale apply moved a file")
	}
	if now := pendingHash(t, svc, id); now == shown {
		t.Error("the current plan should replace the stale one for review")
	}
	if _, err := svc.ApplyNumbering(ctx, id, ""); !errors.Is(err, ErrStalePlan) {
		t.Errorf("an empty plan = %v, want ErrStalePlan", err)
	}

	// The source failing at Apply time is stale too — nothing moves on a stand-in.
	fm.d.NumberingFallback, fm.d.NumberingSource = true, "tvmaze"
	if _, err := svc.ApplyNumbering(ctx, id, pendingHash(t, svc, id)); !errors.Is(err, ErrStalePlan) {
		t.Errorf("apply during an outage = %v, want ErrStalePlan", err)
	}
	fm.d.NumberingFallback, fm.d.NumberingSource = false, "tvdb"
	res, err := svc.ApplyNumbering(ctx, id, pendingHash(t, svc, id))
	if err != nil || !res.Rebuilt {
		t.Fatalf("apply of the current plan: %+v, %v", res, err)
	}
	if mustFile(t, svc, ctx, id, 2, 1) != "/tv/A/Season 1/A - S01E04.mkv" {
		t.Error("absolute 4 should now be S02E01")
	}
}

// A season the owner switched off stays off across a rebuild; seasons new to the model
// take the listing's flag.
func TestRebuildKeepsSeasonMonitoredFlags(t *testing.T) {
	repo, ctx := testRepo(t)
	sr, _ := repo.Create(ctx, Series{TMDBID: 3, Title: "Show", Monitored: true})
	if err := repo.InsertSeasons(ctx, sr.ID, []Season{
		{SeasonNumber: 1, Monitored: true, Episodes: []Episode{{SeasonNumber: 1, EpisodeNumber: 1, AbsoluteNumber: 1, Monitored: true}}},
		{SeasonNumber: 2, Monitored: false, Episodes: []Episode{{SeasonNumber: 2, EpisodeNumber: 1, AbsoluteNumber: 2}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RebuildEpisodes(ctx, sr.ID, []Season{
		{SeasonNumber: 1, Monitored: true, Episodes: []Episode{{SeasonNumber: 1, EpisodeNumber: 1, AbsoluteNumber: 1, Monitored: true}}},
		{SeasonNumber: 2, Monitored: true, Episodes: []Episode{{SeasonNumber: 2, EpisodeNumber: 1, AbsoluteNumber: 2, Monitored: true}}},
		{SeasonNumber: 3, Monitored: true, Episodes: []Episode{{SeasonNumber: 3, EpisodeNumber: 1, AbsoluteNumber: 3, Monitored: true}}},
	}); err != nil {
		t.Fatal(err)
	}
	seasons, _ := repo.SeasonsFor(ctx, sr.ID)
	want := map[int]bool{1: true, 2: false, 3: true}
	for _, sn := range seasons {
		if sn.Monitored != want[sn.SeasonNumber] {
			t.Errorf("season %d monitored = %v, want %v", sn.SeasonNumber, sn.Monitored, want[sn.SeasonNumber])
		}
	}
}
