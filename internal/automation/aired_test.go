package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/series"
)

// An episode with no air date is UNAIRED and must not be searched for.
//
// It used to count as aired, on the theory that odd metadata shouldn't block a grab. The
// opposite turned out to be far more common: TMDB pads a season with placeholder episodes
// carrying no date, so a fully-imported show kept re-grabbing its own complete pack —
// ARK: The Animated Series lists 14 episodes for a season that aired 6. Worse, nothing
// could stop the loop, because SeasonHasMissing already excluded dateless episodes and so
// never reported the season as incomplete.
func TestDatelessEpisodesAreUnaired(t *testing.T) {
	if aired("") {
		t.Error("an episode with no air date must be treated as unaired")
	}
	if !aired("2024-03-21") {
		t.Error("a past air date is aired")
	}
	if !aired(nowDate()) {
		t.Error("an episode airing today is aired")
	}
	if aired("2999-01-01") {
		t.Error("a future air date is not aired")
	}
}

// wantedEpisodes drives what the searcher hunts for, and must apply the same rule — this
// is the path that was grabbing ARK's complete pack forever.
func TestWantedEpisodesSkipsDatelessPlaceholders(t *testing.T) {
	s := seriesWithEpisodes(
		episodeSpec{season: 1, episode: 1, airDate: "2024-03-21", hasFile: true},
		episodeSpec{season: 1, episode: 6, airDate: "2024-03-21", hasFile: true},
		episodeSpec{season: 1, episode: 7, airDate: "", hasFile: false}, // TMDB placeholder
		episodeSpec{season: 1, episode: 8, airDate: "", hasFile: false}, // TMDB placeholder
	)
	want, _ := wantedEpisodes(s)
	if len(want) != 0 {
		t.Errorf("placeholders should not be wanted, got %+v", want)
	}

	// A genuinely missing, aired episode is still wanted.
	s2 := seriesWithEpisodes(
		episodeSpec{season: 1, episode: 1, airDate: "2024-03-21", hasFile: false},
		episodeSpec{season: 1, episode: 7, airDate: "", hasFile: false},
	)
	want2, _ := wantedEpisodes(s2)
	if len(want2) != 1 || want2[0] != (epKey{1, 1}) {
		t.Errorf("want just S01E01, got %+v", want2)
	}
}

// episodeSpec is the minimum needed to build a series for these tests.
type episodeSpec struct {
	season  int
	episode int
	airDate string
	hasFile bool
}

func seriesWithEpisodes(specs ...episodeSpec) series.Series {
	bySeason := map[int][]series.Episode{}
	order := []int{}
	for _, sp := range specs {
		if _, seen := bySeason[sp.season]; !seen {
			order = append(order, sp.season)
		}
		bySeason[sp.season] = append(bySeason[sp.season], series.Episode{
			SeasonNumber: sp.season, EpisodeNumber: sp.episode,
			AirDate: sp.airDate, HasFile: sp.hasFile, Monitored: true,
		})
	}
	var s series.Series
	for _, n := range order {
		s.Seasons = append(s.Seasons, series.Season{SeasonNumber: n, Monitored: true, Episodes: bySeason[n]})
	}
	return s
}
