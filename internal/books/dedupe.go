package books

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Duplicate handling.
//
// The library used to be unique only on the catalogue key. Open Library files the same
// novel under several "works", the Google Books fallback issues its own keys, and the
// disk scan matches by whatever key the lookup returned — so one book ended up in the
// library three times. A book is now also identified by what it is: a normalised
// title (subtitle and edition notes stripped) plus the author's surname-ish words.

// DedupeKey identifies a book by title and author, tolerant of the differences between
// catalogues: punctuation, case, a leading article, a subtitle after a colon, an
// edition note in brackets, initials vs full first names.
func DedupeKey(title, author string) string {
	t := titleKey(title)
	if t == "" {
		return ""
	}
	return t + "|" + authorKey(author)
}

func titleKey(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	// "Dune: Deluxe Edition", "Dune (Dune Chronicles, #1)", "Dune - The Graphic Novel"
	for _, sep := range []string{":", " (", " [", " - ", " — "} {
		if i := strings.Index(t, sep); i > 0 {
			t = t[:i]
		}
	}
	return titlePartKey(t)
}

// volumeRe is the marker before a volume number that catalogues render every way:
// "White Sand, Vol. 1", "White Sand Volume 1", "White Sand #1", "White Sand 1".
var volumeRe = regexp.MustCompile(`\b(?:vol\.?|volume|no\.?|number|bk\.?|book)\s*(\d+)\b|#\s*(\d+)\b`)

// titlePartKey normalises one part of a title (the main title or a subtitle): the
// leading article goes, "&" reads as "and", a volume marker keeps only its number.
func titlePartKey(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	for _, art := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(t, art) && len(t) > len(art) {
			t = t[len(art):]
			break
		}
	}
	t = strings.ReplaceAll(t, "&", " and ")
	t = volumeRe.ReplaceAllString(t, "$1$2")
	return NormKey(t)
}

// titleKeys is every way a title can be read: its main title first, then the subtitle
// when it has one. "Mistborn: The Final Empire" is the same book as "The Final Empire".
func titleKeys(title string) []string {
	keys := []string{titleKey(title)}
	t := strings.TrimSpace(title)
	for _, sep := range []string{":", " - ", " — "} {
		if i := strings.Index(t, sep); i > 0 {
			if sub := titlePartKey(t[i+len(sep):]); sub != "" && sub != keys[0] {
				keys = append(keys, sub)
			}
			break
		}
	}
	return keys
}

