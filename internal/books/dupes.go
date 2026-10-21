package books

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Possible duplicates: rows that may be one book, offered to a manager side by side to
// merge (merge.go) or to mark as different books. Nothing here merges anything.
//
// Pairs come from three places: the same title and author (SameBook); the same title
// with an author that overlaps but doesn't match ("Brandon Sanderson" / "Sanderson,
// Brandon; Dan Wells"); and pairs the Hardcover re-match flagged. A pair a manager said
// are different books stays hidden for good. The review never shows listening data.

// Reasons a pair is offered.
const (
	DupReasonSameBook  = "Same title and author"
	DupReasonSimilar   = "Same title, similar author"
	DupReasonHardcover = "Hardcover lists both as the same book"
)

// Pair review states (book_dupe_reviews.status).
const (
	dupOpen    = "open"
	dupIgnored = "ignored"
)

// ErrNotDuplicates is returned when asked to merge books the review doesn't list
// together.
var ErrNotDuplicates = errors.New("those books aren't listed as possible duplicates of each other")

// ErrPickTwo is returned when fewer than two different books are marked as not the same.
var ErrPickTwo = errors.New("pick at least two different books")

// DupBook is one book as the review shows it: enough to tell two rows apart, and
// nothing about who has listened to it.
type DupBook struct {
	ID            int64     `json:"id"`
	Title         string    `json:"title"`
	Author        string    `json:"author"`
	Year          int       `json:"year,omitempty"`
	CoverURL      string    `json:"cover_url,omitempty"`
	Key           string    `json:"key"`
	Source        string    `json:"source"`
	Ebook         *BookFile `json:"ebook,omitempty"`
	Audiobook     *BookFile `json:"audiobook,omitempty"`
	AudioVersions int       `json:"audio_versions"`
	Monitored     bool      `json:"monitored"`
	AddedAt       string    `json:"added_at,omitempty"`
}

// DupGroup is a set of rows that may all be one book.
type DupGroup struct {
	Books []DupBook `json:"books"`
	// SuggestedKeepID: the row with the most editions, then the most audio versions,
	// then the oldest.
	SuggestedKeepID int64 `json:"suggested_keep_id"`
	// Reasons say why the rows were put together, strongest first.
	Reasons []string `json:"reasons"`
}

// PastMerge is one earlier merge on a kept book's timeline. Automatic ones were done by
// the old boot-time fold; LostTitle and LostKey say which book went, so the owner can
// add it back from Discover if the fold was wrong.
type PastMerge struct {
	BookID      int64  `json:"book_id"`
	KeeperTitle string `json:"keeper_title"`
	Detail      string `json:"detail"`
	CreatedAt   string `json:"created_at"`
	Automatic   bool   `json:"automatic"`
	LostTitle   string `json:"lost_title,omitempty"`
	LostKey     string `json:"lost_key,omitempty"`
}

// PossibleDuplicates lists the groups of rows that may be one book.
func (s *Service) PossibleDuplicates(ctx context.Context) ([]DupGroup, error) {
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	pairs, err := s.dupPairs(ctx, list)
	if err != nil {
		return nil, err
	}
	return groupPairs(list, pairs), nil
}

// dupPair is two book ids, smaller first.
type dupPair [2]int64

func pairOf(a, b int64) dupPair {
	if a > b {
		a, b = b, a
	}
	return dupPair{a, b}
}

