package books

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Handing an ebook file to someone: the shelf's download (GET /books/{id}/ebook), Send to
// Kindle, and the OPDS feeds all start from a book's stored ebook path and need the same
// answers — which file to send, what type it is, what to call it, and whether Amazon will
// take it. They live here, once, so the three never disagree.
//
//	path, err := books.EbookFile(b.Ebook.Path)          // the file to send
//	ext := strings.ToLower(filepath.Ext(path))
//	ct := books.EbookContentType(ext)                   // "" = not an ebook
//	name := books.EbookDownloadName(b, ext)             // "Dune - Frank Herbert.epub"
//	w.Header().Set("Content-Disposition", attachment.Header(name))
//
// None of these log or read anything but the path given; a caller that logs must not name
// the book alongside the person (the audiobook privacy rule applies to reading too).

// EbookFile resolves a stored ebook path to the file to send. An ebook edition is one
// file, but should it ever be a folder, the best-known format inside wins (EbookRank).
// A missing file, or a folder with no ebook in it, is an error (os.ErrNotExist for the
// latter).
func EbookFile(stored string) (string, error) {
	return EbookFileFor(stored, nil)
}

// EbookFileFor is EbookFile that, in a folder, prefers the files accept takes (by
// lower-case extension, ".epub"): Send to Kindle wants the PDF in a folder that also holds
// an AZW3. With nothing acceptable it falls back to the best file overall, so the caller
// can say which format it found. A single stored file is returned as it is.
func EbookFileFor(stored string, accept func(ext string) bool) (string, error) {
	st, err := os.Stat(stored)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return stored, nil
	}
	entries, err := os.ReadDir(stored)
	if err != nil {
		return "", err
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() && EbookContentType(strings.ToLower(filepath.Ext(e.Name()))) != "" {
			found = append(found, e.Name())
		}
	}
	if len(found) == 0 {
		return "", os.ErrNotExist
	}
	sort.SliceStable(found, func(i, j int) bool {
		if accept != nil {
			ai, aj := accept(strings.ToLower(filepath.Ext(found[i]))), accept(strings.ToLower(filepath.Ext(found[j])))
			if ai != aj {
				return ai
			}
		}
		return EbookRank(found[i]) < EbookRank(found[j])
	})
	return filepath.Join(stored, found[0]), nil
}

// EbookRank orders formats by how many readers open them (lower first): EPUB, then the
// Kindle formats, then PDF.
func EbookRank(name string) int {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".epub":
		return 0
	case ".azw3", ".kepub":
		return 1
	case ".mobi", ".azw":
		return 2
	case ".pdf":
		return 3
	}
	return 4
}

// EbookContentType maps a lower-case ebook extension (".epub") to its media type; "" means
// it isn't an ebook format Arrmada hands out.
func EbookContentType(ext string) string {
	switch ext {
	case ".epub", ".kepub":
		return "application/epub+zip"
	case ".mobi":
		return "application/x-mobipocket-ebook"
	case ".azw", ".azw3":
		return "application/vnd.amazon.mobi8-ebook"
	case ".pdf":
		return "application/pdf"
	}
	return ""
}

// EbookDownloadName is "Title - Author.ext" with anything a filesystem or a header would
// choke on removed; a title of nothing usable becomes "book.ext".
func EbookDownloadName(b Book, ext string) string {
	name := b.Title
	if b.Author != "" {
		name += " - " + b.Author
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r == 0x7f:
			return -1
		case strings.ContainsRune(`/\:*?"<>|`, r):
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.Join(strings.Fields(name), " "))
	if name == "" {
		name = "book"
	}
	return name + ext
}

// KindleMaxBytes is the largest ebook Send to Kindle will email: Amazon's limit for a
// document sent by email.
const KindleMaxBytes = 50 << 20

// KindleAccepts reports whether Amazon's Send to Kindle email takes an ebook format,
// given as an extension (".epub") or a format name ("EPUB"). Amazon stopped taking MOBI
// and AZW by email in 2022; its list changes, so this is the one place that knows it.
// KEPUB is EPUB with Kobo's additions under a name Amazon doesn't know, so it's refused.
func KindleAccepts(format string) bool {
	switch strings.TrimPrefix(strings.ToLower(strings.TrimSpace(format)), ".") {
	case "epub", "pdf":
		return true
	}
	return false
}
