package books

import "strings"

// Ebook and audiobook format tags (uppercase). Used to classify releases and files,
// and to derive which editions a quality profile wants (from its format_scores keys).
var ebookFormats = map[string]bool{
	"EPUB": true, "AZW3": true, "AZW": true, "MOBI": true, "PDF": true,
	"CBZ": true, "CBR": true, "FB2": true, "DJVU": true, "LIT": true,
}

var audiobookFormats = map[string]bool{
	"M4B": true, "M4A": true, "MP3": true, "AAC": true, "FLAC": true, "OGG": true, "OPUS": true,
}

// IsEbookFormat reports whether a format tag is an ebook format.
func IsEbookFormat(f string) bool { return ebookFormats[strings.ToUpper(f)] }

// IsAudiobookFormat reports whether a format tag is an audiobook format.
func IsAudiobookFormat(f string) bool { return audiobookFormats[strings.ToUpper(f)] }

// EditionOf classifies a format tag into "ebook", "audiobook", or "".
func EditionOf(format string) string {
	switch {
	case IsEbookFormat(format):
		return KindEbook
	case IsAudiobookFormat(format):
		return KindAudiobook
	default:
		return ""
	}
}

// WantedEditions returns which editions a profile's format-score map wants — ebook if
// any ebook format scores > 0, audiobook if any audiobook format scores > 0. A profile
// with neither (or an unresolved profile) defaults to wanting the ebook.
func WantedEditions(scores map[string]int) (ebook, audiobook bool) {
	for f, s := range scores {
		if s <= 0 {
			continue
		}
		if IsEbookFormat(f) {
			ebook = true
		}
		if IsAudiobookFormat(f) {
			audiobook = true
		}
	}
	if !ebook && !audiobook {
		ebook = true
	}
	return
}
