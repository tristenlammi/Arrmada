package indexer

import "time"

// Release is a single search result from an indexer, normalized across all
// indexer kinds. The acquisition pipeline parses and scores it downstream.
type Release struct {
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"` // indexer blurb; some carry author/narrator (e.g. MyAnonaMouse)
	DownloadURL string    `json:"download_url"`         // .torrent / .nzb / magnet
	InfoHash    string    `json:"info_hash,omitempty"`  // torrent
	SizeBytes   int64     `json:"size_bytes"`
	Seeders     int       `json:"seeders,omitempty"`    // torrent
	Peers       int       `json:"peers,omitempty"`      // torrent
	PublishedAt time.Time `json:"published_at,omitempty"`
	Categories  []int     `json:"categories,omitempty"`
	Indexer     string    `json:"indexer"`   // which indexer returned it
	Transport   Transport `json:"transport"` // torrent | usenet
}

// SizeGB returns the release size in gigabytes.
func (r Release) SizeGB() float64 {
	return float64(r.SizeBytes) / (1 << 30)
}
