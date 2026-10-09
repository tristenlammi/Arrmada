package series

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/store"
)

// EpisodeRemap is one file a renumber carries to a new (season, episode), so the caller
// can rename it on disk. Unplaced marks a file the new numbering has no slot for: it isn't
// moved anywhere, its episode row goes, and the next rescan decides what it is.
type EpisodeRemap struct {
	Absolute   int    `json:"absolute"`
	OldSeason  int    `json:"old_season"`
	OldEpisode int    `json:"old_episode"`
	NewSeason  int    `json:"new_season"`
	NewEpisode int    `json:"new_episode"`
	FilePath   string `json:"file_path"`
	Unplaced   bool   `json:"unplaced,omitempty"`
}

// placementRow is one stored episode row as a rebuild sees it: where it is, its absolute
// number, and what belongs to the user (monitoring) and the library (its file).
type placementRow struct {
	Absolute, Season, Episode int
	HasFile, Monitored        bool
	Path, Release             string
	Size                      int64
}

// filePlacement is where a rebuild puts one stored file.
type filePlacement struct {
	row             placementRow
	season, episode int
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// snapshotRows reads a series' episode rows in (season, episode) order, so planning is
// deterministic.
func snapshotRows(ctx context.Context, q querier, seriesID int64) ([]placementRow, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT absolute_number, season_number, episode_number, has_file, file_path, size_bytes, source_release, monitored
		   FROM episodes WHERE series_id = ? ORDER BY season_number, episode_number`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []placementRow
	for rows.Next() {
		var p placementRow
		var hf, mon int
		if err := rows.Scan(&p.Absolute, &p.Season, &p.Episode, &hf, &p.Path, &p.Size, &p.Release, &mon); err != nil {
			return nil, err
		}
		p.HasFile, p.Monitored = hf != 0 && p.Path != "", mon != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// planPlacements decides where every stored file goes under a new listing. It's the one
// planner behind both the preview (PlanRebuild) and the rebuild itself, so what the owner
// reviews is exactly what Apply does.
//
// Files are carried by ABSOLUTE number, which is source-independent: episode 480 is the
// same content whether TMDB filed it as S20E480 or TVDB as S22E5. In order of trust:
//  1. a file whose absolute the new listing carries goes to that episode;
//  2. a file whose absolute it doesn't carry (or whose slot another file already took)
//     stays on its old (season, episode) if the listing still has that slot and it's free;
//  3. specials (no absolute) stay where they are, when that slot still exists.
//
// Anything left has no slot and is reported Unplaced. One slot never receives two files.
func planPlacements(current []placementRow, desired []Season) (placed []filePlacement, remaps []EpisodeRemap) {
	type slot = [2]int
	exists := map[slot]bool{}
	absAt := map[int]slot{}
	for _, sn := range desired {
		for _, ep := range sn.Episodes {
			at := slot{sn.SeasonNumber, ep.EpisodeNumber}
			exists[at] = true
			if ep.AbsoluteNumber <= 0 {
				continue
			}
			// A listing with a duplicated absolute resolves to its earliest episode, the
			// same rule EpisodeByAbsolute uses.
			if prev, ok := absAt[ep.AbsoluteNumber]; !ok || at[0] < prev[0] || (at[0] == prev[0] && at[1] < prev[1]) {
				absAt[ep.AbsoluteNumber] = at
			}
		}
	}
	taken := map[slot]bool{}
	place := func(r placementRow, at slot) {
		taken[at] = true
		placed = append(placed, filePlacement{row: r, season: at[0], episode: at[1]})
		if at[0] != r.Season || at[1] != r.Episode {
			remaps = append(remaps, EpisodeRemap{Absolute: r.Absolute, OldSeason: r.Season, OldEpisode: r.Episode,
				NewSeason: at[0], NewEpisode: at[1], FilePath: r.Path})
		}
	}
	// One file can serve several rows (a double episode); its rows move together only
	// when each finds its own slot, so they're planned row by row like any other.
	var leftovers []placementRow
	for _, r := range current {
		if !r.HasFile || r.Absolute <= 0 {
			continue
		}
		if at, ok := absAt[r.Absolute]; ok && !taken[at] {
			place(r, at)
			continue
		}
		leftovers = append(leftovers, r)
	}
	for _, r := range current {
		if r.HasFile && r.Absolute <= 0 {
			leftovers = append(leftovers, r)
		}
	}
	for _, r := range leftovers {
		old := slot{r.Season, r.Episode}
		if exists[old] && !taken[old] {
			place(r, old)
			continue
		}
		remaps = append(remaps, EpisodeRemap{Absolute: r.Absolute, OldSeason: r.Season, OldEpisode: r.Episode,
			FilePath: r.Path, Unplaced: true})
	}
	sortRemaps(remaps)
	return placed, remaps
}

// planRemaps is the preview half of planPlacements: every file that would move, or that
// would lose its episode.
func planRemaps(current []placementRow, desired []Season) []EpisodeRemap {
	_, remaps := planPlacements(current, desired)
	return remaps
}

func sortRemaps(remaps []EpisodeRemap) {
	sort.SliceStable(remaps, func(i, j int) bool {
		a, b := remaps[i], remaps[j]
		if a.OldSeason != b.OldSeason {
			return a.OldSeason < b.OldSeason
		}
		if a.OldEpisode != b.OldEpisode {
			return a.OldEpisode < b.OldEpisode
		}
		return a.FilePath < b.FilePath
	})
}

// PlanHash identifies a numbering plan: sha256 over its remaps in (old season, old
// episode) order, so the same plan always hashes the same however it was produced. Apply
// compares it to refuse a plan that changed since the owner reviewed it.
func PlanHash(remaps []EpisodeRemap) string {
	cp := append([]EpisodeRemap(nil), remaps...)
	sortRemaps(cp)
	h := sha256.New()
	for _, r := range cp {
		fmt.Fprintf(h, "%d|%d|%d|%d|%d|%t|%s\n", r.Absolute, r.OldSeason, r.OldEpisode, r.NewSeason, r.NewEpisode, r.Unplaced, r.FilePath)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PlanRebuild is what RebuildEpisodes would do to the stored files under `seasons`,
// without writing anything.
func (r *Repo) PlanRebuild(ctx context.Context, seriesID int64, seasons []Season) ([]EpisodeRemap, error) {
	current, err := snapshotRows(ctx, r.db, seriesID)
	if err != nil {
		return nil, err
	}
	return planRemaps(current, seasons), nil
}

// RebuildEpisodes replaces a series' entire season/episode listing with `seasons`, while
// preserving what belongs to the user and the library — file placement, size, source
// release, and monitoring (episodes by absolute number, seasons by season number). Files
// are placed by planPlacements, the same planner the preview uses.
//
// This is what INSERT-OR-IGNORE cannot do — it can add and re-title, but never renumber an
// existing episode. Runs in one transaction; on any error nothing changes. Returns the
// files whose (season, episode) moved, so the caller can rename them on disk; files the
// listing has no slot for are left out (they move nowhere).
func (r *Repo) RebuildEpisodes(ctx context.Context, seriesID int64, seasons []Season) ([]EpisodeRemap, error) {
	moved, _, err := r.rebuildEpisodes(ctx, seriesID, seasons)
	return moved, err
}

// rebuildEpisodes is RebuildEpisodes, also counting the files left with no episode.
func (r *Repo) rebuildEpisodes(ctx context.Context, seriesID int64, seasons []Season) (moved []EpisodeRemap, unplaced int, err error) {
	var remaps []EpisodeRemap
	err = store.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		var err error
		remaps, err = rebuildEpisodesTx(ctx, tx, seriesID, seasons)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	for _, rm := range remaps {
		if rm.Unplaced {
			unplaced++
		} else {
			moved = append(moved, rm)
		}
	}
	return moved, unplaced, nil
}

// rebuildEpisodesTx is RebuildEpisodes' body, inside the caller's transaction.
func rebuildEpisodesTx(ctx context.Context, tx *sql.Tx, seriesID int64, seasons []Season) ([]EpisodeRemap, error) {
	current, err := snapshotRows(ctx, tx, seriesID)
	if err != nil {
		return nil, err
	}
	// Season flags are the owner's too. Re-inserting seasons from the listing used to
	// reset every one to the series flag, re-monitoring seasons they'd switched off.
	monBySeason := map[int]bool{}
	srows, err := tx.QueryContext(ctx, `SELECT season_number, monitored FROM seasons WHERE series_id = ?`, seriesID)
	if err != nil {
		return nil, err
	}
	for srows.Next() {
		var n, mon int
		if err := srows.Scan(&n, &mon); err != nil {
			srows.Close()
			return nil, err
		}
		monBySeason[n] = mon != 0
	}
	if err := srows.Err(); err != nil {
		srows.Close()
		return nil, err
	}
	srows.Close()

	placed, remaps := planPlacements(current, seasons)

	// Rebuild from scratch. No foreign key points at episodes.id, so a clean replace is safe.
	if _, err := tx.ExecContext(ctx, `DELETE FROM episodes WHERE series_id = ?`, seriesID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM seasons WHERE series_id = ?`, seriesID); err != nil {
		return nil, err
	}
	for _, sn := range seasons {
		mon := sn.Monitored
		if old, ok := monBySeason[sn.SeasonNumber]; ok {
			mon = old
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO seasons (series_id, season_number, name, overview, poster_url, monitored)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			seriesID, sn.SeasonNumber, sn.Name, sn.Overview, sn.PosterURL, b2i(mon)); err != nil {
			return nil, err
		}
		for _, ep := range sn.Episodes {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO episodes (series_id, season_number, episode_number, title, overview, air_date, runtime, still_url, monitored, absolute_number)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				seriesID, ep.SeasonNumber, ep.EpisodeNumber, ep.Title, ep.Overview, ep.AirDate, ep.Runtime, ep.StillURL, b2i(ep.Monitored), ep.AbsoluteNumber); err != nil {
				return nil, err
			}
		}
	}
	for _, p := range placed {
		if _, err := tx.ExecContext(ctx,
			`UPDATE episodes SET has_file = 1, file_path = ?, size_bytes = ?, source_release = ?
			   WHERE series_id = ? AND season_number = ? AND episode_number = ?`,
			p.row.Path, p.row.Size, p.row.Release, seriesID, p.season, p.episode); err != nil {
			return nil, err
		}
	}
	// Preserve the user's episode monitoring: by absolute where there is one, otherwise
	// (specials) by (season, episode).
	for _, r := range current {
		var err error
		if r.Absolute > 0 {
			_, err = tx.ExecContext(ctx,
				`UPDATE episodes SET monitored = ? WHERE series_id = ? AND absolute_number = ?`,
				b2i(r.Monitored), seriesID, r.Absolute)
		} else {
			_, err = tx.ExecContext(ctx,
				`UPDATE episodes SET monitored = ? WHERE series_id = ? AND season_number = ? AND episode_number = ? AND absolute_number = 0`,
				b2i(r.Monitored), seriesID, r.Season, r.Episode)
		}
		if err != nil {
			return nil, err
		}
	}
	return remaps, nil
}

