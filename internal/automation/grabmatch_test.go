package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/download"
)

// The real strings from the wild. An indexer's LISTING title is frequently a prettified
// rendering of the actual torrent — "EAC3" written as "DD+", "10bit" dropped, episode
// titles omitted — so no amount of normalization reconciles the two. Matching on names
// silently failed for entire trackers: seed rules never applied, stall detection saw the
// download as missing, and the Seeding tab called them unmanaged.
var namingDivergence = []struct {
	recorded string // as the indexer listed it, stored on the grab
	client   string // as the download client names it, from the .torrent itself
}{
	{
		"Peppa Pig 2004 S08 1080p WEBRip DD+ 2 0 x265-iVy",
		"Peppa Pig (2004) S08 1080p WEBRip 10bit EAC3 2 0 x265-iVy",
	},
	{
		"Teen Titans Go S09E33 1080p iT WEB-DL AAC 2.0 H.264-NTb",
		"Teen.Titans.Go!.S09E33.Rulers.Rule.1080p.iT.WEB-DL.AAC2.0.H.264-NTb.mkv",
	},
	{
		"You and I Are Polar Opposites  S01 1080p CR WEB-DL Dual-Audio DD+ 2 0 H 264-Kitsune",
		"You.and.I.Are.Polar.Opposites.S01.1080p.CR.WEB-DL.DUAL.DDP2.0.H.264-Kitsune",
	},
	{
		"Ink Master 2012 S05 1080p WEBRip DD+ 2 0 x265-iVy",
		"Ink Master (2012) S05 1080p WEBRip 10bit EAC3 2 0 x265-iVy",
	},
}

// First, pin the premise: these names genuinely do NOT normalize to the same key. If a
// future normalization change made them match, the hash would still be correct — but this
// test documents why the hash was needed.
func TestListingTitlesDoNotNormalizeToTorrentNames(t *testing.T) {
	for _, c := range namingDivergence {
		if normRelease(c.recorded) == normRelease(c.client) {
			t.Errorf("these now normalize alike, so this case no longer demonstrates the problem:\n  %q\n  %q", c.recorded, c.client)
		}
	}
}

// With the info hash recorded, the torrent matches its grab regardless of naming.
func TestMatchGrabByInfoHash(t *testing.T) {
	const hash = "c12fe1c06bba254a9dc9f519b335aa7c1367a88a"
	for _, c := range namingDivergence {
		grabs := []grab{{ID: 1, Title: c.recorded, InfoHash: hash}}

		if g := matchGrab(grabs, hash, c.client); g == nil {
			t.Errorf("hash match failed for %q", c.client)
		}
		// The client reports lowercase; a tracker may have given us uppercase.
		if g := matchGrab(grabs, "C12FE1C06BBA254A9DC9F519B335AA7C1367A88A", c.client); g == nil {
			t.Errorf("hash match should be case-insensitive for %q", c.client)
		}
		// Without the hash there's nothing to match on — the old broken behaviour.
		if g := matchGrab(grabs, "", c.client); g != nil {
			t.Errorf("name-only match unexpectedly succeeded for %q — premise changed", c.client)
		}
	}
}

// Rows predating migration 0062 have no hash and must keep matching by name, or the fix
// would break every existing grab on upgrade.
func TestMatchGrabFallsBackToNameForOldRows(t *testing.T) {
	grabs := []grab{{ID: 7, Title: "Some Release 1080p WEB-DL-GRP", InfoHash: ""}}

	if g := matchGrab(grabs, "deadbeef", "Some.Release.1080p.WEB-DL-GRP.mkv"); g == nil || g.ID != 7 {
		t.Error("a pre-0062 row should still match by name when the hash finds nothing")
	}
	if g := matchGrab(grabs, "", "Something Else Entirely"); g != nil {
		t.Error("an unrelated name must not match")
	}
}

// A hash must never match the WRONG grab just because another row shares a name shape.
func TestMatchGrabPrefersHashOverName(t *testing.T) {
	grabs := []grab{
		{ID: 1, Title: "Show S01 1080p WEB-DL-GRP", InfoHash: "aaaa"},
		{ID: 2, Title: "Show S01 1080p WEB-DL-GRP", InfoHash: "bbbb"}, // a re-grab of the same release
	}
	g := matchGrab(grabs, "bbbb", "Show.S01.1080p.WEB-DL-GRP")
	if g == nil || g.ID != 2 {
		t.Errorf("hash should select the exact row, got %+v", g)
	}
}

// findQueued backs stall detection, which blocklists a release it can't find in the
// queue. A name mismatch there condemns a torrent that is downloading perfectly well, so
// it has to match by hash too.
func TestFindQueuedMatchesByHash(t *testing.T) {
	for _, c := range namingDivergence {
		const hash = "c12fe1c06bba254a9dc9f519b335aa7c1367a88a"
		queue := []download.Item{{Hash: hash, Name: c.client}}

		if _, found := findQueued(queue, grab{Title: c.recorded, InfoHash: hash}); !found {
			t.Errorf("stall detection would wrongly blocklist %q", c.recorded)
		}
		// Pre-0062 row: name fallback still applies, and here it genuinely can't match —
		// which is the old behaviour, not a regression.
		if _, found := findQueued(queue, grab{Title: c.client, InfoHash: ""}); !found {
			t.Errorf("name fallback should still work when the names DO agree: %q", c.client)
		}
	}
}

