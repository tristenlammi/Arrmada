package requests

import (
	"context"
	"errors"
	"fmt"

	"github.com/tristenlammi/arrmada/internal/books"
)

// Book formats: what a book request asked for — to read it, listen to it, or both.
//
// The choice is stored on the request and drives three things: the quality profile the
// book is added on (the Ebook, Audiobook or Ebook + Audiobook preset — a book profile is
// its choice of editions), when the request counts as ready, and the "ready" message.
// "" is a request made before the choice existed; it keeps the old behaviour (ready
// once any file is here, one "ready to read" message).
const (
	FormatsEbook     = "ebook"
	FormatsAudiobook = "audiobook"
	FormatsBoth      = "both"
)

// ErrBadFormats is returned for a formats value other than ebook, audiobook or both.
var ErrBadFormats = errors.New("formats must be ebook, audiobook or both")

// ValidFormats reports whether f is a format choice a request may carry.
func ValidFormats(f string) bool {
	return f == FormatsEbook || f == FormatsAudiobook || f == FormatsBoth
}

// FormatsOf names a choice of editions.
func FormatsOf(ebook, audiobook bool) string {
	switch {
	case ebook && audiobook:
		return FormatsBoth
	case audiobook:
		return FormatsAudiobook
	default:
		return FormatsEbook
	}
}

// editionsOf is FormatsOf backwards. "" reads as the ebook.
func editionsOf(formats string) (ebook, audiobook bool) {
	switch formats {
	case FormatsBoth:
		return true, true
	case FormatsAudiobook:
		return false, true
	default:
		return true, false
	}
}

// unionFormats is everything either choice asked for.
func unionFormats(a, b string) string {
	ae, aa := editionsOf(a)
	be, ba := editionsOf(b)
	return FormatsOf(ae || be, aa || ba)
}

// hasAudiobook reports whether a book has any audiobook on disk: the standard one or
// an extra version.
func hasAudiobook(b books.Book) bool {
	if b.Audiobook != nil && b.Audiobook.Path != "" {
		return true
	}
	for _, v := range b.AudioVersions {
		if v.File != nil && v.File.Path != "" {
			return true
		}
	}
	return false
}

func hasEbook(b books.Book) bool { return b.Ebook != nil && b.Ebook.Path != "" }

// bookReady reports whether a book request is fulfilled: every format it asked for is
// on disk. A request from before the choice existed is ready once any file is.
func bookReady(formats string, b books.Book) bool {
	switch formats {
	case "":
		return b.HasFile
	case FormatsEbook:
		return hasEbook(b)
	case FormatsAudiobook:
		return hasAudiobook(b)
	default:
		return hasEbook(b) && hasAudiobook(b)
	}
}

// StillComing names the formats a request asked for that aren't on disk yet (ebook,
// audiobook or both), or "" when none are missing or the request predates the choice.
func StillComing(formats string, b books.Book) string {
	if formats == "" {
		return ""
	}
	wantE, wantA := editionsOf(formats)
	needE, needA := wantE && !hasEbook(b), wantA && !hasAudiobook(b)
	if !needE && !needA {
		return ""
	}
	return FormatsOf(needE, needA)
}

// partialNote says which half of a "both" request is here and which is still coming,
// or "" when it isn't half-way.
func partialNote(formats string, b books.Book) string {
	if formats != FormatsBoth {
		return ""
	}
	switch e, a := hasEbook(b), hasAudiobook(b); {
	case e && !a:
		return "Ebook ready · audiobook on the way"
	case a && !e:
		return "Audiobook ready · ebook on the way"
	}
	return ""
}

// formatsForProfile is the choice of editions a book profile ref stands for.
func (s *Service) formatsForProfile(ctx context.Context, ref string) string {
	if s.quality == nil {
		return FormatsEbook
	}
	return FormatsOf(s.quality.BookEditions(ctx, ref))
}

// requestedFormats is what a book request asks for: its stored choice, or for a request
// from before the choice existed, the editions of its profile.
func (s *Service) requestedFormats(ctx context.Context, req Request) string {
	if req.Formats != "" {
		return req.Formats
	}
	return s.formatsForProfile(ctx, req.QualityProfile)
}

// normalizeBookFormats settles a new book request's format choice and the profile that
// goes with it. With no choice given, a profile the requester named decides; failing
// that the owner's default book profile does.
func (s *Service) normalizeBookFormats(ctx context.Context, in *Request) error {
	if in.Formats != "" && !ValidFormats(in.Formats) {
		return ErrBadFormats
	}
	if in.Formats == "" {
		ref := ""
		if in.QualityProfile != "" && s.quality != nil && s.quality.Known(ctx, in.QualityProfile) {
			ref = in.QualityProfile
		}
		in.Formats = s.formatsForProfile(ctx, ref) // "" reads through to the default profile
	}
	in.QualityProfile = s.profileForFormats(ctx, in.Formats)
	return nil
}

// profileForFormats is the book profile for a format choice: the Ebook, Audiobook or
// Ebook + Audiobook preset ("" without a quality service).
func (s *Service) profileForFormats(ctx context.Context, formats string) string {
	if s.quality == nil {
		return ""
	}
	e, a := editionsOf(formats)
	return s.quality.BookProfileFor(ctx, e, a)
}

// widenBook makes a library book want every edition a request asked for, never fewer:
// a listener asking for a book the library holds as an ebook adds the audiobook to it.
// It reports whether an edition the request asked for is still missing.
func (s *Service) widenBook(ctx context.Context, bookID int64, formats string) (missing bool) {
	b, err := s.books.Get(ctx, bookID)
	if err != nil {
		return false
	}
	wantE, wantA := editionsOf(formats)
	if s.quality != nil {
		haveE, haveA := s.quality.BookEditions(ctx, b.QualityProfile)
		uE, uA := haveE || wantE, haveA || wantA
		if uE != haveE || uA != haveA {
			ref := s.quality.BookProfileFor(ctx, uE, uA)
			// Only a profile that really is the union: with the presets edited away the
			// fallback default could be narrower than what the book has.
			if e, a := s.quality.BookEditions(ctx, ref); e == uE && a == uA {
				if err := s.books.SetQualityProfile(ctx, b.ID, ref); err != nil {
					s.log.Warn("request: could not widen the book's profile", "book_id", b.ID, "err", err)
				} else {
					added := "audiobook"
					if uE != haveE {
						added = "ebook"
						if uA != haveA {
							added = "ebook and the audiobook"
						}
					}
					s.books.AddEvent(ctx, b.ID, "profile", fmt.Sprintf("Now also wants the %s (requested)", added))
				}
			}
		}
	}
	return (wantE && !hasEbook(b)) || (wantA && !hasAudiobook(b))
}
