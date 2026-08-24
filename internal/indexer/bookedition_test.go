package indexer

import "testing"

// The Coven by "Harper L Woods" sat on MyAnonaMouse the whole time. Arrmada searched
// "Harper L. Woods Coven audiobook" and got nothing, over and over, because MAM matches
// ALL query words against title/author/narrator/series.
func TestMAMSearchTextDropsPunctuation(t *testing.T) {
	cases := map[string]string{
		// The initial: the library records "L." and the listing carries "L". With every
		// word required, that one token is the whole difference between a hit and zero.
		"Harper L. Woods Coven": "Harper L Woods Coven",
		// Punctuation splits rather than joins, so a hyphenated word stays two searchable
		// words instead of becoming one that matches nothing.
		"Sci-Fi Anthology": "Sci Fi Anthology",
		"J.R.R. Tolkien":   "J R R Tolkien",
		"  spaced   out  ": "spaced out",
		"O'Brien":          "O Brien",
		"plain title":      "plain title",
	}
	for in, want := range cases {
		if got := mamSearchText(in); got != want {
			t.Errorf("mamSearchText(%q) = %q, want %q", in, got, want)
		}
	}
}

// The edition is a CATEGORY on a book tracker. Asking for it as a query word could only
// ever return nothing, since no listing's metadata contains the literal word "audiobook".
func TestMAMCatsForEdition(t *testing.T) {
	both := []int{13, 14}
	if got := mamCatsFor("audiobook", both); len(got) != 1 || got[0] != mamCatAudiobooks {
		t.Errorf("audiobook cats = %v, want [%d]", got, mamCatAudiobooks)
	}
	if got := mamCatsFor("ebook", both); len(got) != 1 || got[0] != mamCatEbooks {
		t.Errorf("ebook cats = %v, want [%d]", got, mamCatEbooks)
	}
	// No edition asked for: keep whatever the caller/indexer configured.
	if got := mamCatsFor("", both); len(got) != 2 {
		t.Errorf("unset edition cats = %v, want the fallback %v", got, both)
	}
}

// A general tracker has no edition category, so the word in the query is all it has —
// its release NAMES carry "audiobook". This is the case the old code was written for.
func TestTorznabTextAppendsAudiobookOnly(t *testing.T) {
	base := SearchQuery{Text: "Harper L. Woods Coven"}

	audio := base
	audio.BookEdition = "audiobook"
	if got, want := torznabText(audio), "Harper L. Woods Coven audiobook"; got != want {
		t.Errorf("audiobook query = %q, want %q", got, want)
	}

	// "ebook" rarely appears in a release name, so appending it would filter out the very
	// releases it was meant to find.
	ebook := base
	ebook.BookEdition = "ebook"
	if got := torznabText(ebook); got != base.Text {
		t.Errorf("ebook query = %q, want it unchanged", got)
	}
	if got := torznabText(base); got != base.Text {
		t.Errorf("no-edition query = %q, want it unchanged", got)
	}
	// Nothing to append to: a recent-feed query with no text must stay empty, or it turns
	// into a search for the word "audiobook".
	empty := SearchQuery{BookEdition: "audiobook"}
	if got := torznabText(empty); got != "" {
		t.Errorf("empty query = %q, want empty", got)
	}
}

// Every MyAnonaMouse search result carries a `dl` download token, and the path form that
// uses it is what the API hands you for grabbing. It was discarded in favour of the
// "?tid=" query form, which the site answers with 406 — so a book that searched perfectly
// could never be grabbed.
func TestMAMDownloadURLPrefersTheToken(t *testing.T) {
	const base = "https://www.myanonamouse.net"

	if got, want := mamDownloadURL("12345", "abc123def"), base+"/tor/download.php/abc123def"; got != want {
		t.Errorf("with a token: %q, want %q", got, want)
	}
	// Whitespace around the token would make the path 404 rather than fail loudly.
	if got, want := mamDownloadURL("12345", "  abc123def  "), base+"/tor/download.php/abc123def"; got != want {
		t.Errorf("token trimming: %q, want %q", got, want)
	}
	// No token: still hand back something that might work. Better to try and fail than
	// to return a link that cannot exist.
	if got, want := mamDownloadURL("12345", ""), base+"/tor/download.php?tid=12345"; got != want {
		t.Errorf("without a token: %q, want %q", got, want)
	}
}
