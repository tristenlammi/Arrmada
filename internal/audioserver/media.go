package audioserver

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
)

func decodeLenient(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// handleFile streams one audio file; Range requests make seeking work.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	s.serveFile(w, r, r.PathValue("id"), r.PathValue("ino"), false)
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	s.serveFile(w, r, r.PathValue("id"), r.PathValue("ino"), true)
}

// serveFile streams one of an item's audio files, picked by its ino from the item's own
// file list (so nothing outside the audiobook can be named), or the ebook of its book
// by the ebook's ino.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, key, ino string, attach bool) {
	ctx := r.Context()
	it, err := s.item(ctx, key)
	if err != nil {
		writeError(w, http.StatusNotFound, "Item not found")
		return
	}
	// File lists are cached for a while, so when the file isn't in the list or is no
	// longer on disk (replaced or renamed outside Arrmada), the folder is listed again
	// once before giving up. The ebook is looked at in between, so playing never touches
	// it and asking for it doesn't re-list the audiobook.
	f, ok := s.cachedFileByIno(ctx, it.Path, ino)
	if !ok {
		if e, isEbook := s.ebookOf(ctx, it); isEbook && e.Ino == ino {
			s.sendEbook(w, r, it, e, attach)
			return
		}
		s.probe.cache.drop(it.Path)
		f, ok = s.cachedFileByIno(ctx, it.Path, ino)
	}
	if !ok {
		writeError(w, http.StatusNotFound, "File not found")
		return
	}
	s.sendTrack(w, r, it.Path, f, attach)
}

// sendTrack streams one track: a file as it is, or a chapter track of a very long file,
// cut on first asking (clips.go) and from then on a file like any other.
func (s *Server) sendTrack(w http.ResponseWriter, r *http.Request, root string, f AudioFile, attach bool) {
	path := f.Path
	if f.Clip != nil {
		p, err := s.clips.get(r.Context(), f)
		if err != nil {
			if r.Context().Err() == nil {
				// No title, no user: only that it failed and why.
				s.log.Warn("audiobook server: couldn't prepare a chapter of a long audiobook", "err", err)
			}
			writeError(w, http.StatusServiceUnavailable, "Couldn't prepare this chapter")
			return
		}
		path = p
		// The next chapter is cut now, so the app finds it ready when this one ends, even
		// if the disk the book is on has gone to sleep by then.
		if files, err := s.probe.files(r.Context(), root, false); err == nil {
			if next, ok := nextTrack(files, f); ok {
				s.clips.begin(next)
			}
		}
	}
	w.Header().Set("Content-Type", mimeFor(f.Ext))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if attach {
		w.Header().Set("Content-Disposition", attachment(f.Name))
	}
	http.ServeFile(w, r, path)
}

// cachedFileByIno finds one of an item's tracks (an audio file, or a chapter track of a
// very long one) from its cached file list, if the file is still on disk.
func (s *Server) cachedFileByIno(ctx context.Context, root, ino string) (AudioFile, bool) {
	files, err := s.probe.files(ctx, root, false)
	if err != nil {
		return AudioFile{}, false
	}
	f, ok := trackByIno(files, ino)
	if !ok {
		return AudioFile{}, false
	}
	if _, err := s.probe.statPath(f.Path); err != nil {
		return AudioFile{}, false
	}
	return f, true
}

// handleItemDownload sends the whole audiobook: the file itself, or a zip of its files.
func (s *Server) handleItemDownload(w http.ResponseWriter, r *http.Request) {
	it, err := s.item(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Item not found")
		return
	}
	if err := s.WriteAudiobook(r.Context(), w, r, it.Key); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
	}
}

