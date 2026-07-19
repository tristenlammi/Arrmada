package series

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// TestSetMonitoredCascades is the guard for "monitored the show but it grabs nothing":
// the search only picks episodes with monitored = 1, so toggling the series flag has to
// reach the episodes. Specials stay out of it when enabling; disabling covers all.
func TestSetMonitoredCascades(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	ctx := context.Background()
	repo := NewRepo(db)

	db.ExecContext(ctx, `INSERT INTO series (id,tmdb_id,title,monitored) VALUES (1,7,'Dandadan',0)`)
	for _, sn := range []int{0, 1} {
		db.ExecContext(ctx, `INSERT INTO seasons (series_id,season_number,monitored) VALUES (1,?,0)`, sn)
		db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number,air_date,monitored) VALUES (1,?,1,'2024-10-04',0)`, sn)
	}

	count := func(where string) int {
		var n int
		db.QueryRowContext(ctx, `SELECT COUNT(*) FROM episodes WHERE series_id = 1 AND `+where).Scan(&n)
		return n
	}

	if err := repo.SetMonitored(ctx, 1, true); err != nil {
		t.Fatal(err)
	}
	if got := count(`season_number > 0 AND monitored = 1`); got != 1 {
		t.Errorf("regular episodes monitored = %d, want 1 (the cascade didn't reach episodes)", got)
	}
	if got := count(`season_number = 0 AND monitored = 1`); got != 0 {
		t.Errorf("specials monitored = %d, want 0 (enabling must not pull in specials)", got)
	}

	// Disabling must switch everything off, specials included.
	if err := repo.SetMonitored(ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := count(`monitored = 1`); got != 0 {
		t.Errorf("%d episodes still monitored after disabling the show, want 0", got)
	}
}
