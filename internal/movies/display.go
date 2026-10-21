package movies

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/tristenlammi/arrmada/internal/displaytitle"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The title a film is SHOWN by (Settings → Library → "Show titles as", and the per-film
// "Display title…" override) is presentation only. It lives in two columns: the override,
// and display_title — the title shown now, worked out here from the setting, the override
// and the names stored in extra_json's display_names, so every list, the calendar and
// alerts read it straight from the row. Matching (TitleKeys, the importer) and naming
// (the Importer's templates, renames) never read either: they keep using Title.

// ErrBadOverride is a display-title override CleanOverride refuses (displaytitle).
var ErrBadOverride = errors.New("a display title is at most 200 characters on one line, with something visible and no control or text-direction characters")

// SetDisplayPolicy installs the "Show titles as" setting, read on every use. Call it at
// startup; without it every film shows its canonical title (or its override).
func (s *Service) SetDisplayPolicy(fn func() displaytitle.Policy) { s.displayPolicy = fn }

// policy is the display setting in effect now.
func (s *Service) policy() displaytitle.Policy {
	if s.displayPolicy == nil {
		return displaytitle.Policy{Mode: displaytitle.ModeEnglish}
	}
	return s.displayPolicy()
}

// titleSource is where every name of a film is read from, with its overviews in the
// language the display setting describes titles in, so the import gate, the backfills and
// the display names share one cached TMDB request per film.
func (s *Service) titleSource() metadata.TitlesSource {
	return metadata.WithOverviews(s.titles, s.policy().OverviewLang())
}

