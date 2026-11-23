package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/tristenlammi/arrmada/internal/attachment"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/kepub"
)

// KEPUB for Kobo: an EPUB converted on demand (internal/kepub) for Kobo's stock reader,
// which only shows real page numbers, reading stats and its better typography for a
// KEPUB. My shelf and the book page offer "Download for Kobo (KEPUB)" beside the EPUB
// (GET /books/{id}/ebook?format=kepub), and the OPDS feeds offer it as a second
// download. The library file is never touched; a failed conversion sends the EPUB.

// kepubReady reports whether KEPUBs can be made now: the setting is on and kepubify is
// in the image.
func (a *api) kepubReady(ctx context.Context) bool {
	return a.deps.Kepub != nil && a.deps.Kepub.Ready(ctx)
}

// kepubable reports whether an ebook edition is an EPUB the Kobo download can convert.
// A single file goes by its recorded format. An edition of several files (a Calibre
// folder holding the AZW3 and the EPUB) is recorded under its first file's format, so
// for that rarer case the file a download would send is looked at.
func kepubable(f *books.BookFile) bool {
	if f == nil || f.Path == "" {
		return false
	}
	if f.FileCount > 1 {
		p, err := books.EbookFile(f.Path)
		return err == nil && kepub.IsEPUB(p)
	}
	return strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(f.Format), "."), "epub")
}

// setEbookKepub says whether the book page may offer the Kobo download.
func setEbookKepub(b *books.Book, ready bool) {
	if b.Ebook != nil {
		b.Ebook.Kepub = ready && kepubable(b.Ebook)
	}
}

// serveKepub sends a book's EPUB at path as a KEPUB, answering false (having written
// nothing) when it can't, so the caller sends the EPUB instead. A failed conversion is
// logged without the book or the person.
func (a *api) serveKepub(w http.ResponseWriter, r *http.Request, b books.Book, path string) bool {
	if !kepub.IsEPUB(path) || !a.kepubReady(r.Context()) {
		return false
	}
	f, err := a.deps.Kepub.Open(r.Context(), b.ID, path)
	if err != nil {
		if kepub.Failed(err) && r.Context().Err() == nil {
			a.deps.Log.Warn("ebook download: couldn't make a KEPUB, sending the EPUB", "err", err)
		}
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false
	}
	w.Header().Set("Content-Type", kepub.ContentType)
	w.Header().Set("Content-Disposition", attachment.Header(books.EbookDownloadName(b, kepub.Ext)))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "", st.ModTime(), f)
	return true
}
