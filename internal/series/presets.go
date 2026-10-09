package series

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Monitor presets: the common ways to say which episodes of a show are wanted, applied in
// one action at add time or from the series page. Episode flags are set by rule from
// has_file and the air date; specials are never monitored by a preset.
const (
	PresetAll          = "all"           // every regular episode
	PresetFuture       = "future"        // episodes that haven't aired yet
	PresetMissing      = "missing"       // episodes without a file, and ones not aired yet
	PresetExisting     = "existing"      // episodes with a file, and ones not aired yet
	PresetFirstSeason  = "first_season"  // the first regular season only
	PresetLatestSeason = "latest_season" // the highest season that has episodes
	PresetNone         = "none"          // nothing (specials included)
)

// KeyMonitorDefault is the setting naming the preset a new show gets when the add doesn't
// choose one (the add dialog pre-selects it; requests use it as is).
const KeyMonitorDefault = "series_monitor_default"

// DefaultMonitorPreset is what KeyMonitorDefault falls back to.
const DefaultMonitorPreset = PresetAll

// ErrUnknownPreset is returned for a preset name that isn't one of the above.
var ErrUnknownPreset = errors.New("unknown monitoring preset")

// ValidPreset reports whether p names a preset.
func ValidPreset(p string) bool {
	switch p {
	case PresetAll, PresetFuture, PresetMissing, PresetExisting, PresetFirstSeason, PresetLatestSeason, PresetNone:
		return true
	}
	return false
}

// presetNewSeasons is whether a preset wants seasons that don't exist yet: the ones that
// look forward do; the first season, and nothing, don't.
func presetNewSeasons(p string) bool {
	return p != PresetFirstSeason && p != PresetNone
}

// airedSQL matches automation's aired(): an episode with no air date hasn't aired.
const airedSQL = `(air_date <> '' AND date(air_date) <= date('now'))`

// ApplyMonitorPreset sets every regular episode's monitored flag by the preset's rule, sets
// "monitor new seasons" (the preset's own choice unless newSeasons overrides it) and derives
// each season's flag from its episodes — all in one transaction.
func (r *Repo) ApplyMonitorPreset(ctx context.Context, id int64, preset string, newSeasons *bool) error {
	if !ValidPreset(preset) {
		return fmt.Errorf("%w: %q", ErrUnknownPreset, preset)
	}
	mns := presetNewSeasons(preset)
	if newSeasons != nil {
		mns = *newSeasons
	}
	return store.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM series WHERE id = ?`, id).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		rule := "1"
		var args []any
		switch preset {
		case PresetFuture:
			rule = `NOT ` + airedSQL
		case PresetMissing:
			rule = `(has_file = 0 OR NOT ` + airedSQL + `)`
		case PresetExisting:
			rule = `(has_file = 1 OR NOT ` + airedSQL + `)`
		case PresetFirstSeason, PresetLatestSeason:
			agg := "MIN"
			if preset == PresetLatestSeason {
				agg = "MAX"
			}
			rule = `season_number = (SELECT ` + agg + `(season_number) FROM episodes WHERE series_id = ? AND season_number > 0)`
			args = append(args, id)
		case PresetNone:
			rule = "0"
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE episodes SET monitored = CASE WHEN `+rule+` THEN 1 ELSE 0 END
			 WHERE series_id = ? AND season_number > 0`, append(args, id)...); err != nil {
			return err
		}
		if preset == PresetNone {
			if _, err := tx.ExecContext(ctx, `UPDATE episodes SET monitored = 0 WHERE series_id = ? AND season_number = 0`, id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE series SET monitor_new_seasons = ? WHERE id = ?`, b2i(mns), id); err != nil {
			return err
		}
		return syncSeasonsTx(ctx, tx, id, -1, mns)
	})
}

// syncSeasonsTx derives season flags from episodes: a season is monitored when any of its
// episodes is, which is what the sweep needs (it requires both flags) — monitoring one
// episode in an unmonitored season used to do nothing at all. season < 0 syncs every
// season. A season with no episodes yet (announced, empty) takes emptyFlag, so it follows
// "monitor new seasons" once its episodes arrive.
func syncSeasonsTx(ctx context.Context, tx *sql.Tx, seriesID int64, season int, emptyFlag bool) error {
	q := `UPDATE seasons SET monitored = CASE
	        WHEN EXISTS (SELECT 1 FROM episodes e WHERE e.series_id = seasons.series_id AND e.season_number = seasons.season_number)
	        THEN EXISTS (SELECT 1 FROM episodes e WHERE e.series_id = seasons.series_id AND e.season_number = seasons.season_number AND e.monitored = 1)
	        ELSE ? END
	      WHERE series_id = ?`
	args := []any{b2i(emptyFlag), seriesID}
	if season >= 0 {
		q += ` AND season_number = ?`
		args = append(args, season)
	}
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}

// ApplyMonitorPreset applies a monitoring preset to a show (see Repo.ApplyMonitorPreset).
// It doesn't touch the pause gate.
func (s *Service) ApplyMonitorPreset(ctx context.Context, id int64, preset string, newSeasons *bool) error {
	return s.repo.ApplyMonitorPreset(ctx, id, preset, newSeasons)
}

// SetMonitorDefaultFunc installs where the default preset for new shows is read from
// (the series_monitor_default setting), on every add. Call it at startup.
func (s *Service) SetMonitorDefaultFunc(fn func(ctx context.Context) string) { s.monitorDefault = fn }

// MonitorDefault is the preset a new show gets when the add doesn't choose one.
func (s *Service) MonitorDefault(ctx context.Context) string {
	if s.monitorDefault != nil {
		if p := s.monitorDefault(ctx); ValidPreset(p) {
			return p
		}
	}
	return DefaultMonitorPreset
}
