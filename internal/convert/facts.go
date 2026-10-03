package convert

import (
	"context"
	"encoding/json"

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
		BitrateMbps: float64(mi.BitrateKbps) / 1000,
	}
	if f.HDR == "" {
		f.HDR = "SDR"
	}
	for _, au := range mi.Audio {
		f.Atmos = f.Atmos || au.Atmos
		f.Lossless = f.Lossless || au.Lossless
	}
	return f
}
