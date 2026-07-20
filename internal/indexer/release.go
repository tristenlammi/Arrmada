package indexer

import "time"

// Release is a single search result from an indexer, normalized across all
// indexer kinds. The acquisition pipeline parses and scores it downstream.
type Release struct {
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"` // indexer blurb; some carry author/narrator (e.g. MyAnonaMouse)
	DownloadURL string    `json:"download_url"`          // .torrent / .nzb / magnet
	InfoURL     string    `json:"info_url,omitempty"`    // the release's details page on the tracker
	InfoHash    string    `json:"info_hash,omitempty"`   // torrent
	SizeBytes   int64     `json:"size_bytes"`
	Seeders     int       `json:"seeders,omitempty"` // torrent
	Peers       int       `json:"peers,omitempty"`   // torrent
	PublishedAt time.Time `json:"published_at,omitempty"`
	Categories  []int     `json:"categories,omitempty"`
	Indexer     string    `json:"indexer"`   // which indexer returned it
	Transport   Transport `json:"transport"` // torrent | usenet

	// Structured book metadata, when the indexer exposes it as real fields (e.g.
	// MyAnonaMouse's API) rather than only in the title. These let the book
	// pipeline show narrator/author/series/format reliably instead of scraping
	// them back out of the release name.
	Author   string `json:"author,omitempty"`
	Narrator string `json:"narrator,omitempty"`
	Series   string `json:"series,omitempty"`
	Language string `json:"language,omitempty"`
	Format   string `json:"format,omitempty"` // ebook/audiobook file type: EPUB, M4B, MP3…
}

// SizeGB returns the release size in gigabytes.
func (r Release) SizeGB() float64 {
	return float64(r.SizeBytes) / (1 << 30)
}