// NumberingPending is a renumber a refresh found but didn't apply: the listing from
// another source numbers the show differently, and these files would move. The owner
// reviews it and applies or dismisses it.
type NumberingPending struct {
	From      string         `json:"from"`
	To        string         `json:"to"`
	PlanHash  string         `json:"plan_hash"`
	Remaps    []EpisodeRemap `json:"remaps"`
	CreatedAt string         `json:"created_at"`
}

// UpsertNumberingPending stores the latest proposal for a show. It reports whether this
// plan is news to the owner: not the plan already pending, and not one they dismissed.
func (r *Repo) UpsertNumberingPending(ctx context.Context, seriesID int64, p NumberingPending) (fresh bool, err error) {
	b, err := json.Marshal(p.Remaps)
	if err != nil {
		return false, err
	}
	var prevHash, dismissed string
	err = r.db.QueryRowContext(ctx,
		`SELECT plan_hash, dismissed_hash FROM series_numbering_pending WHERE series_id = ?`, seriesID).Scan(&prevHash, &dismissed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if prevHash == p.PlanHash && err == nil {
		return false, nil // unchanged; keep its created_at
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO series_numbering_pending (series_id, from_source, to_source, plan_hash, remaps_json, created_at)
		 VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(series_id) DO UPDATE SET from_source = excluded.from_source, to_source = excluded.to_source,
		   plan_hash = excluded.plan_hash, remaps_json = excluded.remaps_json, created_at = excluded.created_at`,
		seriesID, p.From, p.To, p.PlanHash, string(b)); err != nil {
		return false, err
	}
	return p.PlanHash != dismissed, nil
}

// ClearNumberingPending withdraws a show's proposal (applied, or no longer what the
// metadata says). What the owner dismissed is remembered, so the same plan coming back
// later stays dismissed.
func (r *Repo) ClearNumberingPending(ctx context.Context, seriesID int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE series_numbering_pending SET plan_hash = '', remaps_json = '[]' WHERE series_id = ?`, seriesID)
	return err
}

// DismissNumberingPending hides the pending plan; the same plan isn't proposed again.
func (r *Repo) DismissNumberingPending(ctx context.Context, seriesID int64) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE series_numbering_pending SET dismissed_hash = plan_hash WHERE series_id = ? AND plan_hash <> '' AND plan_hash <> dismissed_hash`, seriesID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoPendingNumbering
	}
	return nil
}

// NumberingPendingFor returns the show's proposal awaiting review, or nil when there's
// none or the owner dismissed it.
func (r *Repo) NumberingPendingFor(ctx context.Context, seriesID int64) (*NumberingPending, error) {
	var p NumberingPending
	var remapsJSON, dismissed string
	err := r.db.QueryRowContext(ctx,
		`SELECT from_source, to_source, plan_hash, remaps_json, dismissed_hash, created_at
		   FROM series_numbering_pending WHERE series_id = ?`, seriesID).
		Scan(&p.From, &p.To, &p.PlanHash, &remapsJSON, &dismissed, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if p.PlanHash == "" || p.PlanHash == dismissed {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(remapsJSON), &p.Remaps); err != nil {
		return nil, err
	}
	if p.Remaps == nil {
		p.Remaps = []EpisodeRemap{}
	}
	return &p, nil
}

// ErrNoPendingNumbering is returned when there's no proposal to act on.
var ErrNoPendingNumbering = errors.New("there's no numbering change waiting for this series")

// ErrStalePlan is returned when the plan being applied is no longer what the metadata
// says, or the metadata couldn't be fetched to check. Nothing moved.
var ErrStalePlan = errors.New("the numbering changed or the metadata is unavailable — nothing was moved; try again")

// remapLines is the History list of a renumber's moves: the first 20, then a count.
func remapLines(remaps []EpisodeRemap) string {
	parts := make([]string, 0, 21)
	for i, r := range remaps {
		if i == 20 {
			parts = append(parts, fmt.Sprintf("and %d more", len(remaps)-20))
			break
		}
		parts = append(parts, fmt.Sprintf("%s → %s", fmtSxxExx(r.OldSeason, r.OldEpisode), fmtSxxExx(r.NewSeason, r.NewEpisode)))
	}
	return strings.Join(parts, ", ")
}
