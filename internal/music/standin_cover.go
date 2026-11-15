package music

// No metadata source Arrmada reads supplies artist photos (MusicBrainz has none, and
// Artist.ImageURL is never filled), so the library shows one of the artist's album covers
// in the photo's place, the way Books shows a book cover for an author without a photo.

// coverPick is one album's claim to stand for its artist.
type coverPick struct {
	url   string
	held  bool // some of its tracks are on disk: a cover the user has, not just a listing
	album bool // a studio album (primary type Album), not a single, EP or broadcast
	year  int
	id    int64
}

func pickOf(al Album) coverPick {
	return coverPick{url: al.CoverURL, held: al.HaveTracks > 0, album: al.PrimaryType == "Album", year: al.Year, id: al.ID}
}

// beats says whether p is the better stand-in than q: an album with a cover, held before
// listed only, a studio album before the rest, then the newest, then the first added.
func (p coverPick) beats(q coverPick) bool {
	switch {
	case p.url == "" || q.url == "":
		return p.url != ""
	case p.held != q.held:
		return p.held
	case p.album != q.album:
		return p.album
	case p.year != q.year:
		return p.year > q.year
	}
	return p.id < q.id
}

// standInCover is the artist's stand-in cover from its albums ("" when none has one).
func standInCover(albums []Album) string {
	var best coverPick
	for _, al := range albums {
		if c := pickOf(al); c.beats(best) {
			best = c
		}
	}
	return best.url
}
