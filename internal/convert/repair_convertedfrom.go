package convert

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// convertedFromBackfillKey marks the one-time pre-conversion baseline backfill as done.
const convertedFromBackfillKey = "repair:converted_from_v1"

// BackfillConvertedFrom gives files converted before baselines were recorded their
// pre-conversion baseline, once, from the conversion ledger (convert_history).
//
// Without it an already-converted file is judged as its small converted self, and a remux
// of the same film passes as a big bitrate upgrade — the re-grab loop the baseline exists
// to stop. A finished conversion fills in a record only while that record still holds
// the conversion's output: the same path, and a recorded release that is the ledger's
// source release apart from the codec Convert stamped in (a new import since would have
// recorded a different release). Ledger rows are read oldest first and a record that has
// a baseline size keeps it, so a re-converted file keeps its first original; a release the
// migration already recovered from an appended stamp is kept and gains its size. Rows without a
// source release (library-scanned files, extra movie versions) are skipped: nothing ties
// them to the record safely. Only database rows change; no file is read.
func BackfillConvertedFrom(ctx context.Context, db *sql.DB, set *settings.Service, log *slog.Logger) (int, error) {
	if _, done := set.Lookup(convertedFromBackfillKey); done {
		return 0, nil
	}
	type done struct {
		kind              string
		movieID, seriesID int64
		srcRelease, out   string
		srcSize           int64
	}
	rows, err := db.QueryContext(ctx,
		`SELECT kind, movie_id, series_id, src_release, src_size, out_path FROM convert_history
		  WHERE outcome = ? AND src_release <> '' AND out_path <> ''
		  ORDER BY finished_at, id`, OutcomeDone)
	if err != nil {
		return 0, err
	}
	var ledger []done
	for rows.Next() {
		var d done
		if err := rows.Scan(&d.kind, &d.movieID, &d.seriesID, &d.srcRelease, &d.srcSize, &d.out); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ledger = append(ledger, d)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, err
	}

	filled := 0
	err = store.WithTx(ctx, db, func(tx *sql.Tx) error {
		for _, d := range ledger {
			var q string
			var args []any
			if d.kind == "episode" {
				q = `SELECT id, source_release FROM episodes
				      WHERE series_id = ? AND has_file = 1 AND file_path = ? AND converted_from_size = 0`
				args = []any{d.seriesID, d.out}
			} else {
				q = `SELECT id, source_release FROM movies
				      WHERE id = ? AND has_file = 1 AND movie_file_path = ? AND converted_from_size = 0`
				args = []any{d.movieID, d.out}
			}
			cands, err := idReleases(ctx, tx, q, args...)
			if err != nil {
				return err
			}
			table := "movies"
			if d.kind == "episode" {
				table = "episodes"
			}
			for _, c := range cands {
				if parser.WithoutCodec(c.release) != parser.WithoutCodec(d.srcRelease) {
					continue // a different release has been imported since
				}
				if _, err := tx.ExecContext(ctx,
					`UPDATE `+table+` SET converted_from_size = ?,
					        converted_from_release = CASE WHEN converted_from_release = '' THEN ? ELSE converted_from_release END
					  WHERE id = ?`,
					d.srcSize, d.srcRelease, c.id); err != nil {
					return err
				}
				filled++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if err := set.Set(ctx, convertedFromBackfillKey, "1"); err != nil {
		return filled, fmt.Errorf("record the backfill as done: %w", err)
	}
	if filled > 0 && log != nil {
		log.Info("convert: recorded what converted files were before conversion, from the ledger", "records", filled)
	}
	return filled, nil
}

type idRelease struct {
	id      int64
	release string
}

func idReleases(ctx context.Context, tx *sql.Tx, q string, args ...any) ([]idRelease, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []idRelease
	for rows.Next() {
		var r idRelease
		if err := rows.Scan(&r.id, &r.release); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
