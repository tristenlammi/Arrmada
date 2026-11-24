package series

import "testing"

// The one language escape an episode has taken (#137, episodes.lang_escaped) follows its
// file through a renumber, as the upgrade hold does: rebuilt under its new number, the
// episode must not read as never having escaped and take a second one.
func TestRebuildCarriesTheLanguageEscape(t *testing.T) {
	svc, fm, id := renumberFixture(t)
	ctx := t.Context()
	if _, err := svc.repo.db.ExecContext(ctx,
		`UPDATE episodes SET lang_escaped = 1 WHERE series_id = ? AND season_number = 1 AND episode_number = 3`, id); err != nil {
		t.Fatal(err)
	}
	seasons := seasonsFromDetails(&fm.d, func(int) bool { return true })
	if _, err := svc.repo.RebuildEpisodes(ctx, id, seasons); err != nil {
		t.Fatal(err)
	}
	escaped := func(season, episode int) bool {
		t.Helper()
		var n int
		if err := svc.repo.db.QueryRowContext(ctx,
			`SELECT lang_escaped FROM episodes WHERE series_id = ? AND season_number = ? AND episode_number = ?`,
			id, season, episode).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n != 0
	}
	// Absolute 3 (S01E03, with its file) is now S02E01; absolute 4 is S02E02.
	if got := mustFile(t, svc, ctx, id, 2, 1); got != "/tv/A/Season 1/A - S01E03.mkv" {
		t.Fatalf("S02E01 holds %q, want the old S01E03 file", got)
	}
	if !escaped(2, 1) {
		t.Error("the escaped episode lost its record in the renumber")
	}
	if escaped(2, 2) {
		t.Error("the episode that never escaped reads as escaped after the renumber")
	}
}
