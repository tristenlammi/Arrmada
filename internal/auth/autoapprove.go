package auth

import "strings"

// AutoApproval is which media types a user's requests approve themselves for. One flag for
// everything let a Plex user's series request pull every season unasked; per type, the
// owner can trust movies and still decide shows.
type AutoApproval struct {
	Movie, Series, Book bool
}

// AllTypes is every type on (true) or none (false): the old single auto-approve flag.
func AllTypes(on bool) AutoApproval { return AutoApproval{Movie: on, Series: on, Book: on} }

// All reports whether every type is on.
func (a AutoApproval) All() bool { return a.Movie && a.Series && a.Book }

// ParseAutoApproval reads a comma-separated list of types ("movie,series,book"); unknown
// words are ignored, so "" is none.
func ParseAutoApproval(csv string) AutoApproval {
	var a AutoApproval
	for _, t := range strings.Split(csv, ",") {
		switch strings.TrimSpace(strings.ToLower(t)) {
		case "movie":
			a.Movie = true
		case "series":
			a.Series = true
		case "book":
			a.Book = true
		}
	}
	return a
}

// String is the comma-separated list ParseAutoApproval reads.
func (a AutoApproval) String() string {
	var out []string
	if a.Movie {
		out = append(out, "movie")
	}
	if a.Series {
		out = append(out, "series")
	}
	if a.Book {
		out = append(out, "book")
	}
	return strings.Join(out, ",")
}

// AutoApproval is the user's per-type auto-approve.
func (u User) AutoApproval() AutoApproval {
	return AutoApproval{Movie: u.AutoApproveMovie, Series: u.AutoApproveSeries, Book: u.AutoApproveBook}
}

// AutoApproves reports whether this user's request for mediaType ("movie", "series",
// "book") approves itself.
func (u User) AutoApproves(mediaType string) bool {
	switch mediaType {
	case "movie":
		return u.AutoApproveMovie
	case "series":
		return u.AutoApproveSeries
	case "book":
		return u.AutoApproveBook
	}
	return false
}

// setAutoApproval fills the per-type flags and the all-types summary.
func (u *User) setAutoApproval(movie, series, book int) {
	u.AutoApproveMovie, u.AutoApproveSeries, u.AutoApproveBook = movie != 0, series != 0, book != 0
	u.AutoApprove = u.AutoApproval().All()
}

// autoApproveCols are the per-type columns, in setAutoApproval's order. Writes keep the old
// single auto_approve column in step (on only when every type is), for a rolled-back build;
// nothing reads it any more.
const autoApproveCols = `auto_approve_movie, auto_approve_series, auto_approve_book`
