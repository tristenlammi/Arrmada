package insights

import (
	"context"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/plex"
)

func addPlay(t *testing.T, s *Service, rec sessionRecord) {
	t.Helper()
	if _, err := s.repo.insertSession(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
}

// A film's plays are found by its rating key and, for plays under an older key or from an
// imported history, by title and year — never a same-named remake. Two people, newest
// first, with their plays counted.
func TestWatchedByAggregation(t *testing.T) {
	s := newLinkService(t, "http://plex.invalid:32400")
	ctx := context.Background()
	if err := s.repo.upsertUser(ctx, "11", "Mum", "", 0); err != nil {
		t.Fatal(err)
	}
	// Live plays under the current key.
	addPlay(t, s, sessionRecord{SessionKey: "a", UserID: "11", UserName: "mum_old", RatingKey: "500", MediaType: "movie", Title: "Heat", Year: 1995, StartedAt: 1000})
	addPlay(t, s, sessionRecord{SessionKey: "b", UserID: "11", UserName: "mum_old", RatingKey: "500", MediaType: "movie", Title: "Heat", Year: 1995, StartedAt: 3000})
	// An imported play from before Plex re-added the film under a new key.
	addPlay(t, s, sessionRecord{UserID: "22", UserName: "Dad", RatingKey: "77", MediaType: "movie", Title: "heat", Year: 1995, StartedAt: 2000, WatchedMS: 60000})
	// Not this film: the 1986 one, an episode with the same name, a different key.
	addPlay(t, s, sessionRecord{SessionKey: "c", UserID: "33", UserName: "Kid", RatingKey: "900", MediaType: "movie", Title: "Heat", Year: 1986, StartedAt: 4000})
	addPlay(t, s, sessionRecord{SessionKey: "d", UserID: "33", UserName: "Kid", RatingKey: "901", MediaType: "episode", Title: "Heat", GrandparentTitle: "Some Show", Year: 1995, StartedAt: 5000})

	s.links.idx = &plexIndex{
		machineID:   "m",
		movieByTMDB: map[int]plex.Item{949: {RatingKey: "500", Type: "movie", Title: "Heat", Year: 1995}},
		builtAt:     time.Now(),
	}
	ws, err := s.WatchStats(ctx, "movie", ExternalIDs{TMDB: 949}, "Heat", 1995)
	if err != nil {
		t.Fatal(err)
	}
	if !ws.Available || ws.Plays != 3 || ws.LastPlayed != 3000 || len(ws.Users) != 2 {
		t.Fatalf("stats = %+v", ws)
	}
	if u := ws.Users[0]; u.ID != "11" || u.Name != "Mum" || u.Plays != 2 || u.LastPlayed != 3000 {
		t.Errorf("first user = %+v (the known account name wins)", u)
	}
	if u := ws.Users[1]; u.ID != "22" || u.Name != "Dad" || u.Plays != 1 {
		t.Errorf("second user = %+v", u)
	}

	// Not in the index: the title and year still find every play of it.
	s.links.idx = nil
	if ws, _ := s.WatchStats(ctx, "movie", ExternalIDs{TMDB: 949}, "Heat", 1995); ws.Plays != 3 {
		t.Errorf("by title alone: %+v", ws)
	}
	// No year: nothing to tell it from a remake, so no title match at all.
	if ws, _ := s.WatchStats(ctx, "movie", ExternalIDs{TMDB: 949}, "Heat", 0); ws.Plays != 0 {
		t.Errorf("without a year: %+v", ws)
	}
}

func TestWatchedByShowUsesGrandparent(t *testing.T) {
	s := newLinkService(t, "http://plex.invalid:32400")
	ctx := context.Background()
	for i, rec := range []sessionRecord{
		{UserID: "11", UserName: "Mum", MediaType: "episode", Title: "Pilot", GrandparentTitle: "The Bear", RatingKey: "1"},
		{UserID: "11", UserName: "Mum", MediaType: "episode", Title: "Hands", GrandparentTitle: "The Bear", RatingKey: "2"},
		{UserID: "22", UserName: "Dad", MediaType: "episode", Title: "Pilot", GrandparentTitle: "THE BEAR", RatingKey: "1"},
		{UserID: "33", UserName: "Kid", MediaType: "episode", Title: "Pilot", GrandparentTitle: "The Bear Cub", RatingKey: "3"},
		{UserID: "33", UserName: "Kid", MediaType: "movie", Title: "The Bear", Year: 1988, RatingKey: "4"},
	} {
		rec.SessionKey = string(rune('a' + i))
		rec.StartedAt = int64(1000 * (i + 1))
		addPlay(t, s, rec)
	}
	ws, err := s.WatchStats(ctx, "series", ExternalIDs{TMDB: 136315}, "The Bear", 2022)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Plays != 3 || len(ws.Users) != 2 || ws.Users[0].Name != "Dad" || ws.Users[1].Plays != 2 {
		t.Fatalf("show stats = %+v", ws)
	}
}

func TestWatchStatsWithoutPlex(t *testing.T) {
	s := newLinkService(t, "")
	ws, err := s.WatchStats(context.Background(), "movie", ExternalIDs{TMDB: 1}, "Heat", 1995)
	if err != nil || ws.Available || ws.Users == nil {
		t.Fatalf("stats without Plex = %+v, %v", ws, err)
	}
}
