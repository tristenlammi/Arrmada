package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tristenlammi/arrmada/internal/audiotag"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/music/stage"
)

// Music placement (MUS-33, MUS-34, MUS-36): once the matcher has decided which file is
// which track, each file is named by the music naming templates, written in the house tag
// style in a staging copy (never the download, which may still be seeding), checked, and
// moved into the library with one move. Extras (artwork, booklets, logs, lyrics) come
// along, hardlinked, never over anything. With "Write tags" off, files are hardlinked
// straight in as they always were.

// MusicPlacementPrefs are the settings placement reads on every import.
type MusicPlacementPrefs struct {
	// WriteTags writes the house tags (on by default). Off keeps every file exactly as
	// downloaded: hardlinked into the library, never written.
	WriteTags bool
	// Extras is the allowlist of extra files brought along (stage.ParseExtras); empty
	// brings none.
	Extras map[string]bool
	// Transcode optionally converts imported audio to a compact lossy format in staging.
	// Downloads/seeding sources are never touched.
	Transcode MusicTranscodePrefs
}

// musicPlacement is the coordinator's placement wiring. The zero value (tests, or nothing
// wired) writes no tags, brings no extras and fetches no covers: imports hardlink as
// before.
type musicPlacement struct {
	prefs     func(ctx context.Context) MusicPlacementPrefs
	tagger    stage.Tagger
	covers    music.CoverSource
	audioHash func(ctx context.Context, path string) (string, error)
	freeBytes func(path string) (uint64, bool)
	locks     sync.Map // album id → *sync.Mutex: an import, a rename and a retag of one album never run at once
}

// SetMusicPlacement wires placement: the settings it reads, where covers come from (nil =
// none) and the decoded-audio check (nil = only the tag read-back is checked).
func (c *Coordinator) SetMusicPlacement(prefs func(ctx context.Context) MusicPlacementPrefs, covers music.CoverSource,
	audioHash func(ctx context.Context, path string) (string, error)) {
	c.musicPlace.prefs = prefs
	c.musicPlace.covers = covers
	c.musicPlace.audioHash = audioHash
}

// MusicAudioHash is the decoded-audio check the app uses: ffmpeg's md5 of the samples, or
// "no decoder" when ffmpeg isn't installed.
func MusicAudioHash(ctx context.Context, path string) (string, error) {
	sum, err := mediainfo.AudioMD5(ctx, path)
	if errors.Is(err, mediainfo.ErrNoFFmpeg) {
		return "", stage.ErrNoDecoder
	}
	return sum, err
}

func (c *Coordinator) placementPrefs(ctx context.Context) MusicPlacementPrefs {
	if c.musicPlace.prefs == nil {
		return MusicPlacementPrefs{}
	}
	return c.musicPlace.prefs(ctx)
}

func (c *Coordinator) musicTagger() stage.Tagger {
	if c.musicPlace.tagger != nil {
		return c.musicPlace.tagger
	}
	return stage.DefaultTagger()
}

func (c *Coordinator) stageDeps() stage.Deps {
	return stage.Deps{Tagger: c.musicTagger(), AudioHash: c.musicPlace.audioHash, Bin: c.imp.Bin(),
		FreeBytes: c.musicPlace.freeBytes, Log: c.log}
}

