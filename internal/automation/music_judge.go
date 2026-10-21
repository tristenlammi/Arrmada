package automation

import (
	"context"
	"fmt"
	"strings"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

// One album search's judgement of the releases it found (MUS-09, MUS-16, MUS-17, MUS-21).
// The automatic search, the upgrade sweep and interactive search all judge releases here,
// so what interactive search says about a release is exactly what the sweep did with it.
//
// Every release goes through the same steps in order and stops at the first it fails:
//
//  1. a download client can take it (a usenet release needs a usenet client);
//  2. it names this artist and this album (music.ReleaseIsForAlbum);
//  3. it is audio and about the album's size in its tier (music.ReleaseSane);
//  4. it isn't blocklisted for the album, already grabbed, or held out by the caller;
//  5. its tier is at least what the album holds (a part-held album), or better than it
//     under the profile (a complete album: an upgrade);
//  6. the profile takes it (albumVerdict).
//
// A release that passes them all is eligible, and pickBestAlbum chooses among those.

// Album release stages: how far a release got. Ordered, so "got past step N" is a compare.
const (
	stageNetwork  = iota // no download client can take it
	stageTitle           // not this artist and album
	stageSane            // video, or an implausible size
	stageBlocked         // blocklisted, already grabbed, or held out
	stageTier            // not up to what the album holds
	stageProfile         // turned down by the profile
	stageEligible        // fit to grab
)

// albumRelease is one release as the album search judged it.
type albumRelease struct {
	rel    indexer.Release
	tier   music.Quality
	stage  int    // how far it got (stage*)
	code   string // the search attempt's code for where it stopped; classEligible when it passed
	reason string // the same in words, for interactive search ("" when eligible)
	score  int    // the profile's score, when it reached the profile
}

// albumMode is what an album search is for: missing tracks, or a better copy of a complete
// album (upgrade).
type albumMode struct {
	upgrade bool
	// held is the album's floor (music.HeldTiers): the lowest tier among its files.
	// QualityUnknown when it holds nothing, or a file whose tier can't be told.
	held music.Quality
}

// albumJudge holds what judging one album's releases needs.
type albumJudge struct {
	artist, album string
	// names are the artist's name and matchable aliases (MUS-38): a release named under an
	// alias names the album too, when the artist's own name doesn't.
	names   music.ArtistNames
	sp      quality.StoredProfile
	nets    netPolicy
	listing music.SizeListing
	mode    albumMode
	// The release sets that keep a release out by its normalized title: read only when a
	// release gets as far as needing them (readAlbumSets).
	blocked, pending, exclude map[string]bool
	// imported (upgrades only) are the releases imported into the album lately. One that
	// was imported already gave the album everything it had that was better; grabbing it
	// again would download it again for nothing, every sweep.
	imported importedReleases
}

// aliasNames reports whether a release names the album under one of the artist's aliases
// (MUS-38), as the release's whole artist part.
func (j *albumJudge) aliasNames(title string) bool {
	return len(j.names.Aliases) > 0 && j.names.ReleaseIsForAlbum(title, j.album)
}

// early runs the checks that need nothing from the database (steps 1-3).
func (j *albumJudge) early(rel indexer.Release) albumRelease {
	r := albumRelease{rel: rel, tier: music.DetectQuality(rel.Title), code: classEligible}
	if why := j.nets.rejectReason(rel); why != "" {
		r.stage, r.code, r.reason = stageNetwork, j.nets.dropCode(rel), why
		return r
	}
	if why := music.WhyNotForAlbum(rel.Title, j.artist, j.album); why != "" && !j.aliasNames(rel.Title) {
		r.stage, r.code, r.reason = stageTitle, DropWrongTitle, why
		return r
	}
	if ok, code, why := music.ReleaseSane(rel.Title, rel.Categories, rel.SizeBytes, r.tier, j.listing); !ok {
		r.stage, r.reason = stageSane, why
		r.code = DropSize
		if code == music.SaneVideo {
			r.code = DropVideo
		}
		return r
	}
	r.stage = stageBlocked // got past step 3; the rest decides the final stage
	return r
}

// late runs the remaining checks (4-6) on a release that passed early.
func (j *albumJudge) late(r albumRelease) albumRelease {
	key := normTitle(r.rel.Title)
	switch {
	case j.blocked[key]:
		r.stage, r.code, r.reason = stageBlocked, DropBlocklisted, "blocklisted for this album"
		return r
	case j.pending[key]:
		r.stage, r.code, r.reason = stageBlocked, DropPending, "already grabbed and still downloading"
		return r
	case j.exclude[key]:
		r.stage, r.code, r.reason = stageBlocked, DropBlocklisted, "the download being replaced"
		return r
	case j.imported.has(r.rel):
		r.stage, r.code, r.reason = stageBlocked, DropBlocklisted, "imported into this album already"
		return r
	}
	if why, code := j.tierCheck(r.tier); why != "" {
		r.stage, r.code, r.reason = stageTier, code, why
		return r
	}
	score, code := albumVerdict(j.sp, r.rel)
	if code != "" {
		r.stage, r.code, r.reason = stageProfile, code, profileWords(j.sp, r.tier, code)
		return r
	}
	r.stage, r.code, r.score = stageEligible, classEligible, score
	return r
}

// tierCheck is step 5: why a tier isn't wanted for the album as it stands ("" when it is).
func (j *albumJudge) tierCheck(tier music.Quality) (why, code string) {
	held := j.mode.held
	if j.mode.upgrade {
		if !music.IsUpgrade(tier, held, j.sp.FormatScores) {
			if tier == music.QualityUnknown {
				return "can't tell its quality from the name", DropNotBetter
			}
			return fmt.Sprintf("no better than the %s you have", held), DropNotBetter
		}
		return "", ""
	}
	if held == music.QualityUnknown {
		return "", ""
	}
	// A part-held album takes releases at least as good as its worst track: anything lower
	// would be kept out at import anyway, or leave the album a patchwork of tiers. Any
	// lossless tier counts as good as another (the import never churns lossless either).
	if music.QualityRank(tier) >= music.QualityRank(held) ||
		(music.IsLosslessQuality(tier) && music.IsLosslessQuality(held)) {
		return "", ""
	}
	return fmt.Sprintf("lower than the %s you have", held), DropNotWanted
}

// profileWords says why the profile turned a release down.
func profileWords(sp quality.StoredProfile, tier music.Quality, code string) string {
	switch code {
	case quality.RejectTerm:
		return "contains a term the profile rejects"
	case quality.RejectMinFormatScore:
		return "scores below the profile's minimum"
	}
	if tier == music.QualityUnknown {
		return "can't tell its quality from the name"
	}
	name := sp.Name
	if name == "" {
		name = "this profile"
	}
	return fmt.Sprintf("%s isn't on the %s ladder", tier, name)
}

// readAlbumSets reads the blocklist and pending grabs for the album (and, for an upgrade,
// every release grabbed for it); an error means they can't be trusted, and the search
// must not grab (fail closed).
func (c *Coordinator) readAlbumSets(ctx context.Context, albumID int64, j *albumJudge) error {
	blocked, err := c.blockedSetMusic(ctx, albumID)
	if err != nil {
		return err
	}
	pending, err := c.pendingMusicGrabTitles(ctx, albumID)
	if err != nil {
		return err
	}
	j.blocked, j.pending = blocked, pending
	if j.mode.upgrade {
		if j.imported, err = c.musicImportedReleases(ctx, albumID); err != nil {
			return err
		}
	}
	return nil
}

// importedReleases are the releases imported into an album lately (grabs imported or since
// seeded, within importedRecall): by info hash where the grab knows it, by normalized
// title where it doesn't. A failed grab isn't here: failures are the blocklist's job.
type importedReleases struct {
	hashes        map[string]bool // lower-case info hashes
	hashlessTitle map[string]bool // titles of grabs with no info hash
	anyTitle      map[string]bool // every title, for a release whose hash isn't listed
}

// importedRecall is how far back an imported release keeps an upgrade from grabbing it
// again: long enough for any upgrade sweep's backoff, short enough that a release the
// owner later wants again isn't refused for ever.
const importedRecall = "-90 days"

// has reports whether rel is one of them.
func (ir importedReleases) has(rel indexer.Release) bool {
	if h := strings.ToLower(rel.InfoHash); h != "" {
		return ir.hashes[h] || ir.hashlessTitle[normTitle(rel.Title)]
	}
	return ir.anyTitle[normTitle(rel.Title)]
}

// musicImportedReleases reads the releases imported into an album lately.
func (c *Coordinator) musicImportedReleases(ctx context.Context, albumID int64) (importedReleases, error) {
	ir := importedReleases{hashes: map[string]bool{}, hashlessTitle: map[string]bool{}, anyTitle: map[string]bool{}}
	rows, err := c.db.QueryContext(ctx, `SELECT title, info_hash FROM grabs
		WHERE media_type = 'music' AND movie_id = ? AND `+everImportedWhere+`
		  AND grabbed_at > `+store.UTCNowTimestamp(c.db, "'"+importedRecall+"'"), albumID)
	if err != nil {
		return ir, err
	}
	defer rows.Close()
	for rows.Next() {
		var title, hash string
		if err := rows.Scan(&title, &hash); err != nil {
			return ir, err
		}
		key := normTitle(title)
		ir.anyTitle[key] = true
		if hash == "" {
			ir.hashlessTitle[key] = true
		} else {
			ir.hashes[strings.ToLower(hash)] = true
		}
	}
	return ir, rows.Err()
}

// judgeAlbum judges every release for an album. The blocklist and pending grabs are read
// only when some release gets that far, so an unreadable blocklist doesn't turn "nothing
// named this album" into an error.
func (c *Coordinator) judgeAlbum(ctx context.Context, albumID int64, j *albumJudge, releases []indexer.Release) ([]albumRelease, error) {
	out := make([]albumRelease, 0, len(releases))
	need := false
	for _, rel := range releases {
		r := j.early(rel)
		need = need || r.stage >= stageBlocked
		out = append(out, r)
	}
	if !need {
		return out, nil
	}
	if err := c.readAlbumSets(ctx, albumID, j); err != nil {
		return out, err
	}
	for i := range out {
		if out[i].stage >= stageBlocked {
			out[i] = j.late(out[i])
		}
	}
	return out, nil
}

// past counts the releases that got past stage s.
func past(rs []albumRelease, s int) int {
	n := 0
	for _, r := range rs {
		if r.stage > s {
			n++
		}
	}
	return n
}

// albumListing reads an album's listing for a search: its target edition's tracks, after
// fetching them when they aren't stored yet. An album with none can't be searched: a
// download would have nothing to land against.
func (c *Coordinator) albumListing(ctx context.Context, a music.Artist, al music.Album) ([]music.Track, *albumOutcome) {
	if err := c.music.EnsureTracks(ctx, al); err != nil {
		c.log.Warn("music: couldn't fetch the track listing — skipping", "album", al.Title, "err", err)
		return nil, &albumOutcome{Code: outcomeListingError, Detail: err.Error()}
	}
	tracks, err := c.music.Tracks(ctx, al.ID)
	if err != nil {
		return nil, &albumOutcome{Code: outcomeListingError, Detail: err.Error()}
	}
	if len(tracks) == 0 {
		// Still nothing after asking MusicBrainz means no official release carries a
		// listing. Searching the indexers would be pointless, so it's a miss without one.
		c.log.Info("music: no track listing on MusicBrainz yet — not searching", "artist", a.Name, "album", al.Title)
		return nil, &albumOutcome{Code: outcomeNoListing, Detail: "MusicBrainz lists no tracks for this album"}
	}
	return tracks, nil
}

// albumQuery is the indexer query for one album: text, plus artist and album as parts for
// an indexer whose caps take t=music.
func albumQuery(a music.Artist, al music.Album) indexer.SearchQuery {
	return indexer.SearchQuery{
		Text: a.Name + " " + al.Title, Artist: a.Name, Album: al.Title, MediaType: indexer.MediaMusic, Limit: 100,
	}
}

// searchAlbumReleases runs one album search and judges what comes back: the shared core of
// the automatic search (missing or upgrade) and nothing else. exclude holds normalized
// titles to leave out without blocklisting them (a stall fail-over's stalled release).
func (c *Coordinator) searchAlbumReleases(ctx context.Context, a music.Artist, al music.Album, mode albumMode, exclude map[string]bool) ([]albumRelease, quality.StoredProfile, *albumOutcome) {
	tracks, fail := c.albumListing(ctx, a, al)
	if fail != nil {
		return nil, quality.StoredProfile{}, fail
	}
	sp := c.musicProfile(ctx, a.QualityProfile)
	res, err := c.searchMusic(ctx, albumQuery(a, al))
	if err != nil {
		c.log.Warn("music: search failed", "album", al.Title, "err", err)
		return nil, sp, &albumOutcome{Code: outcomeIndexerError, Detail: err.Error()}
	}
	// Nothing named the album under the artist's own name: try its aliases (MUS-38).
	names := c.music.ArtistNames(ctx, a)
	res = c.searchAliases(ctx, res, names, al)
	notes := notesFrom(ctx) // the album's search attempt, when it is recorded
	notes.consider(res.Releases)
	if len(res.Releases) == 0 {
		if len(res.Errors) > 0 && len(res.Errors) >= res.Asked {
			// Nothing came back because every indexer asked failed. That says nothing
			// about the album, so it mustn't count against it. One dead indexer among
			// healthy ones that found nothing is a plain miss, though, or a single broken
			// tracker would keep every unfindable album out of the backoff for good.
			return nil, sp, &albumOutcome{Code: outcomeIndexerError, Detail: fmt.Sprintf("%d indexer(s) failed", len(res.Errors))}
		}
		return nil, sp, &albumOutcome{Code: outcomeNoResults, Detail: "no releases"}
	}
	j := &albumJudge{
		artist: a.Name, album: al.Title, names: names, sp: sp, nets: c.networks(ctx),
		listing: music.ListingOf(tracks), mode: mode, exclude: exclude,
	}
	judged, err := c.judgeAlbum(ctx, al.ID, j, res.Releases)
	for _, r := range judged {
		notes.mark(r.rel.Title, r.code)
	}
	if err != nil {
		c.skipUnreadable(a.Name+" — "+al.Title, err)
		return nil, sp, &albumOutcome{Code: outcomeUnreadable, Detail: err.Error()}
	}
	return judged, sp, nil
}

// albumVerdictOutcome turns a judged search into its outcome when nothing is eligible, or
// returns the eligible releases. The details are what Wanted and the logs show.
func albumVerdictOutcome(judged []albumRelease, sp quality.StoredProfile, mode albumMode) ([]indexer.Release, *albumOutcome) {
	usable, named, sane, free := past(judged, stageNetwork), past(judged, stageTitle), past(judged, stageSane), past(judged, stageBlocked)
	switch {
	case named == 0:
		return nil, &albumOutcome{Code: outcomeNoMatch, Detail: fmt.Sprintf("%d release(s), none named this album", usable)}
	case sane == 0:
		return nil, &albumOutcome{Code: outcomeBelowProfile, Detail: fmt.Sprintf("%d match(es), none plausible: video, or far from the album's size", named)}
	case free == 0:
		return nil, &albumOutcome{Code: outcomeBlocked, Detail: fmt.Sprintf("%d match(es), all blocklisted or already grabbed", named)}
	case past(judged, stageTier) == 0:
		if mode.upgrade {
			return nil, &albumOutcome{Code: outcomeBelowProfile, Detail: fmt.Sprintf("%d release(s), none better than the %s you have", free, mode.held)}
		}
		return nil, &albumOutcome{Code: outcomeBelowProfile, Detail: fmt.Sprintf("%d release(s) below the %s you already have", free, mode.held)}
	}
	var eligible []indexer.Release
	for _, r := range judged {
		if r.stage == stageEligible {
			eligible = append(eligible, r.rel)
		}
	}
	if len(eligible) == 0 {
		return nil, &albumOutcome{Code: outcomeBelowProfile, Detail: fmt.Sprintf("%d match(es), none accepted by %q", past(judged, stageTier), sp.Name)}
	}
	return eligible, nil
}
