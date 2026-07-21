package httpapi

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

func di(media string, id int, vote float64) metadata.DiscoverItem {
	return metadata.DiscoverItem{MediaType: media, TMDBID: id, VoteAverage: vote}
}

// A title recommended by multiple seeds outranks a single-seed one; a title that is
// itself a seed is never returned; recency and rating break ties.
func TestRankRecommendations(t *testing.T) {
	// seeds: the user watched/requested movie 100 (most recent) and movie 200.
	seedSet := map[string]bool{"movie:100": true, "movie:200": true}
	perSeed := [][]metadata.DiscoverItem{
		// recommendations for the most-recent seed (100)
		{di("movie", 1, 7.0), di("movie", 2, 8.0), di("movie", 200, 9.0)},
		// recommendations for the older seed (200)
		{di("movie", 2, 8.0), di("movie", 3, 6.0), di("movie", 100, 9.0)},
	}
	got := rankRecommendations(perSeed, seedSet)

	// movie 200 and 100 are seeds → excluded entirely.
	for _, it := range got {
		if it.TMDBID == 100 || it.TMDBID == 200 {
			t.Fatalf("a seed leaked into recommendations: %d", it.TMDBID)
		}
	}
	// movie 2 is recommended by BOTH seeds → highest score → first.
	if len(got) != 3 || got[0].TMDBID != 2 {
		t.Fatalf("expected movie 2 ranked first, got %+v", got)
	}
	// movie 1 (recommended only by the most-recent seed, weight ~2.0) outranks movie 3
	// (only by the older seed, weight ~1.5).
	if got[1].TMDBID != 1 || got[2].TMDBID != 3 {
		t.Fatalf("recency weighting wrong: %+v", got)
	}
}

func TestRankRecommendationsEmpty(t *testing.T) {
	if got := rankRecommendations(nil, map[string]bool{}); len(got) != 0 {
		t.Fatalf("expected empty, got %+v", got)
	}
}
