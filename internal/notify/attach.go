package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Posters on alerts. A library alert (a movie, show or book landing) carries the title's
// poster: the public TMDB or Open Library image the library already stores, which Apprise
// fetches and attaches. A Plex stream alert carries the Plex image path instead
// ("plex:/library/metadata/…/thumb/…"); at send time Arrmada fetches it itself, with the
// Plex token, into a temporary file Apprise attaches, and deletes the file after that
// attempt whether it went through or not. The token never leaves the server: it isn't in
// the stored row, the attachment argument or the file's name. A Jellyfin stream alert works
// the same way with "jellyfin:/Items/<id>/Images/Primary" and Jellyfin's key.
//
// Only images from the hosts below are ever handed to Apprise as a URL, so a poster link
// can't point it at the server's own network. A poster that can't be had is left off;
// the alert still goes.

// attachHosts are the image hosts a poster URL may come from.
var attachHosts = map[string]bool{
	"image.tmdb.org":         true,
	"covers.openlibrary.org": true,
	"assets.hardcover.app":   true,
	"coverartarchive.org":    true,
}

// plexAttachPrefix marks an attachment that is a Plex image path.
const plexAttachPrefix = "plex:"

// plexImageRe is the shape of a Plex poster or art path: an item's thumb or art, with
// Plex's optional version stamp.
var plexImageRe = regexp.MustCompile(`^/library/metadata/[0-9]+/(thumb|art)(/[0-9]+)?$`)

// jellyfinAttachPrefix marks an attachment that is a Jellyfin item's poster; the rest is
// exactly "/Items/<32 hex>/Images/Primary" (jellyfinImageRe), the one shape Insights'
// image fetch accepts too. It's fetched the same way as a Plex one, with Jellyfin's key
// staying on the server.
const jellyfinAttachPrefix = "jellyfin:"

var jellyfinImageRe = regexp.MustCompile(`^/Items/[0-9a-f]{32}/Images/Primary$`)

// embyAttachPrefix is jellyfinAttachPrefix for an Emby item, which Emby numbers:
// "emby:/Items/<digits>/Images/Primary", fetched the same way with Emby's key.
const embyAttachPrefix = "emby:"

var embyImageRe = regexp.MustCompile(`^/Items/[0-9]{1,19}/Images/Primary$`)

// serverAttach reports whether a is a Jellyfin or Emby poster in its strict form.
func serverAttach(a string) bool {
	if p, ok := strings.CutPrefix(a, jellyfinAttachPrefix); ok {
		return jellyfinImageRe.MatchString(p)
	}
	if p, ok := strings.CutPrefix(a, embyAttachPrefix); ok {
		return embyImageRe.MatchString(p)
	}
	return false
}

// maxPosterBytes bounds a fetched Plex image.
const maxPosterBytes = 10 << 20

// AllowedAttachURL reports whether u is a poster URL Apprise may fetch: https, one of the
// image hosts, no user info or port.
func AllowedAttachURL(u string) bool {
	if u == "" || len(u) > 1000 || strings.ContainsAny(u, " \t\r\n") {
		return false
	}
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" || p.User != nil || p.Port() != "" || p.Opaque != "" {
		return false
	}
	return attachHosts[strings.ToLower(p.Hostname())]
}

// validAttach reports whether a stored attachment is one this package would make.
func validAttach(a string) bool {
	if p, ok := strings.CutPrefix(a, plexAttachPrefix); ok {
		return plexImageRe.MatchString(p)
	}
	if strings.HasPrefix(a, jellyfinAttachPrefix) || strings.HasPrefix(a, embyAttachPrefix) {
		return serverAttach(a)
	}
	return AllowedAttachURL(a)
}

// PosterFunc finds the poster URL of a library item ("movie", "series" or "book" by id),
// "" when it has none.
type PosterFunc func(ctx context.Context, kind string, id int64) string

// ImageFunc fetches a Plex image by its path (or a Jellyfin one, "jellyfin:/Items/…"),
// authenticated server-side. Insights.Image.
type ImageFunc func(ctx context.Context, path string) (*http.Response, error)

// hooks are what main wires in after the service starts (the settings, the libraries and
// the Plex client come up later than the dispatcher), so they're read under a lock.
type hooks struct {
	mu        sync.RWMutex
	publicURL func(ctx context.Context) string
	poster    PosterFunc
	image     ImageFunc
}

