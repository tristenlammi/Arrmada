package audioserver

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tristenlammi/arrmada/internal/attachment"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/library"
)

// The ebook of an audiobook's book, for apps that read as well as listen (the official
// Audiobookshelf app offers "Read" when an item has media.ebookFile). It is only ever
// attached to an audiobook item — the standard one and each version of the same book —
// and the library stays audiobooks-only: an ebook-only book would show up in Lissen and
// Plappa as something they can't play.

// ebook is the file behind an item's ebook edition.
type ebook struct {
	Ino, Path, Name, Ext, Format string
	Size, MTimeMs                int64
}

// ebookFormatFor names the formats Audiobookshelf's reader opens, by extension (lower
// case, with the dot); "" for anything else.
func ebookFormatFor(ext string) string {
	switch ext {
	case ".epub", ".kepub":
		return "epub"
	case ".pdf":
		return "pdf"
	case ".mobi":
		return "mobi"
	case ".azw3":
		return "azw3"
	}
	return ""
}

// ebookFormatOf is an item's ebook format from the database alone ("" = none), for list
// replies, which mustn't touch the disk. The file itself is checked when an app opens
// the item (ebookOf).
func ebookFormatOf(it Item) string {
	e := it.Book.Ebook
	if e == nil || e.Path == "" {
		return ""
	}
	if f := ebookFormatFor(strings.ToLower(filepath.Ext(e.Path))); f != "" {
		return f
	}
	return ebookFormatFor("." + strings.ToLower(strings.TrimSpace(e.Format)))
}

// ebookOf finds an item's ebook on disk through the books package's shared helper. Its
// id is stored like an audio file's, so renaming the ebook doesn't change it.
func (s *Server) ebookOf(ctx context.Context, it Item) (ebook, bool) {
	if ebookFormatOf(it) == "" {
		return ebook{}, false
	}
	// In a folder, a format the app's reader opens wins over one it doesn't (.azw).
	path, err := books.EbookFileFor(it.Book.Ebook.Path, func(ext string) bool { return ebookFormatFor(ext) != "" })
	if err != nil {
		return ebook{}, false
	}
	ext := strings.ToLower(filepath.Ext(path))
	format := ebookFormatFor(ext)
	fi, err := os.Stat(path)
	if format == "" || err != nil || fi.IsDir() {
		return ebook{}, false
	}
	name := filepath.Base(path)
	ino := s.probe.storedIno(ctx, path)
	if ino == "" {
		ino = library.EbookFileID(name)
		s.probe.storeIno(ctx, path, ino)
	}
	return ebook{Ino: ino, Path: path, Name: name, Ext: ext, Format: format,
		Size: fi.Size(), MTimeMs: fi.ModTime().UnixMilli()}, true
}

// libraryFileJSON is the ebook as one of the item's library files, as Audiobookshelf
// lists them.
func (e ebook) libraryFileJSON(it Item) obj {
	return obj{
		"ino": e.Ino,
		"metadata": obj{"filename": e.Name, "ext": e.Ext, "path": e.Path, "relPath": e.Name, "size": e.Size,
			"mtimeMs": e.MTimeMs, "ctimeMs": e.MTimeMs, "birthtimeMs": e.MTimeMs},
		"isSupplementary": false, "addedAt": it.AddedAt, "updatedAt": e.MTimeMs, "fileType": "ebook",
	}
}

// ebookFileJSON is media.ebookFile: the library file plus its format.
func (e ebook) ebookFileJSON(it Item) obj {
	o := e.libraryFileJSON(it)
	o["isSupplementary"], o["ebookFormat"] = nil, e.Format
	return o
}

// handleEbook sends an item's ebook (GET /api/items/{id}/ebook, and …/ebook/{fileid}
// naming its ino).
func (s *Server) handleEbook(w http.ResponseWriter, r *http.Request) {
	it, err := s.item(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Item not found")
		return
	}
	e, ok := s.ebookOf(r.Context(), it)
	if !ok || (r.PathValue("fileid") != "" && r.PathValue("fileid") != e.Ino) {
		writeError(w, http.StatusNotFound, "Ebook not found")
		return
	}
	s.sendEbook(w, r, it, e, false)
}

// sendEbook sends the file as a download, whatever asked: an ebook a browser opened in
// place (an FB2 is XML, which can carry script) would run on this server's origin. A kind
// without a known type goes as plain bytes, never sniffed. Apps read the body either way.
func (s *Server) sendEbook(w http.ResponseWriter, r *http.Request, it Item, e ebook, _ bool) {
	ct := books.EbookContentType(e.Ext)
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	// Named as the shelf's download names it: "Title - Author.epub".
	w.Header().Set("Content-Disposition", attachment.Header(books.EbookDownloadName(it.Book, e.Ext)))
	http.ServeFile(w, r, e.Path)
}
