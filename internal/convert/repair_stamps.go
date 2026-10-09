package convert

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// codecStampRepairKey marks the one-time codec stamp repair as done (a setting).
const codecStampRepairKey = "repair:codec_stamp_v1"

// legacyStamps are the tokens Convert used to append to a converted file's recorded
// release, with the codec each one means.
var legacyStamps = []struct {
	suffix string
	codec  parser.Codec
}{
	{" AV1", parser.CodecAV1},
	{" x265", parser.CodecX265},
}

// RepairCodecStamps rewrites the source releases Convert stamped the old way, once.
//
// A conversion used to append " AV1" or " x265" to the recorded release. That read back
// as the old codec ("...H.264-GRP AV1" parsed as x264) and hid the "-GROUP" suffix, so the
// upgrade sweep costed the converted file at H.264 efficiency and re-grabbed the release
// it was converted from. Each such row gets the suffix taken off and the codec swapped in
// place instead: "...H.264-GRP AV1" becomes "...AV1-GRP". Only rows that actually change
// are written; a settings key stops it running again. The key is saved after the rows,
// so a failure in between only means the next boot looks again, and finds nothing left
// to change.
func RepairCodecStamps(ctx context.Context, db *sql.DB, set *settings.Service, log *slog.Logger) (int, error) {
	if _, done := set.Lookup(codecStampRepairKey); done {
		return 0, nil
	}

	type fix struct {
		table, before, after string
		id                   int64
	}
	var fixes []fix
	for _, table := range []string{"movies", "movie_versions", "episodes"} {
		// LIKE is case-insensitive in SQLite, so this over-selects slightly; the exact
		// suffix is checked below. Rows are read fully before any write.
		rows, err := db.QueryContext(ctx,
			`SELECT id, source_release FROM `+table+` WHERE source_release LIKE '% AV1' OR source_release LIKE '% x265'`)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var id int64
			var rel string
			if err := rows.Scan(&id, &rel); err != nil {
				_ = rows.Close()
				return 0, err
			}
			if after := restampLegacy(rel); after != rel {
				fixes = append(fixes, fix{table: table, id: id, before: rel, after: after})
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return 0, err
		}
	}

	err := store.WithTx(ctx, db, func(tx *sql.Tx) error {
		for _, f := range fixes {
			if _, err := tx.ExecContext(ctx, `UPDATE `+f.table+` SET source_release = ? WHERE id = ?`, f.after, f.id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if err := set.Set(ctx, codecStampRepairKey, "1"); err != nil {
		return len(fixes), fmt.Errorf("record the repair as done: %w", err)
	}

	if len(fixes) > 0 && log != nil {
		log.Info("convert: rewrote appended codec stamps in recorded releases", "rows", len(fixes))
		for i, f := range fixes {
			if i == 5 {
				break
			}
			log.Info("convert: codec stamp rewritten", "table", f.table, "id", f.id, "before", f.before, "after", f.after)
		}
	}
	return len(fixes), nil
}

// restampLegacy turns a release with an appended codec stamp into one with the codec
// swapped in place, or returns it unchanged when it carries no such stamp. A name whose
// last word is simply its codec ("Film 2021 1080p BluRay x265") comes back the same.
func restampLegacy(rel string) string {
	for _, st := range legacyStamps {
		if base, ok := strings.CutSuffix(rel, st.suffix); ok && strings.TrimSpace(base) != "" {
			return parser.RestampCodec(base, st.codec)
		}
	}
	return rel
}
