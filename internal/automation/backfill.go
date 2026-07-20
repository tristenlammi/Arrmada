package automation

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// Grabs recorded before migration 0062 carry no info hash, so they still match their
// torrent by name — which is exactly the comparison that fails when an indexer's listing
// title differs from the .torrent's own name. Those torrents stay unmanaged forever:
// no seed rule, and stall detection unable to see them in the queue.
//
// BackfillGrabHashes repairs them once, by matching on what the two names AGREE about
// rather than on the strings themselves. Both sides parse to the same show, season and
// episodes even when the rendering differs wildly:
//
//	Peppa Pig 2004 S08 1080p WEBRip DD+ 2 0 x265-iVy
//	Peppa Pig (2004) S08 1080p WEBRip 10bit EAC3 2 0 x265-iVy
//
// Writing the WRONG hash is worse than leaving it blank — it would apply another
// release's seed policy and point stall detection at the wrong torrent — so a link is
// only made when one torrent is the unambiguous counterpart of one release.
func (c *Coordinator) BackfillGrabHashes(ctx context.Context) (filled, ambiguous int) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, title FROM grabs
		 WHERE info_hash = '' AND status IN ('grabbed', 'imported')
		 ORDER BY id DESC`)
	if err != nil {
		return 0, 0
	}
	var todo []pendingGrab
	for rows.Next() {
		var p pendingGrab
		if rows.Scan(&p.id, &p.title) == nil {
			p.group = releaseGroup(p.title)
			todo = append(todo, p)
		}
	}
	rows.Close()
	if len(todo) == 0 {
		return 0, 0
	}

	queue, err := c.downloads.Queue(ctx)
	if err != nil || len(queue) == 0 {
		return 0, 0
	}

	type candidate struct {
		hash  string
		group string
	}
	grabsBy := map[string][]pendingGrab{}
	for _, p := range todo {
		if fp := releaseFingerprint(p.title); fp != "" {
			grabsBy[fp] = append(grabsBy[fp], p)
		}
	}
	torrentsBy := map[string][]candidate{}
	for _, it := range queue {
		if fp := releaseFingerprint(it.Name); fp != "" {
			torrentsBy[fp] = append(torrentsBy[fp], candidate{hash: it.Hash, group: releaseGroup(it.Name)})
		}
	}

	// A hash already recorded on another grab must not be reused.
	taken := map[string]bool{}
	if hr, err := c.db.QueryContext(ctx, `SELECT info_hash FROM grabs WHERE info_hash != ''`); err == nil {
		for hr.Next() {
			var h string
			if hr.Scan(&h) == nil {
				taken[strings.ToLower(h)] = true
			}
		}
		hr.Close()
	}

	for fp, gs := range grabsBy {
		ts := torrentsBy[fp]
		if len(ts) == 0 {
			continue // nothing in the client for this release
		}
		if len(ts) > 1 {
			// Two torrents of the same release; we can't tell which grab is which.
			ambiguous++
			c.log.Info("grab backfill: skipping — more than one torrent matches",
				"release", gs[0].title, "torrents", len(ts))
			continue
		}
		hash := strings.ToLower(ts[0].hash)
		if hash == "" || taken[hash] {
			continue
		}

		// Keep only grabs whose release group is compatible with the torrent's. Empty on
		// either side is a wildcard: a name ending in a parenthetical ("… English - HONE)")
		// yields no group at all, so requiring an exact match would strand it.
		var fit []pendingGrab
		for _, g := range gs {
			if g.group == "" || ts[0].group == "" || g.group == ts[0].group {
				fit = append(fit, g)
			}
		}
		if len(fit) == 0 {
			continue
		}

		// Several rows for the SAME release is the normal case, not an ambiguity — the
		// duplicate-grab bug recorded each pack twice. They describe one release with one
		// seed policy, so linking the newest is correct. Rows for DIFFERENT releases that
		// merely share a fingerprint are the dangerous case, and are left alone.
		if !sameRelease(fit) {
			ambiguous++
			titles := make([]string, 0, len(fit))
			for _, g := range fit {
				titles = append(titles, g.title)
			}
			c.log.Info("grab backfill: skipping — one torrent, several different releases",
				"torrent_group", ts[0].group, "releases", strings.Join(titles, " | "))
			continue
		}
		sort.Slice(fit, func(i, j int) bool { return fit[i].id > fit[j].id })
		newest := fit[0]

		if _, err := c.db.ExecContext(ctx, `UPDATE grabs SET info_hash = ? WHERE id = ?`, hash, newest.id); err != nil {
			c.log.Warn("grab backfill: update failed", "release", newest.title, "err", err)
			continue
		}
		taken[hash] = true
		filled++
		c.log.Info("grab backfill: linked a torrent to its grab",
			"release", newest.title, "hash", hash, "duplicate_rows", len(fit)-1)
	}
	return filled, ambiguous
}

// pendingGrab is a grab row awaiting an info hash.
type pendingGrab struct {
	id    int64
	title string
	group string
}

// sameRelease reports whether every grab describes the same release, by normalized title.
func sameRelease(gs []pendingGrab) bool {
	if len(gs) < 2 {
		return true
	}
	first := normRelease(gs[0].title)
	for _, g := range gs[1:] {
		if normRelease(g.title) != first {
			return false
		}
	}
	return true
}

// releaseGroup returns the parsed release group, lowercased ("" when the name doesn't
// end in one — a trailing parenthetical hides it).
func releaseGroup(name string) string {
	return strings.ToLower(parser.Parse(strings.TrimSuffix(name, ".mkv")).Group)
}

// releaseFingerprint reduces a release name to the attributes both an indexer listing and
// the torrent's own name agree on, even when they render them differently. Returns "" when
// the name is too vague to identify anything, since matching on title alone would be a
// coin flip between a show's releases.
//
// The release group is deliberately NOT part of it: a name ending in a parenthetical
// ("… AAC 2.0 English - HONE)") parses to no group at all, so including it would strand
// exactly the releases this repair exists for. Group is checked separately, as a
// compatibility test that treats "unknown" as a wildcard.
func releaseFingerprint(name string) string {
	r := parser.Parse(strings.TrimSuffix(name, ".mkv"))
	title := titleKey(r.Title)
	if title == "" {
		return ""
	}
	if r.Season == 0 && len(r.Seasons) == 0 && len(r.Episodes) == 0 && len(r.AbsoluteEpisodes) == 0 {
		return "" // identifies no season or episode — too vague to match on
	}
	return strings.Join([]string{
		title,
		fmt.Sprint(r.Season),
		joinInts(r.Seasons), // multi-season packs ("Season 1-3 COMPLETE")
		joinInts(r.Episodes),
		joinInts(r.AbsoluteEpisodes),
		string(r.Resolution),
	}, "|")
}

func joinInts(ns []int) string {
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		parts = append(parts, fmt.Sprint(n))
	}
	return strings.Join(parts, "-")
}
