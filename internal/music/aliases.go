package music

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Artist aliases (MUS-38): the other names MusicBrainz knows an artist by. A release named
// "Utada Hikaru - First Love" is 宇多田ヒカル's, one named by a band's old name is the band's.
//
// The rule that keeps an alias from letting another artist's release in: an alias only
// counts as the release's WHOLE artist part (what comes before " - ", or before the first
// "-" of a dotted or underscored scene name), never as words found somewhere in the name.
// That is foldMusic's whole-segment rule for short spellings (artistNamed), applied to every
// alias: "Utada Hikaru - First Love", never "Utada Hikaru Tribute Band - First Love". On top
// of that, only real names count for matching: never a search hint (mostly misspellings,
// listed for MusicBrainz's own search), never one shorter than three letters or digits
// ("Ye"), and never one that folds to another artist's name or alias (NameIndex), so an
// alias can't make one release look like two artists'.

// ArtistNames is an artist's name with the aliases it may be released under, best first.
type ArtistNames struct {
	Name    string
	Aliases []string
	// Search are the aliases worth a second search query when the name finds nothing:
	// the matchable ones (no search hints, which are mostly misspellings, nothing too
	// short), the Latin ones first when the name isn't written in Latin letters.
	Search []string
}

// NamesOf is a name with no aliases.
func NamesOf(name string) ArtistNames { return ArtistNames{Name: name} }

// ReleaseIsForAlbum is ReleaseIsForAlbum for any of the artist's names: the name as before,
// or an alias as the release's whole artist part. The album title must be in the release
// either way, and a discography never is one album.
func (n ArtistNames) ReleaseIsForAlbum(releaseName, album string) bool {
	if ReleaseIsForAlbum(releaseName, n.Name, album) {
		return true
	}
	if len(n.Aliases) == 0 || ParseRelease(releaseName).Discography || !containsWords(releaseName, album) {
		return false
	}
	for _, a := range n.Aliases {
		if aliasIsReleaseArtist(releaseName, a) {
			return true
		}
	}
	return false
}

// Is reports whether a name (a folder's, a tag's) is this artist's name or one of its
// aliases, folded: the scan's artist check.
func (n ArtistNames) Is(name string) bool {
	k := NormKey(name)
	if k == "" {
		return false
	}
	if NormKey(n.Name) == k {
		return true
	}
	for _, a := range n.Aliases {
		if NormKey(a) == k {
			return true
		}
	}
	return false
}

// aliasIsReleaseArtist reports whether an alias is the whole artist part of a release
// name, folded (both readings of a slash; the shorter spelling without a leading "the" too,
// as artistNamed allows for a name).
func aliasIsReleaseArtist(hay, alias string) bool {
	for _, seg := range artistSegments(hay) {
		for _, join := range []bool{true, false} {
			part := foldMusicWith(seg, join, false)
			if part == "" {
				continue
			}
			for _, short := range shortArtistForms(foldMusicWith(alias, join, false)) {
				if longEnough(short) && part == short {
					return true
				}
			}
		}
	}
	return false
}

// artistSegments is a release name's artist part, read the two ways releases write it:
// before " - ", and, for a scene name with no spaces at all, before the first "-" with
// dots and underscores read as spaces ("Utada_Hikaru-First_Love-2CD-FLAC").
func artistSegments(hay string) []string {
	var out []string
	if seg, _, ok := strings.Cut(hay, " - "); ok {
		out = append(out, seg)
	}
	if t := strings.TrimSpace(hay); !strings.ContainsRune(t, ' ') {
		if seg, _, ok := strings.Cut(t, "-"); ok {
			out = append(out, strings.NewReplacer("_", " ", ".", " ").Replace(seg))
		}
	}
	return out
}

// minAliasLen is the fewest letters or digits an alias needs to count for matching: a
// one- or two-letter name is too easily something else.
const minAliasLen = 3

// longEnough reports whether a folded name has at least minAliasLen letters or digits.
func longEnough(folded string) bool {
	n := 0
	for _, r := range folded {
		if isWordRune(r) {
			n++
		}
	}
	return n >= minAliasLen
}

// matchable reports whether a stored alias may be used to match releases and folders: not
// a search hint, and long enough.
func matchable(a aliasRow) bool {
	return !strings.EqualFold(a.Type, "search hint") && longEnough(NormKey(a.Name))
}

// isLatin reports whether every letter of s is a Latin one (a name with no letters is not).
func isLatin(s string) bool {
	letters := 0
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		if !unicode.Is(unicode.Latin, r) {
			return false
		}
		letters++
	}
	return letters > 0
}

