package automation

import (
	"context"
	"fmt"
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// Grabs recorded before migration 0062 carry no info hash, so they still match their
// torrent by name — which is exactly the comparison that fails when an indexer's listing
// title differs from the .torrent's own name. Those torrents stay unmanaged forever:
// no seed rule, and stall detection unable to see them in the queue.
//
// BackfillGrabHashes repairs them once, by matching on what the two names AGREE about
// rather than on the strings themselves. Both sides parse to the same show, season,
// episodes, release group and resolution even when the rendering differs wildly:
//
//	Peppa Pig 2004 S08 1080p WEBRip DD+ 2 0 x265-iVy
//	Peppa Pig (2004) S08 1080p WEBRip 10bit EAC3 2 0 x265-iVy
//
// Deliberately conservative. Writing the wrong hash onto a grab is worse than leaving it
// blank — it would apply another release's seed policy and point stall detection at the
// wrong torrent — so a fingerprint is only accepted when EXACTLY one grab and EXACTLY one
// torrent share it. Anything ambiguous is left alone and counted.
func (c *Coordinator) BackfillGrabHashes(ctx context.Context) (filled, ambiguous int) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, title FROM grabs
		 WHERE info_hash = '' AND status IN ('grabbed', 'imported')`)
	if err != nil {
		return 0, 0
	}
	type pending struct {
		id    int64
		title string
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if rows.Scan(&p.id, &p.title) == nil {
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

	// Group both sides by fingerprint. A fingerprint shared by more than one grab (a
	// release grabbed twice) or more than one torrent can't be resolved safely.
	grabsBy := map[string][]pending{}
	for _, p := range todo {
		if fp := releaseFingerprint(p.title); fp != "" {
			grabsBy[fp] = append(grabsBy[fp], p)
		}
	}
	torrentsBy := map[string][]string{} // fingerprint -> hashes
	for _, it := range queue {
		if fp := releaseFingerprint(it.Name); fp != "" {
			torrentsBy[fp] = append(torrentsBy[fp], it.Hash)
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
		hashes := torrentsBy[fp]
		if len(gs) != 1 || len(hashes) != 1 {
			if len(hashes) > 0 {
				ambiguous++
				c.log.Info("grab backfill: skipping an ambiguous match",
					"grabs", len(gs), "torrents", len(hashes), "release", gs[0].title)
			}
			continue
		}
		hash := strings.ToLower(hashes[0])
		if hash == "" || taken[hash] {
			continue
		}
		if _, err := c.db.ExecContext(ctx, `UPDATE grabs SET info_hash = ? WHERE id = ?`, hash, gs[0].id); err != nil {
			c.log.Warn("grab backfill: update failed", "release", gs[0].title, "err", err)
			continue
		}
		taken[hash] = true
		filled++
		c.log.Info("grab backfill: linked a torrent to its grab", "release", gs[0].title, "hash", hash)
	}
	return filled, ambiguous
}

// releaseFingerprint reduces a release name to the attributes both an indexer listing and
// the torrent's own name agree on, even when they render them differently. Returns "" when
// the name is too vague to identify anything (no season and no episode), since matching on
// title alone would be a coin flip between a show's releases.
func releaseFingerprint(name string) string {
	r := parser.Parse(strings.TrimSuffix(name, ".mkv"))
	title := titleKey(r.Title)
	if title == "" {
		return ""
	}
	// Season 0 with no episodes means we couldn't place it at all.
	if r.Season == 0 && len(r.Episodes) == 0 && len(r.AbsoluteEpisodes) == 0 {
		return ""
	}
	eps := make([]string, 0, len(r.Episodes))
	for _, e := range r.Episodes {
		eps = append(eps, fmt.Sprint(e))
	}
	abs := make([]string, 0, len(r.AbsoluteEpisodes))
	for _, e := range r.AbsoluteEpisodes {
		abs = append(abs, fmt.Sprint(e))
	}
	return strings.Join([]string{
		title,
		fmt.Sprint(r.Season),
		strings.Join(eps, "-"),
		strings.Join(abs, "-"),
		strings.ToLower(r.Group),
		string(r.Resolution),
	}, "|")
}
