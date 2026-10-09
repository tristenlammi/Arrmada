package quality

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// storeService is a quality service over a fully migrated temp database, so deletes
// and repairs run against the real movies/series/books/requests/grabs tables.
func storeService(t *testing.T) (*Service, *sql.DB, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewService(st.DB()), st.DB(), context.Background()
}

func mustProfile(t *testing.T, s *Service, ctx context.Context, media, name string) string {
	t.Helper()
	sp, err := s.Create(ctx, StoredProfile{MediaType: media, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return "custom:" + strconv.FormatInt(sp.ID, 10)
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// profileOf reads one row's stored ref.
func profileOf(t *testing.T, db *sql.DB, table string, id int64) string {
	t.Helper()
	var ref string
	if err := db.QueryRow(`SELECT quality_profile FROM `+table+` WHERE id = ?`, id).Scan(&ref); err != nil {
		t.Fatalf("%s %d: %v", table, id, err)
	}
	return ref
}

func profileID(ref string) int64 { id, _ := customID(ref); return id }

// seedOnProfile puts one row of every kind on ref (plus an approved request and an
// imported grab, which must not move).
func seedOnProfile(t *testing.T, db *sql.DB, movieRef, seriesRef, bookRef, musicRef string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO movies (id, tmdb_id, title, quality_profile) VALUES (1, 101, 'Heat', ?)`, movieRef)
	mustExec(t, db, `INSERT INTO movie_versions (id, movie_id, label, quality_profile) VALUES (1, 1, '4K', ?)`, movieRef)
	mustExec(t, db, `INSERT INTO series (id, tmdb_id, title, quality_profile) VALUES (1, 201, 'Show', ?)`, seriesRef)
	mustExec(t, db, `INSERT INTO books (id, ol_key, title, quality_profile) VALUES (1, 'OL1W', 'Dune', ?)`, bookRef)
	mustExec(t, db, `INSERT INTO artists (id, mbid, name, quality_profile) VALUES (1, 'mb1', 'Band', ?)`, musicRef)
	mustExec(t, db, `INSERT INTO requests (id, media_type, tmdb_id, title, status, quality_profile) VALUES (1, 'movie', 301, 'Pending', 'pending', ?)`, movieRef)
	mustExec(t, db, `INSERT INTO requests (id, media_type, tmdb_id, title, status, quality_profile) VALUES (2, 'movie', 302, 'Approved', 'approved', ?)`, movieRef)
	mustExec(t, db, `INSERT INTO grabs (id, movie_id, title, quality_profile, status, media_type) VALUES (1, 1, 'Heat.2160p', ?, 'grabbed', 'movie')`, movieRef)
	mustExec(t, db, `INSERT INTO grabs (id, movie_id, title, quality_profile, status, media_type) VALUES (2, 1, 'Heat.1080p', ?, 'imported', 'movie')`, movieRef)
}

// Deleting a profile moves its titles, versions, pending requests and in-flight grabs to
// the chosen target in one go, and hands over the default role.
func TestDeleteAndReassignMovesEverything(t *testing.T) {
	s, db, ctx := storeService(t)
	gone := mustProfile(t, s, ctx, MediaMovie, "Going")
	keep := mustProfile(t, s, ctx, MediaMovie, "Keeping")
	if err := s.SetDefaultProfile(ctx, MediaMovie, gone); err != nil {
		t.Fatal(err)
	}
	seedOnProfile(t, db, gone, gone, gone, gone)

	moved, to, err := s.Delete(ctx, profileID(gone), keep)
	if err != nil {
		t.Fatal(err)
	}
	if to != keep {
		t.Errorf("moved to %q, want %q", to, keep)
	}
	// Series, books and artists rows can't carry a movie profile in practice; seeding them
	// on it proves the update covers every table the ref can sit in.
	want := Reassigned{Movies: 1, Versions: 1, Series: 1, Books: 1, Artists: 1, Requests: 1, Grabs: 1}
	if moved != want {
		t.Errorf("moved = %+v, want %+v", moved, want)
	}
	for _, c := range []struct {
		table string
		id    int64
	}{{"movies", 1}, {"movie_versions", 1}, {"series", 1}, {"books", 1}, {"artists", 1}, {"requests", 1}, {"grabs", 1}} {
		if got := profileOf(t, db, c.table, c.id); got != keep {
			t.Errorf("%s %d on %q, want %q", c.table, c.id, got, keep)
		}
	}
	if got := profileOf(t, db, "requests", 2); got != gone {
		t.Errorf("an approved request moved to %q", got)
	}
	if got := profileOf(t, db, "grabs", 2); got != gone {
		t.Errorf("an imported grab moved to %q", got)
	}
	if got := s.DefaultProfile(ctx, MediaMovie); got != keep {
		t.Errorf("default = %q, want the target %q", got, keep)
	}
	if s.Known(ctx, gone) {
		t.Error("the profile is still there")
	}
}

// With no target named, titles go to the default — or, when the default is the one
// going, to the first other profile.
func TestDeletePicksDefaultTarget(t *testing.T) {
	s, _, ctx := storeService(t)
	a := mustProfile(t, s, ctx, MediaSeries, "A")
	b := mustProfile(t, s, ctx, MediaSeries, "B")
	if err := s.SetDefaultProfile(ctx, MediaSeries, b); err != nil {
		t.Fatal(err)
	}
	if _, to, err := s.Delete(ctx, profileID(a), ""); err != nil || to != b {
		t.Errorf("deleting a non-default: to=%q err=%v, want the default %q", to, err, b)
	}
	mustProfile(t, s, ctx, MediaSeries, "C")
	_, to, err := s.Delete(ctx, profileID(b), "")
	if err != nil {
		t.Fatal(err)
	}
	// Migrations may seed series profiles of their own; whichever sorts first that isn't
	// the deleted one is the answer.
	if to == b || to == "" {
		t.Errorf("deleting the default moved titles to %q", to)
	}
	if got := s.DefaultProfile(ctx, MediaSeries); got != to {
		t.Errorf("default = %q, want the target %q", got, to)
	}
}

// The last profile of a media type can't go: its titles would have nowhere real to land.
func TestDeleteRefusesLastProfile(t *testing.T) {
	s, _, ctx := storeService(t)
	list, err := s.ListStored(ctx, MediaMusic)
	if err != nil {
		t.Fatal(err)
	}
	// Whatever the migrations seeded for music, delete down to one.
	if len(list) == 0 {
		mustProfile(t, s, ctx, MediaMusic, "Only")
		list, _ = s.ListStored(ctx, MediaMusic)
	}
	for _, sp := range list[1:] {
		if _, _, err := s.Delete(ctx, sp.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.Delete(ctx, list[0].ID, ""); !errors.Is(err, ErrLastProfile) {
		t.Errorf("err = %v, want ErrLastProfile", err)
	}
	if _, err := s.GetStored(ctx, "custom:"+strconv.FormatInt(list[0].ID, 10)); err != nil {
		t.Errorf("the last profile was deleted: %v", err)
	}
}

// A target of another media type is refused and nothing changes.
func TestDeleteRefusesMediaMismatch(t *testing.T) {
	s, db, ctx := storeService(t)
	gone := mustProfile(t, s, ctx, MediaMovie, "Going")
	mustProfile(t, s, ctx, MediaMovie, "Other movie")
	show := mustProfile(t, s, ctx, MediaSeries, "Show profile")
	seedOnProfile(t, db, gone, show, "", "")

	if _, _, err := s.Delete(ctx, profileID(gone), show); !errors.Is(err, ErrMediaMismatch) {
		t.Fatalf("err = %v, want ErrMediaMismatch", err)
	}
	if !s.Known(ctx, gone) {
		t.Error("the profile was deleted despite the refusal")
	}
	if got := profileOf(t, db, "movies", 1); got != gone {
		t.Errorf("a movie moved to %q", got)
	}
	if _, _, err := s.Delete(ctx, profileID(gone), gone); !errors.Is(err, ErrSameProfile) {
		t.Errorf("err = %v, want ErrSameProfile", err)
	}
	if _, _, err := s.Delete(ctx, profileID(gone), "custom:9999"); !errors.Is(err, ErrTargetNotFound) {
		t.Errorf("err = %v, want ErrTargetNotFound", err)
	}
	if _, _, err := s.Delete(ctx, 9999, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// Boot repair points refs at deleted profiles to their media's default and leaves "n/a",
// "" and live refs alone. A second run changes nothing.
func TestRepairDanglingRefs(t *testing.T) {
	s, db, ctx := storeService(t)
	movieDef := mustProfile(t, s, ctx, MediaMovie, "Movie default")
	seriesDef := mustProfile(t, s, ctx, MediaSeries, "Series default")
	if err := s.SetDefaultProfile(ctx, MediaMovie, movieDef); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultProfile(ctx, MediaSeries, seriesDef); err != nil {
		t.Fatal(err)
	}
	other := mustProfile(t, s, ctx, MediaMovie, "Live")
	mustExec(t, db, `INSERT INTO movies (id, tmdb_id, title, quality_profile) VALUES (1, 1, 'Dangling', 'custom:99')`)
	mustExec(t, db, `INSERT INTO movies (id, tmdb_id, title, quality_profile) VALUES (2, 2, 'Scanned', 'n/a')`)
	mustExec(t, db, `INSERT INTO movies (id, tmdb_id, title, quality_profile) VALUES (3, 3, 'Blank', '')`)
	mustExec(t, db, `INSERT INTO movies (id, tmdb_id, title, quality_profile) VALUES (4, 4, 'Live', ?)`, other)
	mustExec(t, db, `INSERT INTO series (id, tmdb_id, title, quality_profile) VALUES (1, 1, 'Show', 'custom:99')`)
	mustExec(t, db, `INSERT INTO requests (id, media_type, tmdb_id, title, quality_profile) VALUES (1, 'series', 5, 'Asked', 'custom:99')`)
	mustExec(t, db, `INSERT INTO grabs (id, movie_id, title, quality_profile, media_type) VALUES (1, 1, 'Old', 'custom:99', '')`)

	fixed, err := s.RepairDanglingRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fixed["movies"] != 1 || fixed["series"] != 1 || fixed["requests"] != 1 || fixed["grabs"] != 1 {
		t.Errorf("fixed = %v", fixed)
	}
	for _, c := range []struct {
		table string
		id    int64
		want  string
	}{
		{"movies", 1, movieDef}, {"movies", 2, "n/a"}, {"movies", 3, ""}, {"movies", 4, other},
		{"series", 1, seriesDef}, {"requests", 1, seriesDef}, {"grabs", 1, movieDef},
	} {
		if got := profileOf(t, db, c.table, c.id); got != c.want {
			t.Errorf("%s %d = %q, want %q", c.table, c.id, got, c.want)
		}
	}
	again, err := s.RepairDanglingRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("second run changed %v", again)
	}
}
