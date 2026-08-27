package series

import (
	"context"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/store"
)

// bleachFixture builds the real shape of the problem: sixteen seasons of the 2004 run,
// then season 17 holding the whole Thousand-Year Blood War numbered continuously, with
// the six-month broadcast breaks between its cours.
//
// Cour 1 = E1–13 (Oct–Dec 2022), cour 2 = E14–26 (Jul–Sep 2023),
// cour 3 = E27–39 (Jul–Sep 2024), cour 4 = E40–52 (2025).
func bleachFixture(t *testing.T) (*Service, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB()
	ctx := context.Background()

	db.ExecContext(ctx, `INSERT INTO series (id,tmdb_id,title,series_type,monitored) VALUES (1,30984,'Bleach','anime',1)`)
	// A couple of original-run seasons, so "S04E04" has a real season 4 to be confused with.
	for season, n := range map[int]int{1: 20, 2: 21, 3: 22, 4: 28} {
		db.ExecContext(ctx, `INSERT INTO seasons (series_id,season_number) VALUES (1,?)`, season)
		for ep := 1; ep <= n; ep++ {
			db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number,air_date) VALUES (1,?,?,'2005-01-04')`, season, ep)
		}
	}
	db.ExecContext(ctx, `INSERT INTO seasons (series_id,season_number) VALUES (1,17)`)
	cours := []struct {
		from, to int
		date     string
	}{
		{1, 13, "2022-10-11"}, {14, 26, "2023-07-08"},
		{27, 39, "2024-07-06"}, {40, 52, "2025-07-05"},
	}
	for _, c := range cours {
		for ep := c.from; ep <= c.to; ep++ {
			db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number,air_date) VALUES (1,17,?,?)`, ep, c.date)
		}
	}
	svc := &Service{repo: NewRepo(db), log: slog.Default()}
	if err := svc.repo.BackfillAbsolute(ctx, 1); err != nil {
		t.Fatal(err)
	}
	return svc, ctx
}

// The three conventions seen in one real TorrentLeech page, all for the same arc, all
// of which have to land on the right TMDB episode.
func TestAliasResolvesEveryBleachConvention(t *testing.T) {
	svc, ctx := bleachFixture(t)
	if _, err := svc.AddAlias(ctx, 1, "BLEACH Thousand-Year Blood War", 17); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddAlias(ctx, 1, "Bleach - Sennen Kessen Hen", 17); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		release string
		wantEp  int
		why     string
	}{
		// Per-cour seasons: cour 2 starts at E14, so its second episode is E15 — the
		// episode that started all this.
		{"BLEACH Thousand-Year Blood War S02E02 1080p WEB h264-QUiNTESSENCE", 15, "cour 2 episode 2"},
		{"BLEACH Thousand-Year Blood War S02E06 1080p WEB h264-QUiNTESSENCE", 19, "cour 2 episode 6"},
		{"BLEACH Thousand-Year Blood War S01E01 1080p WEB h264-QUiNTESSENCE", 1, "cour 1 episode 1"},
		{"BLEACH Thousand-Year Blood War S04E04 1080p WEB h264-QUiNTESSENCE", 43, "cour 4 episode 4"},
		// One season, continuous numbering: cour 1 has no episode 41, so 41 is read as
		// an index into season 17 instead of being dropped.
		{"BLEACH Thousand Year Blood War S01E41 1080p WEBRip x265-Xiangliu", 41, "continuous numbering"},
		// Absolute, romaji title, no season at all.
		{"[SubsPlease] Bleach - Sennen Kessen Hen - 45 (1080p) [70D690AF]", 45, "absolute within the arc"},
		{"[SubsPlease] Bleach - Sennen Kessen Hen - 15 (1080p) [ABCD1234]", 15, "absolute within the arc"},
	} {
		refs, ok := svc.AliasEpisodes(ctx, 1, parser.Parse(tc.release))
		if !ok || len(refs) != 1 {
			t.Errorf("%s (%s): resolved to %+v ok=%v, want one episode", tc.release, tc.why, refs, ok)
			continue
		}
		if refs[0].Season != 17 || refs[0].Episode != tc.wantEp {
			t.Errorf("%s (%s): resolved to S%02dE%02d, want S17E%02d",
				tc.release, tc.why, refs[0].Season, refs[0].Episode, tc.wantEp)
		}
	}
}