// WriteAudiobook sends an audiobook as a download — a single file directly, several
// files as one zip (stored, not recompressed: audio doesn't shrink and it starts at once).
// Shared with Arrmada's own Books page.
func (s *Server) WriteAudiobook(ctx context.Context, w http.ResponseWriter, r *http.Request, key string) error {
	it, err := s.item(ctx, key)
	if err != nil {
		return errors.New("no audiobook for that book")
	}
	files, err := s.probe.files(ctx, it.Path, false)
	if err != nil || len(files) == 0 {
		return errors.New("the audiobook's files are missing on disk")
	}
	name := safeName(it.Title)
	if it.Book.Author != "" {
		name += " - " + safeName(it.Book.Author)
	}
	if len(files) == 1 {
		f := files[0]
		w.Header().Set("Content-Type", mimeFor(f.Ext))
		w.Header().Set("Content-Disposition", attachment(name+f.Ext))
		w.Header().Set("Cache-Control", "private, no-store")
		http.ServeFile(w, r, f.Path)
		return nil
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", attachment(name+".zip"))
	w.Header().Set("Cache-Control", "private, no-store")
	zw := zip.NewWriter(w)
	for _, f := range files {
		src, err := os.Open(f.Path)
		if err != nil {
			continue
		}
		hdr := &zip.FileHeader{Name: name + "/" + f.Name, Method: zip.Store, Modified: time.UnixMilli(f.MTimeMs)}
		dst, err := zw.CreateHeader(hdr)
		if err == nil {
			_, err = io.Copy(dst, src)
		}
		src.Close()
		if err != nil {
			break // the client went away
		}
	}
	return zw.Close()
}

func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	cache := ""
	if versionedCover(r) {
		cache = "private, " + immutableYear // it came through someone's sign-in
	}
	s.serveCover(w, r, r.PathValue("id"), cache)
}

// immutableYear caches a cover for a year. Only for a request whose URL changes with the
// cover (versionedCover): a new cover is a new URL, so the old one never needs checking.
const immutableYear = "max-age=31536000, immutable"

// versionedCover reports whether a cover URL names its version: apps add ?ts=<updatedAt>
// (Audiobookshelf's own convention), Arrmada's web player ?v=.
func versionedCover(r *http.Request) bool {
	q := r.URL.Query()
	return q.Get("ts") != "" || q.Get("v") != ""
}

// serveCover sends an item's cover. cache overrides the Cache-Control the apps get
// ("" keeps it).
func (s *Server) serveCover(w http.ResponseWriter, r *http.Request, key, cache string) {
	it, err := s.item(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusNotFound, "Item not found")
		return
	}
	s.serveBookCover(w, r, it.Book, cache)
}

// ServeBookCover sends a book's cover whatever editions it has — the OPDS catalogue's
// covers for ebooks — from the same uploaded covers and image cache the apps use.
// cache is the Cache-Control to send ("" keeps the default).
func (s *Server) ServeBookCover(w http.ResponseWriter, r *http.Request, b books.Book, cache string) {
	s.serveBookCover(w, r, b, cache)
}

func (s *Server) serveBookCover(w http.ResponseWriter, r *http.Request, b books.Book, cache string) {
	// A cover uploaded in Arrmada wins.
	if m, _ := filepath.Glob(filepath.Join(s.coverDir, fmt.Sprintf("book-%d.*", b.ID))); len(m) > 0 {
		w.Header().Set("Cache-Control", firstNonEmpty(cache, "private, max-age=86400"))
		setImageGuards(w)
		http.ServeFile(w, r, m[0])
		return
	}
	u := b.CoverURL
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		writeError(w, http.StatusNotFound, "No cover")
		return
	}
	s.images.serve(w, r, u, cache)
}

func (s *Server) handleAuthorImage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("aid")
	for name, u := range s.books.KnownAuthorImages(ctx) {
		if isAuthorID(id, name) && (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) {
			s.images.serve(w, r, u, "")
			return
		}
	}
	writeError(w, http.StatusNotFound, "No image")
}

func attachment(name string) string {
	if h := mime.FormatMediaType("attachment", map[string]string{"filename": name}); h != "" {
		return h
	}
	return `attachment; filename="audiobook` + filepath.Ext(name) + `"`
}

func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if s == "" {
		return "audiobook"
	}
	return s
}
