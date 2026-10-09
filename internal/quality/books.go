package quality

import (
	"context"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/books"
)

// Book profiles are the three edition presets (Ebook, Audiobook, Ebook + Audiobook): a
// book's profile IS its choice of editions. These two translate between the two, so
// the library scan, a book request and the request's approval all pick a profile the
// same way.

// BookEditions reports which editions a book profile ref wants, read through Effective
// (a deleted or missing profile is the default one).
func (s *Service) BookEditions(ctx context.Context, ref string) (ebook, audiobook bool) {
	sp, err := s.GetStored(ctx, s.Effective(ctx, ref, MediaBook))
	if err != nil {
		return true, false // no book profile at all: the ebook, as the sweep assumes
	}
	return books.WantedEditions(sp.FormatScores)
}

// BookProfileFor returns the book profile ref whose wanted editions are exactly these,
// falling back to the default book profile when no profile matches.
func (s *Service) BookProfileFor(ctx context.Context, ebook, audiobook bool) string {
	if profiles, err := s.ListStored(ctx, MediaBook); err == nil {
		for _, sp := range profiles {
			e, a := books.WantedEditions(sp.FormatScores)
			if e == ebook && a == audiobook {
				return "custom:" + strconv.FormatInt(sp.ID, 10)
			}
		}
	}
	return s.DefaultProfile(ctx, MediaBook)
}