func keysOverlap(a, b []string) bool {
	for _, x := range a {
		if x == "" {
			continue
		}
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// authorKey keeps the author's words of two or more letters, sorted — so "J. K.
// Rowling", "Rowling, J.K." and "J.K. Rowling" agree, as do "George R. R. Martin" and
// "Martin, George R.R.".
func authorKey(author string) string {
	words := strings.Fields(wordKey(author))
	var keep []string
	for _, w := range words {
		if len(w) >= 2 {
			keep = append(keep, w)
		}
	}
	if len(keep) == 0 {
		keep = words
	}
	sort.Strings(keep)
	return strings.Join(keep, " ")
}

// findDuplicate returns the library book that is the same title and author, if any.
// An author is required on both sides unless both are blank: "Beloved" by nobody is
// not the same as "Beloved" by Toni Morrison.
func (s *Service) findDuplicate(ctx context.Context, title, author string) (Book, bool) {
	key := DedupeKey(title, author)
	if key == "" {
		return Book{}, false
	}
	list, err := s.repo.List(ctx)
	if err != nil {
		return Book{}, false
	}
	for _, b := range list {
		if DedupeKey(b.Title, b.Author) == key && (strings.TrimSpace(author) == "") == (strings.TrimSpace(b.Author) == "") {
			return b, true
		}
	}
	return Book{}, false
}

// findByKey returns the library book with this catalogue key, if any.
func (s *Service) findByKey(ctx context.Context, key string) (Book, bool) {
	list, err := s.repo.List(ctx)
	if err != nil {
		return Book{}, false
	}
	for _, b := range list {
		if b.OLKey == key {
			return b, true
		}
	}
	return Book{}, false
}

// MergeDuplicates folds books that are the same title and author into one row each —
// keeping the one with files (then the oldest), carrying over any edition, monitoring,
// series, description or cover the keeper lacks — and returns how many were removed.
func (s *Service) MergeDuplicates(ctx context.Context) (int, error) {
	list, err := s.repo.List(ctx)
	if err != nil {
		return 0, err
	}
	groups := map[string][]Book{}
	var order []string
	for _, b := range list {
		key := DedupeKey(b.Title, b.Author)
		if key == "" {
			continue
		}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], b)
	}
	merged := 0
	for _, key := range order {
		g := groups[key]
		if len(g) < 2 {
			continue
		}
		sort.SliceStable(g, func(i, j int) bool {
			// Files first (both editions beats one), then the earliest added.
			fi, fj := editionCount(g[i]), editionCount(g[j])
			if fi != fj {
				return fi > fj
			}
			return g[i].ID < g[j].ID
		})
		keeper := g[0]
		for _, dup := range g[1:] {
			if err := s.foldInto(ctx, keeper, dup); err != nil {
				s.log.Warn("book dedupe: merge failed", "keep", keeper.Title, "drop_id", dup.ID, "err", err)
				continue
			}
			merged++
			// Re-read so a second duplicate sees what the first contributed.
			if k, err := s.repo.Get(ctx, keeper.ID); err == nil {
				keeper = k
			}
		}
	}
	if merged > 0 {
		s.log.Info("book dedupe: merged duplicate books", "removed", merged)
	}
	return merged, nil
}

func editionCount(b Book) int {
	n := 0
	if b.Ebook != nil {
		n++
	}
	if b.Audiobook != nil {
		n++
	}
	return n
}

// foldInto moves what dup has and keeper lacks onto keeper, then deletes dup's row.
func (s *Service) foldInto(ctx context.Context, keeper, dup Book) error {
	if dup.Ebook != nil && keeper.Ebook == nil {
		if err := s.repo.SetEdition(ctx, keeper.ID, KindEbook, dup.Ebook.Path, dup.Ebook.Format, dup.Ebook.SizeBytes, dup.Ebook.FileCount); err != nil {
			return err
		}
	}
	if dup.Audiobook != nil && keeper.Audiobook == nil {
		if err := s.repo.SetEdition(ctx, keeper.ID, KindAudiobook, dup.Audiobook.Path, dup.Audiobook.Format, dup.Audiobook.SizeBytes, dup.Audiobook.FileCount); err != nil {
			return err
		}
	}
	if dup.Monitored && !keeper.Monitored {
		_ = s.repo.SetMonitored(ctx, keeper.ID, true)
	}
	if keeper.SeriesName == "" && dup.SeriesName != "" {
		_ = s.repo.SetSeries(ctx, keeper.ID, dup.SeriesName, dup.SeriesPosition)
	}
	if (keeper.Description == "" && dup.Description != "") || (keeper.CoverURL == "" && dup.CoverURL != "") || (len(keeper.Subjects) == 0 && len(dup.Subjects) > 0) {
		desc, cover, subj := keeper.Description, keeper.CoverURL, keeper.Subjects
		if desc == "" {
			desc = dup.Description
		}
		if cover == "" {
			cover = dup.CoverURL
		}
		if len(subj) == 0 {
			subj = dup.Subjects
		}
		_ = s.repo.UpdateMeta(ctx, keeper.ID, desc, cover, subj)
	}
	if err := s.repo.Delete(ctx, dup.ID); err != nil {
		return err
	}
	s.repo.AddEvent(ctx, keeper.ID, "merged", fmt.Sprintf("Merged duplicate entry %q (%s)", dup.Title, dup.OLKey))
	return nil
}
