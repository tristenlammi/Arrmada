package series

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/displaytitle"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The title a show is SHOWN by (Settings → Library → "Show titles as", and the per-show
// "Display title…" override) is presentation only, as for movies (movies/display.go): the
// override and display_title — the title shown now — are columns of their own, worked out
// here. Matching (FitRelease, MatchRelease, SearchFit) and naming (folders, episode files,
// renames) never read either: they keep using Title.

// ErrBadOverride is a display-title override CleanOverride refuses (displaytitle).
var ErrBadOverride = errors.New("a display title is at most 200 characters on one line, with something visible and no control or text-direction characters")

// SetDisplayPolicy installs the "Show titles as" setting, read on every use. Call it at
// startup; without it every show shows its canonical title (or its override).
func (s *Service) SetDisplayPolicy(fn func() displaytitle.Policy) { s.displayPolicy = fn }

// policy is the display setting in effect now.
func (s *Service) policy() displaytitle.Policy {
	if s.displayPolicy == nil {
		return displaytitle.Policy{Mode: displaytitle.ModeEnglish}
	}
	return s.displayPolicy()
}

// titleSource is where every name of a show is read from, with its overviews in the
// language the display setting describes titles in, so the import gate, the backfills and
// the display names share one cached TMDB request per show.
func (s *Service) titleSource() metadata.TitlesSource {
	return metadata.WithOverviews(s.titles, s.policy().OverviewLang())
}

// withDisplayOverview sets sr's DisplayOverview: its overview in the setting's language
// when one is stored, left empty when that is the English overview it already carries.
// Worked out on read from the stored names; never asks TMDB. Season and episode
// overviews stay TMDB's English ones (series.go, DisplayOverview).
func (s *Service) withDisplayOverview(sr *Series) {
	var names *displaytitle.Names
	if sr.Extra != nil {
		names = sr.Extra.DisplayNames
	}
	if o := s.policy().ShownOverview(sr.Overview, names); o != sr.Overview {
		sr.DisplayOverview = o
	}
}

// shownTitle is the stored display_title, or the canonical title when it's empty
// (the default, and rows not worked out yet).
func shownTitle(display, title string) string {
	if strings.TrimSpace(display) != "" {
		return display
	}
	return title
}

// Shown is the title the app shows for sr: DisplayTitle, or Title for a show built in
// memory rather than read from the database. Never for matching or naming.
func (sr Series) Shown() string { return shownTitle(sr.DisplayTitle, sr.Title) }

// displayFor works out the title sr shows under p. A show's stored original title is
// already in its own script, so it shows even before the display names are read.
func displayFor(p displaytitle.Policy, sr Series) string {
	t := displaytitle.Title{Canonical: sr.Title, Override: sr.TitleOverride}
	if sr.Extra != nil {
		t.OriginalTitle = sr.Extra.OriginalTitle
		t.OriginalLanguage = sr.Extra.OriginalLanguage
		t.Names = sr.Extra.DisplayNames
	}
	return p.Resolve(t)
}

// storedDisplay is what goes in display_title for a shown title: empty when it is
// the canonical title (surrounding spaces aside), so the default setting stores nothing.
func storedDisplay(shown, title string) string {
	if strings.TrimSpace(shown) == strings.TrimSpace(title) {
		return ""
	}
	return shown
}

// displayRow is what a show's shown title is worked out from, exactly as read. The
// update that stores the result lands only while the row still holds these values, so a
// pass that read the row before a refresh, an override or newly stored names can never
// put back a title worked out from the old ones.
type displayRow struct {
	id                              int64
	title, override, display, extra string
}

// series is the row as the fields displayFor reads.
func (r displayRow) series() Series {
	x := Series{ID: r.id, Title: r.title, TitleOverride: r.override, DisplayTitle: r.display}
	if r.extra != "" {
		var ex SeriesExtra
		if json.Unmarshal([]byte(r.extra), &ex) == nil {
			x.Extra = &ex
		}
	}
	return x
}

// want is the display_title the row should hold under p.
func (r displayRow) want(p displaytitle.Policy) string {
	return storedDisplay(displayFor(p, r.series()), r.title)
}

