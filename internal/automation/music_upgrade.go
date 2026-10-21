package automation

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The music upgrade sweep (MUS-16): better copies of albums already held, up to the
// profile's cutoff.
//
// No surprise downloads. An album is only looked at when all of these hold:
//   - it and its artist are monitored, MusicBrainz still lists it, and no upgrade note
//     holds it back (Repo.WantedAlbums);
//   - it is complete, and its profile allows upgrades;
//   - every file's tier is known (the floor is the lowest of them, music.HeldTiers) and
//     its profile's ladder has a tier both preferred to and better than that floor
//     (music.UpgradeTiers): an album at its cutoff is never searched;
//   - nothing for it is downloading, and its upgrade backoff has run out.
//
// And the sweep is paced: at most musicUpgradeCap albums are searched per run (never
// searched first, then the longest-waiting), and at most the per-sweep upgrade budget
// (Settings → Downloads) are grabbed. So the first sweep after an update looks at a
// handful of albums, not the whole library, and works through the rest over the
// following runs.
//
// Never a downgrade: a release only counts when its tier is an upgrade under the ladder,
// and the import replaces a track's file only with a strictly better tier
// (placeFiles/music.BetterTier), the old file going to the recycle bin through staging.
// Never another edition: the import of an upgrade grab doesn't move the album to another
// edition, and an album kept on an edition by hand takes files only from that edition
// (importMusicDownload); anything else goes to Reviews for a person to decide.

// musicUpgradeCap is how many albums one upgrade sweep searches at most.
const musicUpgradeCap = 10

// albumUpgradeKey is the claim an album's upgrade search holds: the same as its search's,
// so a Search now and the sweep never search one album at once.
func albumUpgradeKey(id int64) string { return albumKey(id) }

// upgradeDue is one album the upgrade sweep would search.
type upgradeDue struct {
	artist music.Artist
	album  music.Album
	held   music.Quality
	last   time.Time
}

// UpgradeMusic is the upgrade sweep: see the top of this file.
func (c *Coordinator) UpgradeMusic(ctx context.Context) {
	if c.music == nil || !c.moduleOn(ctx, "music") {
		return
	}
	ctx = WithDefaultSearchTrigger(ctx, TriggerUpgrade)
	due, err := c.musicUpgradesDue(ctx)
	if err != nil {
		c.log.Info("music: upgrade sweep skipped", "why", err)
		return
	}
	if len(due) > musicUpgradeCap {
		c.log.Info("music: more albums could be upgraded than one sweep searches — the rest wait for the next",
			"due", len(due), "searching", musicUpgradeCap)
		due = due[:musicUpgradeCap]
	}
	budget := c.newSweepBudget(ctx)
	titlesLeft := 0
	defer func() { c.logBudget("music", budget, titlesLeft) }()
	for i, d := range due {
		if budget.spent() {
			titlesLeft = len(due) - i
			break
		}
		release, ok := c.claims.claim(albumUpgradeKey(d.album.ID))
		if !ok {
			continue // a person's search of it is running; not a miss
		}
		out := c.upgradeAlbum(ctx, d.artist, d.album, d.held, budget)
		release()
		switch {
		case out.Code == outcomeGrabbed:
			if err := c.music.ResetUpgradeMisses(ctx, d.album.ID); err != nil {
				c.log.Warn("music: clearing the upgrade backoff failed", "album", d.album.Title, "err", err)
			}
		case out.miss():
			if err := c.music.RecordUpgradeMiss(ctx, d.album.ID); err != nil {
				c.log.Warn("music: recording an upgrade miss failed", "album", d.album.Title, "err", err)
			}
		case out.Code == outcomeNoSpace:
			return
		case out.Code == outcomeDeferred:
			// The budget ran out at the grab: not a miss, and searched again next time.
		default:
			if err := c.music.TouchUpgradeSearch(ctx, d.album.ID); err != nil {
				c.log.Warn("music: stamping the upgrade search failed", "album", d.album.Title, "err", err)
			}
		}
	}
}

// outcomeDeferred is an upgrade found but left for the next sweep: the budget was spent.
const outcomeDeferred = "deferred"

