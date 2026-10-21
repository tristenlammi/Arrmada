package series

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Shows are released, and folders named, in the household's language: a German tracker
// lists Money Heist as "Haus.des.Geldes.S01E01.German…". As for movies (movies/
// localtitles.go), two answers by how much is at stake:
//
//   - KnownAs, the episode import gate's check for a download grabbed for one show: every
//     name TMDB has for that show, in any language, with the usual year and country
//     checks. It only confirms the show the grab was made for.
//   - The translated titles in the languages Settings → Library → "Also match titles in"
//     names, stored per show and used by import routing (MatchRelease) and library scans
//     for a name no show goes by otherwise, only when exactly one show fits.
//
// FitRelease itself still knows only the show's own titles and aliases. Searches, RSS and
// upgrades judge by SearchFit (searchtitles.go), which also takes a release named by a
// stored translated title, under stricter rules than import routing's: there a translated
// title could otherwise let a search for one show take another show's releases.

// ErrNoTitles is KnownAs unable to read a show's names (no TMDB key, no TMDB id).
var ErrNoTitles = errors.New("the show's other titles can't be read")

// minLocalKeyLen is the shortest title key a translated title is matched on (as movies'
// alternative titles), in letters (keyLen).
const minLocalKeyLen = 3

// keyLen is a title key's length in letters, not bytes: "тьма" is four letters, eight
// bytes.
func keyLen(k string) int { return utf8.RuneCountInString(k) }

// SetTitlesSource installs where every name of a show is read from (TMDB). Call it at
// startup, before anything runs.
func (s *Service) SetTitlesSource(src metadata.TitlesSource) { s.titles = src }

// SetTitleLanguages installs the "Also match titles in" setting, read on every use as ISO
// 639-1 codes. Call it at startup.
func (s *Service) SetTitleLanguages(fn func() []string) { s.titleLangs = fn }

// localLangs is the languages whose translated titles match library-wide now.
func (s *Service) localLangs() []string {
	if s.titleLangs == nil {
		return nil
	}
	return s.titleLangs()
}

// releaseKeys are the keys FitRelease compares: the release title's, and when it ends in a
// country tag, the title's without it and the tag. A base shorter than minLocalKeyLen is
// dropped: "Up.US" must not stand for a show some country calls "Up".
func releaseKeys(p parser.Release) (key, baseKey, cc string) {
	key = parser.TitleKey(p.Title)
	var base string
	base, cc = parser.SplitCountry(p.Title)
	if cc != "" {
		if baseKey = parser.TitleKey(base); keyLen(baseKey) < minLocalKeyLen {
			baseKey = ""
		}
	}
	return key, baseKey, cc
}

// otherNames is names minus the ones too short to stand for a show and the ones in skip
// (keyed by parser.TitleKey).
func otherNames(names []string, skip map[string]bool) []string {
	var out []string
	for _, n := range names {
		if k := parser.TitleKey(n); keyLen(k) >= minLocalKeyLen && !skip[k] {
			out = append(out, n)
		}
	}
	return out
}

// ownKeys are the keys of the names FitRelease already judges a release by: the show's own
// titles and every alias. KnownAs leaves those to it.
func (s Series) ownKeys() map[string]bool {
	keys := map[string]bool{}
	for _, t := range s.ownTitles() {
		keys[parser.TitleKey(t)] = true
	}
	for _, a := range s.Aliases {
		keys[a.Key()] = true
	}
	return keys
}

// NamesAShow reports whether a parsed release's title is some library show's own title or
// alias (FitRelease's Title), whatever its year or country. Such a name is that show's
// business: a download by it is never confirmed by another show's other names (KnownAs).
func NamesAShow(all []Series, p parser.Release) bool {
	for _, sr := range all {
		if FitRelease(p, sr).Title {
			return true
		}
	}
	return false
}