// lockAlbum holds an album's files for one placement, rename or retag.
func (c *Coordinator) lockAlbum(albumID int64) func() {
	v, _ := c.musicPlace.locks.LoadOrStore(albumID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// placedMeta is what a placed file is recorded with, kept in the staging manifest so a
// file a crash left placed but unrecorded can be recorded on the next start.
type placedMeta struct {
	Quality       music.Quality        `json:"quality"`
	BitrateKbps   int                  `json:"bitrate_kbps,omitempty"`
	SourceRelease string               `json:"source_release,omitempty"`
	TagState      string               `json:"tag_state,omitempty"`
	TagsFound     string               `json:"tags_found,omitempty"`
	Probe         *mediainfo.AudioInfo `json:"probe,omitempty"`
	Extra         bool                 `json:"extra,omitempty"` // a rename's extra file: no row
}

// placement is one file the import will place.
type placement struct {
	t       music.Track
	f       music.AudioFile
	tier    music.Quality
	bitrate int
	probe   *mediainfo.AudioInfo
	target  string
	// albumDir is the album's folder under the naming in effect: cover.jpg and the extras
	// go there (a disc-folder scheme puts tracks one level below it).
	albumDir string
	old      string // the track's file now, when the new one replaces it
	// from is where the file came from in the download when that isn't f.Path: the disc
	// image a split track was cut from (MUS-35). Extras are found beside it.
	from string
	// scratch: f.Path is a split track in staging, Arrmada's own, deleted once placed.
	scratch bool
	// transcode: replace f.Path with a transcoded staging copy before placement.
	transcode MusicTranscodePrefs
	// orig* keep the pre-transcode plan so a failed transcode can fall back safely.
	origTier     music.Quality
	origBitrate  int
	origProbe    *mediainfo.AudioInfo
	origTarget   string
	origAlbumDir string
}

// source is the download file the placement came from.
func (p placement) source() string {
	if p.from != "" {
		return p.from
	}
	return p.f.Path
}

// placeMatch places a matcher result (music.MatchResult) onto the album's target tracks:
// the album already switched to the matched edition when the matcher said so (applyMatch),
// and the assignments are read onto its tracks. Files it couldn't place stay in the
// download; extras come only from inside res.ContentRoot.
func (c *Coordinator) placeMatch(ctx context.Context, a music.Artist, al music.Album, tracks []music.Track, res music.MatchResult, releaseTier music.Quality, release string) albumImport {
	matched, _ := assignmentsOnto(tracks, res)
	return c.placeFiles(ctx, a, al, tracks, matched, releaseTier, release, res.ContentRoot)
}

// placeFiles places files onto an album's target tracks (track id → file). A track that
// already has a file only takes the new one when it is a better tier (music.BetterTier); a
// replaced file goes to the recycle bin first, and with no bin to take it the track is
// left alone. One file is never two tracks (FileOwner). Nothing here ever deletes or
// writes the download.
func (c *Coordinator) placeFiles(ctx context.Context, a music.Artist, al music.Album, tracks []music.Track, matched map[int64]music.AudioFile, releaseTier music.Quality, release, contentRoot string) albumImport {
	var res albumImport
	defer c.lockAlbum(al.ID)()
	prefs := c.placementPrefs(ctx)
	tc, tcErr := c.music.TagContext(ctx, al.ID, al.TargetEditionID)
	if tcErr != nil && prefs.WriteTags {
		c.log.Warn("music import: couldn't read the album's edition for tags — placing the files as they are",
			"album", al.Title, "err", tcErr)
	}

	c.cueHiddenTracks(ctx, tracks, matched)
	var plan []placement
	taken := map[string]bool{} // two files of this import never get one name
	for _, t := range tracks { // in the edition's order
		f, ok := matched[t.ID]
		if !ok {
			continue
		}
		tier, bitrate, probe := c.trackTier(ctx, f.Path, releaseTier)
		outTier, outBitrate, outProbe, outExt, transcode, terr := c.maybeTranscodePlan(ctx, f.Path, tier, bitrate, probe, prefs.Transcode)
		if terr != nil {
			res.failed++
			c.log.Warn("music import: transcoding a track failed — leaving it as downloaded", "track", t.Title, "err", terr)
		}
		if t.HasFile && !music.BetterTier(music.HeldTier(string(outTier), outBitrate), c.heldTier(ctx, t)) {
			res.kept++
			continue
		}
		target, albumDir := c.musicTrackPath(tc, tcErr, a, al, t, outTier, outProbe, outExt)
		if taken[target] {
			res.taken++
			c.log.Warn("music import: two tracks would get the same file name — check the naming template",
				"album", al.Title, "path", target)
			continue
		}
		// One file is never two tracks: a name that is another track's file is left alone.
		if owner, err := c.music.FileOwner(ctx, target); err != nil {
			res.failed++
			c.log.Warn("music import: checking who holds a track's path failed", "track", t.Title, "err", err)
			continue
		} else if owner != 0 && owner != t.ID {
			res.taken++
			c.log.Warn("music import: the file this track would be placed at is another track's — left alone",
				"album", al.Title, "track", t.Title, "path", target)
			continue
		}
		taken[target] = true
		origTarget, origAlbumDir := c.musicTrackPath(tc, tcErr, a, al, t, tier, probe, filepath.Ext(f.Path))
		p := placement{t: t, f: f, tier: outTier, bitrate: outBitrate, probe: outProbe, target: target, albumDir: albumDir, transcode: transcode,
			origTier: tier, origBitrate: bitrate, origProbe: probe, origTarget: origTarget, origAlbumDir: origAlbumDir}
		if t.HasFile {
			p.old = t.FilePath
		}
		plan = append(plan, p)
	}
	if len(plan) == 0 {
		return res
	}
	// A cue-sheet image is split now that it's known which of its tracks are taken.
	plan, splitDone, ok := c.splitPlanned(ctx, al, tracks, plan, releaseTier, &res)
	defer splitDone()
	if !ok {
		return res
	}
	plan, transcodeCleanup, ok := c.prepareTranscodes(ctx, al, plan, &res)
	defer transcodeCleanup()
	if !ok {
		return res
	}

	var placedFrom []placement
	if (prefs.WriteTags && tcErr == nil) || prefs.Transcode.Enabled {
		if err := c.imp.CheckMusicRoot(); err != nil {
			// Staging inside a missing (unmounted) music folder would write to the wrong
			// disk; nothing is placed, and the import is tried again later.
			res.failed += len(plan)
			c.log.Warn("music import: the music folder isn't there — nothing placed", "album", al.Title, "err", err)
			return res
		}
		placedFrom = c.placeStaged(ctx, al, tc, plan, release, &res, prefs.WriteTags && tcErr == nil)
	} else {
		placedFrom = c.placeLinked(ctx, al, plan, release, &res)
	}
	c.bringExtras(al, contentRoot, release, placedFrom, prefs.Extras)
	return res
}

// musicTrackPath is where a track's file goes, and its album's folder: the naming
// templates over the edition, or, without an edition to name from, the fixed scheme
// imports always used.
func (c *Coordinator) musicTrackPath(tc music.TagContext, tcErr error, a music.Artist, al music.Album, t music.Track, tier music.Quality, probe *mediainfo.AudioInfo, ext string) (target, albumDir string) {
	if tcErr == nil {
		facts := music.FileFacts{Quality: tier}
		if probe != nil {
			facts.Codec, facts.BitDepth, facts.SampleRate = probe.Codec, probe.BitsPerSample, probe.SampleRateHz
		}
		if n, ok := tc.Name(t.ID, facts); ok {
			return c.imp.MusicTrackTarget(n, ext), c.imp.MusicAlbumFolder(n)
		}
	}
	target = c.imp.AlbumTrackTarget(a.Name, al.Title, al.Year, t.DiscNumber, t.TrackNumber, t.Title, ext)
	return target, filepath.Dir(target)
}

// tagsFoundJSON keeps a file's tags as found, for music_track_files.tags_json.
func (c *Coordinator) tagsFoundJSON(path string) (audiotag.Tags, string) {
	found, err := c.musicTagger().Read(path)
	if err != nil || len(found) == 0 {
		return nil, ""
	}
	b, err := json.Marshal(found)
	if err != nil {
		return found, ""
	}
	return found, string(b)
}

// placeLinked hardlinks each file straight into the library as it arrived (tags off, or
// no edition to tag from): what imports always did.
func (c *Coordinator) placeLinked(ctx context.Context, al music.Album, plan []placement, release string, res *albumImport) []placement {
	var placed []placement
	for _, p := range plan {
		if err := c.imp.Using(ctx).PlaceAlbumTrack(p.f.Path, p.target, p.old); err != nil {
			if errors.Is(err, library.ErrReplacementRefused) {
				res.refused++
			} else {
				res.failed++
			}
			c.log.Warn("music import: placing a track failed", "track", p.t.Title, "err", err)
			continue
		}
		_, found := c.tagsFoundJSON(p.target)
		if err := c.recordPlaced(ctx, p.t.ID, p.target, placedMeta{Quality: p.tier, BitrateKbps: p.bitrate,
			SourceRelease: release, Probe: p.probe, TagState: music.TagStateAsFound, TagsFound: found}); err != nil {
			res.failed++
			c.log.Warn("music import: recording a track failed", "track", p.t.Title, "err", err)
			continue
		}
		res.placed++
		placed = append(placed, p)
	}
	return placed
}

// recordPlaced records a placed file against its track.
func (c *Coordinator) recordPlaced(ctx context.Context, trackID int64, path string, m placedMeta) error {
	rec := music.FileRecord{Path: path, Quality: m.Quality, BitrateKbps: m.BitrateKbps, SourceRelease: m.SourceRelease,
		Probe: m.Probe, TagState: m.TagState, TagsFound: m.TagsFound}
	if fi, err := os.Stat(path); err == nil {
		rec.SizeBytes, rec.MtimeUnix = fi.Size(), fi.ModTime().Unix()
	}
	return c.music.RecordTrackFile(ctx, trackID, rec)
}

// placeStaged runs the files through a staging job: copy, optional house tags (and the
// cover), verify, one move. A file the tag library can't write (WAV, an odd MP3) goes in
// as it arrived, hardlinked. A write or check that fails sends the album to Reviews with
// the reason; the download is never touched.
func (c *Coordinator) placeStaged(ctx context.Context, al music.Album, tc music.TagContext, plan []placement, release string, res *albumImport, writeTags bool) []placement {
	// Formats the tag library doesn't write (WAV, APE, WavPack…) go straight in as they
	// arrived, without a pointless copy into staging first.
	var asIs []placement
	kept := plan[:0:0]
	for _, p := range plan {
		if taggable(p.f.Path) {
			kept = append(kept, p)
		} else {
			asIs = append(asIs, p)
		}
	}
	placedAsIs := c.placeLinked(ctx, al, asIs, release, res)
	if plan = kept; len(plan) == 0 {
		return placedAsIs
	}
	items := make([]stage.Item, len(plan))
	for i, p := range plan {
		items[i] = stage.Item{Source: p.f.Path, Final: p.target, Ref: p.t.ID, Scratch: p.scratch}
		if _, err := os.Lstat(p.target); err == nil {
			items[i].Replace = p.target // a better copy, or a file no track owns: to the bin first
		}
		if p.old != "" && p.old != p.target && insideDir(p.old, c.imp.MusicDir()) {
			items[i].Retire = p.old
		}
	}
	job, err := stage.Start(c.imp.MusicDir(), stage.Manifest{Kind: stage.KindImport, AlbumID: al.ID, Release: release, Items: items}, c.stageDeps())
	if err != nil {
		res.failed += len(plan)
		c.log.Warn("music import: couldn't start a staging job", "album", al.Title, "err", err)
		return placedAsIs
	}
	if err := job.CheckSpace(); err != nil {
		res.failed += len(plan)
		c.log.Warn("music import: not enough space to stage the copies — trying again later", "album", al.Title, "err", err)
		_ = job.Abandon("no space")
		return placedAsIs
	}
	covers := c.albumCovers(tc)
	cover := covers.front(ctx, 500)
	var embed *audiotag.Picture
	if cover != nil {
		embed = &audiotag.Picture{Data: cover}
	}

	placed := placedAsIs
	for i, p := range plan {
		if err := job.Stage(i); err != nil {
			res.failed++
			c.log.Warn("music import: couldn't copy a track into staging", "track", p.t.Title, "err", err)
			_ = job.Discard(i, err.Error())
			continue
		}
		found, foundJSON := c.tagsFoundJSON(p.f.Path)
		meta := placedMeta{Quality: p.tier, BitrateKbps: p.bitrate, SourceRelease: release, Probe: p.probe,
			TagState: music.TagStateAsFound, TagsFound: foundJSON}
		if writeTags {
			tags, ok := tc.HouseTags(p.t.ID, found)
			if !ok {
				_ = job.Discard(i, "the track isn't on the edition")
				res.failed++
				continue
			}
			meta.TagState = music.TagStateHouse
			if err := job.Tag(ctx, i, tags, embed); err != nil {
				if errors.Is(err, audiotag.ErrUnsupported) {
					// Not a format the tag library writes: the file goes in as it arrived.
					c.log.Info("music import: no house tags for this file type — placing it as downloaded", "file", filepath.Base(p.f.Path))
					placed = append(placed, c.placeLinked(ctx, al, []placement{p}, release, res)...)
					continue
				}
				if errors.Is(err, stage.ErrTransient) || ctx.Err() != nil {
					// The check couldn't run just now (a decode timed out, shutting down): try
					// the import again later rather than send it to Review.
					res.failed++
					c.log.Warn("music import: couldn't check a tagged copy just now — trying again later", "track", p.t.Title, "err", err)
					continue
				}
				res.tagFailed++
				if res.tagWhy == "" {
					res.tagWhy = fmt.Sprintf("%s: %v", filepath.Base(p.f.Path), err)
				}
				c.log.Warn("music import: writing the house tags failed — the download is untouched", "track", p.t.Title, "err", err)
				continue
			}
		}
		if err := setMeta(job, i, meta); err != nil {
			c.log.Warn("music import: couldn't save the staging manifest", "err", err)
		}
		if err := job.Place(i); err != nil {
			if serr := job.Settle(i); serr != nil {
				c.log.Error("music import: a replaced file is in the recycle bin and its new copy is still in staging — it's finished at the next start", "track", p.t.Title, "err", serr)
			}
			if job.Items()[i].State != stage.Placed {
				switch {
				case errors.Is(err, stage.ErrReplaceRefused):
					res.refused++
				case errors.Is(err, stage.ErrTargetTaken):
					res.taken++
				default:
					res.failed++
				}
				c.log.Warn("music import: placing a track failed", "track", p.t.Title, "err", err)
				continue
			}
		}
		if err := c.recordPlaced(ctx, p.t.ID, p.target, meta); err != nil {
			// In the library but not recorded: the job is kept and the next start records it.
			res.failed++
			c.log.Warn("music import: recording a track failed", "track", p.t.Title, "err", err)
			continue
		}
		_ = job.Recorded(i)
		res.placed++
		placed = append(placed, p)
	}
	if len(placed) > 0 {
		if big := covers.front(ctx, 1200); big != nil && music.IsJPEG(big) {
			if _, err := job.PlaceCover(placed[0].albumDir, big); err != nil {
				c.log.Warn("music import: couldn't write cover.jpg", "album", al.Title, "err", err)
			}
		}
	}
	if err := job.Abandon("import finished"); err != nil {
		c.log.Warn("music import: a staging job was kept for the next start", "album", al.Title, "err", err)
	}
	return placed
}

// musicTagReview sends a download to Review when house tags couldn't be written into its
// staged copies, or the copies didn't check out. Retrying would most likely fail the same
// way; the download is untouched, tracks already placed stay placed.
func (c *Coordinator) musicTagReview(ctx context.Context, it download.Item, res albumImport, albumID int64, title string) {
	c.clearMusicRetry(it.Hash)
	c.log.Warn("music import: house tags couldn't be written — sending to review", "what", title, "release", it.Name, "why", res.tagWhy)
	c.addReview(ctx, Review{
		Hash: it.Hash, Name: it.Name, ContentPath: it.ContentPath, MediaType: "music", ReasonCode: ReasonImportFailed,
		ExpectedID: albumID, ExpectedTitle: title, SizeBytes: it.SizeBytes,
		Reason: fmt.Sprintf("Tag write failed for %d file(s): %s (the download is untouched; Settings → Naming & metadata can switch house tags off)", res.tagFailed, res.tagWhy),
	})
}

// taggable reports whether the tag library writes this kind of file (by extension; the
// library itself still checks the content, and anything it refuses goes in as it is).
func taggable(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".flac", ".mp3", ".m4a", ".m4b", ".mp4", ".alac", ".aac", ".ogg", ".oga", ".opus":
		return true
	}
	return false
}

// setMeta stores what a staged file will be recorded with, so a crash between placing and
// recording can be finished on the next start.
func setMeta(job *stage.Job, i int, m placedMeta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return job.SetMeta(i, b)
}

// albumCovers fetches one album's front cover at the sizes it needs. A missing cover never
// stops an import, and after one failure that isn't "no cover" (the Cover Art Archive slow
// or down) it stops asking, so an outage costs an album one timeout.
type albumCovers struct {
	c    *Coordinator
	tc   music.TagContext
	down bool
}

func (c *Coordinator) albumCovers(tc music.TagContext) *albumCovers {
	return &albumCovers{c: c, tc: tc}
}

// front is the cover at size, or nil.
func (a *albumCovers) front(ctx context.Context, size int) []byte {
	if a.c.musicPlace.covers == nil || a.down {
		return nil
	}
	b, err := a.c.musicPlace.covers.Front(ctx, a.tc.Edition.MBID, a.tc.Album.MBID, size)
	if err != nil {
		if !errors.Is(err, music.ErrNoCoverArt) {
			a.down = true
			a.c.log.Info("music: couldn't fetch the album cover — placing without it", "album", a.tc.Album.Title, "err", err)
		}
		return nil
	}
	return b
}

// commonDir is the deepest folder holding every dir.
func commonDir(dirs []string) string {
	if len(dirs) == 0 {
		return ""
	}
	common := filepath.Clean(dirs[0])
	for _, d := range dirs[1:] {
		d = filepath.Clean(d)
		for !insideDir(d, common) {
			parent := filepath.Dir(common)
			if parent == common {
				return ""
			}
			common = parent
		}
	}
	return common
}

// insideDir reports whether path is dir or inside it.
func insideDir(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
