package indexer

import "testing"

const tlSample = `{
  "numFound": 2,
  "torrentList": [
    {
      "fid": "123456",
      "filename": "Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX.torrent",
      "name": "Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX",
      "categoryID": 47,
      "size": 25769803776,
      "seeders": 212,
      "leechers": 18,
      "completed": 900,
      "addedTimestamp": "2024-07-13 10:00:00",
      "imdbID": "tt15239678"
    },
    {
      "fid": "123457",
      "filename": "Some.Show.S01.1080p.WEB-DL-GRP.torrent",
      "name": "[REQUESTED] Some.Show.S01.1080p.WEB-DL-GRP",
      "categoryID": 27,
      "size": 8589934592,
      "seeders": 40,
      "leechers": 3,
      "addedTimestamp": "2024-07-12 08:30:00"
    }
  ]
}`

func TestTorrentLeechParse(t *testing.T) {
	s := NewTorrentLeechSearcher(nil)
	idx := Indexer{ID: 1, Name: "TorrentLeech", Kind: KindTorrentLeech}

	releases, err := s.releasesFromJSON(idx, []byte(tlSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(releases))
	}

	r0 := releases[0]
	if r0.Title != "Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX" {
		t.Errorf("title = %q", r0.Title)
	}
	if r0.SizeBytes != 25769803776 || r0.Seeders != 212 || r0.Peers != 18 {
		t.Errorf("size/seeders/peers = %d/%d/%d", r0.SizeBytes, r0.Seeders, r0.Peers)
	}
	if r0.Transport != TransportTorrent {
		t.Errorf("transport = %q", r0.Transport)
	}
	if len(r0.Categories) != 1 || r0.Categories[0] != 47 {
		t.Errorf("categories = %v", r0.Categories)
	}
	if r0.PublishedAt.IsZero() {
		t.Error("expected parsed addedTimestamp")
	}
	// Session-cookie download URL (no rss key configured).
	if r0.DownloadURL != "https://www.torrentleech.org/download/123456/Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX.torrent" {
		t.Errorf("download url = %q", r0.DownloadURL)
	}

	// The [REQUESTED] prefix should be stripped from the title.
	if releases[1].Title != "Some.Show.S01.1080p.WEB-DL-GRP" {
		t.Errorf("title[1] = %q (prefix not stripped)", releases[1].Title)
	}
}

func TestTorrentLeechDownloadURL(t *testing.T) {
	s := NewTorrentLeechSearcher(nil)
	// The RSS key is no longer used for downloads (FlareSolverr + session handles it).
	idx := Indexer{ID: 1, Kind: KindTorrentLeech, APIKey: "abcdef0123456789abcd"}
	got := s.downloadURL(idx, "999", "Foo.Bar.torrent")
	want := "https://www.torrentleech.org/download/999/Foo.Bar.torrent"
	if got != want {
		t.Errorf("download url = %q, want %q", got, want)
	}
}
