package subtitles

import (
	"context"
	"errors"
)

// ErrNotConfigured means the subtitle provider is missing required config (API key etc.).
var ErrNotConfigured = errors.New("subtitle provider not configured")

// SearchRequest describes a subtitle search for one media file in one language.
type SearchRequest struct {
	IMDBID   string // e.g. "tt1375666" or "1375666" (empty falls back to title search)
	Title    string
	Year     int
	Season   int // 0 for movies
	Episode  int
	Language string // ISO 639-1 code, e.g. "en"
}

// SubtitleResult is one candidate subtitle from a provider.
type SubtitleResult struct {
	FileID          string
	Language        string
	Release         string
	Downloads       int
	HearingImpaired bool
}

// Provider searches for and downloads external subtitles. Providers sit behind this
// interface (OpenSubtitles first) so more can be added with fallback, per the roadmap.
type Provider interface {
	// Available reports whether the provider is configured enough to search.
	Available() bool
	// CanDownload reports whether downloads are possible (some providers need an account).
	CanDownload() bool
	Search(ctx context.Context, req SearchRequest) ([]SubtitleResult, error)
	// Download returns the raw subtitle bytes (SRT) for a result's FileID.
	Download(ctx context.Context, fileID string) ([]byte, error)
}