// withDisplayOverview sets m's DisplayOverview: its overview in the setting's language
// when one is stored, left empty when that is the English overview it already carries.
// Worked out on read from the stored names; never asks TMDB.
func (s *Service) withDisplayOverview(m *Movie) {
	var names *displaytitle.Names
	if m.Extra != nil {
		names = m.Extra.DisplayNames
	}
	if o := s.policy().ShownOverview(m.Overview, names); o != m.Overview {
		m.DisplayOverview = o
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

// Shown is the title the app shows for m: DisplayTitle, or Title for a film built in
// memory rather than read from the database. Never for matching or naming.
func (m Movie) Shown() string { return shownTitle(m.DisplayTitle, m.Title) }

// displayFor works out the title m shows under p.
func displayFor(p displaytitle.Policy, m Movie) string {
	t := displaytitle.Title{Canonical: m.Title, Override: m.TitleOverride}
	if m.Extra != nil {
		t.OriginalLanguage = m.Extra.OriginalLanguage
		t.Names = m.Extra.DisplayNames
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

// displayRow is what a film's shown title is worked out from, exactly as read. The
// update that stores the result lands only while the row still holds these values, so a
// pass that read the row before a refresh, an override or newly stored names can never
// put back a title worked out from the old ones.
type displayRow struct {
	id                              int64
	title, override, display, extra string
}

// movie is the row as the fields displayFor reads.
func (r displayRow) movie() Movie {
	x := Movie{ID: r.id, Title: r.title, TitleOverride: r.override, DisplayTitle: r.display}
	if r.extra != "" {
		var ex MovieExtra
		if json.Unmarshal([]byte(r.extra), &ex) == nil {
			x.Extra = &ex
		}
	}
	return x
}

// want is the display_title the row should hold under p.
func (r displayRow) want(p displaytitle.Policy) string {
	return storedDisplay(displayFor(p, r.movie()), r.title)
}

// displayRows reads the rows display titles are worked out from: one film (id > 0) or
// all of them.
func (r *Repo) displayRows(ctx context.Context, id int64) ([]displayRow, error) {
	q := `SELECT id, title, title_override, display_title, extra_json FROM movies`
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
	res, err := ex.ExecContext(ctx, `UPDATE movies SET display_title = ?
		WHERE id = ? AND title = ? AND title_override = ? AND display_title = ? AND extra_json = ?`,
		want, d.id, d.title, d.override, d.display, d.extra)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// syncDisplay works out one film's shown title again and stores it, after anything it
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
			s.log.Warn("movies: couldn't store the display title", "film", d.title, "err", err)
			return
		}
		if done {
			return
		}
	}
}

// SyncDisplayTitles works out every film's shown title again — at start and whenever
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

// showCreated stores a just-added film's shown title and sets it on m.
func (s *Service) showCreated(ctx context.Context, m *Movie) {
	s.syncDisplay(ctx, m.ID)
	m.DisplayTitle = shownTitle(storedDisplay(displayFor(s.policy(), *m), m.Title), m.Title)
}

// SetTitleOverride sets (or, with "", clears) a film's display-title override and returns
// the film as it now shows. A refresh never touches it: it is a column of its own.
func (s *Service) SetTitleOverride(ctx context.Context, id int64, override string) (Movie, error) {
	clean, ok := displaytitle.CleanOverride(override)
	if !ok {
		return Movie{}, ErrBadOverride
	}
	if _, err := s.repo.Get(ctx, id); err != nil {
		return Movie{}, err
	}
	if _, err := s.repo.q().ExecContext(ctx, `UPDATE movies SET title_override = ? WHERE id = ?`, clean, id); err != nil {
		return Movie{}, err
	}
	s.syncDisplay(ctx, id)
	return s.repo.Get(ctx, id)
}

// TitleChoices lists the names a film may be shown by, for the "Display title…" picker:
// its English and original titles and every translation TMDB has (read through the titles
// cache; the stored names alone when TMDB can't be reached).
func (s *Service) TitleChoices(ctx context.Context, id int64) ([]displaytitle.Choice, error) {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	var names *displaytitle.Names
	if m.Extra != nil {
		names = m.Extra.DisplayNames
	}
	var all *metadata.Titles
	if s.titles != nil && m.TMDBID > 0 {
		if t, err := s.titleSource().Titles(ctx, metadata.TitlesMovie, m.TMDBID); err == nil {
			all = t
		} else {
			s.log.Debug("movies: couldn't read every title for the picker; offering the stored ones", "movie", m.Title, "err", err)
		}
	}
	return displaytitle.Choices(m.Title, names, all), nil
}

// needsDisplayNames reports whether m's display names haven't been read for what p shows.
// The default setting shows only the canonical title, so it reads nothing.
func needsDisplayNames(m Movie, p displaytitle.Policy) bool {
	if p.Default() || m.TMDBID <= 0 {
		return false
	}
	return m.Extra == nil || !m.Extra.DisplayNames.Covers(p)
}

// readDisplayNames reads a film's display names for p through the titles cache.
func (s *Service) readDisplayNames(ctx context.Context, tmdbID int, p displaytitle.Policy) (*displaytitle.Names, error) {
	if s.titles == nil || tmdbID <= 0 {
		return nil, ErrNoTitles
	}
	t, err := s.titleSource().Titles(ctx, metadata.TitlesMovie, tmdbID)
	if err != nil {
		return nil, err
	}
	return p.NamesFrom(t), nil
}

// withDisplayNames fills a fresh extra blob's display names on add and refresh, keeping
// the ones stored before (prev) when they can't be read now. Under the default setting
// nothing is read; the stored ones are kept for when the setting changes.
func (s *Service) withDisplayNames(ctx context.Context, ex, prev *MovieExtra, tmdbID int, title string) {
	if ex == nil {
		return
	}
	if prev != nil {
		ex.DisplayNames = prev.DisplayNames
	}
	p := s.policy()
	if p.Default() {
		return
	}
	got, err := s.readDisplayNames(ctx, tmdbID, p)
	if err != nil {
		if !errors.Is(err, ErrNoTitles) {
			s.log.Debug("movies: couldn't read display titles; the titles backfill tries again", "movie", title, "err", err)
		}
		return
	}
	ex.DisplayNames = got
}

// setDisplayNames stores a film's display names, leaving the rest of its extra metadata
// as it is (json_set, as SetLocalTitles), and works out its shown title again.
func (s *Service) setDisplayNames(ctx context.Context, id int64, n *displaytitle.Names) error {
	if err := s.repo.SetDisplayNames(ctx, id, n); err != nil {
		return err
	}
	s.syncDisplay(ctx, id)
	return nil
}
