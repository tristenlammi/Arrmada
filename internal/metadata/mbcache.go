package metadata

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// MusicBrainz answers are kept for good in music_mb_cache. Every answer that is stored is
// served again until it is checked, and checking it never removes it: an answer that can't
// be fetched again (MusicBrainz down, refusing us, or saying 404 about something it used to
// know) leaves the old one in place. Lidarr's library emptied itself when its metadata
// server answered with nothing; this one keeps what it had and says it's stale.
//
// Rows are keyed by what was asked (the kind of entity, its MBID and the include set, or a
// normalised search), never by the host, so switching to or from a self-hosted mirror keeps
// the cache. Bodies are gzipped JSON exactly as MusicBrainz sent them.

// ErrMusicBrainzUnavailable means MusicBrainz couldn't be asked (down, refusing, timing
// out) and nothing was cached for the question. Callers treat it as "unknown", never as
// "empty".
var ErrMusicBrainzUnavailable = errors.New("MusicBrainz is unavailable")

func errMusicBrainzUnavailable(why string) error {
	return fmt.Errorf("%w: %s", ErrMusicBrainzUnavailable, why)
}

// How long a cached answer counts as fresh, per kind. A stale answer is still served at
// once; it is refetched in the background (and by the refresh-music-cache task).
const (
	MBFreshArtist       = 7 * 24 * time.Hour  // an artist's name, type, genres
	MBFreshReleaseGroup = 3 * 24 * time.Hour  // an artist's albums: where new releases show up
	MBFreshRelease      = 30 * 24 * time.Hour // a release's track listing, a release group's releases
	MBFreshSearch       = 24 * time.Hour      // an artist search
)

// mbKind is one kind of cached question, with its freshness and, for answers that are
// mostly a list, the list's field (an empty list never replaces a full one; see keep).
type mbKind struct {
	name  string
	fresh time.Duration
	list  string
}

var (
	mbKindArtist       = mbKind{"artist", MBFreshArtist, ""}
	mbKindReleaseGroup = mbKind{"release-group-browse", MBFreshReleaseGroup, "release-groups"}
	mbKindReleases     = mbKind{"release-browse", MBFreshRelease, "releases"}
	mbKindRelease      = mbKind{"release", MBFreshRelease, "media"}
	mbKindSearch       = mbKind{"artist-search", MBFreshSearch, ""}
	// mbKindReleaseGroupLookup is one release group looked up by MBID: asked only when an
	// album the library holds stops appearing in its artist's list (merged or removed).
	mbKindReleaseGroupLookup = mbKind{"release-group", MBFreshArtist, ""}
)

var mbKinds = map[string]mbKind{}

func init() {
	for _, k := range []mbKind{mbKindArtist, mbKindReleaseGroup, mbKindReleases, mbKindRelease, mbKindSearch, mbKindReleaseGroupLookup} {
		mbKinds[k.name] = k
	}
}

// mbEntry is one cached answer.
type mbEntry struct {
	body      []byte // the JSON, unzipped
	status    int    // 200, or 404 when MusicBrainz said there's no such thing
	fetchedAt time.Time
	checkedAt time.Time // the last time MusicBrainz was asked and answered
}

// fresh reports whether a cached answer can be used without asking again. An empty list
// (an album announced with no release listed yet, an artist with nothing out) is only
// trusted for a day, so a listing that appears is noticed long before the kind's own
// freshness runs out.
func (e mbEntry) fresh(k mbKind, now time.Time) bool {
	window := k.fresh
	if k.list != "" && e.status == 200 && mbListLen(e.body, k.list) == 0 && window > MBFreshSearch {
		window = MBFreshSearch
	}
	return now.Sub(e.checkedAt) < window
}

// mbCache is the store behind music_mb_cache. A nil db caches nothing (tests).
type mbCache struct{ db store.Pool }

func (c mbCache) get(ctx context.Context, key string) (mbEntry, bool) {
	if c.db == nil {
		return mbEntry{}, false
	}
	var gz []byte
	var e mbEntry
	var fetched, checked int64
	err := c.db.QueryRowContext(ctx,
		`SELECT body, status, fetched_at, checked_at FROM music_mb_cache WHERE key = ?`, key).
		Scan(&gz, &e.status, &fetched, &checked)
	if err != nil {
		return mbEntry{}, false
	}
	body, err := gunzip(gz)
	if err != nil {
		return mbEntry{}, false
	}
	e.body, e.fetchedAt, e.checkedAt = body, time.UnixMilli(fetched), time.UnixMilli(checked)
	return e, true
}

