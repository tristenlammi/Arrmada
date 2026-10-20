package requests

import (
	"context"

	"github.com/tristenlammi/arrmada/internal/adultfilter"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/notify"
)

// posterFor is the poster a movie or show's 'ready' notice carries: the library's stored
// poster once the title is there, else the one the request was made with. It is the public
// TMDB image URL, never an Arrmada route, so a push service or Apprise can fetch it without
// a login. "" for a book, for a URL off the allowed image hosts (notify.AllowedAttachURL),
// and for anything the adult filter hides, by the request's title or the library's.
func (s *Service) posterFor(ctx context.Context, req Request) string {
	if req.MediaType != "movie" && req.MediaType != "series" {
		return ""
	}
	poster := req.PosterURL
	if req.TMDBID > 0 {
		switch req.MediaType {
		case "movie":
			if s.movies != nil {
				if m, err := s.movies.GetByTMDB(ctx, req.TMDBID); err == nil {
					if adultfilter.Matches(m.Title) {
						return ""
					}
					if m.PosterURL != "" {
						poster = m.PosterURL
					}
				}
			}
		case "series":
			if s.series != nil {
				if sr, err := s.series.GetByTMDB(ctx, req.TMDBID); err == nil {
					if adultfilter.Matches(sr.Title) {
						return ""
					}
					if sr.PosterURL != "" {
						poster = sr.PosterURL
					}
				}
			}
		}
	}
	if adultfilter.Matches(req.Title) || !notify.AllowedAttachURL(poster) {
		return ""
	}
	return poster
}

// A request keeps the title it was made under (TMDB's English title): it names the request
// in the API and in a staff search. What people READ — the request list, and the notices
// once the title is in the library — follows the library's shown title instead (Settings →
// Library → "Show titles as", or the title's own override), so a Greek household's
// request for "The Travelling Players" arrives as "Ο Θίασος" like the rest of the library.

// withShownTitle is req with its title as the library shows it, for a notice's text. A
// title not in the library (yet), or one shown under its own name, keeps req.Title.
func (s *Service) withShownTitle(ctx context.Context, req Request) Request {
	if shown := s.shownTitle(ctx, req); shown != "" {
		req.Title = shown
	}
	return req
}

// shownTitle is the library's shown title for the movie or show req asked for, or "". A
// book's is its title edited by hand on the book page, when it has one.
func (s *Service) shownTitle(ctx context.Context, req Request) string {
	if req.MediaType == "book" {
		if req.BookID > 0 && s.books != nil {
			if b, err := s.books.Get(ctx, req.BookID); err == nil && b.Edited(books.FieldTitle) {
				return b.Title
			}
		}
		return ""
	}
	if req.TMDBID <= 0 {
		return ""
	}
	switch req.MediaType {
	case "movie":
		if s.movies != nil {
			if m, err := s.movies.GetByTMDB(ctx, req.TMDBID); err == nil {
				return m.Shown()
			}
		}
	case "series":
		if s.series != nil {
			if sr, err := s.series.GetByTMDB(ctx, req.TMDBID); err == nil {
				return sr.Shown()
			}
		}
	}
	return ""
}
