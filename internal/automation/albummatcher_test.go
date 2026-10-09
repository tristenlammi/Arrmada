package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/music"
)

// ACQ-17: the Downloads feed resolves a music torrent to its album's artist (for the
// profile), reading the library once however many torrents it labels.
func TestAlbumMatcher(t *testing.T) {
	h := newStallHarness(t)
	if _, _, ok := h.c.AlbumMatcher(h.ctx)("Radiohead - Kid A (2000) [FLAC]"); ok {
		t.Fatal("matched with music off")
	}
	h.c.music = music.NewService(h.c.db, nil, h.c.log)
	mustExec(t, h.c, `INSERT INTO artists (id, mbid, name, quality_profile) VALUES (1, 'a1', 'Radiohead', 'custom:7')`)
	mustExec(t, h.c, `INSERT INTO albums (id, artist_id, mbid, title, year) VALUES (1, 1, 'r1', 'Kid A', 2000)`)
	match := h.c.AlbumMatcher(h.ctx)
	al, ar, ok := match("Radiohead - Kid A (2000) [FLAC]")
	if !ok || al.Title != "Kid A" || ar.QualityProfile != "custom:7" {
		t.Errorf("match = (%+v, %+v, %v)", al, ar, ok)
	}
	if _, _, ok := match("Inception.2010.1080p.BluRay.x264-GRP"); ok {
		t.Error("a film matched an album")
	}
}
