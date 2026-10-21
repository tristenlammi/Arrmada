package insights

import (
	"context"
	"fmt"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

func seedRows(t testing.TB, s *Service, rows ...sessionRecord) {
	t.Helper()
	for _, r := range rows {
		if _, err := s.repo.insertSession(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
}

// User and date filters narrow History, and the total counts what they match.
func TestHistoryDateFilter(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)
	const day = 86400
	base := int64(1_760_000_000)
	seedRows(t, s,
		sessionRecord{UserID: "1", UserName: "Amy", RatingKey: "10", MediaType: "movie", Title: "Old", StartedAt: base - 10*day, StoppedAt: base - 10*day + 3600},
		sessionRecord{UserID: "1", UserName: "Amy", RatingKey: "11", MediaType: "movie", Title: "Recent", StartedAt: base - day, StoppedAt: base - day + 3600},
		sessionRecord{UserID: "2", UserName: "Ben", RatingKey: "12", MediaType: "movie", Title: "Ben's", StartedAt: base - day, StoppedAt: base - day + 3600},
		sessionRecord{UserID: "1", UserName: "Amy", RatingKey: "13", MediaType: "movie", Title: "Edge", StartedAt: base, StoppedAt: base + 3600},
	)
	res, err := s.History(ctx, HistoryFilter{UserID: "1", From: base - 7*day, To: base})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Rows) != 1 || res.Rows[0].Title != "Recent" {
		t.Fatalf("last 7 days for Amy, to exclusive = %+v (total %d)", res.Rows, res.Total)
	}
	res, _ = s.History(ctx, HistoryFilter{From: base - 7*day})
	if res.Total != 3 {
		t.Errorf("from only: total %d, want 3", res.Total)
	}
	res, _ = s.History(ctx, HistoryFilter{To: base - 5*day})
	if res.Total != 1 || res.Rows[0].Title != "Old" {
		t.Errorf("to only: %+v", res.Rows)
	}
}

// Sittings of the same item by the same person within three hours fold into one row with
// their summed watch time; an overnight gap starts a new one; the total counts rows.
func TestHistoryGrouping(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)
	const h = 3600
	base := int64(1_760_000_000)
	seedRows(t, s,
		// Amy: a film in two sittings 20 minutes apart → one row, 2 sessions.
		sessionRecord{UserID: "1", UserName: "Amy", RatingKey: "100", MediaType: "movie", Title: "Dune", DurationMS: 9000_000,
			StartedAt: base, StoppedAt: base + h, ViewOffsetMS: 3600_000},
		sessionRecord{UserID: "1", UserName: "Amy", RatingKey: "100", MediaType: "movie", Title: "Dune", DurationMS: 9000_000,
			StartedAt: base + h + 20*60, StoppedAt: base + 2*h + 20*60, ViewOffsetMS: 7200_000, BufferCount: 2},
		// ...then the rest the next morning, 5 hours later → its own row.
		sessionRecord{UserID: "1", UserName: "Amy", RatingKey: "100", MediaType: "movie", Title: "Dune", DurationMS: 9000_000,
			StartedAt: base + 7*h + 20*60, StoppedAt: base + 8*h, ViewOffsetMS: 9000_000},
		// Ben watching the same film in between is someone else's viewing.
		sessionRecord{UserID: "2", UserName: "Ben", RatingKey: "100", MediaType: "movie", Title: "Dune",
			StartedAt: base + 30*60, StoppedAt: base + h},
		// Two imports without a rating key are different items, not one.
		sessionRecord{UserID: "1", UserName: "Amy", MediaType: "movie", Title: "Arrival", StartedAt: base - 2*h, StoppedAt: base - h},
		sessionRecord{UserID: "1", UserName: "Amy", MediaType: "movie", Title: "Heat", StartedAt: base - h + 60, StoppedAt: base - 60},
	)
	res, err := s.History(ctx, HistoryFilter{Group: true, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 5 || len(res.Rows) != 5 {
		t.Fatalf("grouped total %d rows %d, want 5: %+v", res.Total, len(res.Rows), res.Rows)
	}
	morning, evening := res.Rows[0], res.Rows[2]
	if morning.Title != "Dune" || morning.Sessions != 1 || morning.UserID != "1" {
		t.Errorf("newest row = %+v", morning)
	}
	if res.Rows[1].UserID != "2" {
		t.Errorf("second row should be Ben's: %+v", res.Rows[1])
	}
	if evening.Sessions != 2 || evening.WatchedSecs != 2*h || evening.StartedAt != base || evening.StoppedAt != base+2*h+20*60 {
		t.Errorf("evening viewing = sessions %d watched %d span %d..%d", evening.Sessions, evening.WatchedSecs, evening.StartedAt, evening.StoppedAt)
	}
	if evening.ProgressPct != 80 || evening.BufferCount != 2 {
		t.Errorf("evening progress %d%% buffers %d, want the furthest point (80%%) and summed buffers", evening.ProgressPct, evening.BufferCount)
	}

	// Paging: the total stays the viewing count, and a page past the end still says it.
	page, _ := s.History(ctx, HistoryFilter{Group: true, Limit: 2, Offset: 4})
	if page.Total != 5 || len(page.Rows) != 1 {
		t.Errorf("last page: total %d rows %d", page.Total, len(page.Rows))
	}
	past, err := s.History(ctx, HistoryFilter{Group: true, Limit: 2, Offset: 10})
	if err != nil || past.Total != 5 || len(past.Rows) != 0 {
		t.Errorf("past the end: total %d rows %d err %v", past.Total, len(past.Rows), err)
	}
	// Filters apply to the sittings before grouping.
	amy, _ := s.History(ctx, HistoryFilter{Group: true, UserID: "1", Limit: 50})
	if amy.Total != 4 {
		t.Errorf("Amy grouped total %d, want 4", amy.Total)
	}
	// Ungrouped, every sitting is its own row.
	flat, _ := s.History(ctx, HistoryFilter{Limit: 50})
	if flat.Total != 6 || flat.Rows[0].Sessions != 0 {
		t.Errorf("ungrouped total %d, sessions %d", flat.Total, flat.Rows[0].Sessions)
	}
}

// The export walks every matching play, not one page.
func TestHistoryExportIgnoresPaging(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)
	for i := 0; i < 260; i++ {
		seedRows(t, s, sessionRecord{UserID: "1", RatingKey: fmt.Sprint(i), MediaType: "movie", Title: "T", StartedAt: int64(1000 + i*10), StoppedAt: int64(1005 + i*10)})
	}
	n := 0
	if err := s.HistoryExport(ctx, HistoryFilter{Limit: 5, Offset: 100}, func(HistoryEntry) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	if n != 260 {
		t.Errorf("exported %d rows, want 260", n)
	}
}

// BenchmarkHistoryGrouped runs one grouped page over 10k and 100k seeded plays. A window
// function over every play put it near half a second at 100k; reading the starts marked
// by migration 0308 it is a few milliseconds at either size. (Grouping with other filters
// still takes the window path: around 0.45 s when 100k plays match.)
func BenchmarkHistoryGrouped(b *testing.B) {
	for _, n := range []int{10_000, 100_000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			ctx := context.Background()
			s := newDataTestService(b)
			err := store.WithTx(ctx, s.repo.db, func(tx *store.Transaction) error {
				stmt, err := tx.PrepareContext(ctx, `INSERT INTO stream_sessions (session_key,user_id,user_name,rating_key,media_type,title,started_at,stopped_at)
					VALUES ('',?,?,?,'movie','T',?,?)`)
				if err != nil {
					return err
				}
				defer stmt.Close()
				for i := 0; i < n; i++ {
					start := int64(1_700_000_000 + i*600)
					if _, err := stmt.ExecContext(ctx, fmt.Sprint(i%12), "u", fmt.Sprint(i%3000), start, start+1800); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.History(ctx, HistoryFilter{Group: true, Limit: 50}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