// aliasRow is one stored alias.
type aliasRow struct {
	ArtistID int64
	Name     string
	Locale   string
	Type     string
	Primary  bool
}

// aliasRank orders an artist's aliases best first: MusicBrainz's primary names, then (for
// a name not in Latin letters) the Latin ones, then artist and legal names before the rest,
// search hints last.
func aliasRank(name string, a aliasRow) int {
	r := 0
	if !a.Primary {
		r += 8
	}
	if !isLatin(name) && !isLatin(a.Name) {
		r += 4
	}
	switch strings.ToLower(a.Type) {
	case "artist name", "legal name":
	case "search hint":
		r += 2
	default:
		r++
	}
	return r
}

// NameIndex is every artist's name and usable aliases, built in two queries: what the
// scan, the search and the matcher ask "who is this?" and "what else is X called?".
type NameIndex struct {
	names map[int64]ArtistNames
	// owners lists, by folded name, the artists whose name or usable alias it is.
	owners map[string][]int64
}

// Names is an artist's names (just the name when it has no aliases, or isn't indexed).
func (ix *NameIndex) Names(artistID int64, fallback string) ArtistNames {
	if ix != nil {
		if n, ok := ix.names[artistID]; ok {
			return n
		}
	}
	return NamesOf(fallback)
}

// ArtistsNamed lists the artists whose name or usable alias folds to name.
func (ix *NameIndex) ArtistsNamed(name string) []int64 {
	if ix == nil {
		return nil
	}
	return ix.owners[NormKey(name)]
}

// buildNameIndex builds the index from every artist's name and every stored alias. An alias
// that folds to another artist's name, or that two artists share, is dropped for everyone
// it would make ambiguous; one that folds to the artist's own name adds nothing and is
// dropped too.
func buildNameIndex(artists map[int64]string, aliases []aliasRow) *NameIndex {
	claim := map[string]map[int64]bool{}
	add := func(k string, id int64) {
		if k == "" {
			return
		}
		if claim[k] == nil {
			claim[k] = map[int64]bool{}
		}
		claim[k][id] = true
	}
	for id, name := range artists {
		add(NormKey(name), id)
	}
	for _, a := range aliases {
		if _, ok := artists[a.ArtistID]; ok && matchable(a) {
			add(NormKey(a.Name), a.ArtistID)
		}
	}
	byArtist := map[int64][]aliasRow{}
	for _, a := range aliases {
		k := NormKey(a.Name)
		name, ok := artists[a.ArtistID]
		if !ok || k == "" || k == NormKey(name) || len(claim[k]) > 1 || !matchable(a) {
			continue
		}
		byArtist[a.ArtistID] = append(byArtist[a.ArtistID], a)
	}
	ix := &NameIndex{names: make(map[int64]ArtistNames, len(artists)), owners: map[string][]int64{}}
	for id, name := range artists {
		n := ArtistNames{Name: name}
		rows := byArtist[id]
		sort.SliceStable(rows, func(i, j int) bool { return aliasRank(name, rows[i]) < aliasRank(name, rows[j]) })
		seen := map[string]bool{NormKey(name): true}
		for _, a := range rows {
			k := NormKey(a.Name)
			if seen[k] {
				continue
			}
			seen[k] = true
			n.Aliases = append(n.Aliases, a.Name)
			if isLatin(a.Name) || !isLatin(name) {
				n.Search = append(n.Search, a.Name)
			}
		}
		ix.names[id] = n
		for k := range seen {
			if k != "" {
				ix.owners[k] = append(ix.owners[k], id)
			}
		}
	}
	// A name two artists share stays ambiguous: both are listed, and the caller decides.
	for k := range ix.owners {
		sort.Slice(ix.owners[k], func(i, j int) bool { return ix.owners[k][i] < ix.owners[k][j] })
	}
	return ix
}

