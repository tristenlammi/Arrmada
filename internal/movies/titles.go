package movies

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/tristenlammi/arrmada/internal/displaytitle"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/store"
)

// minAltKeyLen is the shortest alternative-title key matching uses. A one- or two-letter
// alternative ("M", "Up") matches far too many release names to stand in for a film on its
// own; the film's own title is always used whatever its length.
const minAltKeyLen = 3

// PrimaryKeys maps each library movie's own title key (parser.TitleKey of its Title) to
// the ids of the movies titled so. It is the collision guard for alternative titles.
type PrimaryKeys map[string][]int64

// NewPrimaryKeys indexes the own title keys of a library snapshot.
func NewPrimaryKeys(all []Movie) PrimaryKeys {
	p := make(PrimaryKeys, len(all))
	for _, m := range all {
		k := normalizeTitle(m.Title)
		p[k] = append(p[k], m.ID)
	}
	return p
}

// claimedByOther reports whether k is the own title of a library movie other than id.
func (p PrimaryKeys) claimedByOther(k string, id int64) bool {
	for _, other := range p[k] {
		if other != id {
			return true
		}
	}
	return false
}

// PrimaryKeys reads the library's own title keys (see the type).
func (s *Service) PrimaryKeys(ctx context.Context) (PrimaryKeys, error) {
	rows, err := s.repo.q().QueryContext(ctx, `SELECT id, title FROM movies`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	p := PrimaryKeys{}
	for rows.Next() {
		var (
			id    int64
			title string
		)
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		k := normalizeTitle(title)
		p[k] = append(p[k], id)
	}
	return p, rows.Err()
}

// TitleKeys returns the title keys a release for m may carry: its own title's first, then
// its original title and alternative titles (Latin script only, at least minAltKeyLen
// long). An alternative that is ANOTHER library movie's own title is left out, so a
// release by that name stays that film's: with "Dune" (2021) in the library, a film whose
// alternative title is also "Dune" never takes its releases. A nil taken guards nothing.
//
// Search and import share these keys (both normalise with parser.TitleKey), so a release
// a search accepted under an alternative title is matched to the same movie on import.
func TitleKeys(m Movie, taken PrimaryKeys) []string {
	primary := normalizeTitle(m.Title)
	keys := []string{primary}
	if m.Extra == nil {
		return keys
	}
	seen := map[string]bool{primary: true}
	add := func(title string) {
		k := normalizeTitle(title)
		if len(k) < minAltKeyLen || seen[k] || !parser.IsLatin(title) || taken.claimedByOther(k, m.ID) {
			return
		}
		seen[k] = true
		keys = append(keys, k)
	}
	add(m.Extra.OriginalTitle)
	for _, a := range m.Extra.AltTitles {
		add(a)
	}
	return keys
}

// TitleMatches reports whether a parsed release title and year are for m under keys
// (TitleKeys): the title keys the same as one of them, and the year — when both are
// known — is within one. The year check is what keeps a same-titled remake out.
func TitleMatches(keys []string, m Movie, title string, year int) bool {
	k := normalizeTitle(title)
	for _, key := range keys {
		if k == key {
			return year == 0 || m.Year == 0 || abs(m.Year-year) <= 1
		}
	}
	return false
}

// BackfillAltTitles reads the original and alternative titles of every movie stored before
// they were kept (Extra.TitlesRead unset), the translated titles of every movie not yet
// read for all the "Also match titles in" languages (needsLocalTitles), and the display
// names the "Show titles as" setting needs (needsDisplayNames), one movie per every so a
// large library never bursts TMDB, and returns how many it filled. It stops
// when none are left, when TMDB isn't configured (the next start tries again), or when ctx
// ends. A movie TMDB can't answer right now is left for the next start; one TMDB no longer
// has is marked read. New movies and Refresh fill the titles themselves.
//
// It runs at start and again whenever the languages or the display setting change. One pass runs at a time: a
// call while one is running asks it to go round once more (the languages it read may be
// out of date) and returns at once with 0.
func (s *Service) BackfillAltTitles(ctx context.Context, every time.Duration) (int, error) {
	total := 0
	s.titlesAgain.Store(true)
	for s.titlesAgain.Load() {
		if !s.titlesPass.TryLock() {
			return total, nil // the pass running now sees titlesAgain and goes round again
		}
		var err error
		for err == nil && s.titlesAgain.Swap(false) {
			var n int
			n, err = s.backfillTitlesOnce(ctx, every)
			total += n
		}
		s.titlesPass.Unlock()
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// backfillTitlesOnce is one pass of BackfillAltTitles over the library.
func (s *Service) backfillTitlesOnce(ctx context.Context, every time.Duration) (int, error) {
	if s.meta == nil || !s.meta.Available() {
		return 0, nil
	}
	all, err := s.repo.List(ctx)
	if err != nil {
		return 0, err
	}
	langs := s.localLangs()
	display := s.policy()
	filled := 0
	for _, m := range all {
		needAlt := m.TMDBID > 0 && (m.Extra == nil || !m.Extra.TitlesRead)
		needLocal := s.titles != nil && needsLocalTitles(m, langs)
		needDisplay := s.titles != nil && needsDisplayNames(m, display)
		if !needAlt && !needLocal && !needDisplay {
			continue
		}
		t := time.NewTimer(every)
		select {
		case <-ctx.Done():
			t.Stop()
			return filled, ctx.Err()
		case <-t.C:
		}
		if needAlt {
			d, err := s.meta.GetMovie(ctx, m.TMDBID)
			switch {
			case errors.Is(err, metadata.ErrNotConfigured):
				return filled, nil
			case errors.Is(err, metadata.ErrNotFound):
				d = &metadata.MovieDetails{}
			case err != nil:
				if ctx.Err() != nil {
					return filled, ctx.Err()
				}
				s.log.Debug("movies: couldn't read alternative titles; trying again next start", "movie", m.Title, "err", err)
				continue
			}
			if err := s.repo.SetTitles(ctx, m.ID, d.OriginalTitle, d.AltTitles); err != nil {
				return filled, err
			}
		}
		if needLocal {
			got, err := s.readLocalTitles(ctx, m.TMDBID, m.Title, langs)
			switch {
			case errors.Is(err, metadata.ErrNotConfigured):
				return filled, nil
			case errors.Is(err, metadata.ErrNotFound):
				got = []metadata.TranslatedTitle{} // TMDB no longer has it: read, nothing to match
			case err != nil:
				if ctx.Err() != nil {
					return filled, ctx.Err()
				}
				s.log.Debug("movies: couldn't read translated titles; trying again next start", "movie", m.Title, "err", err)
				continue
			}
			if err := s.repo.SetLocalTitles(ctx, m.ID, got, langs); err != nil {
				return filled, err
			}
		}
		if needDisplay {
			// The same titles read as the translated titles above (one request, cached).
			got, err := s.readDisplayNames(ctx, m.TMDBID, display)
			switch {
			case errors.Is(err, metadata.ErrNotConfigured):
				return filled, nil
			case errors.Is(err, metadata.ErrNotFound):
				got = display.NamesFrom(nil) // read, nothing to show
			case err != nil:
				if ctx.Err() != nil {
					return filled, ctx.Err()
				}
				s.log.Debug("movies: couldn't read display titles; trying again next start", "movie", m.Title, "err", err)
				continue
			}
			if err := s.setDisplayNames(ctx, m.ID, got); err != nil {
				return filled, err
			}
		}
		filled++
	}
	return filled, nil
}

// SetTitles stores a movie's original and alternative titles and marks them read, leaving
// the rest of its extra metadata as it is (json_set, so a Refresh writing the whole blob
// at the same moment can't be half-overwritten by a read-modify-write here).
func (r *Repo) SetTitles(ctx context.Context, id int64, original string, alts []string) error {
	if alts == nil {
		alts = []string{}
	}
	if store.DialectOf(r.q()) == store.Postgres {
		return r.inTx(ctx, func(tx *store.Transaction, _ *Repo) error {
			return store.SetJSONFields(ctx, tx, "movies", "extra_json", id, map[string]any{
				"original_title": original, "alt_titles": alts, "titles_read": true,
			})
		})
	}
	b, err := json.Marshal(alts)
	if err != nil {
		return err
	}
	_, err = r.q().ExecContext(ctx, `UPDATE movies SET extra_json = json_set(
			CASE WHEN json_valid(extra_json) AND extra_json <> '' THEN extra_json ELSE '{}' END,
			'$.original_title', ?, '$.alt_titles', json(?), '$.titles_read', json('true'))
		WHERE id = ?`, original, string(b), id)
	return err
}

// SetDisplayNames stores a movie's display names (display.go), leaving the rest of its
// extra metadata as it is (json_set, as SetTitles).
func (r *Repo) SetDisplayNames(ctx context.Context, id int64, n *displaytitle.Names) error {
	if store.DialectOf(r.q()) == store.Postgres {
		return r.inTx(ctx, func(tx *store.Transaction, _ *Repo) error {
			return store.SetJSONFields(ctx, tx, "movies", "extra_json", id, map[string]any{"display_names": n})
		})
	}
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	_, err = r.q().ExecContext(ctx, `UPDATE movies SET extra_json = json_set(
			CASE WHEN json_valid(extra_json) AND extra_json <> '' THEN extra_json ELSE '{}' END,
			'$.display_names', json(?))
		WHERE id = ?`, string(b), id)
	return err
}

// SetLocalTitles stores a movie's translated titles and the languages they were read for,
// leaving the rest of its extra metadata as it is (json_set, as SetTitles).
func (r *Repo) SetLocalTitles(ctx context.Context, id int64, titles []metadata.TranslatedTitle, langs []string) error {
	if titles == nil {
		titles = []metadata.TranslatedTitle{}
	}
	if langs == nil {
		langs = []string{}
	}
	if store.DialectOf(r.q()) == store.Postgres {
		return r.inTx(ctx, func(tx *store.Transaction, _ *Repo) error {
			return store.SetJSONFields(ctx, tx, "movies", "extra_json", id, map[string]any{"local_titles": titles, "local_langs": langs})
		})
	}
	tb, err := json.Marshal(titles)
	if err != nil {
		return err
	}
	lb, err := json.Marshal(langs)
	if err != nil {
		return err
	}
	_, err = r.q().ExecContext(ctx, `UPDATE movies SET extra_json = json_set(
			CASE WHEN json_valid(extra_json) AND extra_json <> '' THEN extra_json ELSE '{}' END,
			'$.local_titles', json(?), '$.local_langs', json(?))
		WHERE id = ?`, string(tb), string(lb), id)
	return err
}
