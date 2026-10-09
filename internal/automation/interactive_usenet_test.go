package automation

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/indexer"
)

// MOV-11: a usenet release (no client can take it) is listed but never Recommended, and
// the recommended release is the one a search would actually grab.
func TestRankReleasesSkipsUsenetForRecommendation(t *testing.T) {
	h := newStallHarness(t)
	nzb := &fakeTorznab{}
	srv := httptest.NewServer(nzb.handler())
	t.Cleanup(srv.Close)
	if _, err := h.c.indexers.Create(h.ctx, indexer.Indexer{Name: "Usenet", Kind: indexer.KindNewznab, URL: srv.URL, Priority: 5, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	mid := h.addMovie(t, 1, "Arrival", 2016)
	const top = "Arrival.2016.2160p.BluRay.REMUX.x265-NZB" // outscores everything, usenet only
	nzb.offer(top)
	h.ix.offer(arrivalRelease)

	list, err := h.c.RankReleases(h.ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	var recommended string
	for _, r := range list.Releases {
		if r.Recommended {
			recommended = r.Title
		}
		if r.Title == top {
			if r.Eligible || r.Recommended || r.RejectReason != usenetReject || r.Transport != string(indexer.TransportUsenet) {
				t.Errorf("usenet row = %+v", r)
			}
		}
	}
	if recommended != arrivalRelease {
		t.Fatalf("recommended %q, want the torrent %q; rows %+v", recommended, arrivalRelease, list.Releases)
	}
	if len(list.Releases) != 2 {
		t.Errorf("rows = %d, want both releases listed", len(list.Releases))
	}

	// Auto-grab takes the same release.
	if _, err := h.c.SearchMovie(h.ctx, mid); err != nil {
		t.Fatal(err)
	}
	var grabbed string
	if err := h.c.db.QueryRow(`SELECT title FROM grabs WHERE movie_id = ?`, mid).Scan(&grabbed); err != nil {
		t.Fatal(err)
	}
	if grabbed != recommended {
		t.Errorf("auto-grab took %q, the list recommended %q", grabbed, recommended)
	}
}

// Each row carries the release's age and peers for the modal.
func TestRankedReleaseJSONHasAgeAndPeers(t *testing.T) {
	pub := time.Date(2026, 9, 30, 12, 0, 0, 0, time.FixedZone("x", 3600))
	rr := withIndexerFacts(RankedRelease{Title: "A", Eligible: true}, indexer.Release{Title: "A", Peers: 7, PublishedAt: pub, Transport: indexer.TransportTorrent})
	b, err := json.Marshal(rr)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"published_at":"2026-09-30T11:00:00Z"`, `"peers":7`, `"transport":"torrent"`, `"eligible":true`} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON %s lacks %s", s, want)
		}
	}
	// Unknown age is left out rather than sent as the zero time.
	b, _ = json.Marshal(withIndexerFacts(RankedRelease{Title: "B"}, indexer.Release{Title: "B"}))
	if strings.Contains(string(b), "published_at") {
		t.Errorf("zero time serialized: %s", b)
	}
}

// A title offered as both a torrent and an NZB is listed once, as the torrent.
func TestSplitTransportPrefersTheTorrentCopy(t *testing.T) {
	rels := []indexer.Release{
		{Title: "X", Transport: indexer.TransportUsenet},
		{Title: "X", Transport: indexer.TransportTorrent, Seeders: 3},
		{Title: "Y", Transport: indexer.TransportUsenet},
		{Title: "Y", Transport: indexer.TransportUsenet},
	}
	tor, nzb := splitTransport(rels)
	if len(tor) != 1 || tor[0].Title != "X" || len(nzb) != 1 || nzb[0].Title != "Y" {
		t.Errorf("torrents %+v, usenet %+v", tor, nzb)
	}
}
