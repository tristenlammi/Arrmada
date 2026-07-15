package indexer

import (
	"testing"
)

const torznabSample = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
  <channel>
    <title>Test Torznab</title>
    <item>
      <title>Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX</title>
      <guid>abc123</guid>
      <link>https://tracker.example/dl/abc123</link>
      <pubDate>Sat, 13 Jul 2024 10:00:00 +0000</pubDate>
      <size>25769803776</size>
      <enclosure url="https://tracker.example/abc123.torrent" length="25769803776" type="application/x-bittorrent"/>
      <torznab:attr name="seeders" value="212"/>
      <torznab:attr name="peers" value="230"/>
      <torznab:attr name="infohash" value="DEADBEEF"/>
      <torznab:attr name="category" value="2040"/>
    </item>
    <item>
      <title>Dune.Part.Two.2024.1080p.BluRay.x264.DTS-HD.MA.5.1-SPARKS</title>
      <guid>def456</guid>
      <link>https://tracker.example/dl/def456</link>
      <pubDate>Fri, 12 Jul 2024 08:30:00 +0000</pubDate>
      <size>17179869184</size>
      <torznab:attr name="seeders" value="180"/>
      <torznab:attr name="category" value="2030"/>
    </item>
  </channel>
</rss>`

const newznabSample = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <item>
      <title>Some.Movie.2023.1080p.WEB-DL-GRP</title>
      <link>https://usenet.example/getnzb/xyz</link>
      <enclosure url="https://usenet.example/getnzb/xyz.nzb" length="8589934592" type="application/x-nzb"/>
      <newznab:attr name="size" value="8589934592"/>
      <newznab:attr name="category" value="2040"/>
    </item>
  </channel>
</rss>`

func TestParseTorznabFeed(t *testing.T) {
	releases, err := ParseFeed([]byte(torznabSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(releases))
	}

	r := releases[0]
	if r.Title != "Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX" {
		t.Errorf("title = %q", r.Title)
	}
	if r.DownloadURL != "https://tracker.example/abc123.torrent" {
		t.Errorf("download url = %q (want enclosure url)", r.DownloadURL)
	}
	if r.Seeders != 212 || r.Peers != 230 {
		t.Errorf("seeders/peers = %d/%d, want 212/230", r.Seeders, r.Peers)
	}
	if r.InfoHash != "deadbeef" {
		t.Errorf("infohash = %q, want lowercased deadbeef", r.InfoHash)
	}
	if r.SizeBytes != 25769803776 {
		t.Errorf("size = %d", r.SizeBytes)
	}
	if got := r.SizeGB(); got < 23.9 || got > 24.1 {
		t.Errorf("SizeGB = %.2f, want ~24", got)
	}
	if len(r.Categories) != 1 || r.Categories[0] != 2040 {
		t.Errorf("categories = %v", r.Categories)
	}
	if r.PublishedAt.IsZero() {
		t.Error("expected a parsed pubDate")
	}

	// Second item falls back to <link> when there's no enclosure.
	if releases[1].DownloadURL != "https://tracker.example/dl/def456" {
		t.Errorf("fallback download url = %q", releases[1].DownloadURL)
	}
}

func TestParseNewznabFeed(t *testing.T) {
	releases, err := ParseFeed([]byte(newznabSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("expected 1 release, got %d", len(releases))
	}
	r := releases[0]
	// Size comes from the newznab:attr since there's no <size> element.
	if r.SizeBytes != 8589934592 {
		t.Errorf("size = %d, want 8589934592 (from attr)", r.SizeBytes)
	}
	if r.DownloadURL != "https://usenet.example/getnzb/xyz.nzb" {
		t.Errorf("download url = %q", r.DownloadURL)
	}
}

func TestBuildURL(t *testing.T) {
	idx := Indexer{
		Name: "x", Kind: KindTorznab,
		URL: "https://tracker.example/api", APIKey: "secret", Categories: []int{2000},
	}
	got, err := buildURL(idx, "search", SearchQuery{Text: "dune 2024", Limit: 50})
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	for _, want := range []string{"t=search", "apikey=secret", "q=dune+2024", "cat=2000", "limit=50"} {
		if !contains(got, want) {
			t.Errorf("url %q missing %q", got, want)
		}
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (indexOf(hay, needle) >= 0)
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
