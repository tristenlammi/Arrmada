package series

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// TestAcquisitionSummary checks the wanted/upcoming split: aired monitored episodes
// with no file count as "searching"; the soonest unaired monitored episode is the
// "upcoming" one; unmonitored and already-filed episodes are ignored.
func TestAcquisitionSummary(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `INSERT INTO series (id,tmdb_id,title,year,monitored) VALUES (1,7,'Test Show',2020,1)`); err != nil {
		t.Fatal(err)
	}
	// Two aired, monitored, missing episodes → searching = 2.
	db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number,air_date,monitored,has_file) VALUES (1,1,1,'2020-01-01',1,0)`)
	db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number,air_date,monitored,has_file) VALUES (1,1,2,'2020-01-08',1,0)`)
	// Aired but already have the file → not searching.
	db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number,air_date,monitored,has_file) VALUES (1,1,3,'2020-01-15',1,1)`)
	// Aired but unmonitored → still counts (a downloaded file should import regardless),
	// so searching stays at 2 only if we DON'T count it. We deliberately exclude
	// unmonitored from the wanted view, since the user isn't asking us to find it.
	db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number,air_date,monitored,has_file) VALUES (1,1,4,'2020-01-22',0,0)`)
	// Two future monitored episodes → upcoming picks the soonest.
	db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number,air_date,monitored,has_file) VALUES (1,2,1,'2999-02-01',1,0)`)
	db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number,air_date,monitored,has_file) VALUES (1,2,2,'2999-03-01',1,0)`)

	repo := NewRepo(db)
	out, err := repo.AcquisitionSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d series, want 1", len(out))
	}
	a := out[0]
	if a.SearchingCount != 2 {
		t.Errorf("SearchingCount = %d, want 2", a.SearchingCount)
	}
	if a.NextAir != "2999-02-01" {
		t.Errorf("NextAir = %q, want 2999-02-01", a.NextAir)
	}
	if a.NextLabel != "S02E01" {
		t.Errorf("NextLabel = %q, want S02E01", a.NextLabel)
	}

	// An unmonitored series is excluded entirely.
	db.ExecContext(ctx, `INSERT INTO series (id,tmdb_id,title,monitored) VALUES (2,8,'Off',0)`)
	db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number,air_date,monitored,has_file) VALUES (2,1,1,'2020-01-01',1,0)`)
	out, _ = repo.AcquisitionSummary(ctx)
	if len(out) != 1 {
		t.Errorf("unmonitored series should be excluded; got %d", len(out))
	}
}