// SetPublicURL wires the outside address alerts link back to the app with ("" = none).
func (s *Service) SetPublicURL(f func(ctx context.Context) string) {
	s.hooks.mu.Lock()
	s.hooks.publicURL = f
	s.hooks.mu.Unlock()
}

// SetPosters wires how library alerts find their posters, and how Plex images are fetched.
// Either may be nil.
func (s *Service) SetPosters(poster PosterFunc, image ImageFunc) {
	s.hooks.mu.Lock()
	s.hooks.poster, s.hooks.image = poster, image
	s.hooks.mu.Unlock()
}

func (s *Service) publicURL(ctx context.Context) string {
	s.hooks.mu.RLock()
	f := s.hooks.publicURL
	s.hooks.mu.RUnlock()
	if f == nil {
		return ""
	}
	return strings.TrimRight(f(ctx), "/")
}

// attachFor is the attachment an event's alert carries, "" for none.
func (s *Service) attachFor(ctx context.Context, key string, d map[string]any) string {
	if d == nil {
		return ""
	}
	if p := str(d, "poster_url"); AllowedAttachURL(p) {
		return p
	}
	kind := ""
	switch key {
	case "movie.imported":
		kind = "movie"
	case "episodes.imported":
		kind = "series"
	case "book.imported":
		kind = "book"
	case eventStreamStarted, eventBuffering:
		thumb := str(d, "thumb")
		if plexImageRe.MatchString(thumb) {
			return plexAttachPrefix + thumb
		}
		// A Jellyfin stream's thumb already carries its prefix ("jellyfin:/Items/…").
		if serverAttach(thumb) { // or an Emby one's ("emby:/Items/…")
			return thumb
		}
		return ""
	default:
		return ""
	}
	id := num(d, "id")
	s.hooks.mu.RLock()
	poster := s.hooks.poster
	s.hooks.mu.RUnlock()
	if poster == nil || id <= 0 {
		return ""
	}
	if u := poster(ctx, kind, id); AllowedAttachURL(u) {
		return u
	}
	return ""
}

// resolveAttach turns a stored attachment into what Apprise is given: the URL as is, or a
// Plex image fetched into a temporary file. done removes that file; call it after the
// attempt, sent or not. Anything that fails leaves the alert without its poster.
func (s *Service) resolveAttach(ctx context.Context, a string) (arg string, done func()) {
	noop := func() {}
	if a == "" || !validAttach(a) {
		return "", noop
	}
	// The image function takes a Plex path as is, and a Jellyfin one with its prefix.
	p, ok := strings.CutPrefix(a, plexAttachPrefix)
	if !ok {
		if !serverAttach(a) {
			return a, noop
		}
		p = a
	}
	file, err := s.fetchPlexImage(ctx, p)
	if err != nil {
		s.log.Debug("notify: couldn't fetch the server's poster for an alert", "err", err)
		return "", noop
	}
	return file, func() {
		if err := os.RemoveAll(filepath.Dir(file)); err != nil {
			s.log.Warn("notify: couldn't remove an alert's poster file", "err", err)
		}
	}
}

// fetchPlexImage saves a Plex image into a fresh temporary folder and returns its path.
func (s *Service) fetchPlexImage(ctx context.Context, path string) (string, error) {
	s.hooks.mu.RLock()
	image := s.hooks.image
	s.hooks.mu.RUnlock()
	if image == nil {
		return "", errors.New("no Plex connection")
	}
	resp, err := image(ctx, path)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("plex answered %d", resp.StatusCode)
	}
	ext := ".jpg"
	if ct, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil {
		switch ct {
		case "image/png":
			ext = ".png"
		case "image/webp":
			ext = ".webp"
		case "image/jpeg", "image/jpg":
		default:
			return "", fmt.Errorf("not an image (%s)", ct)
		}
	}
	dir, err := os.MkdirTemp(s.tempDir, "arrmada-alert-")
	if err != nil {
		return "", err
	}
	file := filepath.Join(dir, "poster"+ext)
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		var n int64
		n, err = io.Copy(f, io.LimitReader(resp.Body, maxPosterBytes+1))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil && n > maxPosterBytes {
			err = errors.New("the image is too large")
		}
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return file, nil
}
