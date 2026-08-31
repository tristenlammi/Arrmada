package automation

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The pairs from a real log, where the seed rule silently stopped applying. In each the
// indexer's listing title and the torrent that actually arrived describe the same thing
// while differing textually — which is exactly what name matching cannot survive.
func TestReleaseIdentityIgnoresListingDifferences(t *testing.T) {
	for _, tc := range []struct{ recorded, torrent, why string }{
		{
			"Pokemon Heroes (2002) 1080p BDRip x265 10bit AC3 5 1 DUAL - Goki",
			"Pokemon Heroes (2002) 1080p BDRip x265 10bit AC3 5.1 - Goki",
			"the listing said DUAL and spelled 5.1 as 5 1",
		},
		{
			"House of the Dragon S03E05 1080p AMZN WEB-DL DDP5 1 Atmos H 264-Draken02",
			"House.of.the.Dragon.S03E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-Draken02.mkv",
			"the listing claimed Atmos; the file is dotted and has an extension",
		},
	} {
		a, b := releaseIdentity(tc.recorded), releaseIdentity(tc.torrent)
		if a == "" || b == "" {
			t.Errorf("%s: identity came back empty (%q / %q)", tc.why, a, b)
			continue
		}
		if a != b {
			t.Errorf("%s:\n  recorded → %q\n  torrent  → %q\nthese are the same release", tc.why, a, b)
		}
	}
}

// The safety property. Pairing writes a hash onto a row that drives REMOVAL, so an
// identity has to distinguish things that genuinely differ — a neighbouring episode
// differs by one character, which is far less than the noise it must tolerate.
func TestReleaseIdentitySeparatesDifferentMedia(t *testing.T) {
	distinct := []string{
		"House of the Dragon S03E05 1080p WEB-DL-GRP",
		"House of the Dragon S03E06 1080p WEB-DL-GRP",
		"House of the Dragon S02E05 1080p WEB-DL-GRP",
		"The Rookie S03E05 1080p WEB-DL-GRP",
		"Pokemon Heroes (2002) 1080p BDRip-Goki",
		"Pokemon Heroes (2003) 1080p BDRip-Goki",
	}
	seen := map[string]string{}
	for _, name := range distinct {
		id := releaseIdentity(name)
		if id == "" {
			t.Errorf("%q produced no identity", name)
			continue
		}
		if prev, dup := seen[id]; dup {
			t.Errorf("%q and %q share identity %q — pairing could cross them", prev, name, id)
		}
		seen[id] = name
	}
}

// A name with nothing to pin it down must refuse to pair. Matching on a bare show title
// would let any release of that show adopt any other's hash.
func TestReleaseIdentityRefusesUnidentifiableNames(t *testing.T) {
	for _, name := range []string{
		"House of the Dragon 1080p WEB-DL-GRP", // no season, episode or year
		"1080p WEB-DL-GRP",                     // no title at all
		"",
	} {
		if id := releaseIdentity(name); id != "" {
			t.Errorf("%q produced identity %q, but there's nothing here to pair on", name, id)
		}
	}
}

// Absolute-numbered anime pairs on its number, and two episodes of it must not collide.
func TestReleaseIdentityHandlesAbsoluteNumbering(t *testing.T) {
	a := releaseIdentity("[SubsPlease] Bleach - Sennen Kessen Hen - 46 (1080p) [8B5C54DB]")
	b := releaseIdentity("[SubsPlease] Bleach - Sennen Kessen Hen - 45 (1080p) [ABCD1234]")
	if a == "" || b == "" {
		t.Fatalf("absolute-numbered releases produced no identity (%q / %q)", a, b)
	}
	if a == b {
		t.Errorf("episodes 45 and 46 share identity %q", a)
	}
}

