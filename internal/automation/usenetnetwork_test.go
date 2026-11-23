package automation

import (
	"testing"
)

// Blocklist rows and history lines about a release grabbed from Usenet say so; a
// torrent's don't, nor does the same title under another item's history.
func TestUsenetNetworkMarks(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Rare Film", 1971)
	const nzb = "Rare.Film.1971.1080p.BluRay.x264-NZB"
	const tor = "Rare.Film.1971.1080p.WEB-DL.x264-TOR"
	for _, g := range [][2]string{{nzb, "usenet"}, {tor, "torrent"}} {
		if _, err := h.c.db.Exec(`INSERT INTO grabs (movie_id, title, indexer, media_type, info_hash, transport, status) VALUES (?, ?, 'x', 'movie', '', ?, 'failed')`, mid, g[0], g[1]); err != nil {
			t.Fatal(err)
		}
		if err := h.c.addBlock(h.ctx, mid, g[0], "x", "", "test"); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := h.c.Blocklisted(h.ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range entries {
		got[e.Title] = e.Network
	}
	if got[nzb] != "usenet" || got[tor] != "" {
		t.Errorf("movie blocklist networks = %v", got)
	}
	pg, err := h.c.ListAllBlocks(h.ctx, BlockFilter{})
	if err != nil {
		t.Fatal(err)
	}
	rows := pg.Items
	got = map[string]string{}
	for _, r := range rows {
		got[r.Title] = r.Network
	}
	if got[nzb] != "usenet" || got[tor] != "" {
		t.Errorf("blocklist page networks = %v", got)
	}

	u, err := h.c.UsenetReleases(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		kind string
		id   int64
		text string
		want bool
	}{
		{"movie", mid, nzb + " failed in SABnzbd (articles are missing from your provider) — blocklisted, nothing else found yet", true},
		{"movie", mid, "Uploaded NZB — " + nzb, true},
		{"movie", mid, tor, false},
		{"movie", mid + 1, nzb, false},
		{"series", mid, nzb, false},
	} {
		if got := u.Mentions(c.kind, c.id, c.text); got != c.want {
			t.Errorf("Mentions(%s %d, %q) = %v", c.kind, c.id, c.text, got)
		}
	}
}
