package convert

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/tristenlammi/arrmada/internal/quality"
)

// The library index is the one place every movie and episode file has been analysed, so
// it's also what the ideal-file check (quality.CheckFit) reads: no file is probed when a
// table opens.

// IndexedFile is one analysed library file.
type IndexedFile struct {
	MediaType string
	MovieID   int64
	SeriesID  int64
	Season    int
	Episode   int
	Path      string
	Info      MediaInfo
}

// IndexedFiles returns the analysed files of a media type ("movie" | "episode"); seriesID
// > 0 narrows episodes to one show. Files not analysed yet are left out — and so are
// files analysed by an older version, which may lack facts the check reads (before
// version 4 there was no Atmos), so they'd be flagged for something they do have. They
// reappear once the background pass re-analyses them.
func (s *Service) IndexedFiles(ctx context.Context, mediaType string, seriesID int64) ([]IndexedFile, error) {
	if s.index == nil {
		return nil, nil
	}
	q := `SELECT media_type, movie_id, series_id, season, episode, path, info_json
	      FROM convert_library WHERE media_type = ? AND info_json <> '' AND info_ver = ?`
	args := []any{mediaType, probeSchemaVersion}
	if seriesID > 0 {
		q += ` AND series_id = ?`
		args = append(args, seriesID)
	}
	rows, err := s.index.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexedFile
	for rows.Next() {
		var f IndexedFile
		var infoJSON string
		if err := rows.Scan(&f.MediaType, &f.MovieID, &f.SeriesID, &f.Season, &f.Episode, &f.Path, &infoJSON); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(infoJSON), &f.Info) == nil {
			out = append(out, f)
		}
	}
	return out, rows.Err()
}

// Facts is what the ideal-file check needs from an analysis. A Dolby Vision file reports
// the format under its Dolby Vision layer as its HDR; the bitrate is the whole file's,
// the same figure the library tables show.
func Facts(mi *MediaInfo) quality.FileFacts {
	f := quality.FileFacts{
		Resolution:  mi.Resolution,
		Codec:       codecClass(mi.VideoCodec),
		HDR:         mi.EncodeHDR(),
		DolbyVision: mi.HDR == "Dolby Vision",
		// A profile-5 picture has nothing under its Dolby Vision layer to fall back on:
		// it is Dolby Vision, not SDR.
		DVNoFallback: mi.DVUnconvertible(),
		BitrateMbps:  float64(mi.BitrateKbps) / 1000,
	}
	if f.HDR == "" {
		f.HDR = "SDR"
	}
	for _, au := range mi.Audio {
		f.Atmos = f.Atmos || au.Atmos
		if au.Lossless && !f.Lossless {
			f.Lossless = true
			f.LosslessCodec = losslessLabel(au.Codec)
		}
	}
	return f
}

// losslessLabel names a lossless audio codec the way a release name would, so the upgrade
// engine scores a probed track like a release that says it has one.
func losslessLabel(codec string) string {
	switch c := strings.ToLower(codec); {
	case c == "truehd" || c == "mlp":
		return "TrueHD"
	case c == "dts":
		return "DTS-HD"
	case c == "flac":
		return "FLAC"
	case strings.HasPrefix(c, "pcm_"):
		return "LPCM"
	}
	return ""
}

// FactsForPath returns the probed facts for one library file, for the upgrade decisions
// (quality.CurrentFile.Facts). The analysis is used only while it still describes the file:
// sizeBytes must be the file's size as the caller knows it and match the indexed size, and
// the entry must come from the current analysis version. An entry left over from a file
// since replaced at the same path, or one from an older analysis that may lack facts
// (before version 4 there was no Atmos), returns false, and the caller judges the file by
// its release name as it always did.
func (s *Service) FactsForPath(ctx context.Context, path string, sizeBytes int64) (quality.FileFacts, bool) {
	if s.index == nil || path == "" || sizeBytes <= 0 {
		return quality.FileFacts{}, false
	}
	var (
		size     int64
		ver      int
		codec    string
		infoJSON string
	)
	err := s.index.db.QueryRowContext(ctx,
		`SELECT size_bytes, info_ver, video_codec, info_json FROM convert_library WHERE path = ?`, path).
		Scan(&size, &ver, &codec, &infoJSON)
	if err != nil {
		return quality.FileFacts{}, false
	}
	return factsIfCurrent(indexedFacts{size: size, ver: ver, codec: codec, infoJSON: infoJSON}, sizeBytes)
}

// indexedFacts is one convert_library row as the facts lookup reads it.
type indexedFacts struct {
	size     int64
	ver      int
	codec    string
	infoJSON string
}

// factsIfCurrent applies the gates FactsForPath and FactsIndex share.
func factsIfCurrent(r indexedFacts, sizeBytes int64) (quality.FileFacts, bool) {
	// An empty codec marks a probe that failed; a size mismatch, a different file.
	if sizeBytes <= 0 || r.size != sizeBytes || r.ver != probeSchemaVersion || r.codec == "" || r.infoJSON == "" {
		return quality.FileFacts{}, false
	}
	var mi MediaInfo
	if json.Unmarshal([]byte(r.infoJSON), &mi) != nil || mi.VideoCodec == "" {
		return quality.FileFacts{}, false
	}
	return Facts(&mi), true
}

// FactsIndex is every analysed file of one media type, for callers judging many files at
// once (a profile edit's impact count) without a query per file. Lookup applies the same
// gates as FactsForPath.
type FactsIndex map[string]indexedFacts

// Lookup returns the facts for a path whose size the caller knows, or false.
func (ix FactsIndex) Lookup(path string, sizeBytes int64) (quality.FileFacts, bool) {
	r, ok := ix[path]
	if !ok {
		return quality.FileFacts{}, false
	}
	return factsIfCurrent(r, sizeBytes)
}

// FactsByPath loads the FactsIndex for a media type ("movie" | "episode") in one query.
func (s *Service) FactsByPath(ctx context.Context, mediaType string) (FactsIndex, error) {
	out := FactsIndex{}
	if s.index == nil {
		return out, nil
	}
	rows, err := s.index.db.QueryContext(ctx,
		`SELECT path, size_bytes, info_ver, video_codec, info_json FROM convert_library
		  WHERE media_type = ? AND info_json <> '' AND info_ver = ?`, mediaType, probeSchemaVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var r indexedFacts
		if err := rows.Scan(&p, &r.size, &r.ver, &r.codec, &r.infoJSON); err != nil {
			return nil, err
		}
		out[p] = r
	}
	return out, rows.Err()
}