// musicUpgradesDue lists the albums the upgrade sweep may search now, oldest look first.
// An error means the sweep can't tell what is downloading, and searches nothing.
func (c *Coordinator) musicUpgradesDue(ctx context.Context) ([]upgradeDue, error) {
	artists, err := c.music.LibraryArtists(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]music.Artist, len(artists))
	for _, a := range artists {
		byID[a.ID] = a
	}
	albums, err := c.music.WantedAlbums(ctx)
	if err != nil {
		return nil, err
	}
	states, err := c.music.UpgradeStates(ctx)
	if err != nil {
		return nil, err
	}
	queue, err := c.musicQueue(ctx)
	if err != nil {
		// An unreadable queue looks like an empty one: every album would read as not
		// downloading, and an upgrade could be grabbed twice.
		return nil, fmt.Errorf("can't read the download queue: %w", err)
	}
	active, err := c.ActiveByItem(ctx, "music")
	if err != nil {
		return nil, err
	}
	profiles := map[string]quality.StoredProfile{}
	now := c.clock().UTC()
	var due []upgradeDue
	for _, al := range albums {
		a, ok := byID[al.ArtistID]
		if !ok || !al.Complete() || al.HaveTracks == 0 {
			continue
		}
		sp, ok := profiles[a.QualityProfile]
		if !ok {
			sp = c.musicProfile(ctx, a.QualityProfile)
			profiles[a.QualityProfile] = sp
		}
		if !sp.UpgradesEnabled {
			continue
		}
		held, ok, err := c.music.HeldTiers(ctx, al.ID)
		if err != nil {
			c.skipUnreadable(a.Name+" — "+al.Title, err)
			continue
		}
		if !ok || len(music.UpgradeTiers(held, sp.FormatScores)) == 0 {
			continue // at its cutoff, or a file's tier isn't known yet (measured later)
		}
		if len(active[al.ID]) > 0 || c.albumDownloading(ctx, queue, a, al) {
			continue
		}
		st := states[al.ID]
		last := parseTime(st.LastAt)
		if wait := musicSearchWait(st.Misses); wait > 0 && !last.IsZero() && now.Sub(last) < wait {
			continue
		}
		due = append(due, upgradeDue{artist: a, album: al, held: held, last: last})
	}
	// Never looked at first (a zero time sorts first), then the longest-waiting.
	sort.SliceStable(due, func(i, j int) bool { return due[i].last.Before(due[j].last) })
	return due, nil
}

// upgradeAlbum searches for a better copy of one complete album and grabs the best, under
// budget (nil: no limit, a person's Search now). Each call is one search attempt, scope
// "upgrade".
func (c *Coordinator) upgradeAlbum(ctx context.Context, a music.Artist, al music.Album, held music.Quality, budget *upgradeBudget) albumOutcome {
	ctx, notes := newSearchNotes(WithDefaultSearchTrigger(ctx, TriggerUpgrade))
	out := c.upgradeAlbumSearch(ctx, a, al, held, budget, nil)
	so, err := out.searchOutcome()
	c.recordAttempt(ctx, notes, AttemptMusic, al.ID, ScopeUpgrade, &so, err)
	return out
}

func (c *Coordinator) upgradeAlbumSearch(ctx context.Context, a music.Artist, al music.Album, held music.Quality, budget *upgradeBudget, exclude map[string]bool) albumOutcome {
	mode := albumMode{upgrade: true, held: held}
	judged, sp, fail := c.searchAlbumReleases(ctx, a, al, mode, exclude)
	if fail != nil {
		return *fail
	}
	cands, fail := albumVerdictOutcome(judged, sp, mode)
	if fail != nil {
		return *fail
	}
	best := pickBestAlbum(c.networks(ctx), sp, cands)
	if best == nil {
		return albumOutcome{Code: outcomeBelowProfile, Detail: fmt.Sprintf("%d match(es), none accepted by %q", len(cands), sp.Name)}
	}
	if !budget.reserve() {
		budget.defer1()
		return albumOutcome{Code: outcomeDeferred, Release: best.Title}
	}
	if !c.diskOKFor(ctx, best.Indexer, float64(best.SizeBytes)/(1<<30)) {
		budget.unreserve()
		c.log.Warn("music: not enough free space for an upgrade", "album", al.Title, "release", best.Title)
		return albumOutcome{Code: outcomeNoSpace, Release: best.Title}
	}
	tier := music.DetectQuality(best.Title)
	// Recorded as an upgrade: the import then keeps the album on its edition.
	notesFrom(ctx).picked(best.Title, quality.KeyCustom, decidedUpgrade)
	hash, err := c.grabMusic(ctx, best.Indexer, best.DownloadURL, best.Title, musicCategory)
	if err != nil {
		budget.unreserve()
		c.log.Warn("music: upgrade grab failed", "album", al.Title, "err", err)
		return albumOutcome{Code: outcomeGrabFailed, Detail: err.Error(), Release: best.Title}
	}
	c.recordMusicGrab(ctx, "music", al.ID, best.Title, best.Indexer, a.QualityProfile, hash)
	c.music.AddEvent(ctx, a.ID, "grabbed", fmt.Sprintf("Upgrading %q from %s to %s: %s", al.Title, held, tier, best.Title))
	c.log.Info("music: upgrading", "artist", a.Name, "album", al.Title, "from", held, "to", tier, "release", best.Title)
	return albumOutcome{Code: outcomeGrabbed, Release: best.Title}
}

