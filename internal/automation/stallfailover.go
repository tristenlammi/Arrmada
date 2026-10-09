package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/series"
)

// maxStallFailoversPerCheck caps how many stalled grabs one DetectStalled pass acts on,
// across every media type. Each costs an indexer search, and when the stall timeout is
// switched on for a library full of long-dead grabs they must not all go in one tick; the
// rest are picked up two minutes later.
const maxStallFailoversPerCheck = 3

// stallTick is one DetectStalled pass's fail-over budget.
type stallTick struct {
	left     int // fail-overs this pass may still perform
	deferred int // stalled grabs left for the next pass because the budget ran out
}

// take spends one fail-over from the budget, or counts the grab as deferred when there's
// none left. A nil tick is unlimited.
func (t *stallTick) take() bool {
	if t == nil {
		return true
	}
	if t.left <= 0 {
		t.deferred++
		return false
	}
	t.left--
	return true
}

// stallTarget is what a fail-over needs to know about the title a stalled grab was for.
// Each media type fills it in; failOver holds the logic they share.
type stallTarget struct {
	kind string // movie | series | book | music, as published on download.stalled
	id   int64  // the title's id (the album's, for music)
	name string // the title's display name
	// block blocklists the stalled release for this title with the given reason.
	block func(ctx context.Context, reason string) error
	// event writes a line to the title's history.
	event func(ctx context.Context, detail string)
	// replace searches for and grabs a different release covering what the stalled one
	// was for, never one whose normalized title is in exclude. It returns what it
	// grabbed ("" for nothing) and the search's error when the search couldn't run.
	replace func(ctx context.Context, exclude map[string]bool) (string, error)
}

// errSearchUnavailable marks a replacement search that couldn't run for a reason the
// indexer package doesn't report as an outage (music folds its search errors into an
// outcome code). failOver treats it like indexer.IsOutage.
var errSearchUnavailable = errors.New("search unavailable")

// judgeStall is the stall check every media type shares once its own "has it landed?"
// test has passed: is fail-over on for this grab, has the window elapsed, and is the
// torrent gone or stuck? target is only built when the answer is yes, so a healthy queue
// costs no extra lookups.
func (c *Coordinator) judgeStall(ctx context.Context, g grab, queue []download.Item, tick *stallTick, target func(ctx context.Context) (stallTarget, bool)) {
	if g.StallMinutes <= 0 {
		return // fail-over off for this grab's profile
	}
	window := time.Duration(g.StallMinutes) * time.Minute
	if time.Since(parseTime(g.GrabbedAt)) < window {
		return
	}
	item, found := findQueued(queue, g)
	if !c.stalledInQueue(g, item, found, window) {
		return
	}
	// The last attempt found nothing to replace it with. Try again a window later, not
	// every tick: a torrent in a hard error state reads as stalled on every pass, and each
	// attempt is a full indexer search.
	if found && c.waitingOut(g.ID, window) {
		return
	}
	t, ok := target(ctx)
	if !ok {
		return
	}
	if !tick.take() {
		return
	}
	c.failOver(ctx, g, item, found, window, t)
}

