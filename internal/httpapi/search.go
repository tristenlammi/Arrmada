package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/displaytitle"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The command palette's library lookup (GET /api/v1/library/search?q=, staff only): the titles in
// Movies, Series, Books and Music that match what's typed, with just enough to draw a row
// (kind, id, title, year, a poster) and nothing else. The palette asks on every pause in
// typing, so the answer comes from a small in-memory index of every title rather than from
// the library list endpoints, which carry download state, media facts and profiles.

const (
	// searchLimit caps one answer: the palette shows a screenful, not the library.
	searchLimit = 20
	// searchIndexTTL is how long the title index is reused. A title added a moment ago
	// shows up within this long; a burst of keystrokes costs one read of the tables.
	searchIndexTTL = 15 * time.Second
	// searchMaxQuery bounds the work a long paste can cause.
	searchMaxQuery = 100
)

// searchHit is one row of the answer. Subtitle says whose it is where the title alone is
// ambiguous: a book's author, an album's artist.
type searchHit struct {
	Kind      string `json:"kind"` // movie | series | book | artist | album
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Year      int    `json:"year,omitempty"`
	Subtitle  string `json:"subtitle,omitempty"`
	PosterURL string `json:"poster_url,omitempty"`
}

// searchEntry is a title in the index with its matching forms worked out once. names is
// every name the title answers to, its own title first; a second name (a display title in
// another language, say) is appended here and matches the same way.
type searchEntry struct {
	hit   searchHit
	rank  int // kind order for ties: movies, series, books, artists, albums
	names []searchName
}

type searchName struct {
	key   string   // parser.TitleKey
	words []string // parser.TitleWords
}

func newSearchEntry(hit searchHit, rank int, names ...string) searchEntry {
	e := searchEntry{hit: hit, rank: rank}
	for _, n := range names {
		if k := parser.TitleKey(n); k != "" {
			e.names = append(e.names, searchName{key: k, words: parser.TitleWords(n)})
		}
	}
	return e
}

// searchRow is the one method buildSearchEntries needs from *sql.Rows.
type searchRow interface{ Scan(...any) error }

// searchIndex is the cached index; books and music record whether those modules were on
// when it was built, so switching one doesn't wait out the TTL.
type searchIndex struct {
	mu      sync.Mutex
	built   time.Time
	books   bool
	music   bool
	entries []searchEntry
}

func (a *api) handleLibrarySearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if rs := []rune(q); len(rs) > searchMaxQuery {
		q = string(rs[:searchMaxQuery])
	}
	limit := searchLimit
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v < limit {
		limit = v
	}
	out := []searchHit{}
	if q == "" || a.deps.Store == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"results": out})
		return
	}
	entries, err := a.searchEntries(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not search the library")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"results": matchSearch(entries, q, limit)})
}

// searchEntries returns the title index, rebuilding it when it's older than the TTL or a
// module was switched since.
func (a *api) searchEntries(ctx context.Context) ([]searchEntry, error) {
	booksOn, musicOn := true, false
	if a.deps.Settings != nil {
		booksOn, musicOn = a.booksEnabled(ctx), a.musicEnabled(ctx)
	}
	idx := &a.search
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if !idx.built.IsZero() && time.Since(idx.built) < searchIndexTTL && idx.books == booksOn && idx.music == musicOn {
		return idx.entries, nil
	}
	entries, err := a.buildSearchEntries(ctx, booksOn, musicOn)
	if err != nil {
		return nil, err
	}
	idx.entries, idx.built, idx.books, idx.music = entries, time.Now(), booksOn, musicOn
	return entries, nil
}