func adoptFixture(t *testing.T) (*Coordinator, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Coordinator{db: st.DB(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}, context.Background()
}

func seedGrab(t *testing.T, c *Coordinator, ctx context.Context, title, hash string) int64 {
	t.Helper()
	res, err := c.db.ExecContext(ctx,
		`INSERT INTO grabs (movie_id, title, info_hash, status, seed_enabled, media_type)
		 VALUES (1, ?, ?, 'imported', 1, 'series')`, title, hash)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func hashOf(t *testing.T, c *Coordinator, ctx context.Context, id int64) string {
	t.Helper()
	var h string
	if err := c.db.QueryRowContext(ctx, `SELECT info_hash FROM grabs WHERE id = ?`, id).Scan(&h); err != nil {
		t.Fatal(err)
	}
	return h
}

// The repair itself: a grab recorded under the indexer's listing title adopts the hash
// of the torrent that actually arrived, so every later pass matches exactly.
func TestAdoptRecordsTheHashOnce(t *testing.T) {
	c, ctx := adoptFixture(t)
	id := seedGrab(t, c, ctx, "House of the Dragon S03E05 1080p AMZN WEB-DL DDP5 1 Atmos H 264-Draken02", "")

	n := c.AdoptTorrentHashes(ctx, []download.Item{{
		Hash: "ABC123", Name: "House.of.the.Dragon.S03E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-Draken02.mkv",
	}})
	if n != 1 {
		t.Fatalf("adopted %d, want 1", n)
	}
	if got := hashOf(t, c, ctx, id); got != "ABC123" {
		t.Errorf("hash = %q, want it recorded", got)
	}
}

// An existing hash is never overwritten: it is the reliable identity, and replacing it
// on a name-based guess would aim a removal rule at the wrong torrent.
func TestAdoptNeverOverwritesAKnownHash(t *testing.T) {
	c, ctx := adoptFixture(t)
	id := seedGrab(t, c, ctx, "House of the Dragon S03E05 1080p AMZN WEB-DL Atmos-Draken02", "GOODHASH")

	c.AdoptTorrentHashes(ctx, []download.Item{{
		Hash: "OTHERHASH", Name: "House.of.the.Dragon.S03E05.1080p.AMZN.WEB-DL.H.264-Draken02.mkv",
	}})
	if got := hashOf(t, c, ctx, id); got != "GOODHASH" {
		t.Errorf("hash = %q, want the original kept", got)
	}
}

// Two grabs of the same episode is exactly when guessing does damage — refuse, and leave
// the diagnostic to report it.
func TestAdoptRefusesAnAmbiguousPairing(t *testing.T) {
	c, ctx := adoptFixture(t)
	a := seedGrab(t, c, ctx, "House of the Dragon S03E05 1080p AMZN WEB-DL Atmos-Draken02", "")
	b := seedGrab(t, c, ctx, "House of the Dragon S03E05 2160p HMAX WEB-DL DV HDR-FLUX", "")

	if n := c.AdoptTorrentHashes(ctx, []download.Item{{
		Hash: "ABC123", Name: "House.of.the.Dragon.S03E05.1080p.AMZN.WEB-DL.H.264-Draken02.mkv",
	}}); n != 0 {
		t.Errorf("adopted %d, want 0 — two grabs claim this episode", n)
	}
	if hashOf(t, c, ctx, a) != "" || hashOf(t, c, ctx, b) != "" {
		t.Error("a hash was written despite the ambiguity")
	}
}

// A torrent that already matches by name needs no repair, and a different show must
// never be adopted.
func TestAdoptLeavesMatchedAndUnrelatedTorrentsAlone(t *testing.T) {
	c, ctx := adoptFixture(t)
	exact := seedGrab(t, c, ctx, "The Rookie S03E05 1080p WEB-DL-GRP", "")
	other := seedGrab(t, c, ctx, "Pokemon Heroes (2002) 1080p BDRip-Goki", "")

	c.AdoptTorrentHashes(ctx, []download.Item{
		{Hash: "SAMEHASH", Name: "The Rookie S03E05 1080p WEB-DL-GRP"},      // matches by name already
		{Hash: "STRANGER", Name: "Some Other Show S01E01 1080p WEB-DL-GRP"}, // nothing to pair with
	})
	if got := hashOf(t, c, ctx, exact); got != "" {
		t.Errorf("a name-matched grab was rewritten to %q — there was nothing to repair", got)
	}
	if got := hashOf(t, c, ctx, other); got != "" {
		t.Errorf("an unrelated grab adopted %q", got)
	}
}