// KnownAs reports whether a parsed download is sr under one of its names in any language
// — every alternative title from every country and every translation TMDB has — with the
// same year and country checks as its own title (fitTitles). err is set when the names
// couldn't be read (no TMDB key, an outage); ok is then false and the caller does what it
// did before this check existed.
//
// The show's own titles and aliases are left to FitRelease, and names shorter than three
// letters never count.
func (s *Service) KnownAs(ctx context.Context, sr Series, p parser.Release) (bool, error) {
	key, baseKey, cc := releaseKeys(p)
	if keyLen(key) < minLocalKeyLen {
		return false, nil
	}
	skip := sr.ownKeys()
	fits := func(names []string) bool {
		f, ok := sr.fitTitles(p, key, baseKey, cc, otherNames(names, skip))
		return ok && f.OK
	}
	if sr.Extra != nil && fits(titlesOf(sr.Extra.LocalTitles, nil)) {
		return true, nil // stored already: no TMDB call
	}
	if s.titles == nil || sr.TMDBID <= 0 {
		return false, ErrNoTitles
	}
	t, err := s.titleSource().Titles(ctx, metadata.TitlesSeries, sr.TMDBID)
	if err != nil {
		return false, err
	}
	return fits(t.All()), nil
}

// titlesOf is the titles of ts in langs (every language when langs is nil).
func titlesOf(ts []metadata.TranslatedTitle, langs []string) []string {
	var want map[string]bool
	if langs != nil {
		want = map[string]bool{}
		for _, l := range langs {
			want[l] = true
		}
	}
	var out []string
	for _, t := range ts {
		if want == nil || want[t.Lang] {
			out = append(out, t.Title)
		}
	}
	return out
}

// matchLocal is MatchRelease's last tier: the shows whose translated titles in langs name
// the release, with the year and country checks. One show matches; several are handed back
// as candidates (the import holds the download for a human), never guessed between.
func matchLocal(all []Series, p parser.Release, langs []string) (Series, bool, []Series) {
	key, baseKey, cc := releaseKeys(p)
	if keyLen(key) < minLocalKeyLen {
		return Series{}, false, nil
	}
	var cands []Series
	for _, sr := range all {
		if sr.Extra == nil || len(sr.Extra.LocalTitles) == 0 {
			continue
		}
		if f, ok := sr.fitTitles(p, key, baseKey, cc, otherNames(titlesOf(sr.Extra.LocalTitles, langs), nil)); ok && f.OK {
			cands = append(cands, sr)
		}
	}
	switch len(cands) {
	case 0:
		return Series{}, false, nil
	case 1:
		return cands[0], true, nil
	}
	return Series{}, false, cands
}

// refreshLocalTitles reads and stores a show's translated titles for the languages set
// now, on add and refresh. When they can't be read the stored ones stay, and the titles
// backfill tries again at the next start.
func (s *Service) refreshLocalTitles(ctx context.Context, id int64, tmdbID int, title string) {
	langs := s.localLangs()
	if len(langs) == 0 || s.titles == nil || tmdbID <= 0 {
		return
	}
	got, err := metadata.ReadLocalTitles(ctx, s.titleSource(), metadata.TitlesSeries, tmdbID, title, langs)
	if err != nil {
		s.log.Debug("series: couldn't read translated titles; the titles backfill tries again", "series", title, "err", err)
		return
	}
	if err := s.repo.SetLocalTitles(ctx, id, got, langs); err != nil {
		s.log.Warn("series: couldn't store translated titles", "series", title, "err", err)
	}
}

// needsLocalTitles reports whether sr's translated titles haven't been read for every
// language in langs.
func needsLocalTitles(sr Series, langs []string) bool {
	if len(langs) == 0 || sr.TMDBID <= 0 {
		return false
	}
	return sr.Extra == nil || !metadata.CoversLangs(sr.Extra.LocalLangs, langs)
}

