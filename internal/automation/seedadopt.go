package automation

import (
	"context"
	"strings"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/parser"
)

// Seed rules are matched to torrents by info hash, falling back to the release name.
// The fallback is where it breaks: an indexer's LISTING title is often a prettified
// rendering of the actual torrent, so a grab recorded as
//
//	Pokemon Heroes (2002) 1080p BDRip x265 10bit AC3 5 1 DUAL - Goki
//
// never matches the torrent that arrives as
//
//	Pokemon Heroes (2002) 1080p BDRip x265 10bit AC3 5.1 - Goki
//
// and the seed rule silently doesn't apply. Rows written before migration 0062 have no
// hash at all, so for those the broken name comparison is the ONLY thing available.
//
// The repair is to pair them once and write the hash down, after which matching is exact
// forever. Pairing is done on parsed IDENTITY — title plus season/episode or year —
// rather than string similarity, because "AC3 5 1 DUAL" vs "AC3 5.1" is a large textual
// difference describing the same thing, while two different episodes of one show differ
// by a single character.

// AdoptTorrentHashes pairs hash-less grabs with the torrents they produced and records
// the hash. Returns how many it repaired.
//
// Deliberately conservative — it writes to grab rows that drive removal decisions:
//   - only fills a hash that is empty; an existing one is never overwritten
//   - only when exactly ONE grab matches the torrent's identity, so an ambiguous pairing
//     is left alone rather than guessed at
//   - only when that hash isn't already claimed by another grab
func (c *Coordinator) AdoptTorrentHashes(ctx context.Context, queue []download.Item) int {
	grabs, err := c.liveGrabs(ctx)
	if err != nil || len(grabs) == 0 {
		return 0
	}

	taken := make(map[string]bool, len(grabs))
	for _, g := range grabs {
		if g.InfoHash != "" {
			taken[strings.ToLower(g.InfoHash)] = true
		}
	}

	adopted := 0
	for _, it := range queue {
		hash := strings.ToLower(strings.TrimSpace(it.Hash))
		if hash == "" || taken[hash] {
			continue // no hash to record, or some grab already owns it
		}
		if matchGrab(grabs, it.Hash, it.Name) != nil {
			continue // already matches by hash or name — nothing to repair
		}
		want := releaseIdentity(it.Name)
		if want == "" {
			continue // nothing identifiable in the torrent's name
		}

		var found *grab
		for i := range grabs {
			if grabs[i].InfoHash != "" {
				continue
			}
			if releaseIdentity(grabs[i].Title) != want {
				continue
			}
			if found != nil {
				found = nil // two candidates: refuse rather than pick
				break
			}
			found = &grabs[i]
		}
		if found == nil {
			continue
		}
		if err := c.setGrabInfoHash(ctx, found.ID, it.Hash); err != nil {
			c.log.Warn("seeding: could not record a torrent's hash on its grab",
				"grab", found.ID, "torrent", it.Name, "err", err)
			continue
		}
		c.log.Info("seeding: paired a torrent with its grab and recorded the hash",
			"torrent", it.Name, "recorded_title", found.Title, "identity", want, "hash", it.Hash)
		found.InfoHash = it.Hash
		taken[hash] = true
		adopted++
	}
	return adopted
}

// releaseIdentity reduces a release name to what actually identifies the media: the
// title, plus the season/episode for TV or the year for a film. Empty when nothing
// identifiable can be read, which refuses the pairing rather than matching on a blank.
//
// This is the whole point of the repair. Quality and audio tokens — the parts that
// differ between an indexer's listing and the real torrent — are excluded, while the
// parts that distinguish one episode from another are required.
func releaseIdentity(name string) string {
	p := parser.Parse(name)
	key := parser.TitleKey(p.Title)
	if key == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(key)
	switch {
	case p.Season > 0 && len(p.Episodes) > 0:
		b.WriteString("|s")
		b.WriteString(itoa(p.Season))
		for _, e := range p.Episodes {
			b.WriteString("e")
			b.WriteString(itoa(e))
		}
	case p.Season > 0:
		b.WriteString("|s")
		b.WriteString(itoa(p.Season))
	case len(p.AbsoluteEpisodes) > 0:
		for _, e := range p.AbsoluteEpisodes {
			b.WriteString("|a")
			b.WriteString(itoa(e))
		}
	case p.Year > 0:
		b.WriteString("|y")
		b.WriteString(itoa(p.Year))
	default:
		// A bare title with no season, episode or year identifies too little to pair on
		// — every release of that show would look the same.
		return ""
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d [12]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d[i:])
}