// failOver acts on a grab judged stalled. The rule is that a release still in the client
// is only removed once something else has been grabbed in its place.
//
// It used to blocklist the release, delete the torrent and its data, and only then search.
// For rare content with one intermittent seeder that destroyed the only copy even when no
// alternate existed — and the blocklist entry then kept it from ever being grabbed again.
//
//   - Gone from the client: there is no copy to protect. Blocklist it, fail the grab and
//     search, as before.
//   - Still there, and a replacement was grabbed: blocklist and remove the stalled one.
//   - Still there, nothing else found (or the indexers are down): leave it alone, write
//     nothing to the blocklist, and try again a window later. Its seeder may come back.
func (c *Coordinator) failOver(ctx context.Context, g grab, item download.Item, found bool, window time.Duration, t stallTarget) {
	minutes := int(window / time.Minute)
	// The stalled release is kept out of the replacement search by name rather than by
	// the blocklist, which it only joins once replaced. normTitle, so the same release
	// re-listed on another indexer is excluded too.
	exclude := map[string]bool{normTitle(g.Title): true}
	payload := map[string]any{
		"kind": t.kind, "id": t.id, "title": t.name, "release": g.Title, "minutes": minutes,
	}

	if !found {
		c.log.Info("automation: download disappeared from the client, failing over",
			"kind", t.kind, "title", t.name, "release", g.Title)
		if err := t.block(ctx, "disappeared from the download client"); err != nil {
			// Without the blocklist row the search below could grab the same release
			// straight back. Leave the grab for the next pass.
			c.log.Warn("automation: stall blocklist failed — leaving the grab for the next check", "release", g.Title, "err", err)
			return
		}
		c.setGrabStatus(ctx, g.ID, "failed")
		t.event(ctx, g.Title+" disappeared from the download client — searching for another")
		repl, err := t.replace(ctx, exclude)
		if err != nil {
			c.log.Warn("automation: replacement search failed", "kind", t.kind, "title", t.name, "err", err)
		}
		payload["vanished"], payload["replaced"], payload["replacement"] = true, repl != "", repl
		c.publishStall(payload)
		return
	}

	why, reason := "Stalled for "+stallSpan(minutes), fmt.Sprintf("stalled %d min", minutes)
	if item.Phase() == "error" {
		why, reason = "The download client reported an error", "client error"
	}
	repl, err := t.replace(ctx, exclude)
	outage := indexer.IsOutage(err) || errors.Is(err, errSearchUnavailable)
	if err != nil && !outage {
		c.log.Warn("automation: replacement search failed", "kind", t.kind, "title", t.name, "err", err)
	}
	if repl == "" {
		// Restart the window rather than leave it expired, so the next attempt is one
		// window away instead of two minutes.
		c.holdStallClock(g.ID, item.Progress)
		c.markStillWaiting(g.ID)
		what := "no other release found"
		if outage {
			what = "indexers unavailable"
		}
		c.log.Info("automation: stalled download kept — nothing to replace it with",
			"kind", t.kind, "title", t.name, "release", g.Title, "reason", what)
		t.event(ctx, fmt.Sprintf("%s — %s, still waiting on %s", why, what, g.Title))
		payload["replaced"] = false
		c.publishStall(payload)
		return
	}

	c.log.Info("automation: stalled download replaced", "kind", t.kind, "title", t.name, "release", g.Title, "replacement", repl)
	if err := t.block(ctx, reason+" — replaced by "+repl); err != nil {
		// The replacement is already in the client, so carry on: the grab goes to
		// 'failed' below and leaves the pending guard either way.
		c.log.Warn("automation: stall blocklist failed", "release", g.Title, "err", err)
	}
	if err := c.removeStalled(ctx, item.Hash); err != nil {
		c.log.Warn("automation: couldn't remove the stalled torrent", "release", g.Title, "err", err)
	}
	c.setGrabStatus(ctx, g.ID, "failed")
	t.event(ctx, fmt.Sprintf("%s — replaced by %s", why, repl))
	payload["replaced"], payload["replacement"] = true, repl
	c.publishStall(payload)
}

// removeStalled deletes a replaced torrent and its data, through the test seam when set.
func (c *Coordinator) removeStalled(ctx context.Context, hash string) error {
	if c.removeTorrent != nil {
		return c.removeTorrent(ctx, hash, true)
	}
	return c.downloads.Remove(ctx, hash, true)
}

// publishStall announces a fail-over on the bus, so alerts can say what happened.
func (c *Coordinator) publishStall(payload map[string]any) {
	if c.bus != nil {
		c.bus.Publish("download.stalled", payload)
	}
}

// markStillWaiting records that grab id came up with no replacement just now.
func (c *Coordinator) markStillWaiting(id int64) {
	c.stallMu.Lock()
	defer c.stallMu.Unlock()
	if c.stillWaitingAt == nil {
		c.stillWaitingAt = map[int64]time.Time{}
	}
	c.stillWaitingAt[id] = time.Now()
}

// waitingOut reports whether grab id came up with no replacement less than a window ago.
func (c *Coordinator) waitingOut(id int64, window time.Duration) bool {
	c.stallMu.Lock()
	defer c.stallMu.Unlock()
	at, ok := c.stillWaitingAt[id]
	return ok && time.Since(at) < window
}

// stallSpan renders a stall window for a history line: "45 min", "6h", "1h 30m".
func stallSpan(minutes int) string {
	switch {
	case minutes < 60:
		return fmt.Sprintf("%d min", minutes)
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	default:
		return fmt.Sprintf("%dh %dm", minutes/60, minutes%60)
	}
}

