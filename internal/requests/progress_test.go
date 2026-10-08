package requests

import (
	"context"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
)

// Each stage a request can be in, worked out from its grabs and the download queue.
func TestTrackStages(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	db := s.repo.db
	grab := func(mediaType string, id int64, hash, title string, at time.Time) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO grabs (movie_id, title, status, media_type, info_hash, grabbed_at) VALUES (?, ?, 'grabbed', ?, ?, ?)`,
			id, title, mediaType, hash, at.UTC().Format("2006-01-02 15:04:05")); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-3 * time.Hour)
	grab("movie", 1, "AAA", "Heat.1995.2160p", old)        // downloading
	grab("movie", 2, "BBB", "Alien.1979.1080p", old)       // finished → importing
	grab("movie", 3, "", "Arrival.2016.1080p.WEB-DL", old) // no hash: matched by name, paused
	grab("movie", 4, "DDD", "Gone.2020", old)              // no longer in the client
	grab("movie", 5, "EEE", "Fresh.2024", time.Now())      // just grabbed, not in the client yet
	grab("movie", 6, "FFF", "Broken.2021", old)            // errored
	grab("series", 7, "GGG", "Show.S02.1080p", old)        // season pack downloading
	queue := []download.Item{
		{Hash: "aaa", Name: "Heat 1995 2160p", State: "downloading", Progress: 0.4, SizeBytes: 1000, DownloadedBytes: 400, DownSpeed: 50, ETASeconds: 12},
		{Hash: "bbb", Name: "Alien", State: "seeding", Progress: 1, SizeBytes: 500, DownloadedBytes: 500},
		{Hash: "ccc", Name: "Arrival.2016.1080p.WEB-DL", State: "paused", Progress: 0.5, SizeBytes: 200, DownloadedBytes: 100},
		{Hash: "fff", Name: "Broken", State: "error", Progress: 0.1, SizeBytes: 100},
		{Hash: "ggg", Name: "Show S02", State: "downloading", Progress: 0.25, SizeBytes: 800, DownloadedBytes: 200, ETASeconds: 8640000},
	}
	reqs := []Request{
		{Title: "pending", Status: StatusPending},
		{Title: "declined", Status: StatusDeclined},
		{Title: "heat", Status: StatusApproved, MediaType: "movie", libID: 1, released: true},
		{Title: "alien", Status: StatusApproved, MediaType: "movie", libID: 2, released: true},
		{Title: "arrival", Status: StatusApproved, MediaType: "movie", libID: 3, released: true},
		{Title: "gone", Status: StatusApproved, MediaType: "movie", libID: 4, released: true},
		{Title: "fresh", Status: StatusApproved, MediaType: "movie", libID: 5, released: true},
		{Title: "broken", Status: StatusApproved, MediaType: "movie", libID: 6, released: true},
		{Title: "show", Status: StatusApproved, MediaType: "series", libID: 7, released: true, Available: true, epHave: 8, epTotal: 18},
		{Title: "unreleased", Status: StatusApproved, MediaType: "movie", libID: 9},
		{Title: "ready", Status: StatusApproved, MediaType: "movie", libID: 1, Available: true, released: true},
		{Title: "some episodes", Status: StatusApproved, MediaType: "series", libID: 10, Available: true, epHave: 3, epTotal: 10, released: true},
		{Title: "whole show", Status: StatusApproved, MediaType: "series", libID: 11, Available: true, epHave: 10, epTotal: 10, released: true},
		{Title: "pending but there", Status: StatusPending, MediaType: "movie", libID: 12, Available: true},
	}
	s.Track(ctx, reqs, queue)
	want := map[string]string{
		"pending": StagePending, "declined": StageDeclined, "heat": StageDownloading, "alien": StageImporting,
		"arrival": StagePaused, "gone": StageSearching, "fresh": StageQueued, "broken": StageFailed,
		"show": StageDownloading, "unreleased": StageSearching, "ready": StageAvailable,
		"some episodes": StagePartial, "whole show": StageAvailable, "pending but there": StageAvailable,
	}
	for _, rq := range reqs {
		if rq.Tracking == nil || rq.Tracking.Stage != want[rq.Title] {
			t.Errorf("%s: stage = %+v, want %s", rq.Title, rq.Tracking, want[rq.Title])
		}
	}
	heat := reqs[2].Tracking
	if heat.Progress != 0.4 || heat.ETASeconds != 12 || heat.SpeedBps != 50 || reqs[2].DownloadProgress != 0.4 {
		t.Errorf("heat tracking = %+v", heat)
	}
	if show := reqs[8].Tracking; show.Have != 8 || show.Total != 18 || show.ETASeconds != 0 || show.Note != "Waiting for peers" {
		t.Errorf("show tracking = %+v (qBittorrent's infinite ETA must not show; no speed means waiting)", show)
	}
	if reqs[9].Tracking.Note != "Not out yet" {
		t.Errorf("unreleased note = %q", reqs[9].Tracking.Note)
	}
}