// buildSearchEntries reads every title's name, year and picture: a few narrow columns per
// table, no joins but the album's artist. A module that's switched off isn't read, so its
// titles never appear (their pages would answer "switched off" anyway).
func (a *api) buildSearchEntries(ctx context.Context, booksOn, musicOn bool) ([]searchEntry, error) {
	db := a.deps.Store.DB()
	var out []searchEntry
	read := func(query string, scan func(rows searchRow) (searchEntry, error)) error {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scan(rows)
			if err != nil {
				return err
			}
			if len(e.names) > 0 {
				out = append(out, e)
			}
		}
		return rows.Err()
	}
	// A film or show is listed by the title the app shows (Settings → Library → Display
	// titles, or its own pick) and found by that or its own (TMDB English) title.
	titled := func(kind string, rank int) func(rows searchRow) (searchEntry, error) {
		return func(rows searchRow) (searchEntry, error) {
			h := searchHit{Kind: kind}
			var canonical string
			if err := rows.Scan(&h.ID, &canonical, &h.Title, &h.Year, &h.PosterURL); err != nil {
				return searchEntry{}, err
			}
			if canonical == h.Title {
				return newSearchEntry(h, rank, h.Title), nil
			}
			return newSearchEntry(h, rank, h.Title, canonical), nil
		}
	}
	if err := read(`SELECT id, title, `+displaytitle.SQL("m")+`, year, poster_url FROM movies m`, titled("movie", 0)); err != nil {
		return nil, err
	}
	if err := read(`SELECT id, title, `+displaytitle.SQL("s")+`, year, poster_url FROM series s`, titled("series", 1)); err != nil {
		return nil, err
	}
	if booksOn {
		// A book is listed by the title it shows (an edit by hand, when it has one) and
		// found by that or the catalogue's title kept underneath the edit.
		err := read(`SELECT id, title, year, author, cover_url,
		                    COALESCE(`+store.JSONScalar(db, "edits_json", "title")+`, '')
		               FROM books`, func(rows searchRow) (searchEntry, error) {
			h := searchHit{Kind: "book"}
			var matched string
			if err := rows.Scan(&h.ID, &h.Title, &h.Year, &h.Subtitle, &h.PosterURL, &matched); err != nil {
				return searchEntry{}, err
			}
			if matched == "" || matched == h.Title {
				return newSearchEntry(h, 2, h.Title), nil
			}
			return newSearchEntry(h, 2, h.Title, matched), nil
		})
		if err != nil {
			return nil, err
		}
	}
	if musicOn {
		err := read(`SELECT id, name, image_url FROM artists WHERE in_library = 1`, func(rows searchRow) (searchEntry, error) {
			h := searchHit{Kind: "artist"}
			if err := rows.Scan(&h.ID, &h.Title, &h.PosterURL); err != nil {
				return searchEntry{}, err
			}
			return newSearchEntry(h, 3, h.Title), nil
		})
		if err != nil {
			return nil, err
		}
		err = read(`SELECT al.id, al.title, al.year, ar.name, al.cover_url FROM albums al JOIN artists ar ON ar.id = al.artist_id`, func(rows searchRow) (searchEntry, error) {
			h := searchHit{Kind: "album"}
			if err := rows.Scan(&h.ID, &h.Title, &h.Year, &h.Subtitle, &h.PosterURL); err != nil {
				return searchEntry{}, err
			}
			return newSearchEntry(h, 4, h.Title), nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// matchSearch ranks the entries against q and returns the best limit of them. Matching is
// on the one title normaliser (parser.TitleKey / TitleWords), so case, accents,
// punctuation and "&" against "and" never matter: "pokemon" finds "Pokémon", "fast and
// furious" finds "Fast & Furious".
func matchSearch(entries []searchEntry, q string, limit int) []searchHit {
	qKey := parser.TitleKey(q)
	qWords := parser.TitleWords(q)
	out := []searchHit{}
	if qKey == "" {
		return out
	}
	type scored struct {
		e     *searchEntry
		score int
	}
	var hits []scored
	for i := range entries {
		best := 0
		for _, n := range entries[i].names {
			if s := scoreName(qKey, qWords, n); s > best {
				best = s
			}
		}
		if best > 0 {
			hits = append(hits, scored{&entries[i], best})
		}
	}
	// Best match first; then movies before shows before books and music, a shorter title
	// (closer to what was typed) first, then alphabetical and by id so the order is stable.
	sort.Slice(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.e.rank != b.e.rank {
			return a.e.rank < b.e.rank
		}
		if la, lb := len(a.e.hit.Title), len(b.e.hit.Title); la != lb {
			return la < lb
		}
		if a.e.hit.Title != b.e.hit.Title {
			return a.e.hit.Title < b.e.hit.Title
		}
		return a.e.hit.ID < b.e.hit.ID
	})
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].e.hit)
	}
	return out
}

// scoreName is how well one name matches: the whole title (100), the start of it (80),
// every typed word starting a word of the title in any order (60), or the typed letters
// anywhere in it (40). Zero is no match.
func scoreName(qKey string, qWords []string, n searchName) int {
	switch {
	case n.key == qKey:
		return 100
	case strings.HasPrefix(n.key, qKey):
		return 80
	case wordsPrefix(qWords, n.words):
		return 60
	case strings.Contains(n.key, qKey):
		return 40
	}
	return 0
}

// wordsPrefix reports whether each query word starts a different word of the title.
func wordsPrefix(query, title []string) bool {
	if len(query) == 0 || len(query) > len(title) {
		return false
	}
	used := make([]bool, len(title))
	for _, q := range query {
		found := false
		for i, t := range title {
			if !used[i] && strings.HasPrefix(t, q) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