// upgradeAlbumNow is a person's Search now on a complete album: an upgrade search when its
// profile allows upgrades and it is below its cutoff, ignoring the backoff. Otherwise
// nothing is searched: grabbing another copy of what the album already has would be a
// download nobody wanted.
func (c *Coordinator) upgradeAlbumNow(ctx context.Context, a music.Artist, al music.Album) (SearchOutcome, error) {
	sp := c.musicProfile(ctx, a.QualityProfile)
	if !sp.UpgradesEnabled {
		return SearchOutcome{Reason: ReasonNothingWanted}, nil
	}
	held, ok, err := c.music.HeldTiers(ctx, al.ID)
	if err != nil {
		return SearchOutcome{}, err
	}
	if !ok || len(music.UpgradeTiers(held, sp.FormatScores)) == 0 {
		return SearchOutcome{Reason: ReasonNothingWanted}, nil
	}
	out := c.upgradeAlbum(ctx, a, al, held, nil)
	switch {
	case out.Code == outcomeGrabbed:
		_ = c.music.ResetUpgradeMisses(ctx, al.ID)
	case out.miss():
		_ = c.music.RecordUpgradeMiss(ctx, al.ID)
	}
	return out.searchOutcome()
}

// musicGrabWasUpgrade reports whether the grab a finished download came from (the album's
// newest grab with its info hash, else its name) was the upgrade sweep's own, not a
// person's choice. An unreadable grab reads as not: the guard below then doesn't apply,
// and the import is what it was before upgrades existed.
func (c *Coordinator) musicGrabWasUpgrade(ctx context.Context, hash, name string, albumID int64) bool {
	rows, err := c.db.QueryContext(ctx,
		`SELECT title, info_hash, decided_mode, manual FROM grabs WHERE media_type = 'music' AND movie_id = ? ORDER BY id DESC LIMIT 50`, albumID)
	if err != nil {
		return false
	}
	defer rows.Close()
	target := normRelease(name)
	byName := false
	for rows.Next() {
		var title, ihash, mode string
		var manual int
		if rows.Scan(&title, &ihash, &mode, &manual) != nil {
			continue
		}
		upgrade := mode == decidedUpgrade && manual == 0
		if hash != "" && ihash != "" && strings.EqualFold(ihash, hash) {
			return upgrade
		}
		if !byName && normRelease(title) == target {
			byName = upgrade
			if hash == "" {
				return byName
			}
		}
	}
	return byName
}

// grabMode is a grab's decided_mode ("" when it can't be read).
func (c *Coordinator) grabMode(ctx context.Context, g grab) string {
	var mode string
	if err := c.db.QueryRowContext(ctx, `SELECT decided_mode FROM grabs WHERE id = ?`, g.ID).Scan(&mode); err != nil {
		return ""
	}
	return mode
}

// completedUnderGrab reports whether grab g of an album that now reads complete is one the
// stall check may close as no longer needed: any grab except an upgrade the sweep grabbed
// or a person's own grab. Those were meant for a complete album, and closing one as
// 'dismissed' would keep its files forever, let a second upgrade be grabbed beside it, and
// lose it before its import. (An album also completes without a new file: unwanting a
// track, an edition switch, a shorter listing; so no timestamp is trusted here.) An
// unreadable row keeps the old rule.
func (c *Coordinator) completedUnderGrab(ctx context.Context, g grab, _ music.Album) bool {
	var mode string
	var manual int
	if err := c.db.QueryRowContext(ctx, `SELECT decided_mode, manual FROM grabs WHERE id = ?`, g.ID).Scan(&mode, &manual); err != nil {
		return true
	}
	return mode != decidedUpgrade && manual == 0
}

// replaceUpgrade is the fail-over search for a stalled grab of a complete album: another
// upgrade when the profile allows upgrades and the album is below its cutoff, else
// nothing (the outcome's empty code).
func (c *Coordinator) replaceUpgrade(ctx context.Context, a music.Artist, al music.Album, exclude map[string]bool) albumOutcome {
	sp := c.musicProfile(ctx, a.QualityProfile)
	if !sp.UpgradesEnabled {
		return albumOutcome{}
	}
	held, ok, err := c.music.HeldTiers(ctx, al.ID)
	if err != nil {
		return albumOutcome{Code: outcomeUnreadable, Detail: err.Error()}
	}
	if !ok || len(music.UpgradeTiers(held, sp.FormatScores)) == 0 {
		return albumOutcome{}
	}
	// Another grab for the album still in flight (besides the stalled one being replaced):
	// that one may bring the upgrade, so nothing is grabbed beside it.
	acqs, err := c.Active(ctx, "music", al.ID)
	if err != nil {
		return albumOutcome{Code: outcomeUnreadable, Detail: err.Error()}
	}
	for _, acq := range acqs {
		if !exclude[normTitle(acq.Title)] {
			return albumOutcome{}
		}
	}
	// And the upgrade budget: no more upgrade grabs in any six hours than one sweep may make.
	if full, err := c.upgradeBudgetUsed(ctx); err != nil {
		return albumOutcome{Code: outcomeUnreadable, Detail: err.Error()}
	} else if full {
		c.log.Info("music: upgrade budget used — a stalled upgrade waits for the next sweep", "album", al.Title)
		return albumOutcome{}
	}
	// The grab must be recorded as an upgrade, which the search's collector carries.
	ctx, _ = withSearchNotes(ctx)
	return c.upgradeAlbumSearch(ctx, a, al, held, nil, exclude)
}