// The safety property the whole design rests on: a release under the series' REAL
// title is untouched. "Bleach S04E04" is a 2005 episode and must not be dragged into
// the arc just because an alias pins season 17.
func TestAliasLeavesTheRealTitleAlone(t *testing.T) {
	svc, ctx := bleachFixture(t)
	if _, err := svc.AddAlias(ctx, 1, "BLEACH Thousand-Year Blood War", 17); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"Bleach S04E04 1080p BluRay x264-GRP",
		"[HorribleSubs] Bleach - 63 [1080p]",
		"Bleach S04 1080p BluRay x264-GRP",
	} {
		if refs, ok := svc.AliasEpisodes(ctx, 1, parser.Parse(rel)); ok {
			t.Errorf("%q was claimed by the alias (%+v) — it uses the series' own title", rel, refs)
		}
	}
}

// A series with no aliases must behave exactly as it did before this existed.
func TestNoAliasesIsInert(t *testing.T) {
	svc, ctx := bleachFixture(t)
	for _, rel := range []string{
		"BLEACH Thousand-Year Blood War S02E02 1080p WEB h264-QUiNTESSENCE",
		"Bleach S04E04 1080p BluRay x264-GRP",
	} {
		if _, ok := svc.AliasEpisodes(ctx, 1, parser.Parse(rel)); ok {
			t.Errorf("%q resolved through an alias, but none are configured", rel)
		}
	}
}

// A title-only alias (season 0) makes the release match the series without claiming
// its numbering — for a show released under a translated name with normal numbering.
func TestTitleOnlyAliasDoesNotTouchNumbering(t *testing.T) {
	svc, ctx := bleachFixture(t)
	if _, err := svc.AddAlias(ctx, 1, "Bleach - Sennen Kessen Hen", 0); err != nil {
		t.Fatal(err)
	}
	if a, ok := svc.AliasFor(ctx, 1, "[SubsPlease] Bleach - Sennen Kessen Hen - 45 (1080p)"); !ok || a.TMDBSeason != 0 {
		t.Fatalf("alias lookup = %+v ok=%v, want a season-0 match", a, ok)
	}
	if _, ok := svc.AliasEpisodes(ctx, 1, parser.Parse("[SubsPlease] Bleach - Sennen Kessen Hen - 45 (1080p)")); ok {
		t.Error("a title-only alias claimed the numbering — it must leave that to the normal resolver")
	}
}

// Cour boundaries are discovered from air dates, never hardcoded.
func TestSeasonCoursComeFromAirDates(t *testing.T) {
	svc, ctx := bleachFixture(t)
	groups := svc.seasonCours(ctx, 1, 17)
	if len(groups) != 4 {
		t.Fatalf("found %d cours in season 17, want 4", len(groups))
	}
	for i, want := range []struct{ first, last, n int }{
		{1, 13, 13}, {14, 26, 13}, {27, 39, 13}, {40, 52, 13},
	} {
		g := groups[i]
		if len(g) != want.n || g[0].Episode != want.first || g[len(g)-1].Episode != want.last {
			t.Errorf("cour %d = E%d–E%d (%d eps), want E%d–E%d (%d)",
				i+1, g[0].Episode, g[len(g)-1].Episode, len(g), want.first, want.last, want.n)
		}
	}
}

// Guard rails on what can be added.
func TestAddAliasRejectsNonsense(t *testing.T) {
	svc, ctx := bleachFixture(t)
	if _, err := svc.AddAlias(ctx, 1, "   ", 17); err == nil {
		t.Error("an empty alias was accepted")
	}
	if _, err := svc.AddAlias(ctx, 1, "Bleach", 17); err == nil {
		t.Error("the series' own title was accepted as an alias")
	}
	if _, err := svc.AddAlias(ctx, 1, "Bleach Thousand-Year Blood War", 99); err == nil {
		t.Error("a season the series doesn't have was accepted")
	}
}

// Re-adding an alias corrects its season rather than erroring — the user is fixing a
// mistake, and a duplicate-key error would read as "it's already right".
func TestReAddingAnAliasUpdatesIt(t *testing.T) {
	svc, ctx := bleachFixture(t)
	if _, err := svc.AddAlias(ctx, 1, "BLEACH Thousand-Year Blood War", 4); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddAlias(ctx, 1, "BLEACH Thousand-Year Blood War", 17); err != nil {
		t.Fatal(err)
	}
	aliases := svc.Aliases(ctx, 1)
	if len(aliases) != 1 {
		t.Fatalf("got %d aliases, want 1 corrected row: %+v", len(aliases), aliases)
	}
	if aliases[0].TMDBSeason != 17 {
		t.Errorf("season = %d, want the corrected 17", aliases[0].TMDBSeason)
	}
}