// put stores a fresh answer under key.
func (c mbCache) put(ctx context.Context, key string, status int, body []byte, now time.Time) error {
	if c.db == nil {
		return nil
	}
	gz, err := gzipBytes(body)
	if err != nil {
		return err
	}
	ms := now.UnixMilli()
	_, err = c.db.ExecContext(ctx,
		`INSERT INTO music_mb_cache (key, body, status, fetched_at, checked_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET body = excluded.body, status = excluded.status,
		   fetched_at = excluded.fetched_at, checked_at = excluded.checked_at`,
		key, gz, status, ms, ms)
	return err
}

// touch says MusicBrainz was asked about key and the answer kept is still the one to use
// (it answered the same, or answered with less than we have).
func (c mbCache) touch(ctx context.Context, key string, now time.Time) {
	if c.db == nil {
		return
	}
	_, _ = c.db.ExecContext(ctx, `UPDATE music_mb_cache SET checked_at = ? WHERE key = ?`, now.UnixMilli(), key)
}

// mbRefreshKinds are the kinds the refresh task rechecks. Searches aren't among them: a
// search is only worth answering again when someone searches again.
var mbRefreshKinds = []mbKind{mbKindReleaseGroup, mbKindArtist, mbKindReleases, mbKindRelease, mbKindReleaseGroupLookup}

// keyRange is the primary-key range holding a kind's rows ("kind:" up to "kind;"), so a
// per-kind read goes through the index.
func (k mbKind) keyRange() (from, to string) { return k.name + ":", k.name + ";" }

// stalest lists up to limit keys whose answers are past their freshness, longest unchecked
// first within each kind, for the refresh task. Each kind is read on its own and the
// kinds are taken in turn, so a kind with a long freshness and many rows (thousands of
// release listings) can't crowd out a short-lived one (an artist's album list, where new
// releases show up).
func (c mbCache) stalest(ctx context.Context, now time.Time, limit int) ([]string, error) {
	if c.db == nil || limit <= 0 {
		return nil, nil
	}
	perKind := make([][]string, len(mbRefreshKinds))
	for i, k := range mbRefreshKinds {
		from, to := k.keyRange()
		rows, err := c.db.QueryContext(ctx,
			`SELECT key FROM music_mb_cache WHERE key >= ? AND key < ? AND checked_at < ? ORDER BY checked_at LIMIT ?`,
			from, to, now.Add(-k.fresh).UnixMilli(), limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return nil, err
			}
			perKind[i] = append(perKind[i], key)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	var out []string
	for round := 0; len(out) < limit; round++ {
		took := false
		for _, keys := range perKind {
			if round < len(keys) && len(out) < limit {
				out = append(out, keys[round])
				took = true
			}
		}
		if !took {
			break
		}
	}
	return out, nil
}

// mbSearchKeep is how long a cached artist search is kept. Searches aren't library data:
// they're asked once, by someone typing, and a week-old answer is only clutter.
const mbSearchKeep = 7 * 24 * time.Hour

// pruneSearches drops cached searches nobody has asked again for a week. Nothing else in
// the cache is ever removed.
func (c mbCache) pruneSearches(ctx context.Context, now time.Time) {
	if c.db == nil {
		return
	}
	from, to := mbKindSearch.keyRange()
	_, _ = c.db.ExecContext(ctx, `DELETE FROM music_mb_cache WHERE key >= ? AND key < ? AND checked_at < ?`,
		from, to, now.Add(-mbSearchKeep).UnixMilli())
}

// MBCacheStats is what the Settings card shows about the cache.
type MBCacheStats struct {
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
	Stale   int   `json:"stale"`
}

func (c mbCache) stats(ctx context.Context, now time.Time) (MBCacheStats, error) {
	var s MBCacheStats
	if c.db == nil {
		return s, nil
	}
	err := c.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(length(body)), 0) FROM music_mb_cache`).Scan(&s.Entries, &s.Bytes)
	if err != nil {
		return s, err
	}
	rows, err := c.db.QueryContext(ctx, `SELECT key, checked_at FROM music_mb_cache`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var checked int64
		if rows.Scan(&key, &checked) != nil {
			continue
		}
		if k, ok := mbKindOfKey(key); ok && now.Sub(time.UnixMilli(checked)) >= k.fresh {
			s.Stale++
		}
	}
	return s, rows.Err()
}

func mbKindOfKey(key string) (mbKind, bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			k, ok := mbKinds[key[:i]]
			return k, ok
		}
	}
	return mbKind{}, false
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzip(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
