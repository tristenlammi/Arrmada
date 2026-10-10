package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// detailStub answers MediaDetails with a fixed record.
type detailStub struct{ stubDiscovery }

func (detailStub) MediaDetails(context.Context, string, int) (*metadata.MediaDetail, error) {
	return &metadata.MediaDetail{MediaType: "movie", TMDBID: 603, IMDBID: "tt0133093", Title: "The Matrix", Ratings: metadata.Ratings{TMDB: 8.2}}, nil
}

// slowRatings answers after delay, or not at all if the context ends first.
type slowRatings struct {
	delay time.Duration
	done  chan struct{} // closed when a fetch finishes either way
}

func (slowRatings) Available() bool { return true }

func (s slowRatings) Ratings(ctx context.Context, _ string) (metadata.Ratings, error) {
	defer close(s.done)
	select {
	case <-time.After(s.delay):
		return metadata.Ratings{IMDB: "8.7"}, nil
	case <-ctx.Done():
		return metadata.Ratings{}, ctx.Err()
	}
}

func getDetail(t *testing.T, a *api) (metadata.MediaDetail, time.Duration) {
	t.Helper()
	// The response carries the title's card; give it an empty library to read instead of
	// real services.
	if _, ok := enrichSnaps.Load(a); !ok {
		enrichSnaps.Store(a, &enrichSnapEntry{at: time.Now().Add(time.Hour), snap: emptySnap()})
		t.Cleanup(func() { enrichSnaps.Delete(a) })
	}
	r := httptest.NewRequest("GET", "/api/v1/discover/movie/603", nil)
	r.SetPathValue("media", "movie")
	r.SetPathValue("id", "603")
	rec := httptest.NewRecorder()
	start := time.Now()
	a.handleMediaDetail(rec, r)
	took := time.Since(start)
	var d metadata.MediaDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || rec.Code != 200 {
		t.Fatalf("HTTP %d %s", rec.Code, rec.Body)
	}
	return d, took
}

// A slow OMDb never holds the sheet past the wait: it answers with the TMDB score only,
// and the fetch is left to finish in the background (filling the cache for next time).
// A quick one is included.
func TestMediaDetailRatingsTimeout(t *testing.T) {
	slow := slowRatings{delay: 5 * time.Second, done: make(chan struct{})}
	a := &api{deps: Deps{Discovery: detailStub{stubDiscovery{ok: true}}, Ratings: slow}}
	d, took := getDetail(t, a)
	if took > ratingsWait+time.Second {
		t.Errorf("the sheet waited %v for slow ratings, want about %v", took, ratingsWait)
	}
	if d.Ratings.IMDB != "" || d.Ratings.TMDB != 8.2 {
		t.Errorf("ratings = %+v, want the TMDB score only", d.Ratings)
	}
	select {
	case <-slow.done: // the fetch carried on past the response and finished
	case <-time.After(10 * time.Second):
		t.Error("the background ratings fetch never finished")
	}

	quick := slowRatings{delay: 10 * time.Millisecond, done: make(chan struct{})}
	a.deps.Ratings = quick
	d, took = getDetail(t, a)
	if d.Ratings.IMDB != "8.7" || took > ratingsWait {
		t.Errorf("quick ratings: %+v after %v", d.Ratings, took)
	}
}