// upgradeBudgetUsed reports whether the music upgrade grabs of the last six hours (one
// upgrade sweep's interval) already reach the per-sweep upgrade budget, for an upgrade
// grabbed outside the sweep (a stall fail-over's replacement).
func (c *Coordinator) upgradeBudgetUsed(ctx context.Context) (bool, error) {
	max := c.newSweepBudget(ctx).max
	if max <= 0 {
		return false, nil
	}
	var n int
	err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM grabs WHERE media_type = 'music' AND decided_mode = ?
		AND grabbed_at > `+store.UTCNowTimestamp(c.db, "'-6 hours'"), decidedUpgrade).Scan(&n)
	return n >= max, err
}

// upgradeEditionHold is the review an upgrade's import becomes instead of moving the album
// to another edition, or of putting another edition's files on an album a person kept on
// its edition; nil when the match may go ahead. A person can still import it from Reviews
// onto the edition they choose.
func (c *Coordinator) upgradeEditionHold(ctx context.Context, albumID int64, results []music.MatchResult) *music.MatchResult {
	al, err := c.music.AlbumRecord(ctx, albumID)
	if err != nil {
		return nil
	}
	for _, res := range results {
		if res.AlbumID != albumID || res.Best == nil {
			continue
		}
		pinnedElsewhere := al.EditionPinned && res.Best.Cand.Edition.ID != al.TargetEditionID
		if !res.Switch && !pinnedElsewhere {
			continue
		}
		hold := res
		hold.Decision, hold.Reason = music.MatchReview, music.MatchReasonCloseEditions
		hold.Summary = "An upgrade, but these files are another edition than the album's"
		if al.EditionPinned {
			hold.Summary += " (kept on the edition you chose)"
		}
		hold.Summary += ". Nothing was replaced; import it from here onto the edition you want, or reject it."
		return &hold
	}
	return nil
}

// AlbumUpgrade is where an album stands against its profile's cutoff, for the album page.
type AlbumUpgrade struct {
	Profile string `json:"profile"` // the profile's name
	// Held is the album's floor (the lowest tier among its files; "" when it holds
	// nothing or a file's tier isn't known yet) and Cutoff the ladder's top tier.
	Held   music.Quality `json:"held,omitempty"`
	Cutoff music.Quality `json:"cutoff,omitempty"`
	// State: "off" (the profile doesn't upgrade), "incomplete" (missing tracks come
	// first), "unknown" (a file's tier isn't measured yet), "below" (upgrades are looked
	// for) or "at" (at its cutoff).
	State string `json:"state"`
	// NextAt is when the sweep may next look for an upgrade (below only; "" = its next run).
	NextAt string `json:"next_at,omitempty"`
}

// AlbumUpgradeState says where an album stands against its cutoff (see AlbumUpgrade).
func (c *Coordinator) AlbumUpgradeState(ctx context.Context, albumID int64) (AlbumUpgrade, error) {
	al, err := c.music.AlbumRecord(ctx, albumID)
	if err != nil {
		return AlbumUpgrade{}, err
	}
	a, err := c.music.GetArtist(ctx, al.ArtistID)
	if err != nil {
		return AlbumUpgrade{}, err
	}
	sp := c.musicProfile(ctx, a.QualityProfile)
	up := AlbumUpgrade{Profile: sp.Name, Cutoff: music.Cutoff(sp.FormatScores)}
	held, ok, err := c.music.HeldTiers(ctx, al.ID)
	if err != nil {
		return up, err
	}
	if ok {
		up.Held = held
	}
	switch {
	case !sp.UpgradesEnabled:
		up.State = "off"
	case !al.Complete():
		up.State = "incomplete"
	case !ok || held == music.QualityUnknown:
		up.State = "unknown"
	case len(music.UpgradeTiers(held, sp.FormatScores)) == 0:
		up.State = "at"
	default:
		up.State = "below"
		st, err := c.music.UpgradeStateOf(ctx, al.ID)
		if err != nil {
			return up, err
		}
		if next := AlbumNextSearchAt(parseTime(st.LastAt), st.Misses); next.After(c.clock()) {
			up.NextAt = next.UTC().Format(time.RFC3339)
		}
	}
	return up, nil
}
