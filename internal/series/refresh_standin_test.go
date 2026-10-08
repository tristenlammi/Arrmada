package series

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// episodeSourceStub is a numbering source whose answer the test sets.
type episodeSourceStub struct {
	name    string
	seasons []metadata.SeasonDetails
	err     error
}

func (e *episodeSourceStub) Name() string    { return e.name }
func (e *episodeSourceStub) Available() bool { return true }
func (e *episodeSourceStub) Episodes(context.Context, int, string) ([]metadata.SeasonDetails, error) {
	return e.seasons, e.err
}

func airDate(t *testing.T, svc *Service, ctx context.Context, id int64, season, episode int) string {
	t.Helper()
	seasons, err := svc.repo.SeasonsFor(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, sn := range seasons {
		for _, e := range sn.Episodes {
			if sn.SeasonNumber == season && e.EpisodeNumber == episode {
				return e.AirDate
			}
		}
	}
	t.Fatalf("no S%02dE%02d", season, episode)
	return ""
}

// A numbering source that errors on every call — TVDB down for days — used to leave every
// show in the library on stand-in refreshes for as long as it lasted: titles and air dates
// never refreshed, so a TBA episode never learned its date and was never searched for. When
// the listing that does come back is from the source the stored rows already follow, it's
// numbered the same way and refreshes them normally.
func TestRefreshStandInFromStoredSourceRefreshesMetadata(t *testing.T) {
	for _, anime := range []bool{false, true} {
		base := standardDetails()
		if anime {
			base = animeDetails()
		}
		base.Seasons = listing(3) // what TMDB says; the numbering comes from the sources
		primary := &fakeMeta{d: base}
		tvdb := &episodeSourceStub{name: "tvdb", err: errors.New("tvdb: 503")}
		tvmaze := &episodeSourceStub{name: "tvmaze", seasons: listing(3)}
		tvmaze.seasons[0].Episodes[2].AirDate = "" // S01E03 is still TBA
		log := slog.New(slog.NewTextHandler(io.Discard, nil))

		st, err := store.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		svc := NewService(st.DB(), metadata.NewSeriesWithEpisodes(primary, log, tvdb, tvmaze), t.TempDir(), log)
		ctx := context.Background()

		sr, err := svc.Add(ctx, base.TMDBID, "", true)
		if err != nil {
			t.Fatal(err)
		}
		if sr.NumberingSource != "tvmaze" {
			t.Fatalf("anime=%v: Add should record the source the rows follow, got %q", anime, sr.NumberingSource)
		}
		const file = "/tv/Show/Season 1/Show - S01E02.mkv"
		_ = svc.repo.SetEpisodeFile(ctx, sr.ID, 1, 2, file, 1)
		absBefore := svc.AbsoluteNumber(ctx, sr.ID, 1, 2)

		// TVmaze learns S01E03's date and lists a new S01E04; TVDB is still failing.
		tvmaze.seasons = listing(4)
		tvmaze.seasons[0].Episodes[2].AirDate = "2026-10-01"
		_, res, err := svc.Refresh(ctx, sr.ID, RefreshOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Fallback || res.ModelChanged || res.Renumbered {
			t.Errorf("anime=%v: result = %+v, want a normal refresh", anime, res)
		}
		if got := airDate(t, svc, ctx, sr.ID, 1, 3); got != "2026-10-01" {
			t.Errorf("anime=%v: S01E03 air date = %q, want the refreshed one", anime, got)
		}
		if !svc.EpisodeExists(ctx, sr.ID, 1, 4) {
			t.Errorf("anime=%v: the new S01E04 should be added", anime)
		}
		if mustFile(t, svc, ctx, sr.ID, 1, 2) != file || svc.AbsoluteNumber(ctx, sr.ID, 1, 2) != absBefore {
			t.Errorf("anime=%v: S01E02's file or absolute changed", anime)
		}
	}
}

// For a standard show (season, episode) is the identity and files never move, so a season
// the fresh listing lacks — a legacy year-numbered one holding files, say — is no reason to
// stop refreshing the rest. It used to decline the refresh for as long as that season
// existed, with nothing the owner could do about it.
func TestRefreshStandardWithUnlistedSeasonStillRefreshes(t *testing.T) {
	d := standardDetails()
	d.NumberingSource, d.Seasons = "tvmaze", listing(2, 2)
	svc, fm, ctx := refreshTestService(t, d)
	sr, err := svc.Add(ctx, d.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	const f1, f2 = "/tv/S/Season 1/S01E01.mkv", "/tv/S/Season 2/S02E01.mkv"
	_ = svc.repo.SetEpisodeFile(ctx, sr.ID, 1, 1, f1, 1)
	_ = svc.repo.SetEpisodeFile(ctx, sr.ID, 2, 1, f2, 1)

	// Season 2 is gone from the listing; season 1's S01E02 has a new air date.
	fm.d.Seasons = listing(2)
	fm.d.Seasons[0].Episodes[1].AirDate = "2026-10-02"
	for _, allow := range []bool{false, true} {
		_, res, err := svc.Refresh(ctx, sr.ID, RefreshOptions{AllowRebuild: allow})
		if err != nil {
			t.Fatal(err)
		}
		if res.ModelChanged || res.Renumbered || res.Fallback {
			t.Errorf("allow=%v: result = %+v, want a plain refresh", allow, res)
		}
	}
	if got := airDate(t, svc, ctx, sr.ID, 1, 2); got != "2026-10-02" {
		t.Errorf("S01E02 air date = %q, want the refreshed one", got)
	}
	if mustFile(t, svc, ctx, sr.ID, 1, 1) != f1 || mustFile(t, svc, ctx, sr.ID, 2, 1) != f2 {
		t.Error("no file may move")
	}
	if !svc.repo.SeasonExists(ctx, sr.ID, 2) {
		t.Error("a season holding files is kept even when the listing drops it")
	}
	evs, _ := svc.Events(ctx, sr.ID, 20)
	for _, e := range evs {
		if e.Event == "numbering" {
			t.Errorf("unexpected numbering event: %q", e.Detail)
		}
	}
}

// Anime added while TVmaze numbered it (no TVDB key yet, or TVDB down) gets a key later.
// Moving onto TVDB is always an upgrade, so the owner's Refresh applies TVDB's model; the
// unattended ones only say so.
func TestRefreshAnimeFromTVmazeToTVDB(t *testing.T) {
	a := animeDetails()
	a.NumberingSource, a.Seasons = "tvmaze", listing(4)
	svc, fm, ctx := refreshTestService(t, a)
	sr, err := svc.Add(ctx, a.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	const file = "/tv/Anime Show/Season 1/Anime Show - S01E04.mkv"
	_ = svc.repo.SetEpisodeFile(ctx, sr.ID, 1, 4, file, 1)

	fm.d.NumberingSource, fm.d.Seasons = "tvdb", withAbsolutes(listing(2, 2))
	if _, res, _ := svc.Refresh(ctx, sr.ID, RefreshOptions{}); !res.ModelChanged || res.Renumbered {
		t.Fatalf("scheduled: result = %+v, want the change noticed but not applied", res)
	}
	got, res, err := svc.Refresh(ctx, sr.ID, RefreshOptions{AllowRebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Renumbered || len(res.Remaps) != 1 {
		t.Fatalf("manual: result = %+v, want one remap", res)
	}
	if mustFile(t, svc, ctx, sr.ID, 2, 2) != file {
		t.Error("the file should now belong to S02E02")
	}
	if got.NumberingSource != "tvdb" {
		t.Errorf("numbering_source = %q, want tvdb", got.NumberingSource)
	}
}