// BackfillLocalTitles reads the translated titles of every show not yet read for all the
// "Also match titles in" languages, then the display names the "Show titles as" setting
// needs (display.go), one show per every so a large library never bursts TMDB, and returns
// how many it filled. It runs at start and whenever the languages or the display setting
// change; one pass runs at a time, and a call while one runs asks it to go round once more
// and returns at once with 0. It stops when TMDB isn't configured or ctx ends; a show TMDB
// can't answer right now is left for the next run.
func (s *Service) BackfillLocalTitles(ctx context.Context, every time.Duration) (int, error) {
	total := 0
	s.titlesAgain.Store(true)
	for s.titlesAgain.Load() {
		if !s.titlesPass.TryLock() {
			return total, nil // the pass running now sees titlesAgain and goes round again
		}
		var err error
		for err == nil && s.titlesAgain.Swap(false) {
			var n int
			n, err = s.backfillLocalOnce(ctx, every)
			total += n
			if err == nil {
				n, err = s.backfillDisplayOnce(ctx, every)
				total += n
			}
		}
		s.titlesPass.Unlock()
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// backfillLocalOnce is one pass of BackfillLocalTitles over the library.
func (s *Service) backfillLocalOnce(ctx context.Context, every time.Duration) (int, error) {
	langs := s.localLangs()
	if len(langs) == 0 || s.titles == nil {
		return 0, nil
	}
	all, err := s.repo.List(ctx)
	if err != nil {
		return 0, err
	}
	filled := 0
	for _, sr := range all {
		if !needsLocalTitles(sr, langs) {
			continue
		}
		t := time.NewTimer(every)
		select {
		case <-ctx.Done():
			t.Stop()
			return filled, ctx.Err()
		case <-t.C:
		}
		got, err := metadata.ReadLocalTitles(ctx, s.titleSource(), metadata.TitlesSeries, sr.TMDBID, sr.Title, langs)
		switch {
		case errors.Is(err, metadata.ErrNotConfigured):
			return filled, nil
		case errors.Is(err, metadata.ErrNotFound):
			got = []metadata.TranslatedTitle{} // TMDB no longer has it: read, nothing to match
		case err != nil:
			if ctx.Err() != nil {
				return filled, ctx.Err()
			}
			s.log.Debug("series: couldn't read translated titles; trying again next run", "series", sr.Title, "err", err)
			continue
		}
		if err := s.repo.SetLocalTitles(ctx, sr.ID, got, langs); err != nil {
			return filled, err
		}
		filled++
	}
	return filled, nil
}

// SetLocalTitles stores a show's translated titles and the languages they were read for,
// leaving the rest of its extra metadata as it is (json_set, so it can't half-overwrite a
// refresh writing the whole blob).
func (r *Repo) SetLocalTitles(ctx context.Context, id int64, titles []metadata.TranslatedTitle, langs []string) error {
	if titles == nil {
		titles = []metadata.TranslatedTitle{}
	}
	if langs == nil {
		langs = []string{}
	}
	if store.DialectOf(r.db) == store.Postgres {
		return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
			return store.SetJSONFields(ctx, tx, "series", "extra_json", id, map[string]any{"local_titles": titles, "local_langs": langs})
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
	_, err = r.db.ExecContext(ctx, `UPDATE series SET extra_json = json_set(
			CASE WHEN json_valid(extra_json) AND extra_json <> '' THEN extra_json ELSE '{}' END,
			'$.local_titles', json(?), '$.local_langs', json(?))
		WHERE id = ?`, string(tb), string(lb), id)
	return err
}

// localizedScanMatch identifies a scanned show folder named in one of the owner's extra
// title languages (metadata.LocalizedMatch: exactly one show so named, or none).
func (s *Service) localizedScanMatch(ctx context.Context, title string, year int) (int, bool) {
	langs := s.localLangs()
	if len(langs) == 0 || s.titles == nil {
		return 0, false
	}
	return metadata.LocalizedMatch(ctx, s.titles, metadata.TitlesSeries, title, year, langs)
}