// displayRows reads the rows display titles are worked out from: one show (id > 0) or
// all of them.
func (r *Repo) displayRows(ctx context.Context, id int64) ([]displayRow, error) {
	q := `SELECT id, title, title_override, display_title, extra_json FROM series`
	var args []any
	if id > 0 {
		q += ` WHERE id = ?`
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []displayRow
	for rows.Next() {
		var d displayRow
		if err := rows.Scan(&d.id, &d.title, &d.override, &d.display, &d.extra); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// storeDisplayIf stores want as d's display_title only while the row still holds what
// was read into d, and reports whether it did.
func storeDisplayIf(ctx context.Context, ex store.Execer, d displayRow, want string) (bool, error) {
	res, err := ex.ExecContext(ctx, `UPDATE series SET display_title = ?
		WHERE id = ? AND title = ? AND title_override = ? AND display_title = ? AND extra_json = ?`,
		want, d.id, d.title, d.override, d.display, d.extra)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// syncDisplay works out one show's shown title again and stores it, after anything it
// depends on changed (a refresh, an override, newly read names). A row that changes
// between the read and the write is read again (a few times at most). Best effort: a
// failure leaves the previous title shown, and the next start puts it right.
func (s *Service) syncDisplay(ctx context.Context, id int64) {
	for range 3 {
		rows, err := s.repo.displayRows(ctx, id)
		if err != nil || len(rows) == 0 {
			return
		}
		d := rows[0]
		want := d.want(s.policy())
		if want == storedDisplay(d.display, d.title) {
			return
		}
		done, err := storeDisplayIf(ctx, s.repo.db, d, want)
		if err != nil {
			s.log.Warn("series: couldn't store the display title", "show", d.title, "err", err)
			return
		}
		if done {
			return
		}
	}
}

// SyncDisplayTitles works out every show's shown title again — at start and whenever
// the setting changes — and returns how many changed. It only reads the database: names
// not read yet show the best title available now and are filled by the titles backfill.
// One pass runs at a time; a row that changed while the pass worked is worked out again
// on its own.
func (s *Service) SyncDisplayTitles(ctx context.Context) (int, error) {
	s.displaySync.Lock()
	defer s.displaySync.Unlock()
	all, err := s.repo.displayRows(ctx, 0)
	if err != nil {
		return 0, err
	}
	p := s.policy()
	type change struct {
		row  displayRow
		want string
	}
	var changes []change
	for _, d := range all {
		if want := d.want(p); want != storedDisplay(d.display, d.title) {
			changes = append(changes, change{d, want})
		}
	}
	if len(changes) == 0 {
		return 0, nil
	}
	var missed []int64
	err = store.WithTx(ctx, s.repo.db, func(tx *store.Transaction) error {
		missed = missed[:0]
		for _, c := range changes {
			done, err := storeDisplayIf(ctx, tx, c.row, c.want)
			if err != nil {
				return err
			}
			if !done {
				missed = append(missed, c.row.id)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, id := range missed {
		s.syncDisplay(ctx, id)
	}
	return len(changes), nil
}

// SetTitleOverride sets (or, with "", clears) a show's display-title override and returns
// the show as it now shows. A refresh never touches it: it is a column of its own.
func (s *Service) SetTitleOverride(ctx context.Context, id int64, override string) (Series, error) {
	clean, ok := displaytitle.CleanOverride(override)
	if !ok {
		return Series{}, ErrBadOverride
	}
	if _, err := s.repo.Get(ctx, id); err != nil {
		return Series{}, err
	}
	if _, err := s.repo.db.ExecContext(ctx, `UPDATE series SET title_override = ? WHERE id = ?`, clean, id); err != nil {
		return Series{}, err
	}
	s.syncDisplay(ctx, id)
	return s.Get(ctx, id)
}

// TitleChoices lists the names a show may be shown by, for the "Display title…" picker:
// its English and original titles and every translation TMDB has (through the titles
// cache; the stored names alone when TMDB can't be reached).
func (s *Service) TitleChoices(ctx context.Context, id int64) ([]displaytitle.Choice, error) {
	sr, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	names := &displaytitle.Names{}
	if sr.Extra != nil {
		if sr.Extra.DisplayNames != nil {
			names = sr.Extra.DisplayNames
		} else {
			names = &displaytitle.Names{Original: sr.Extra.OriginalTitle, OriginalLanguage: sr.Extra.OriginalLanguage}
		}
	}
	var all *metadata.Titles
	if s.titles != nil && sr.TMDBID > 0 {
		if t, err := s.titleSource().Titles(ctx, metadata.TitlesSeries, sr.TMDBID); err == nil {
			all = t
		} else {
			s.log.Debug("series: couldn't read every title for the picker; offering the stored ones", "series", sr.Title, "err", err)
		}
	}
	return displaytitle.Choices(sr.Title, names, all), nil
}

// needsDisplayNames reports whether sr's display names haven't been read for what p
// shows. The default setting shows only the canonical title, so it reads nothing.
func needsDisplayNames(sr Series, p displaytitle.Policy) bool {
	if p.Default() || sr.TMDBID <= 0 {
		return false
	}
	return sr.Extra == nil || !sr.Extra.DisplayNames.Covers(p)
}

// refreshDisplayNames reads and stores a show's display names for the setting now, on add
// and refresh, and works out its shown title again either way (the title itself may have
// changed). When the names can't be read the stored ones stay, and the titles backfill
// tries again.
func (s *Service) refreshDisplayNames(ctx context.Context, id int64, tmdbID int, title string) {
	defer s.syncDisplay(ctx, id)
	p := s.policy()
	if p.Default() || s.titles == nil || tmdbID <= 0 {
		return
	}
	t, err := s.titleSource().Titles(ctx, metadata.TitlesSeries, tmdbID)
	if err != nil {
		s.log.Debug("series: couldn't read display titles; the titles backfill tries again", "series", title, "err", err)
		return
	}
	if err := s.repo.SetDisplayNames(ctx, id, p.NamesFrom(t)); err != nil {
		s.log.Warn("series: couldn't store display titles", "series", title, "err", err)
	}
}

// backfillDisplayOnce reads the display names of every show not yet read for the setting
// now, one show per every so a large library never bursts TMDB, and returns how many it
// filled. It stops when TMDB isn't configured or ctx ends; a show TMDB can't answer right
// now is left for the next run.
func (s *Service) backfillDisplayOnce(ctx context.Context, every time.Duration) (int, error) {
	p := s.policy()
	if p.Default() || s.titles == nil {
		return 0, nil
	}
	all, err := s.repo.List(ctx)
	if err != nil {
		return 0, err
	}
	filled := 0
	for _, sr := range all {
		if !needsDisplayNames(sr, p) {
			continue
		}
		t := time.NewTimer(every)
		select {
		case <-ctx.Done():
			t.Stop()
			return filled, ctx.Err()
		case <-t.C:
		}
		got, err := s.titleSource().Titles(ctx, metadata.TitlesSeries, sr.TMDBID)
		var names *displaytitle.Names
		switch {
		case errors.Is(err, metadata.ErrNotConfigured):
			return filled, nil
		case errors.Is(err, metadata.ErrNotFound):
			names = p.NamesFrom(nil) // read, nothing to show
		case err != nil:
			if ctx.Err() != nil {
				return filled, ctx.Err()
			}
			s.log.Debug("series: couldn't read display titles; trying again next run", "series", sr.Title, "err", err)
			continue
		default:
			names = p.NamesFrom(got)
		}
		if err := s.repo.SetDisplayNames(ctx, sr.ID, names); err != nil {
			return filled, err
		}
		s.syncDisplay(ctx, sr.ID)
		filled++
	}
	return filled, nil
}

// SetDisplayNames stores a show's display names, leaving the rest of its extra metadata as
// it is (json_set, as SetLocalTitles).
func (r *Repo) SetDisplayNames(ctx context.Context, id int64, n *displaytitle.Names) error {
	if store.DialectOf(r.db) == store.Postgres {
		return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
			return store.SetJSONFields(ctx, tx, "series", "extra_json", id, map[string]any{"display_names": n})
		})
	}
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `UPDATE series SET extra_json = json_set(
			CASE WHEN json_valid(extra_json) AND extra_json <> '' THEN extra_json ELSE '{}' END,
			'$.display_names', json(?))
		WHERE id = ?`, string(b), id)
	return err
}