// nameRows reads every artist's name and every alias.
func (r *Repo) nameRows(ctx context.Context) (map[int64]string, []aliasRow, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name FROM artists`)
	if err != nil {
		return nil, nil, err
	}
	artists := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, nil, err
		}
		artists[id] = name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = r.db.QueryContext(ctx, `SELECT artist_id, name, locale, alias_type, is_primary FROM music_artist_aliases ORDER BY artist_id, name`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []aliasRow
	for rows.Next() {
		var a aliasRow
		var primary int
		if err := rows.Scan(&a.ArtistID, &a.Name, &a.Locale, &a.Type, &primary); err != nil {
			return nil, nil, err
		}
		a.Primary = primary != 0
		out = append(out, a)
	}
	return artists, out, rows.Err()
}

// SetArtistAliases replaces an artist's stored aliases with MusicBrainz's list.
func (r *Repo) SetArtistAliases(ctx context.Context, artistID int64, aliases []metadata.ArtistAlias) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM music_artist_aliases WHERE artist_id = ?`, artistID); err != nil {
			return err
		}
		for _, a := range aliases {
			name := strings.TrimSpace(a.Name)
			if name == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO music_artist_aliases (artist_id, name, sort_name, locale, alias_type, is_primary, folded)
				VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(artist_id, name) DO NOTHING`,
				artistID, name, a.SortName, a.Locale, a.Type, b2i(a.Primary), NormKey(name)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ArtistAliases lists an artist's stored aliases (all of them, usable or not).
func (r *Repo) ArtistAliases(ctx context.Context, artistID int64) ([]metadata.ArtistAlias, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT name, sort_name, locale, alias_type, is_primary FROM music_artist_aliases
		 WHERE artist_id = ? ORDER BY is_primary DESC, name`, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metadata.ArtistAlias
	for rows.Next() {
		var a metadata.ArtistAlias
		var primary int
		if err := rows.Scan(&a.Name, &a.SortName, &a.Locale, &a.Type, &primary); err != nil {
			return nil, err
		}
		a.Primary = primary != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// nameIndexTTL is how long a built NameIndex is reused. Every write the service makes to
// names (an add, a refresh, a removal) drops it at once; the TTL only bounds how long a
// change made some other way goes unseen.
const nameIndexTTL = time.Minute

// nameCache holds the service's NameIndex.
type nameCache struct {
	mu sync.Mutex
	ix *NameIndex
	at time.Time
}

// NameIndex is every artist's names, built at most once a minute (see nameIndexTTL).
func (s *Service) NameIndex(ctx context.Context) (*NameIndex, error) {
	s.names.mu.Lock()
	defer s.names.mu.Unlock()
	now := s.clock()
	if s.names.ix != nil && now.Sub(s.names.at) < nameIndexTTL {
		return s.names.ix, nil
	}
	artists, aliases, err := s.repo.nameRows(ctx)
	if err != nil {
		return nil, err
	}
	s.names.ix, s.names.at = buildNameIndex(artists, aliases), now
	return s.names.ix, nil
}

// forgetNames drops the cached NameIndex after a change to anyone's names.
func (s *Service) forgetNames() {
	s.names.mu.Lock()
	s.names.ix = nil
	s.names.mu.Unlock()
}

// ArtistNames is one artist's names (just the name when the index can't be read: an alias
// only ever widens matching, so going without is the safe side).
func (s *Service) ArtistNames(ctx context.Context, a Artist) ArtistNames {
	ix, err := s.NameIndex(ctx)
	if err != nil {
		s.log.Warn("music: couldn't read artist aliases", "err", err)
		return NamesOf(a.Name)
	}
	return ix.Names(a.ID, a.Name)
}

// ArtistAliases lists an artist's stored aliases, for its page.
func (s *Service) ArtistAliases(ctx context.Context, artistID int64) ([]metadata.ArtistAlias, error) {
	return s.repo.ArtistAliases(ctx, artistID)
}

// storeAliases keeps the aliases MusicBrainz just gave for an artist. An answer with none
// changes nothing: it may be a cached answer from before aliases were asked for, or
// MusicBrainz having a bad moment, and an alias is never forgotten on an empty answer.
func (s *Service) storeAliases(ctx context.Context, artistID int64, aliases []metadata.ArtistAlias) {
	if len(aliases) == 0 {
		return
	}
	if err := s.repo.SetArtistAliases(ctx, artistID, aliases); err != nil {
		s.log.Warn("music: couldn't store the artist's aliases", "artist_id", artistID, "err", err)
		return
	}
	s.forgetNames()
}

// AliasMatching is the alias of a search result that a query names when its own name
// doesn't ("" otherwise): what the add dialog shows as "also known as".
func AliasMatching(r metadata.ArtistResult, query string) string {
	q := NormKey(query)
	if q == "" || strings.Contains(NormKey(r.Name), q) {
		return ""
	}
	for _, a := range r.Aliases {
		if strings.Contains(NormKey(a.Name), q) {
			return a.Name
		}
	}
	return ""
}