// dupPairs works out every open pair and why it was offered.
func (s *Service) dupPairs(ctx context.Context, list []Book) (map[dupPair]string, error) {
	pairs := map[dupPair]string{}
	add := func(a, b int64, reason string) {
		if a == b {
			return
		}
		p := pairOf(a, b)
		if cur, ok := pairs[p]; !ok || reasonRank(reason) < reasonRank(cur) {
			pairs[p] = reason
		}
	}

	// Rows indexed by title alone; SameTitle then decides, and the author decides the tier.
	ids := make(map[int64]Identity, len(list))
	byFull := map[string][]Book{}
	bySub := map[string][]Book{}
	for _, b := range list {
		id := IdentityOf(b.Title, b.Author)
		if id.Full == "" {
			continue
		}
		ids[b.ID] = id
		byFull[id.Full] = append(byFull[id.Full], b)
		if id.Sub != "" {
			bySub[id.Sub] = append(bySub[id.Sub], b)
		}
	}
	for _, b := range list {
		q, ok := ids[b.ID]
		if !ok {
			continue
		}
		cands := append([]Book{}, byFull[q.Full]...)
		if q.Sub != "" {
			cands = append(cands, byFull[q.Sub]...)
		}
		cands = append(cands, bySub[q.Full]...)
		for _, c := range cands {
			ci := ids[c.ID]
			if c.ID == b.ID || !q.SameTitle(ci) {
				continue
			}
			switch {
			case q.sameAuthor(ci):
				add(b.ID, c.ID, DupReasonSameBook)
			case !q.NoAuthor && !ci.NoAuthor && authorsOverlap(b.Author, c.Author):
				add(b.ID, c.ID, DupReasonSimilar)
			}
		}
	}

	rows, err := s.repo.db.QueryContext(ctx, `SELECT a_id, b_id, reason, status FROM book_dupe_reviews`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ignored := map[dupPair]bool{}
	for rows.Next() {
		var a, b int64
		var reason, status string
		if err := rows.Scan(&a, &b, &reason, &status); err != nil {
			return nil, err
		}
		if status == dupIgnored {
			ignored[pairOf(a, b)] = true
			continue
		}
		add(a, b, orStr(reason, DupReasonHardcover))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for p := range ignored {
		delete(pairs, p)
	}
	return pairs, nil
}

// reasonRank orders reasons strongest first.
func reasonRank(r string) int {
	switch r {
	case DupReasonSameBook:
		return 0
	case DupReasonSimilar:
		return 2
	}
	return 1 // a catalogue saying so
}

// groupPairs joins pairs that share a book into groups (a, b and b, c are one group).
func groupPairs(list []Book, pairs map[dupPair]string) []DupGroup {
	parent := map[int64]int64{}
	var find func(int64) int64
	find = func(x int64) int64 {
		if p, ok := parent[x]; ok && p != x {
			r := find(p)
			parent[x] = r
			return r
		}
		parent[x] = x
		return x
	}
	for p := range pairs {
		ra, rb := find(p[0]), find(p[1])
		if ra != rb {
			parent[max(ra, rb)] = min(ra, rb)
		}
	}
	byID := make(map[int64]Book, len(list))
	for _, b := range list {
		byID[b.ID] = b
	}
	members := map[int64][]Book{}
	for id := range parent {
		b, ok := byID[id]
		if !ok {
			continue // a flagged pair whose book is gone
		}
		root := find(id)
		members[root] = append(members[root], b)
	}
	reasons := map[int64]map[string]bool{}
	for p, r := range pairs {
		root := find(p[0])
		if reasons[root] == nil {
			reasons[root] = map[string]bool{}
		}
		reasons[root][r] = true
	}
	var out []DupGroup
	for root, books := range members {
		if len(books) < 2 {
			continue
		}
		sort.Slice(books, func(i, j int) bool { return books[i].ID < books[j].ID })
		g := DupGroup{SuggestedKeepID: suggestKeep(books)}
		for _, b := range books {
			g.Books = append(g.Books, dupBook(b))
		}
		for r := range reasons[root] {
			g.Reasons = append(g.Reasons, r)
		}
		sort.Slice(g.Reasons, func(i, j int) bool { return reasonRank(g.Reasons[i]) < reasonRank(g.Reasons[j]) })
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Books[0].ID < out[j].Books[0].ID })
	return out
}

// suggestKeep picks the row to keep: most editions, then most audio versions, then the
// oldest (the lowest id).
func suggestKeep(books []Book) int64 {
	best := books[0]
	score := func(b Book) (int, int) {
		e := 0
		if b.Ebook != nil {
			e++
		}
		if b.Audiobook != nil {
			e++
		}
		return e, len(b.AudioVersions)
	}
	for _, b := range books[1:] {
		be, bv := score(best)
		ce, cv := score(b)
		if ce > be || ce == be && (cv > bv || cv == bv && b.ID < best.ID) {
			best = b
		}
	}
	return best.ID
}

func dupBook(b Book) DupBook {
	return DupBook{ID: b.ID, Title: b.Title, Author: b.Author, Year: b.Year, CoverURL: b.CoverURL, Key: b.OLKey,
		Source: KeySource(b.OLKey), Ebook: b.Ebook, Audiobook: b.Audiobook, AudioVersions: len(b.AudioVersions),
		Monitored: b.Monitored, AddedAt: b.AddedAt}
}

// DuplicateGroupOf returns the review's group holding keepID, or false when it's in none.
func (s *Service) DuplicateGroupOf(ctx context.Context, keepID int64) (DupGroup, bool, error) {
	groups, err := s.PossibleDuplicates(ctx)
	if err != nil {
		return DupGroup{}, false, err
	}
	for _, g := range groups {
		for _, b := range g.Books {
			if b.ID == keepID {
				return g, true, nil
			}
		}
	}
	return DupGroup{}, false, nil
}

// MergeDuplicates merges each of dropIDs into keepID — but only books the review lists
// in one group with it, so a stale page or a hand-made request can't fold two unrelated
// books. Merges run one by one; an error stops the rest, and the results say which
// went through.
func (s *Service) MergeDuplicates(ctx context.Context, keepID int64, dropIDs []int64, opts MergeOptions) ([]MergeResult, error) {
	if len(dropIDs) == 0 {
		return nil, ErrNotDuplicates
	}
	g, ok, err := s.DuplicateGroupOf(ctx, keepID)
	if err != nil {
		return nil, err
	}
	in := map[int64]bool{}
	if ok {
		for _, b := range g.Books {
			in[b.ID] = true
		}
	}
	seen := map[int64]bool{}
	for _, id := range dropIDs {
		if id == keepID || !in[id] || seen[id] {
			return nil, ErrNotDuplicates
		}
		seen[id] = true
	}
	var out []MergeResult
	for _, id := range dropIDs {
		res, err := s.Merge(ctx, keepID, id, opts)
		if err != nil {
			return out, err
		}
		out = append(out, res)
	}
	return out, nil
}

// IgnoreDuplicates marks every pair among ids as different books, for good.
func (s *Service) IgnoreDuplicates(ctx context.Context, ids []int64) error {
	uniq := map[int64]bool{}
	for _, id := range ids {
		uniq[id] = true
	}
	if len(uniq) < 2 {
		return ErrPickTwo
	}
	list := make([]int64, 0, len(uniq))
	for id := range uniq {
		list = append(list, id)
	}
	sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
	return store.WithTx(ctx, s.repo.db, func(tx *store.Transaction) error {
		for i := range list {
			for j := i + 1; j < len(list); j++ {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO book_dupe_reviews (a_id, b_id, status) VALUES (?, ?, ?)
					 ON CONFLICT(a_id, b_id) DO UPDATE SET status = excluded.status`,
					list[i], list[j], dupIgnored); err != nil {
					if strings.Contains(strings.ToLower(err.Error()), "foreign key") {
						return ErrNotFound
					}
					return err
				}
			}
		}
		return nil
	})
}

// flagDuplicate records a pair for the review (the Hardcover re-match placing both on
// one book). A pair a manager already said are different books stays that way.
func (s *Service) flagDuplicate(ctx context.Context, a, b int64, reason string) {
	if a == b {
		return
	}
	p := pairOf(a, b)
	if _, err := s.repo.db.ExecContext(ctx,
		`INSERT INTO book_dupe_reviews (a_id, b_id, reason, status) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		p[0], p[1], reason, dupOpen); err != nil {
		s.log.Warn("books: couldn't record a possible duplicate", "book_id", a, "other_id", b, "err", err)
	}
}

// carryDupeReviewsTx gives the kept book the dropped one's review pairs with other
// books (a "these are different" still holds for the merged book), before the dropped
// row's own pairs go with it.
func carryDupeReviewsTx(ctx context.Context, tx *store.Transaction, keepID, dropID int64) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO book_dupe_reviews (a_id, b_id, reason, status, created_at)
		 SELECT CASE WHEN ? < other THEN ? ELSE other END,
		        CASE WHEN ? > other THEN ? ELSE other END, reason, status, created_at FROM (
		   SELECT CASE WHEN a_id = ? THEN b_id ELSE a_id END AS other, reason, status, created_at
		     FROM book_dupe_reviews WHERE a_id = ? OR b_id = ?) AS pairs
		 WHERE other != ? ON CONFLICT DO NOTHING`,
		keepID, keepID, keepID, keepID, dropID, dropID, dropID, keepID)
	return err
}

// mergedDetailRe reads the book a merge took away off its timeline entry: the old
// automatic fold's `Merged duplicate entry "Title" (key)` (a Go-quoted title), or
// Merge's `Merged “Title” (key)…`.
var mergedDetailRe = regexp.MustCompile(`^Merged (duplicate entry )?(?:("(?:[^"\\]|\\.)*")|“([^”]*)”) \(([^)]*)\)`)

// PastMerges lists the newest merges of one book into another on any book's timeline (at
// most limit). Combining an audiobook's chapter files is a 'merged' entry too ("Merged
// 12 chapter files into …"); that isn't a book merge and is left out.
func (s *Service) PastMerges(ctx context.Context, limit int) ([]PastMerge, error) {
	rows, err := s.repo.db.QueryContext(ctx,
		`SELECT e.book_id, b.title, e.detail, e.created_at FROM book_events e JOIN books b ON b.id = e.book_id
		  WHERE e.event = 'merged' AND (`+store.LikeASCII(s.repo.db, "e.detail", "'Merged duplicate entry %'", false)+` OR `+store.LikeASCII(s.repo.db, "e.detail", "'Merged “%'", false)+`)
		  ORDER BY e.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PastMerge{}
	for rows.Next() {
		var m PastMerge
		if err := rows.Scan(&m.BookID, &m.KeeperTitle, &m.Detail, &m.CreatedAt); err != nil {
			return nil, err
		}
		if g := mergedDetailRe.FindStringSubmatch(m.Detail); g != nil {
			m.Automatic = g[1] != ""
			m.LostTitle, m.LostKey = g[3], g[4]
			if g[2] != "" {
				if t, err := strconv.Unquote(g[2]); err == nil {
					m.LostTitle = t
				}
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