// The backfill matches on what two differently-rendered names AGREE about — show,
// season, episodes, group, resolution — rather than on the strings themselves. Every
// real-world pair must fingerprint identically, or the repair can't find them.
func TestFingerprintBridgesTheNamingDivergence(t *testing.T) {
	for _, c := range namingDivergence {
		got, want := releaseFingerprint(c.client), releaseFingerprint(c.recorded)
		if want == "" {
			t.Errorf("recorded title produced no fingerprint: %q", c.recorded)
			continue
		}
		if got != want {
			t.Errorf("fingerprints differ, backfill would miss this pair:\n  recorded %q -> %q\n  client   %q -> %q",
				c.recorded, want, c.client, got)
		}
	}
}

// Different releases must NOT collide. Writing the wrong hash onto a grab is worse than
// leaving it blank: it applies another release's seed policy and points stall detection
// at the wrong torrent.
func TestFingerprintSeparatesDifferentReleases(t *testing.T) {
	distinct := []string{
		"Peppa Pig (2004) S08 1080p WEBRip 10bit EAC3 2 0 x265-iVy",
		"Peppa Pig (2004) S05 1080p WEBRip 10bit EAC3 2 0 x265-iVy", // different season
		"Teen.Titans.Go!.S09E33.Rulers.Rule.1080p.iT.WEB-DL.AAC2.0.H.264-NTb.mkv",
		"Teen.Titans.Go!.S09E26.Activate.1080p.iT.WEB-DL.AAC2.0.H.264-NTb.mkv", // different episode
		"Ink Master (2012) S05 1080p WEBRip 10bit EAC3 2 0 x265-iVy",           // different show
		"Ink Master (2012) S05 720p WEBRip 10bit EAC3 2 0 x265-iVy",            // different resolution
	}
	seen := map[string]string{}
	for _, name := range distinct {
		fp := releaseFingerprint(name)
		if prev, dup := seen[fp]; dup {
			t.Errorf("these fingerprint alike and could be cross-linked:\n  %q\n  %q", prev, name)
		}
		seen[fp] = name
	}
}

// A name too vague to identify anything must produce no fingerprint at all, rather than
// one that matches every other vague name.
func TestFingerprintRejectsUnidentifiableNames(t *testing.T) {
	for _, name := range []string{"", "random-download", "Some Movie 2019 1080p BluRay x264-GRP"} {
		if fp := releaseFingerprint(name); fp != "" {
			t.Errorf("releaseFingerprint(%q) = %q, want empty — it identifies no episode", name, fp)
		}
	}
}

// The release group is checked separately from the fingerprint, because a name ending in
// a parenthetical hides it entirely — and that's true of exactly the releases this repair
// exists for. Requiring an exact group match would strand them.
func TestReleaseGroupIsAbsentWhenNameEndsInParenthesis(t *testing.T) {
	hidden := "Peppa Pig (2004) S09 (1080p MY5 WEB-DL H264 SDR AAC 2.0 English - HONE)"
	if g := releaseGroup(hidden); g != "" {
		t.Errorf("releaseGroup(%q) = %q — the premise for the wildcard has changed", hidden, g)
	}
	if g := releaseGroup("Peppa Pig (2004) S08 1080p WEBRip 10bit EAC3 2 0 x265-iVy"); g != "ivy" {
		t.Errorf("releaseGroup = %q, want ivy", g)
	}
	// The hidden-group name must still fingerprint alike to its listing form, so the
	// group wildcard gets the chance to apply.
	listing := "Peppa Pig 2004 S09 1080p MY5 WEB-DL H264 SDR AAC 2 0 English-HONE"
	if releaseFingerprint(hidden) != releaseFingerprint(listing) {
		t.Errorf("fingerprints differ:\n  %q -> %q\n  %q -> %q",
			hidden, releaseFingerprint(hidden), listing, releaseFingerprint(listing))
	}
}

// A multi-season pack identifies real seasons and must fingerprint, not be discarded as
// too vague — "Season 1-3 COMPLETE" was being skipped entirely.
func TestFingerprintHandlesMultiSeasonPacks(t *testing.T) {
	name := "Smiling Friends (2020) Season 1-3 COMPLETE.1080p.H265.EAC3.6CH-MNKYDDL"
	fp := releaseFingerprint(name)
	if fp == "" {
		t.Fatalf("releaseFingerprint(%q) = empty — a season range identifies real seasons", name)
	}
	// It must not collide with a single-season pack of the same show.
	if single := releaseFingerprint("Smiling Friends (2020) S01 1080p H265 EAC3-MNKYDDL"); fp == single {
		t.Error("a 1-3 pack and a season 1 pack must not fingerprint alike")
	}
}

// Duplicate grab rows for one release are the NORMAL case here — the duplicate-grab bug
// recorded each pack twice, and treating that as ambiguous is what blocked the repair for
// nearly every torrent. They describe one release with one seed policy.
func TestSameReleaseAcceptsDuplicateRowsButNotDifferentReleases(t *testing.T) {
	dupes := []pendingGrab{
		{id: 1, title: "Teen Titans Go S09E33 1080p iT WEB-DL AAC 2.0 H.264-NTb"},
		{id: 2, title: "Teen Titans Go S09E33 1080p iT WEB-DL AAC 2.0 H.264-NTb"},
	}
	if !sameRelease(dupes) {
		t.Error("identical rows describe one release and must be linkable")
	}

	different := []pendingGrab{
		{id: 1, title: "Ink Master 2012 S05 1080p WEBRip DD+ 2 0 x265-iVy"},
		{id: 2, title: "Ink Master 2012 S05 1080p WEBRip DD+ 2 0 x265-OTHER"},
	}
	if sameRelease(different) {
		t.Error("two different releases must never be collapsed onto one torrent")
	}
	if !sameRelease(nil) || !sameRelease(different[:1]) {
		t.Error("zero or one row is trivially unambiguous")
	}
}
