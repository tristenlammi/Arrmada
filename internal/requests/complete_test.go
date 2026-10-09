package requests

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// seedShow puts a show in the library: per season, whether the season is monitored and
// its episodes as "F" (aired, has a file), "M" (aired, missing) or "U" (not aired yet).
// Every episode is monitored; an unmonitored season is the owner's choice not to want it.
func seedShow(t *testing.T, db *sql.DB, tmdbID int, seasons map[int]struct {
	monitored bool
	eps       string
}) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO series (tmdb_id, title, monitored) VALUES (?, 'Show', 1)`, tmdbID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	past := time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	future := time.Now().AddDate(0, 1, 0).Format("2006-01-02")
	for sn, s := range seasons {
		if _, err := db.Exec(`INSERT INTO seasons (series_id, season_number, monitored) VALUES (?, ?, ?)`, id, sn, s.monitored); err != nil {
			t.Fatal(err)
		}
		for i, c := range s.eps {
			air, file := past, 0
			switch c {
			case 'F':
				file = 1
			case 'U':
				air = future
			}
			if _, err := db.Exec(`INSERT INTO episodes (series_id, season_number, episode_number, title, air_date, monitored, has_file)
				VALUES (?, ?, ?, ?, ?, 1, ?)`, id, sn, i+1, fmt.Sprintf("E%d", i+1), air, file); err != nil {
				t.Fatal(err)
			}
		}
	}
	return id
}

// A show whose first two seasons nobody monitors is complete once the season that is
// wanted is in: the request card says Ready at the same moment the ready notice goes out
// (the card used to count every aired episode and stay 'Partly ready'). A wanted episode
// still missing keeps both waiting; one that hasn't aired doesn't.
func TestTrackAndNotifierAgree(t *testing.T) {
	type season = struct {
		monitored bool
		eps       string
	}
	cases := []struct {
		name    string
		seasons map[int]season
		ready   bool
	}{
		{"older seasons unmonitored", map[int]season{1: {false, "MMMM"}, 2: {false, "MMMM"}, 3: {true, "FFFF"}}, true},
		{"a wanted episode missing", map[int]season{1: {true, "FFFM"}}, false},
		{"next episode not out yet", map[int]season{1: {true, "FFFU"}}, true},
		{"nothing on disk", map[int]season{1: {true, "MMMM"}}, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, db, ctx := bookLinkFixture(t, catalogue{byKey: map[string]metadata.BookResult{}})
			tmdb := 500 + i
			sid := seedShow(t, db, tmdb, tc.seasons)
			req, err := s.repo.Create(ctx, Request{MediaType: "series", TMDBID: tmdb, Title: "Show", Status: StatusApproved, RequestedBy: 7})
			if err != nil {
				t.Fatal(err)
			}

			// The card.
			list, _, err := s.List(ctx, ListFilter{})
			if err != nil || len(list) != 1 {
				t.Fatalf("list: %v %d", err, len(list))
			}
			s.Track(ctx, list, nil, true)
			cardReady := list[0].Tracking.Stage == StageAvailable

			// The sweep's view, and the import event's.
			ready, err := s.readyNow(ctx, []Request{req})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.NotifySeriesReady(ctx, sid); err != nil {
				t.Fatal(err)
			}
			inbox, _ := s.repo.listUserNotifications(ctx, 7)
			told := len(inbox) == 1

			if cardReady != tc.ready || ready[req.ID] != tc.ready || told != tc.ready {
				t.Errorf("card ready %v (stage %s), sweep ready %v, notified %v; want all %v",
					cardReady, list[0].Tracking.Stage, ready[req.ID], told, tc.ready)
			}
		})
	}
}

// seriesComplete: something on disk, and all of it.
func TestSeriesComplete(t *testing.T) {
	for _, c := range []struct {
		have, total int
		want        bool
	}{{0, 0, false}, {0, 5, false}, {3, 5, false}, {5, 5, true}, {6, 5, true}, {2, 0, true}} {
		if got := seriesComplete(c.have, c.total); got != c.want {
			t.Errorf("seriesComplete(%d, %d) = %v, want %v", c.have, c.total, got, c.want)
		}
	}
}