// detectStalledMovie is the movie kind's stall check: a grab whose version now has its
// file is done; otherwise a stall is replaced with another release for that version.
func (c *Coordinator) detectStalledMovie(ctx context.Context, g grab, queue []download.Item, tick *stallTick) {
	if c.movieHasFileFor(ctx, g) {
		c.setGrabStatus(ctx, g.ID, "imported")
		return
	}
	c.judgeStall(ctx, g, queue, tick, func(ctx context.Context) (stallTarget, bool) {
		m, err := c.movies.Get(ctx, g.MovieID)
		if err != nil {
			return stallTarget{}, false
		}
		return stallTarget{
			kind: "movie", id: m.ID, name: m.Title,
			block: func(ctx context.Context, reason string) error {
				return c.addBlock(ctx, m.ID, g.Title, g.Indexer, "", reason)
			},
			event: func(ctx context.Context, detail string) { c.movies.AddEvent(ctx, m.ID, "failed", detail) },
			replace: func(ctx context.Context, exclude map[string]bool) (string, error) {
				titles, err := c.searchAndGrabExcluding(ctx, m, g.VersionID, exclude)
				return strings.Join(titles, ", "), err
			},
		}, true
	})
}

// searchAndGrabExcluding is the movie replacement search: the version the stalled grab
// was for, never a release in exclude. It ignores the sweep backoff — a fail-over is a
// single deliberate search, already limited to one per window.
func (c *Coordinator) searchAndGrabExcluding(ctx context.Context, m movies.Movie, versionID int64, exclude map[string]bool) ([]string, error) {
	var want []movies.Version
	for _, v := range c.missingVersions(ctx, m.ID) {
		if v.ID == versionID {
			want = append(want, v)
		}
	}
	if len(want) == 0 {
		return nil, nil // the version is no longer wanted — nothing to replace it with
	}
	result, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: movieQuery(m), MediaType: indexer.MediaMovie, Limit: 100})
	if err != nil {
		return nil, err
	}
	byName, cands := c.candidatesExcluding(ctx, m.ID, matchingMovieReleases(m, result.Releases), exclude)
	return c.grabMissingTitles(ctx, m, want, byName, cands), nil
}

// detectStalledSeries is the series kind's stall check. Series grabs have no "landed"
// test of their own here: the import marks them, and an episode pack can land partly.
func (c *Coordinator) detectStalledSeries(ctx context.Context, g grab, queue []download.Item, tick *stallTick) {
	if c.series == nil {
		return
	}
	c.judgeStall(ctx, g, queue, tick, func(ctx context.Context) (stallTarget, bool) {
		s, err := c.series.Get(ctx, g.MovieID) // series id lives in movie_id on the shared table
		if err != nil {
			return stallTarget{}, false
		}
		return stallTarget{
			kind: "series", id: s.ID, name: s.Title,
			block: func(ctx context.Context, reason string) error {
				c.addBlockSeries(ctx, s.ID, g.Title, g.Indexer, reason)
				return nil
			},
			event: func(ctx context.Context, detail string) { c.series.AddEvent(ctx, s.ID, "failed", detail) },
			replace: func(ctx context.Context, exclude map[string]bool) (string, error) {
				return c.replaceSeriesGrab(ctx, s, g, exclude)
			},
		}, true
	})
}

// replaceSeriesGrab searches for what a stalled series release covered — its episode, its
// season for a season pack, every season for a complete pack — narrowed to the episodes
// still wanted. A replacement for one stalled S03 pack must not also grab S04 episodes:
// those have their own grabs (or sweep) to come from.
func (c *Coordinator) replaceSeriesGrab(ctx context.Context, s series.Series, g grab, exclude map[string]bool) (string, error) {
	wanted, _ := wantedEpisodes(s)
	scope := c.coveredByFor(ctx, s, parser.Parse(g.Title), setOf(wanted))
	if len(scope) == 0 {
		// Nothing it covers is still wanted (landed another way, or unmonitored since).
		// No replacement is needed, but removing it is the import's call, not ours.
		return "", nil
	}
	releases, err := c.searchSeriesReleases(ctx, s)
	if err != nil {
		return "", err
	}
	_, _, took := c.grabSeriesScoped(ctx, s, releases, sortedKeys(setOf(scope)), exclude)
	return strings.Join(took, ", "), nil
}

