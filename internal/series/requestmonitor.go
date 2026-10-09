package series

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/store"
)

// NormalizeSeasons is a season list as stored and compared: regular seasons only,
// ascending, each once. nil stays nil; a list with nothing regular in it becomes empty.
func NormalizeSeasons(in []int) []int {
	if in == nil {
		return nil
	}
	seen := map[int]bool{}
	out := []int{}
	for _, n := range in {
		if n > 0 && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// SeasonsLabel writes a season list compactly, runs collapsed: [1,2,3,5] is "S1–3, S5".
func SeasonsLabel(seasons []int) string {
	ns := NormalizeSeasons(seasons)
	var parts []string
	for i := 0; i < len(ns); {
		j := i
		for j+1 < len(ns) && ns[j+1] == ns[j]+1 {
			j++
		}
		if j > i {
			parts = append(parts, fmt.Sprintf("S%d–%d", ns[i], ns[j]))
		} else {
			parts = append(parts, fmt.Sprintf("S%d", ns[i]))
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
}

// seasonIn is "season_number IN (?, ?, …)" with its arguments.
func seasonIn(seasons []int) (string, []any) {
	marks := make([]string, len(seasons))
	args := make([]any, len(seasons))
	for i, n := range seasons {
		marks[i], args[i] = "?", n
	}
	return "season_number IN (" + strings.Join(marks, ", ") + ")", args
}

// MonitorOnlySeasons sets a show's monitoring to exactly the chosen regular seasons: their
// episodes monitored, every other episode (specials included) not, and "monitor new
// seasons" set to newSeasons. A chosen season with no episodes yet (announced) is
// monitored too, so the episodes a refresh adds to it are wanted. The pause gate is left
// alone.
func (r *Repo) MonitorOnlySeasons(ctx context.Context, id int64, seasons map[int]bool, newSeasons bool) error {
	var want []int
	for n, on := range seasons {
		if on && n > 0 {
			want = append(want, n)
		}
	}
	sort.Ints(want)
	return store.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE episodes SET monitored = 0 WHERE series_id = ?`, id); err != nil {
			return err
		}
		in, args := seasonIn(want)
		if len(want) > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE episodes SET monitored = 1 WHERE series_id = ? AND `+in, append([]any{id}, args...)...); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE series SET monitor_new_seasons = ? WHERE id = ?`, b2i(newSeasons), id); err != nil {
			return err
		}
		if err := syncSeasonsTx(ctx, tx, id, -1, newSeasons); err != nil {
			return err
		}
		if len(want) > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE seasons SET monitored = 1 WHERE series_id = ? AND `+in, append([]any{id}, args...)...); err != nil {
				return err
			}
		}
		return nil
	})
}

// monitorForRequest is EnsureMonitored's write, in one transaction. whole monitors every
// regular season and new ones. Otherwise only the listed seasons are turned on, and when
// the show was paused every other regular season and "monitor new seasons" are turned
// off, so resuming the show for one season doesn't also resume what the owner had paused.
// turnedOff reports whether that switched any monitored episode off.
func (r *Repo) monitorForRequest(ctx context.Context, id int64, seasons []int, whole, wasPaused bool) (turnedOff bool, err error) {
	err = store.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		if whole {
			for _, q := range []string{
				`UPDATE seasons SET monitored = 1 WHERE series_id = ? AND season_number > 0`,
				`UPDATE episodes SET monitored = 1 WHERE series_id = ? AND season_number > 0`,
				`UPDATE series SET monitor_new_seasons = 1, monitored = 1 WHERE id = ?`,
			} {
				if _, err := tx.ExecContext(ctx, q, id); err != nil {
					return err
				}
			}
			return nil
		}
		in, args := seasonIn(seasons)
		withID := append([]any{id}, args...)
		if wasPaused {
			var others int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM episodes WHERE series_id = ? AND season_number > 0 AND monitored = 1 AND NOT `+in,
				withID...).Scan(&others); err != nil {
				return err
			}
			turnedOff = others > 0
			for _, q := range []string{
				`UPDATE episodes SET monitored = 0 WHERE series_id = ? AND season_number > 0 AND NOT ` + in,
				`UPDATE seasons SET monitored = 0 WHERE series_id = ? AND season_number > 0 AND NOT ` + in,
			} {
				if _, err := tx.ExecContext(ctx, q, withID...); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE series SET monitor_new_seasons = 0 WHERE id = ?`, id); err != nil {
				return err
			}
		}
		for _, q := range []string{
			`UPDATE seasons SET monitored = 1 WHERE series_id = ? AND ` + in,
			`UPDATE episodes SET monitored = 1 WHERE series_id = ? AND ` + in,
		} {
			if _, err := tx.ExecContext(ctx, q, withID...); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE series SET monitored = 1 WHERE id = ?`, id)
		return err
	})
	return turnedOff, err
}

// EnsureMonitored makes a show already in the library fetch what an approved request
// asked for: the listed regular seasons (nil = the whole show), with the pause gate on.
// It never goes through Repo.SetMonitored, whose cascade decides other seasons too. Other
// seasons' choices stand — unless the show was paused, when they are switched off rather
// than resumed along with the request (see monitorForRequest). A listed season the
// library has no row for yet (TMDB added it since the last refresh) is fetched by a
// refresh first. The show's History records who it was for.
func (s *Service) EnsureMonitored(ctx context.Context, id int64, seasons []int, requestedBy string) error {
	sr, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	whole := seasons == nil
	want := NormalizeSeasons(seasons)
	if !whole && len(want) == 0 {
		return nil
	}
	if !whole {
		if flags, err := s.repo.SeasonMonitorFlags(ctx, id); err == nil {
			for _, n := range want {
				if _, ok := flags[n]; !ok {
					if _, _, rerr := s.Refresh(ctx, id, RefreshOptions{}); rerr != nil {
						s.log.Warn("series: refresh before monitoring a requested season failed", "series", sr.Title, "season", n, "err", rerr)
					}
					break
				}
			}
		}
	}
	turnedOff, err := s.repo.monitorForRequest(ctx, id, want, whole, !sr.Monitored)
	if err != nil {
		return err
	}
	what := "all seasons"
	if !whole {
		what = SeasonsLabel(want)
	}
	who := strings.TrimSpace(requestedBy)
	if who == "" {
		who = "a requester"
	}
	detail := "Monitored by request from " + who + ": " + what
	if turnedOff {
		detail += " (the show was paused, so its other seasons are no longer monitored)"
	}
	s.AddEvent(ctx, id, "monitored", detail)
	s.log.Info("series: monitored by request", "series", sr.Title, "seasons", what, "by", who)
	return nil
}
