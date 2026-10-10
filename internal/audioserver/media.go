package audioserver

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func decodeLenient(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// handleFile streams one audio file; Range requests make seeking work.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) { s.serveFile(w, r, false) }

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) { s.serveFile(w, r, true) }

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, attach bool) {
	ctx := r.Context()
	it, err := s.item(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Item not found")
		return
	}
	files, err := s.probe.files(ctx, it.Path, false)
	if err != nil {
		writeError(w, http.StatusNotFound, "File not found")
		return
	}
	ino := r.PathValue("ino")
	for _, f := range files {
		if f.Ino != ino {
			continue
		}
		w.Header().Set("Content-Type", mimeFor(f.Ext))
		w.Header().Set("Cache-Control", "private, max-age=86400")
		if attach {
			w.Header().Set("Content-Disposition", attachment(f.Name))
		}
		http.ServeFile(w, r, f.Path)
		return
	}
	writeError(w, http.StatusNotFound, "File not found")
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
	it, err := s.item(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Item not found")
		return
	}
	// A cover uploaded in Arrmada wins.
	if m, _ := filepath.Glob(filepath.Join(s.coverDir, fmt.Sprintf("book-%d.*", it.Book.ID))); len(m) > 0 {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, m[0])
		return
	}
	u := it.Book.CoverURL
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		writeError(w, http.StatusNotFound, "No cover")
		return
	}
	s.images.serve(w, r, u)
}

func (s *Server) handleAuthorImage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("aid")
	for name, u := range s.books.KnownAuthorImages(ctx) {
		if isAuthorID(id, name) && (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) {
			s.images.serve(w, r, u)
			return
		}
	}
	writeError(w, http.StatusNotFound, "No image")
}

// imageCache fetches remote cover and author images once and serves them from disk, so
// a phone scrolling the library doesn't hit the catalogue's image hosts every time.
type imageCache struct {
	dir    string
	client *http.Client
	mu     sync.Mutex
}

func newImageCache(dir string) *imageCache {
	return &imageCache{dir: dir, client: &http.Client{Timeout: 20 * time.Second}}
}

func (c *imageCache) serve(w http.ResponseWriter, r *http.Request, url string) {
	h := sha1.Sum([]byte(url))
	path := filepath.Join(c.dir, hex.EncodeToString(h[:]))
	if _, err := os.Stat(path); err != nil {
		if err := c.fetch(r.Context(), url, path); err != nil {
			writeError(w, http.StatusNotFound, "Image unavailable")
			return
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=604800")
	if ct, err := os.ReadFile(path + ".type"); err == nil {
		w.Header().Set("Content-Type", string(ct))
	}
	http.ServeFile(w, r, path)
}

func (c *imageCache) fetch(ctx context.Context, url, path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("image fetch: %s", resp.Status)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		return errors.New("not an image")
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 10<<20))
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.WriteFile(path+".type", []byte(ct), 0o644)
	return os.Rename(tmp, path)
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
