package series

import (
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// A TVmaze timeout handed the refresh TMDB's listing — one episode fewer in season 1, so
// every later counted absolute shifted — and the refresh rebuilt the show by absolute,
// moving S02E01's file onto S01E03. With the source failing, a refresh may only add
// genuinely new episodes; nothing moves, nothing is pruned, nothing is re-titled.
func TestRefreshFallbackNeverRebuilds(t *testing.T) {
	d := standardDetails()
	d.NumberingSource, d.Seasons = "tvmaze", listing(3, 2)
	svc, fm, ctx := refreshTestService(t, d)
	sr, err := svc.Add(ctx, d.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if sr.NumberingSource != "tvmaze" {
		t.Fatalf("Add should record the listing's source, got %q", sr.NumberingSource)
	}
	const file = "/tv/Standard Show/Season 2/Standard Show - S02E01.mkv"
	if err := svc.repo.SetEpisodeFile(ctx, sr.ID, 2, 1, file, 10); err != nil {
		t.Fatal(err)
	}
	absBefore := svc.AbsoluteNumber(ctx, sr.ID, 2, 1)

	standIn := listing(2, 3)
	for i := range standIn {
		for j := range standIn[i].Episodes {
			standIn[i].Episodes[j].Title = "stand-in"
		}
	}
	fm.d.Seasons, fm.d.NumberingSource, fm.d.NumberingFallback = standIn, "tmdb", true

	for _, allow := range []bool{false, true} { // even the owner's Refresh, mid-outage
		_, res, err := svc.Refresh(ctx, sr.ID, RefreshOptions{AllowRebuild: allow})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Fallback || res.Renumbered || len(res.Remaps) != 0 {
			t.Fatalf("allow=%v: result = %+v, want a fallback that renumbers nothing", allow, res)
		}
	}
	if got := mustFile(t, svc, ctx, sr.ID, 2, 1); got != file {
		t.Errorf("S02E01's file moved to %q", got)
	}
	if svc.AbsoluteNumber(ctx, sr.ID, 2, 1) != absBefore {
		t.Error("a fallback listing must not reassign absolute numbers")
	}
	if !svc.EpisodeExists(ctx, sr.ID, 1, 3) {
		t.Error("S01E03 was dropped on a stand-in listing's say-so")
	}
	if !svc.EpisodeExists(ctx, sr.ID, 2, 3) {
		t.Error("a genuinely new episode should still be added")
	}
	if title := svc.EpisodeTitle(ctx, sr.ID, 1, 1); title != "orig" {
		t.Errorf("S01E01 re-titled to %q from a listing that may number it differently", title)
	}
	got, _ := svc.Get(ctx, sr.ID)
	if got.NumberingSource != "tvmaze" {
		t.Errorf("numbering_source = %q, want it unchanged after a fallback", got.NumberingSource)
	}

	// Anime takes nothing from a stand-in: its "new" episodes would be phantoms.
	a := animeDetails()
	a.NumberingSource, a.Seasons = "tvdb", withAbsolutes(listing(3, 2))
	asvc, afm, actx := refreshTestService(t, a)
	as, err := asvc.Add(actx, a.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	afm.d.Seasons, afm.d.NumberingSource, afm.d.NumberingFallback = listing(5), "tmdb", true
	if _, res, _ := asvc.Refresh(actx, as.ID, RefreshOptions{AllowRebuild: true}); !res.Fallback || res.Renumbered {
		t.Fatalf("anime fallback result = %+v", res)
	}
	if asvc.EpisodeExists(actx, as.ID, 1, 5) {
		t.Error("anime gained a phantom S01E05 from TMDB's stand-in listing")
	}
}

// A standard show whose season 1 gains an episode keeps every file on its (season,
// episode); only the absolute numbers change.
func TestRefreshStandardCountShiftKeepsFilesOnSE(t *testing.T) {
	d := standardDetails()
	d.NumberingSource, d.Seasons = "tvmaze", listing(3, 2)
	svc, fm, ctx := refreshTestService(t, d)
	sr, err := svc.Add(ctx, d.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	const f1, f2 = "/tv/S/Season 2/S02E01.mkv", "/tv/S/Season 2/S02E02.mkv"
	_ = svc.repo.SetEpisodeFile(ctx, sr.ID, 2, 1, f1, 1)
	_ = svc.repo.SetEpisodeFile(ctx, sr.ID, 2, 2, f2, 1)
	if svc.AbsoluteNumber(ctx, sr.ID, 2, 1) != 4 {
		t.Fatalf("setup: S02E01 should count as absolute 4")
	}

	fm.d.Seasons = listing(4, 2)
	for _, allow := range []bool{false, true} {
		_, res, err := svc.Refresh(ctx, sr.ID, RefreshOptions{AllowRebuild: allow})
		if err != nil {
			t.Fatal(err)
		}
		if res.ModelChanged || res.Renumbered || res.Fallback {
			t.Errorf("allow=%v: result = %+v, want a plain refresh", allow, res)
		}
	}
	if mustFile(t, svc, ctx, sr.ID, 2, 1) != f1 || mustFile(t, svc, ctx, sr.ID, 2, 2) != f2 {
		t.Error("files must stay on their (season, episode)")
	}
	if got := svc.AbsoluteNumber(ctx, sr.ID, 2, 1); got != 5 {
		t.Errorf("S02E01 absolute = %d, want 5 after season 1 grew", got)
	}
	if !svc.EpisodeExists(ctx, sr.ID, 1, 4) {
		t.Error("the new S01E04 should be added")
	}

	// Even with both sides on TVDB, a standard show is never rebuilt by absolute number.
	d2 := standardDetails()
	d2.NumberingSource, d2.Seasons = "tvdb", withAbsolutes(listing(3, 2))
	svc2, fm2, ctx2 := refreshTestService(t, d2)
	s2, _ := svc2.Add(ctx2, d2.TMDBID, "", true)
	_ = svc2.repo.SetEpisodeFile(ctx2, s2.ID, 2, 1, f1, 1)
	fm2.d.Seasons = withAbsolutes(listing(4, 2))
	if _, res, _ := svc2.Refresh(ctx2, s2.ID, RefreshOptions{AllowRebuild: true}); res.Renumbered || res.ModelChanged {
		t.Errorf("standard TVDB show: result = %+v, want no renumber", res)
	}
	if mustFile(t, svc2, ctx2, s2.ID, 2, 1) != f1 {
		t.Error("standard TVDB show: S02E01's file moved")
	}
}

// Scheduled, refresh-all and import-time refreshes never rebuild, even for a real
// authoritative renumber — they note it once in History for the owner to apply.
func TestRefreshScheduledNeverRebuilds(t *testing.T) {
	a := animeDetails()
	// Stored TVDB model: one season of 3 episodes, absolutes 1-3.
	a.NumberingSource, a.Seasons = "tvdb", withAbsolutes(listing(3))
	svc, fm, ctx := refreshTestService(t, a)
	sr, err := svc.Add(ctx, a.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	const file = "/tv/Anime Show/Season 1/Anime Show - S01E03.mkv"
	_ = svc.repo.SetEpisodeFile(ctx, sr.ID, 1, 3, file, 1)

	// TVDB splits the season: absolute 3 is now S02E01.
	split := []metadata.SeasonDetails{
		{SeasonNumber: 1, Episodes: []metadata.EpisodeDetails{{EpisodeNumber: 1, AbsoluteNumber: 1}, {EpisodeNumber: 2, AbsoluteNumber: 2}}},
		{SeasonNumber: 2, Episodes: []metadata.EpisodeDetails{{EpisodeNumber: 1, AbsoluteNumber: 3}}},
	}
	fm.d.Seasons = split
	for i := 0; i < 2; i++ {
		_, res, err := svc.Refresh(ctx, sr.ID, RefreshOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !res.ModelChanged || res.Renumbered || len(res.Remaps) != 0 {
			t.Fatalf("scheduled refresh result = %+v, want the change noticed but not applied", res)
		}
	}
	if mustFile(t, svc, ctx, sr.ID, 1, 3) != file {
		t.Error("a scheduled refresh moved a file")
	}
	evs, _ := svc.Events(ctx, sr.ID, 20)
	n := 0
	for _, e := range evs {
		if e.Event == "numbering" {
			n++
			if !strings.Contains(e.Detail, "1 file would move") || !strings.Contains(e.Detail, "Refresh") {
				t.Errorf("event detail = %q", e.Detail)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d numbering events, want exactly one for one unchanged proposal", n)
	}

	// The owner's Refresh then applies it.
	_, res, err := svc.Refresh(ctx, sr.ID, RefreshOptions{AllowRebuild: true})
	if err != nil || !res.Renumbered || len(res.Remaps) != 1 {
		t.Fatalf("manual refresh: %+v, %v", res, err)
	}
	if mustFile(t, svc, ctx, sr.ID, 2, 1) != file {
		t.Error("the manual refresh should carry the file to S02E01")
	}
}

// The case the rebuild exists for: anime stored on TMDB's numbering, a TVDB key added,
// the owner presses Refresh. It still rebuilds, records TVDB as the source, and reports
// the remaps for the safe rename.
func TestRefreshManualTVDBRebuildStillWorks(t *testing.T) {
	a := animeDetails()
	a.NumberingSource, a.Seasons = "tmdb", listing(4) // TMDB: one long season
	svc, fm, ctx := refreshTestService(t, a)
	sr, err := svc.Add(ctx, a.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	const file = "/tv/Anime Show/Season 1/Anime Show - S01E04.mkv"
	_ = svc.repo.SetEpisodeFile(ctx, sr.ID, 1, 4, file, 1)

	fm.d.NumberingSource = "tvdb"
	fm.d.Seasons = []metadata.SeasonDetails{
		{SeasonNumber: 1, Episodes: []metadata.EpisodeDetails{{EpisodeNumber: 1, AbsoluteNumber: 1}, {EpisodeNumber: 2, AbsoluteNumber: 2}}},
		{SeasonNumber: 2, Episodes: []metadata.EpisodeDetails{{EpisodeNumber: 1, AbsoluteNumber: 3}, {EpisodeNumber: 2, AbsoluteNumber: 4}}},
	}
	got, res, err := svc.Refresh(ctx, sr.ID, RefreshOptions{AllowRebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.ModelChanged || !res.Renumbered || len(res.Remaps) != 1 {
		t.Fatalf("result = %+v, want one remap", res)
	}
	r := res.Remaps[0]
	if r.OldSeason != 1 || r.OldEpisode != 4 || r.NewSeason != 2 || r.NewEpisode != 2 || r.FilePath != file {
		t.Errorf("remap = %+v, want S01E04 → S02E02", r)
	}
	if mustFile(t, svc, ctx, sr.ID, 2, 2) != file {
		t.Error("the file should now belong to S02E02")
	}
	if got.NumberingSource != "tvdb" {
		t.Errorf("numbering_source = %q, want tvdb once the rebuild applied", got.NumberingSource)
	}
}

// A declined rebuild leaves numbering_source alone, so the stored rows and the recorded
// source never disagree — and removing a TVDB key never moves files back.
func TestRefreshDeclinedRebuildKeepsSource(t *testing.T) {
	a := animeDetails()
	a.NumberingSource, a.Seasons = "tmdb", listing(4)
	svc, fm, ctx := refreshTestService(t, a)
	sr, _ := svc.Add(ctx, a.TMDBID, "", true)
	_ = svc.repo.SetEpisodeFile(ctx, sr.ID, 1, 4, "/tv/a.mkv", 1)
	fm.d.NumberingSource = "tvdb"
	fm.d.Seasons = withAbsolutes(listing(2, 2))
	got, res, _ := svc.Refresh(ctx, sr.ID, RefreshOptions{}) // scheduled
	if !res.ModelChanged || res.Renumbered {
		t.Fatalf("result = %+v", res)
	}
	if got.NumberingSource != "tmdb" {
		t.Errorf("numbering_source = %q, want tmdb kept after a declined rebuild", got.NumberingSource)
	}
	if mustFile(t, svc, ctx, sr.ID, 1, 4) != "/tv/a.mkv" {
		t.Error("a declined rebuild moved a file")
	}
	if svc.EpisodeExists(ctx, sr.ID, 2, 1) {
		t.Error("a declined anime rebuild must not mix the fresh model's episodes in")
	}

	// TVDB-numbered anime whose key is removed: TMDB's listing drops seasons holding
	// files. Even the owner's Refresh keeps the stored numbering.
	b := animeDetails()
	b.NumberingSource, b.Seasons = "tvdb", withAbsolutes(listing(2, 2))
	svc2, fm2, ctx2 := refreshTestService(t, b)
	s2, _ := svc2.Add(ctx2, b.TMDBID, "", true)
	_ = svc2.repo.SetEpisodeFile(ctx2, s2.ID, 2, 2, "/tv/b.mkv", 1)
	fm2.d.NumberingSource, fm2.d.Seasons = "tmdb", listing(4)
	got2, res2, _ := svc2.Refresh(ctx2, s2.ID, RefreshOptions{AllowRebuild: true})
	if !res2.ModelChanged || res2.Renumbered {
		t.Fatalf("key removed: result = %+v", res2)
	}
	if got2.NumberingSource != "tvdb" || mustFile(t, svc2, ctx2, s2.ID, 2, 2) != "/tv/b.mkv" {
		t.Error("key removed: the stored TVDB numbering and its files must stay put")
	}
}

func TestReassignAbsolutesLeavesFiles(t *testing.T) {
	repo, ctx := testRepo(t)
	sr, _ := repo.Create(ctx, Series{TMDBID: 5, Title: "Show", Monitored: true})
	if err := repo.InsertSeasons(ctx, sr.ID, []Season{
		{SeasonNumber: 1, Episodes: []Episode{{SeasonNumber: 1, EpisodeNumber: 1, AbsoluteNumber: 1}}},
		{SeasonNumber: 2, Episodes: []Episode{{SeasonNumber: 2, EpisodeNumber: 1, AbsoluteNumber: 2}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetEpisodeFile(ctx, sr.ID, 2, 1, "/tv/S02E01.mkv", 7); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReassignAbsolutes(ctx, sr.ID, []Season{
		{SeasonNumber: 1, Episodes: []Episode{{SeasonNumber: 1, EpisodeNumber: 1, AbsoluteNumber: 1}, {SeasonNumber: 1, EpisodeNumber: 2, AbsoluteNumber: 2}}},
		{SeasonNumber: 2, Episodes: []Episode{{SeasonNumber: 2, EpisodeNumber: 1, AbsoluteNumber: 3}}},
	}); err != nil {
		t.Fatal(err)
	}
	if f := repo.CurrentEpisodeFile(ctx, sr.ID, 2, 1); f.Path != "/tv/S02E01.mkv" || f.SizeBytes != 7 {
		t.Errorf("the file must stay on S02E01, got %+v", f)
	}
	stored, _ := repo.StoredNumbering(ctx, sr.ID)
	if stored[3] != [2]int{2, 1} {
		t.Errorf("S02E01 should now carry absolute 3: %+v", stored)
	}
	if repo.EpisodeExists(ctx, sr.ID, 1, 2) {
		t.Error("ReassignAbsolutes only updates existing rows; it must not insert")
	}
}