// detectStalledBook is the book kind's stall check: done once the edition (or audiobook
// version) this grab was for has its file; otherwise replaced with another release of
// that same edition.
func (c *Coordinator) detectStalledBook(ctx context.Context, g grab, queue []download.Item, tick *stallTick) {
	if c.books == nil {
		c.setGrabStatus(ctx, g.ID, "failed")
		return
	}
	b, err := c.books.Get(ctx, g.MovieID) // book id is stored in movie_id on the shared grabs table
	if err != nil {
		c.setGrabStatus(ctx, g.ID, "failed")
		return
	}
	// Only the edition THIS grab was for counts as landed. Checking b.HasFile (ebook OR
	// audiobook) meant a landed ebook flipped a still-downloading AUDIOBOOK grab to
	// "imported" — after which ManageSeeding removed that torrent WITH its data the moment
	// it completed, before the import sweep could run, and the book was re-grabbed on the
	// next pass: a grab/delete/re-grab loop that also destroyed the download.
	landed := bookEditionLanded(b, g.Title)
	if g.VersionID > 0 {
		// A version grab has landed when THAT version has its file — the standard
		// audiobook being present says nothing about it. A version since removed has
		// nothing left to wait for.
		landed = true
		for _, v := range b.AudioVersions {
			if v.ID == g.VersionID {
				landed = v.File != nil
			}
		}
	}
	if landed {
		c.setGrabStatus(ctx, g.ID, "imported")
		return
	}
	c.judgeStall(ctx, g, queue, tick, func(context.Context) (stallTarget, bool) {
		return stallTarget{
			kind: "book", id: b.ID, name: b.Title,
			block: func(ctx context.Context, reason string) error {
				c.addBlockBook(ctx, b.ID, g.Title, g.Indexer, reason)
				return nil
			},
			event: func(ctx context.Context, detail string) { c.books.AddEvent(ctx, b.ID, "failed", detail) },
			replace: func(ctx context.Context, exclude map[string]bool) (string, error) {
				return c.replaceBookGrab(ctx, b, g, exclude)
			},
		}, true
	})
}

// replaceBookGrab searches for the same edition the stalled grab was for: its audiobook
// version, or the ebook / audiobook read off the release's format. A release whose format
// can't be told tries each edition the book still lacks and the profile wants.
func (c *Coordinator) replaceBookGrab(ctx context.Context, b books.Book, g grab, exclude map[string]bool) (string, error) {
	sp := c.bookProfile(ctx, b.QualityProfile)
	if g.VersionID > 0 {
		for _, v := range b.AudioVersions {
			if v.ID == g.VersionID {
				return c.grabAudioVersionExcluding(ctx, b, v, sp, exclude)
			}
		}
		return "", nil
	}
	if kind := books.EditionOf(detectBookFormat(g.Title)); kind == books.KindEbook || kind == books.KindAudiobook {
		return c.grabBookEditionExcluding(ctx, b, kind, sp, exclude)
	}
	wantEbook, wantAudio := books.WantedEditions(sp.FormatScores)
	if wantEbook && b.Ebook == nil {
		if title, err := c.grabBookEditionExcluding(ctx, b, books.KindEbook, sp, exclude); title != "" || err != nil {
			return title, err
		}
	}
	if wantAudio && b.Audiobook == nil {
		return c.grabBookEditionExcluding(ctx, b, books.KindAudiobook, sp, exclude)
	}
	return "", nil
}

// detectStalledMusic is the music kind's stall check: done once the album is complete;
// otherwise replaced with another release of the album.
func (c *Coordinator) detectStalledMusic(ctx context.Context, g grab, queue []download.Item, tick *stallTick) {
	if c.music == nil {
		c.setGrabStatus(ctx, g.ID, "failed")
		return
	}
	al, err := c.music.GetAlbum(ctx, g.MovieID) // album id lives in movie_id on the shared table
	if err != nil {
		c.setGrabStatus(ctx, g.ID, "failed")
		return
	}
	if al.Complete() {
		c.setGrabStatus(ctx, g.ID, "imported")
		return
	}
	c.judgeStall(ctx, g, queue, tick, func(ctx context.Context) (stallTarget, bool) {
		a, err := c.music.GetArtist(ctx, al.ArtistID)
		if err != nil {
			return stallTarget{}, false
		}
		return stallTarget{
			kind: "music", id: al.ID, name: a.Name + " — " + al.Title,
			block: func(ctx context.Context, reason string) error {
				c.addBlockMusic(ctx, al.ID, g.Title, g.Indexer, reason)
				return nil
			},
			// Music history is kept per artist.
			event: func(ctx context.Context, detail string) { c.music.AddEvent(ctx, a.ID, "failed", detail) },
			replace: func(ctx context.Context, exclude map[string]bool) (string, error) {
				out := c.grabAlbumExcluding(ctx, a, al, exclude)
				switch out.Code {
				case outcomeGrabbed:
					return out.Release, nil
				case outcomeIndexerError:
					return "", fmt.Errorf("%w: %s", errSearchUnavailable, out.Detail)
				}
				return "", nil
			},
		}, true
	})
}
