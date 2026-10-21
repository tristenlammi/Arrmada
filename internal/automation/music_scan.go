package automation

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/safego"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The library scan, v2 (MUS-37): catalogue music already in the music folder.
//
//   - Read-only on files. It records what is there, where it is: nothing is moved,
//     renamed, retagged or deleted (placement and retagging are separate, explicit actions).
//   - Incremental. Each album folder's modification time, size and inode (and those of its
//     disc folders) are remembered (music_scan_dirs); an unchanged folder whose files are
//     all still on record is not opened again. A file whose inode is on record under a path
//     that's gone is followed to its new path rather than read as gone and new.
//   - Parallel and bounded. The folder walk, and the reading of new folders (tags and the
//     audio facts in their headers, in-process: no ffprobe), run on a small worker pool
//     (Settings: music_scan_workers, default 4). The one MusicBrainz limiter paces any
//     lookups. A first scan of a huge library goes in chunks (musicScanFoldersPerRun).
//   - Tags first. Each new folder goes through the matcher (MUS-31) against the target
//     editions of the albums its release MBIDs, tags or folder names point at: a release
//     MBID most files share names the album outright, tags and names are weighed as the
//     matcher always does. Only the album's target edition is weighed, and MusicBrainz is
//     only asked for a listing the album doesn't have yet (and for an artist the library
//     doesn't have, which is added unmonitored, as before).
//   - Reviews. A folder the matcher won't place goes to Reviews with its explanation and a
//     Match… action, which records the files in place against the album a person picks.
//
// Thresholds: a folder whose artist and album folder names exactly name a library album is
// judged as if it had been grabbed for that album (the owner filed it there, which is the
// same evidence a grab is); any other folder needs the strict threshold, and a folder with
// nothing but file names to go on isn't placed without that.

// MusicScanResult summarizes a library scan.
type MusicScanResult struct {
	Folders int `json:"folders"` // album folders in the music folder
	Skipped int `json:"skipped"` // unchanged since the last scan: not opened
	Artists int `json:"artists"` // artists newly catalogued
	Albums  int `json:"albums"`  // albums that got files recorded
	Tracks  int `json:"tracks"`  // track files recorded
	Moved   int `json:"moved"`   // recorded files followed to a new path
	// Unmatched are the folders sent to Reviews by this scan (the first
	// musicScanListed); Reviews counts every folder of this scan held there.
	Unmatched []string `json:"unmatched"`
	Reviews   int      `json:"reviews"`
	Failed    int      `json:"failed"`  // couldn't be judged just now (MusicBrainz down): tried again next scan
	Waiting   int      `json:"waiting"` // their album has no track listing on MusicBrainz yet: tried again next scan
	// Held are folders that matched, but some of whose files' tracks still hold another
	// file that's on disk (a duplicate, or a move half done): looked at again next scan.
	Held      int  `json:"held"`
	Remaining int  `json:"remaining"` // left for the next scan by the per-run cap
	Opened    int  `json:"opened"`    // files whose tags were read
	Measured  int  `json:"measured"`  // tracks on record measured with ffprobe afterwards
	Workers   int  `json:"workers"`
	Full      bool `json:"full,omitempty"`
	// Verify is the file check that runs after the scan (MUS-15).
	Verify *MusicVerifyResult `json:"verify,omitempty"`
	// FollowUp says the check found files newly gone and one more incremental pass ran
	// (its counts are in the totals), to link files renamed outside Arrmada.
	FollowUp bool `json:"follow_up,omitempty"`
	// Timings, in milliseconds: the walk, the reading of new files (summed over the
	// workers), and the whole scan.
	WalkMs  int64 `json:"walk_ms"`
	ReadMs  int64 `json:"read_ms"`
	TotalMs int64 `json:"total_ms"`
}

// MusicScanOptions are a scan's choices.
type MusicScanOptions struct {
	// Full looks at every folder again, ignoring what earlier scans remembered.
	Full bool
	// Workers overrides the saved worker count (0: the setting).
	Workers int
	// FoldersPerRun overrides musicScanFoldersPerRun (0: the default).
	FoldersPerRun int

	// followUp marks the one follow-up pass a scan runs after its file check found files
	// newly gone: it never runs another.
	followUp bool
}

// musicScanFoldersPerRun caps the album folders one scan reads. A first scan of a big
// collection is paced by MusicBrainz (an album without a listing costs a call a second),
// so it goes in chunks a run can finish; the result says how many are left.
const musicScanFoldersPerRun = 2000

// musicScanListed caps the folder paths a result lists (Reviews has them all).
const musicScanListed = 200

// minTrackBytes is the smallest file read as a track: under it is artwork, a jingle or a
// broken file.
const minTrackBytes = 256 << 10

// ScanMusicLibrary is ScanMusicLibraryWith with the default options.
func (c *Coordinator) ScanMusicLibrary(ctx context.Context, progress func(done, total int)) (MusicScanResult, error) {
	return c.ScanMusicLibraryWith(ctx, MusicScanOptions{}, progress)
}

// ScanMusicLibraryWith catalogues music already on disk (see the top of this file).
//
// progress, when set, hears how many album folders are done out of how many will be
// read; it is only ever called from the calling goroutine. A cancelled or timed-out scan
// stops handing out folders and returns an error saying how far it got: folders already
// started are finished (an artist one added gets the album's tracks recorded), nothing
// after them is touched, and folders cut short aren't reported as unrecognised.
func (c *Coordinator) ScanMusicLibraryWith(ctx context.Context, opt MusicScanOptions, progress func(done, total int)) (MusicScanResult, error) {
	res := MusicScanResult{Unmatched: []string{}, Full: opt.Full}
	if progress == nil {
		progress = func(int, int) {}
	}
	if c.music == nil || c.imp == nil {
		return res, nil
	}
	if !c.moduleOn(ctx, "music") {
		return res, ErrModuleOff
	}
	// MusicBrainz lookups of a scan wait behind anyone using the app: the background lane
	// yields to interactive requests, and pauses when MusicBrainz refuses (a folder that
	// couldn't be judged then is tried again by the next scan).
	ctx = metadata.WithMBLane(ctx, metadata.MBBackground)
	started := time.Now()
	root := filepath.Clean(c.imp.MusicDir())
	workers := opt.Workers
	if workers <= 0 {
		workers = c.music.ScanWorkers()
	}
	res.Workers = workers
	perRun := opt.FoldersPerRun
	if perRun <= 0 {
		perRun = musicScanFoldersPerRun
	}

	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return res, fmt.Errorf("the music folder %s can't be read: %w", root, errOr(err, errors.New("not a folder")))
	}
	if !library.FolderLooksMounted(root) {
		// An empty music folder is most often a share or array that isn't mounted: a scan
		// then would settle every folder held in Reviews as gone. Nothing is touched.
		return res, fmt.Errorf("%w: %s", ErrMusicFolderEmpty, root)
	}
	walkStart := time.Now()
	dirs, err := walkMusicRoot(ctx, c.log, root, workers)
	res.WalkMs = time.Since(walkStart).Milliseconds()
	if err != nil {
		return res, err
	}
	groups := groupScanDirs(root, dirs)
	res.Folders = len(groups)
	c.log.Info("music scan: walked the library", "root", root, "album_folders", len(groups), "ms", res.WalkMs)

	st, err := c.newScanState(ctx, root, opt.Full)
	if err != nil {
		return res, err
	}
	// Folders unchanged since the last scan, with their files still on record, are done.
	// Folders waiting in Reviews whose folder hasn't changed only need their files' sizes
	// and times checked (a retag in place), which is cheap: they don't count towards the
	// per-run cap, so a long Reviews list can never hold new folders back.
	// Folders still waiting for their album's track listing go after everything else, so
	// a backlog of them never holds new folders back either.
	var todo, recheck, waiting []*scanGroup
	for _, g := range groups {
		switch {
		case st.unchanged(g):
			res.Skipped++
		case st.heldUnchanged(g):
			recheck = append(recheck, g)
		case st.waitingUnchanged(g):
			waiting = append(waiting, g)
		default:
			todo = append(todo, g)
		}
	}
	todo = append(todo, waiting...)
	if len(todo) > perRun {
		res.Remaining = len(todo) - perRun
		todo = todo[:perRun]
	}
	todo = append(recheck, todo...)
	st.settleGone(ctx, groups)

	total := len(todo)
	progress(0, total)
	if err := ctx.Err(); err != nil {
		return res, fmt.Errorf("the scan stopped after 0 of %d album folders: %w", total, err)
	}
	// Folders go out to the workers one at a time; progress is reported, and what the
	// workers found written, from this goroutine only.
	wctx := context.WithoutCancel(ctx) // a folder started is finished, even if the scan is stopped
	jobs := make(chan *scanGroup)
	done := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		safego.Go(c.log, "music-scan-worker", func() {
			defer wg.Done()
			for g := range jobs {
				st.scanFolder(wctx, g)
				done <- struct{}{}
			}
		})
	}
	sent, finished := 0, 0
	var stopped error
	stop := ctx.Done()
	for finished < total && (stopped == nil || finished < sent) {
		var send chan *scanGroup
		var g *scanGroup
		if stopped == nil && sent < total {
			send, g = jobs, todo[sent]
		}
		select {
		case send <- g:
			sent++
		case <-done:
			finished++
			progress(finished, total)
			st.flush(wctx, false)
		case <-stop:
			stopped, stop = ctx.Err(), nil // no new folders; the ones started finish
		}
	}
	close(jobs)
	wg.Wait()
	st.flush(wctx, true)
	st.fill(&res)
	if stopped != nil && finished < total {
		return res, fmt.Errorf("the scan stopped after %d of %d album folders: %w", finished, total, stopped)
	}
	res.Measured = c.measureUnmeasuredTracks(ctx, musicMeasurePerScan)
	// Then the verify pass (MUS-15), after moved files were followed: files gone from disk
	// and files changed outside Arrmada, with its guards.
	if v, err := c.verifyMusicFiles(ctx, false); err == nil {
		res.Verify = &v
		// Files it newly found gone may only be renamed (and retagged, so not followed
		// by inode): their tracks can only take the new files now that the check has
		// noted the old ones. One incremental pass more links them; its own check never
		// asks for another, so a scan never loops.
		if !opt.followUp && v.NotFound > 0 && v.Refused == "" && ctx.Err() == nil {
			fopt := opt
			fopt.Full, fopt.followUp = false, true
			if fu, err := c.ScanMusicLibraryWith(ctx, fopt, nil); err == nil {
				res.Tracks += fu.Tracks
				res.Albums += fu.Albums
				res.Moved += fu.Moved
				res.Artists += fu.Artists
				res.Held, res.Waiting = fu.Held, fu.Waiting
				res.FollowUp = true
			} else if ctx.Err() == nil {
				c.log.Warn("music scan: the follow-up pass after the file check failed", "err", err)
			}
		}
	} else if ctx.Err() == nil {
		c.log.Warn("music scan: the file check after the scan failed", "err", err)
	}
	res.TotalMs = time.Since(started).Milliseconds()
	if raw, err := json.Marshal(res); err == nil {
		if err := c.music.SaveScanRun(context.WithoutCancel(ctx), started, time.Now(), string(raw)); err != nil {
			c.log.Warn("music scan: couldn't keep the scan's summary", "err", err)
		}
	}
	c.log.Info("music scan: finished", "folders", res.Folders, "skipped", res.Skipped, "artists", res.Artists,
		"albums", res.Albums, "tracks", res.Tracks, "moved", res.Moved, "reviews", res.Reviews, "failed", res.Failed,
		"remaining", res.Remaining, "opened", res.Opened, "measured", res.Measured, "ms", res.TotalMs)
	return res, nil
}

// ErrMusicFolderEmpty refuses a scan of a music folder with nothing in it: most often a
// share that isn't mounted, which a scan must never read as "everything's gone".
var ErrMusicFolderEmpty = errors.New("the music folder is empty or not mounted, so nothing was scanned — check the share or array is mounted")

// maxScanCandidates bounds the albums one folder is weighed against.
const maxScanCandidates = 5

// mbDown reports whether an error is MusicBrainz not answering (worth trying again later,
// never a reason to judge a folder).
func mbDown(err error) bool { return errors.Is(err, metadata.ErrMusicBrainzUnavailable) }

func errOr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

// --- the walk ---------------------------------------------------------------------------

// scanDirInfo is one folder the walk saw: its signature (modification time, size, inode)
// and the audio files directly in it.
type scanDirInfo struct {
	sig   string
	audio []string
}

// walkMusicRoot walks the music folder with up to workers folders read at once, skipping
// the recycle bin, staging and other hidden Arrmada folders (library.SkipScanDir). Every
// folder is listed (its own signature is part of its album's); files are not stat'ed here.
// A folder that can't be read is left out with a warning; the root failing is an error.
func walkMusicRoot(ctx context.Context, log *slog.Logger, root string, workers int) (map[string]*scanDirInfo, error) {
	var (
		mu      sync.Mutex
		dirs    = map[string]*scanDirInfo{}
		wg      sync.WaitGroup
		sem     = make(chan struct{}, max(workers, 1))
		rootErr error
	)
	var visit func(dir string)
	visit = func(dir string) {
		defer wg.Done()
		if ctx.Err() != nil {
			return
		}
		sem <- struct{}{}
		fi, serr := os.Lstat(dir)
		entries, err := os.ReadDir(dir)
		<-sem
		if serr != nil || err != nil {
			if dir == root {
				mu.Lock()
				rootErr = errOr(err, serr)
				mu.Unlock()
				return
			}
			log.Warn("music scan: a folder couldn't be read — left out of this scan", "folder", dir, "err", errOr(err, serr))
			return
		}
		info := &scanDirInfo{sig: dirSig(fi)}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				if library.SkipScanDir(name) {
					continue
				}
				wg.Add(1)
				sub := filepath.Join(dir, name)
				safego.Go(log, "music-scan-walk", func() { visit(sub) })
				continue
			}
			if e.Type().IsRegular() && music.IsAudioFile(name) {
				info.audio = append(info.audio, filepath.Join(dir, name))
			}
		}
		mu.Lock()
		dirs[dir] = info
		mu.Unlock()
	}
	wg.Add(1)
	visit(root)
	wg.Wait()
	if rootErr != nil {
		return nil, fmt.Errorf("the music folder %s can't be read: %w", root, rootErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("the scan stopped while walking the music folder: %w", err)
	}
	return dirs, nil
}

// dirSig is a folder's own signature: modification time, size and inode.
func dirSig(fi fs.FileInfo) string {
	return strconv.FormatInt(fi.ModTime().UnixNano(), 10) + ":" + strconv.FormatInt(fi.Size(), 10) + ":" + music.FileIdentity(fi)
}

// scanGroup is one album folder: the folder (a disc folder's parent), the folders its
// audio is in, the audio files, and its signature.
type scanGroup struct {
	path  string
	dirs  []string
	audio []string
	sig   string
	// artist and album are what the folder names say ("Artist/Album", or "Artist - Album"
	// directly under the root); year when one is in the name.
	artist, album string
	year          int
}

// groupScanDirs turns the walk into album folders: every folder with audio in it, a disc
// folder ("CD1", "Disc 2") joining its parent's group so a multi-disc album is one.
func groupScanDirs(root string, dirs map[string]*scanDirInfo) []*scanGroup {
	byPath := map[string]*scanGroup{}
	for dir, info := range dirs {
		if len(info.audio) == 0 {
			continue
		}
		key := dir
		if parent := filepath.Dir(dir); music.DiscOfFolder(filepath.Base(dir)) > 0 && !sameDir(parent, root) && within(root, parent) {
			key = parent
		}
		g := byPath[key]
		if g == nil {
			g = &scanGroup{path: key}
			byPath[key] = g
		}
		g.dirs = append(g.dirs, dir)
		g.audio = append(g.audio, info.audio...)
	}
	out := make([]*scanGroup, 0, len(byPath))
	for _, g := range byPath {
		if !contains(g.dirs, g.path) {
			if _, ok := dirs[g.path]; ok {
				g.dirs = append(g.dirs, g.path) // the parent of disc folders: its own signature counts
			}
		}
		sort.Strings(g.dirs)
		sort.Strings(g.audio)
		h := sha1.New()
		for _, d := range g.dirs {
			info := dirs[d]
			fmt.Fprintf(h, "%s\t%s\t%d\n", d, info.sig, len(info.audio))
		}
		g.sig = hex.EncodeToString(h.Sum(nil))[:24]
		g.artist, g.album, g.year = folderNames(root, g.path)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// within reports whether p is inside root (or is it).
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// folderNames reads an album folder's artist, album and year from where it sits: the
// conventional <Artist>/<Album>, or "Artist - Album" directly under the root.
func folderNames(root, dir string) (artist, album string, year int) {
	base := filepath.Base(dir)
	parent := filepath.Dir(dir)
	rel := music.ParseRelease(base)
	if sameDir(parent, root) {
		return rel.Artist, rel.Album, rel.Year
	}
	// ParseRelease strips a "(1997)" tag; without a " - " it leaves the whole string as
	// Artist, which for an album folder IS the album title.
	album, year = rel.Artist, rel.Year
	if rel.Album != "" {
		album = rel.Album
	}
	return strings.TrimSpace(filepath.Base(parent)), strings.TrimSpace(album), year
}

// --- the scan's state -------------------------------------------------------------------

// scanState is one scan's shared state: what is on record, what earlier scans remember,
// the library's artists and albums, and what this scan has found. The workers share it;
// every field written after newScanState is guarded by mu.
type scanState struct {
	c    *Coordinator
	root string
	full bool

	known    map[string]music.ScanKnownFile   // by path
	byFileID map[string][]music.ScanKnownFile // by inode
	inDir    map[string]int                   // recorded files per folder
	cache    map[string]music.ScanDir         // music_scan_dirs
	reviews  map[string]int64                 // pending scan reviews, by folder
	artists  *scanArtists

	mu        sync.Mutex
	claimed   map[int64]bool // file rows a moved file has already been matched to
	dirsOut   []music.ScanDir
	reviewOut []Review
	resolve   []int64 // pending scan reviews whose folders are now recorded
	res       MusicScanResult
	readNs    atomic.Int64
}

func (c *Coordinator) newScanState(ctx context.Context, root string, full bool) (*scanState, error) {
	st := &scanState{c: c, root: root, full: full, known: map[string]music.ScanKnownFile{},
		byFileID: map[string][]music.ScanKnownFile{}, inDir: map[string]int{}, claimed: map[int64]bool{}}
	files, err := c.music.ScanKnownFiles(ctx)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		st.known[f.Path] = f
		if f.FileID != "" {
			st.byFileID[f.FileID] = append(st.byFileID[f.FileID], f)
		}
		st.inDir[filepath.Dir(f.Path)]++
	}
	if st.cache, err = c.music.ScanDirs(ctx); err != nil {
		return nil, err
	}
	if st.reviews, err = c.pendingScanReviews(ctx); err != nil {
		return nil, err
	}
	if st.artists, err = c.newScanArtists(ctx); err != nil {
		return nil, err
	}
	return st, nil
}

// recordedIn counts the files on record in a group's folders.
func (st *scanState) recordedIn(g *scanGroup) int {
	n := 0
	for _, d := range g.dirs {
		n += st.inDir[d]
	}
	return n
}

// unchanged reports whether a folder can be skipped without opening anything: a done
// folder whose signature is the remembered one, with at least as many files on record as
// when it was judged. A folder in Reviews is checked by its files' sizes and times (a
// retag in place doesn't change the folder), in the worker.
func (st *scanState) unchanged(g *scanGroup) bool {
	if st.full {
		return false
	}
	d, ok := st.cache[g.path]
	return ok && d.State == music.ScanDirDone && d.Sig == g.sig && st.recordedIn(g) >= d.Files
}

// heldUnchanged reports whether a folder is one waiting in Reviews whose folder signature
// is the remembered one (its files are checked in the worker).
func (st *scanState) heldUnchanged(g *scanGroup) bool {
	if st.full {
		return false
	}
	d, ok := st.cache[g.path]
	return ok && d.State == music.ScanDirUnmatched && d.Sig == g.sig
}

// waitingUnchanged reports whether a folder last waited for its album's track listing and
// hasn't changed since.
func (st *scanState) waitingUnchanged(g *scanGroup) bool {
	d, ok := st.cache[g.path]
	return ok && d.State == music.ScanDirWaiting && d.Sig == g.sig
}

// settleGone resolves pending scan reviews whose folder isn't in the music folder any more
// (renamed, moved or deleted since): the folder they describe is gone. Only a folder that
// is really not there counts; one the walk couldn't read stays in Reviews.
func (st *scanState) settleGone(ctx context.Context, groups []*scanGroup) {
	if len(st.reviews) == 0 {
		return
	}
	present := make(map[string]bool, len(groups))
	for _, g := range groups {
		present[g.path] = true
	}
	for path, id := range st.reviews {
		if present[path] || !within(st.root, path) {
			continue
		}
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := st.c.resolveReview(ctx, id, ResolutionRemoved); err != nil {
			st.c.log.Warn("music scan: couldn't settle the review of a folder that's gone", "folder", path, "err", err)
			continue
		}
		// Forgotten too: if the folder comes back (a share remounted), it is judged and
		// held again rather than skipped as already looked at.
		if err := st.c.music.ForgetScanDir(ctx, path); err != nil {
			st.c.log.Warn("music scan: couldn't forget a folder that's gone", "folder", path, "err", err)
		}
	}
}

// remember queues a folder's state to write with the next flush.
func (st *scanState) remember(d music.ScanDir) {
	st.mu.Lock()
	st.dirsOut = append(st.dirsOut, d)
	st.mu.Unlock()
}

// flush writes what the workers found: folder states, new reviews and settled ones. Done
// every so often (always at the end), so a scan stopped part way keeps its progress.
func (st *scanState) flush(ctx context.Context, final bool) {
	st.mu.Lock()
	if !final && len(st.dirsOut) < 200 && len(st.reviewOut) < 50 {
		st.mu.Unlock()
		return
	}
	dirs, reviews, resolve := st.dirsOut, st.reviewOut, st.resolve
	st.dirsOut, st.reviewOut, st.resolve = nil, nil, nil
	st.mu.Unlock()
	if err := st.c.music.SaveScanDirs(ctx, dirs); err != nil {
		st.c.log.Warn("music scan: couldn't remember the folders it judged — the next scan reads them again", "err", err)
	}
	if len(reviews) > 0 {
		st.c.addScanReviews(ctx, reviews)
	}
	for _, id := range resolve {
		if err := st.c.resolveReview(ctx, id, ResolutionImported); err != nil {
			st.c.log.Warn("music scan: couldn't settle a review whose folder is now recorded", "review", id, "err", err)
		}
	}
}

// fill copies the scan's counts into res.
func (st *scanState) fill(res *MusicScanResult) {
	st.mu.Lock()
	defer st.mu.Unlock()
	res.Albums, res.Tracks, res.Moved = st.res.Albums, st.res.Tracks, st.res.Moved
	res.Reviews, res.Failed, res.Waiting, res.Held, res.Opened = st.res.Reviews, st.res.Failed, st.res.Waiting, st.res.Held, st.res.Opened
	res.Unmatched = append(res.Unmatched, st.res.Unmatched...)
	res.Skipped += st.res.Skipped
	res.Artists = st.artists.added()
	res.ReadMs = st.readNs.Load() / int64(time.Millisecond)
}

// --- one folder --------------------------------------------------------------------------

// scanFile is one audio file of a folder, as stat'ed.
type scanFile struct {
	path        string
	size, mtime int64
	fileID      string
	known       bool
}

// scanFolder judges one album folder: follows moved files, and matches and records new
// ones, or holds the folder for review.
func (st *scanState) scanFolder(ctx context.Context, g *scanGroup) {
	c := st.c
	files := make([]scanFile, 0, len(g.audio))
	for _, p := range g.audio {
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() < minTrackBytes {
			continue
		}
		files = append(files, scanFile{path: p, size: fi.Size(), mtime: fi.ModTime().Unix(), fileID: music.FileIdentity(fi)})
	}
	fsig := filesSig(files)
	if d, ok := st.cache[g.path]; ok && !st.full && d.State == music.ScanDirUnmatched && d.Sig == g.sig && d.FilesSig == fsig {
		st.mu.Lock()
		st.res.Skipped++
		if _, pending := st.reviews[g.path]; pending {
			st.res.Reviews++
		}
		st.mu.Unlock()
		return
	}
	recorded, fresh := 0, 0
	for i := range files {
		f := &files[i]
		if _, ok := st.known[f.path]; ok {
			f.known = true
			recorded++
			continue
		}
		if st.followMove(ctx, f) {
			f.known = true
			recorded++
			continue
		}
		fresh++
	}
	if fresh == 0 {
		st.folderDone(g, fsig, recorded)
		return
	}

	// Read every file of the folder, not only the new ones: the matcher judges the album
	// as a whole (a folder of 12 with 2 new files is still the 12-track album).
	start := time.Now()
	mfiles := make([]music.MatchFile, 0, len(files))
	props := map[string]*mediainfo.AudioInfo{}
	for _, f := range files {
		tags, info, err := c.readScanFile(f.path)
		mf := music.MatchFile{Path: f.path, SizeBytes: f.size, FolderDisc: music.DiscOfFolder(filepath.Base(filepath.Dir(f.path)))}
		if err == nil {
			mf.Tags = tags
			if info != nil {
				mf.DurationMs = int(info.DurationSec * 1000)
				props[f.path] = info
			}
		}
		mfiles = append(mfiles, mf)
	}
	st.readNs.Add(int64(time.Since(start)))
	st.mu.Lock()
	st.res.Opened += len(files)
	st.mu.Unlock()

	id := st.identify(ctx, g, mfiles)
	if id.transient {
		c.log.Info("music scan: couldn't judge a folder just now — the next scan tries again", "folder", g.path, "why", id.why)
		st.mu.Lock()
		st.res.Failed++
		st.mu.Unlock()
		return
	}
	var cands []music.MatchCandidate
	seen := map[int64]bool{}
	namedListed := false
	for _, albumID := range id.albums {
		if seen[albumID] || len(seen) >= maxScanCandidates {
			continue
		}
		seen[albumID] = true
		cs, err := c.music.TargetCandidates(ctx, albumID)
		if err != nil {
			if mbDown(err) || ctx.Err() != nil {
				c.log.Info("music scan: couldn't read an album's listing just now — the next scan tries again", "folder", g.path, "err", err)
				st.mu.Lock()
				st.res.Failed++
				st.mu.Unlock()
				return
			}
			c.log.Warn("music scan: reading an album's listing failed", "folder", g.path, "album_id", albumID, "err", err)
			continue
		}
		namedListed = namedListed || (albumID == id.named && len(cs) > 0)
		cands = append(cands, cs...)
	}
	if len(id.albums) > 0 && (len(cands) == 0 || (id.named > 0 && !namedListed)) {
		// The album the folder is (or its name says it is) has no track listing on
		// MusicBrainz yet: nothing to place the files against, and judging them against
		// other albums meanwhile would be a guess. Looked at again by the next scan.
		c.log.Info("music scan: the album has no track listing yet — the next scan looks again", "folder", g.path)
		st.remember(music.ScanDir{Path: g.path, Sig: g.sig, FilesSig: fsig, Files: recorded, State: music.ScanDirWaiting})
		st.mu.Lock()
		st.res.Waiting++
		st.mu.Unlock()
		return
	}
	in := music.MatchInput{
		Files: mfiles, FolderName: folderName(g), Candidates: cands, GrabbedAlbumID: id.named, ExpectedFromNames: id.named > 0,
		Settings: c.music.MatchSettings(), Policy: c.music.EditionPolicy(),
	}
	if id.named > 0 {
		if w, err := c.music.AlbumWanted(ctx, id.named); err == nil {
			in.Wanted = w
		}
	}
	var res music.MatchResult
	if len(cands) > 0 {
		res = music.Match(in)
	}
	if len(cands) == 0 || res.Decision == music.MatchReview {
		st.holdFolder(ctx, g, fsig, id, res, len(cands) > 0)
		return
	}
	n, held, ok := st.recordMatch(ctx, g, res, files, props)
	if !ok {
		return // not remembered: the next scan tries again
	}
	if held > 0 {
		// A matched file went unrecorded because its track still holds another file that's
		// there (or couldn't be checked). Never cached as done: the folder is looked at
		// again by the next scan, after the folders that changed, so once the other file is
		// gone (a rename finished, a remount) this one is linked.
		st.remember(music.ScanDir{Path: g.path, Sig: g.sig, FilesSig: fsig, Files: recorded + n, State: music.ScanDirWaiting})
		st.mu.Lock()
		st.res.Held++
		st.mu.Unlock()
		return
	}
	st.folderDone(g, fsig, recorded+n)
}

// filesSig is the signature of a folder's audio files: names, sizes and times.
func filesSig(files []scanFile) string {
	h := sha1.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\t%d\t%d\n", f.path, f.size, f.mtime)
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// folderName is the name the matcher reads an album folder by: "Artist - Album (Year)"
// from where it sits, so a folder of untagged files still says what it is.
func folderName(g *scanGroup) string {
	switch {
	case g.artist != "" && g.album != "":
		s := g.artist + " - " + g.album
		if g.year > 0 {
			s += " (" + strconv.Itoa(g.year) + ")"
		}
		return s
	case g.album != "":
		return g.album
	}
	return filepath.Base(g.path)
}

// followMove matches a file whose path isn't on record to a recorded file with the same
// inode whose own path is gone: the file was renamed or its folder was, and the record
// follows it.
func (st *scanState) followMove(ctx context.Context, f *scanFile) bool {
	if f.fileID == "" {
		return false
	}
	for _, k := range st.byFileID[f.fileID] {
		if k.Path == f.path || !sameRecordedFile(k, *f, time.Now()) {
			continue
		}
		if _, err := os.Lstat(k.Path); !errors.Is(err, fs.ErrNotExist) {
			continue // still there: a second name for the same file (a hardlink)
		}
		st.mu.Lock()
		taken := st.claimed[k.ID]
		st.claimed[k.ID] = true
		st.mu.Unlock()
		if taken {
			continue
		}
		ok, err := st.c.music.FollowMovedFile(ctx, k.ID, k.Path, f.path, f.size, f.mtime, f.fileID)
		if err != nil || !ok {
			st.c.log.Warn("music scan: couldn't follow a moved file", "from", k.Path, "to", f.path, "err", err)
			continue
		}
		st.c.log.Info("music scan: followed a moved file", "from", k.Path, "to", f.path)
		st.mu.Lock()
		st.res.Moved++
		st.mu.Unlock()
		return true
	}
	return false
}

// musicFollowWindow is how long after a file was marked missing the scan may still follow
// it by inode. A filesystem hands a freed inode to the next new file, so an old missing
// record is never matched to whatever has its number now.
const musicFollowWindow = 3 * 24 * time.Hour

// sameRecordedFile reports whether a file found at a new path is the recorded one moved:
// the same inode (the caller's lookup), and the size and modification time recorded for it,
// both known — a rename keeps both, a new file that was given a freed inode has neither.
// A record missing for longer than musicFollowWindow isn't followed at all; the file goes
// through the matcher as a new one.
func sameRecordedFile(k music.ScanKnownFile, f scanFile, now time.Time) bool {
	if k.SizeBytes <= 0 || k.MtimeUnix <= 0 || k.SizeBytes != f.size || k.MtimeUnix != f.mtime {
		return false
	}
	if k.MissingSince != "" {
		since := parseTime(k.MissingSince)
		if since.IsZero() || now.Sub(since) > musicFollowWindow {
			return false
		}
	}
	return true
}

// folderDone remembers a folder whose files are all accounted for; a pending review of it
// is settled.
func (st *scanState) folderDone(g *scanGroup, fsig string, recorded int) {
	st.remember(music.ScanDir{Path: g.path, Sig: g.sig, FilesSig: fsig, Files: recorded, State: music.ScanDirDone})
	if id, ok := st.reviews[g.path]; ok && recorded > 0 {
		st.mu.Lock()
		st.resolve = append(st.resolve, id)
		st.mu.Unlock()
	}
}

// recordMatch records the files a confident match placed, where they are: only files not
// on record yet, on tracks without a file that's still there (RecordScanned). It returns
// how many were recorded and how many were held back by a track's other file; ok is false
// when the recording failed (the folder is tried again by the next scan).
func (st *scanState) recordMatch(ctx context.Context, g *scanGroup, res music.MatchResult, files []scanFile, props map[string]*mediainfo.AudioInfo) (recorded, held int, ok bool) {
	c := st.c
	byPath := make(map[string]scanFile, len(files))
	for _, f := range files {
		byPath[f.path] = f
	}
	var out []music.ScannedTrack
	for _, a := range res.Assignments {
		f, ok := byPath[a.File.Path]
		if !ok || f.known {
			continue
		}
		rec := music.FileRecord{Path: f.path, SizeBytes: f.size, MtimeUnix: f.mtime, FileID: f.fileID,
			Quality: music.Quality(music.FormatOf(f.path))}
		if info := props[f.path]; info != nil {
			if q := music.TierFromProbe(*info); q != music.QualityUnknown {
				rec.Quality, rec.BitrateKbps = q, info.BitrateKbps
			}
			rec.Props = info
		}
		out = append(out, music.ScannedTrack{TrackID: a.TrackID, Rec: rec})
	}
	if len(out) == 0 {
		return 0, 0, true
	}
	n, held, err := c.music.RecordScanned(ctx, out)
	if err != nil {
		c.log.Warn("music scan: recording an album's files failed", "folder", g.path, "err", err)
		st.mu.Lock()
		st.res.Failed++
		st.mu.Unlock()
		return 0, 0, false
	}
	if n > 0 {
		title := ""
		var artistID int64
		if al, err := c.music.AlbumRecord(ctx, res.AlbumID); err == nil {
			title, artistID = al.Title, al.ArtistID
		}
		c.music.AddEvent(ctx, artistID, "imported", fmt.Sprintf("Found %d track(s) of %q during a library scan", n, title))
		st.mu.Lock()
		st.res.Albums++
		st.res.Tracks += n
		st.mu.Unlock()
	}
	return n, held, true
}

// holdFolder sends a folder the matcher wouldn't place to Reviews, with its explanation.
func (st *scanState) holdFolder(ctx context.Context, g *scanGroup, fsig string, id scanIdent, res music.MatchResult, weighed bool) {
	r := Review{
		Hash: scanReviewKey(g.path), Name: st.relName(g.path), ContentPath: g.path, MediaType: "music",
		ParsedTitle: g.album,
	}
	switch {
	case !weighed:
		r.ReasonCode = ReasonUnmatched
		r.Reason = id.why
		if r.Reason == "" {
			r.Reason = "Couldn't tell which album this is: nothing in your library fits its tags or folder names."
		}
	default:
		r.ReasonCode, r.Reason = res.Reason, res.Summary
		if res.Best != nil && res.Reason != music.MatchReasonUnmatched {
			r.ExpectedID, r.ExpectedTitle = res.Best.Cand.AlbumID, res.Best.Cand.AlbumTitle
		}
		if raw, err := json.Marshal(res.Report); err == nil {
			r.Match = raw
		}
	}
	for _, f := range g.audio {
		if fi, err := os.Lstat(f); err == nil {
			r.SizeBytes += fi.Size()
		}
	}
	st.remember(music.ScanDir{Path: g.path, Sig: g.sig, FilesSig: fsig, Files: st.recordedIn(g), State: music.ScanDirUnmatched})
	st.mu.Lock()
	defer st.mu.Unlock()
	st.res.Reviews++
	if _, pending := st.reviews[g.path]; pending {
		return // already waiting in Reviews
	}
	st.reviewOut = append(st.reviewOut, r)
	if len(st.res.Unmatched) < musicScanListed {
		st.res.Unmatched = append(st.res.Unmatched, g.path)
	}
}

// relName is a folder's path under the music folder, for a review's name.
func (st *scanState) relName(p string) string {
	if rel, err := filepath.Rel(st.root, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return p
}

// --- which album might it be -------------------------------------------------------------

// scanIdent is what a folder's tags and names point at.
type scanIdent struct {
	albums []int64 // candidate albums, strongest evidence first
	named  int64   // the album the folder's artist and album names name exactly (0: none)
	why    string  // when nothing fits: why, in words
	// transient: MusicBrainz couldn't answer, so nothing can be said yet.
	transient bool
}

// identify finds the albums a folder might be, locally where it can: an album a release
// MBID most files carry is stored under; a release-group MBID; then the artist the album
// artist MBID, tags or folder name (or an alias, MUS-38) say, and that artist's albums
// whose title the tags or folder name say. An artist the library doesn't have is looked up
// on MusicBrainz and added unmonitored when exactly one artist has that name.
func (st *scanState) identify(ctx context.Context, g *scanGroup, files []music.MatchFile) scanIdent {
	c := st.c
	var id scanIdent
	add := func(ids ...int64) {
		for _, x := range ids {
			if x > 0 && !containsID(id.albums, x) {
				id.albums = append(id.albums, x)
			}
		}
	}
	rels := map[string]int{}
	groups := map[string]bool{}
	var tagArtists, tagAlbums, artistMBIDs []string
	for _, f := range files {
		if m := strings.ToLower(f.Tags.ReleaseMBID); m != "" {
			rels[m]++
		}
		if m := strings.ToLower(f.Tags.ReleaseGroupMBID); m != "" {
			groups[m] = true
		}
		if f.Tags.AlbumArtistMBID != "" {
			artistMBIDs = append(artistMBIDs, f.Tags.AlbumArtistMBID)
		}
		a := f.Tags.AlbumArtist
		if a == "" {
			a = f.Tags.Artist
		}
		tagArtists = append(tagArtists, a)
		tagAlbums = append(tagAlbums, music.StripDiscSuffix(f.Tags.Album))
	}
	for rel, n := range rels {
		if float64(n) >= 0.75*float64(len(files)) {
			if albumID, err := c.music.LocalAlbumOfRelease(ctx, rel); err == nil {
				add(albumID)
			}
		}
	}
	for rg := range groups {
		add(st.artists.albumByRG(rg))
	}
	// Files whose MBIDs already name a library album need no artist looked up on
	// MusicBrainz: the names below are only checked against the library.
	remote := len(id.albums) == 0

	// The folder's own names: the old scan's rule, and the strongest local evidence.
	if g.artist != "" && g.album != "" {
		if artistID, _, _ := st.artists.find(ctx, g.artist, "", remote); artistID > 0 {
			if albumID := st.artists.albumTitled(artistID, g.album); albumID > 0 {
				id.named = albumID
				add(albumID)
			}
		}
	}
	tagArtist, _ := plurality(tagArtists)
	tagAlbum, _ := plurality(tagAlbums)
	artistMBID, _ := plurality(artistMBIDs)
	var why string
	for _, try := range []struct{ name, mbid string }{{tagArtist, artistMBID}, {g.artist, ""}} {
		if try.name == "" && try.mbid == "" {
			continue
		}
		artistID, w, transient := st.artists.find(ctx, try.name, try.mbid, remote)
		if transient {
			id.transient, id.why = true, w
			return id
		}
		if artistID == 0 {
			if why == "" {
				why = w
			}
			continue
		}
		found := false
		for _, title := range []string{tagAlbum, g.album} {
			if albumID := st.artists.albumTitled(artistID, title); albumID > 0 {
				add(albumID)
				found = true
			}
		}
		if !found && why == "" {
			title := tagAlbum
			if title == "" {
				title = g.album
			}
			why = fmt.Sprintf("%s has no album called %q in your library (it may be a release type the artist's options don't list).",
				st.artists.name(artistID), title)
		}
	}
	if len(id.albums) == 0 {
		id.why = why
	}
	return id
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// plurality is the most common non-empty value (fold-compared) and how many share it.
func plurality(vals []string) (string, int) {
	counts := map[string]int{}
	first := map[string]string{}
	var order []string
	for _, v := range vals {
		k := music.NormKey(v)
		if k == "" {
			continue
		}
		if counts[k] == 0 {
			order = append(order, k)
			first[k] = strings.TrimSpace(v)
		}
		counts[k]++
	}
	best := ""
	for _, k := range order {
		if best == "" || counts[k] > counts[best] {
			best = k
		}
	}
	return first[best], counts[best]
}

// readScanFile reads a file's tags and the audio facts in its headers. The test seam
// musicTagsFn stands in for both (facts from its length only).
func (c *Coordinator) readScanFile(path string) (music.FileTags, *mediainfo.AudioInfo, error) {
	if c.musicTagsFn != nil {
		tags, ms, err := c.musicTagsFn(path)
		if err != nil || ms <= 0 {
			return tags, nil, err
		}
		return tags, &mediainfo.AudioInfo{DurationSec: float64(ms) / 1000}, nil
	}
	return readAudioFacts(path)
}

// --- artists -----------------------------------------------------------------------------

// scanArtists resolves the names a scan meets to library artists, once per name: the
// library's names and aliases first, then MusicBrainz (an exact, single match on the name
// or an alias is added, unmonitored). It also knows every library album by artist, title
// and release-group MBID. Safe for the workers to share.
type scanArtists struct {
	c  *Coordinator
	ix *music.NameIndex

	mu       sync.Mutex
	inLib    map[int64]string            // library artists: id → name
	albums   map[int64][]music.ScanAlbum // by artist
	byRG     map[string]int64            // release-group MBID → album
	resolved map[string]scanArtistAnswer // by "n:"+folded name or "m:"+mbid
	locks    map[string]*sync.Mutex
	newCount int
}

type scanArtistAnswer struct {
	id        int64
	why       string
	transient bool
}

func (c *Coordinator) newScanArtists(ctx context.Context) (*scanArtists, error) {
	ix, err := c.music.NameIndex(ctx)
	if err != nil {
		return nil, err
	}
	lib, err := c.music.LibraryArtists(ctx)
	if err != nil {
		return nil, err
	}
	albums, err := c.music.ScanAlbums(ctx)
	if err != nil {
		return nil, err
	}
	sa := &scanArtists{c: c, ix: ix, inLib: map[int64]string{}, albums: map[int64][]music.ScanAlbum{},
		byRG: map[string]int64{}, resolved: map[string]scanArtistAnswer{}, locks: map[string]*sync.Mutex{}}
	for _, a := range lib {
		sa.inLib[a.ID] = a.Name
	}
	sa.addAlbums(albums)
	return sa, nil
}

func (sa *scanArtists) addAlbums(albums []music.ScanAlbum) {
	for _, al := range albums {
		sa.albums[al.ArtistID] = append(sa.albums[al.ArtistID], al)
		if al.MBID != "" {
			sa.byRG[strings.ToLower(al.MBID)] = al.ID
		}
	}
}

func (sa *scanArtists) added() int {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	return sa.newCount
}

func (sa *scanArtists) name(id int64) string {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	return sa.inLib[id]
}

func (sa *scanArtists) albumByRG(mbid string) int64 {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	return sa.byRG[strings.ToLower(mbid)]
}

// albumTitled is the artist's album whose title folds equal to title (0: none, or more
// than one).
func (sa *scanArtists) albumTitled(artistID int64, title string) int64 {
	want := music.NormKey(music.StripDiscSuffix(title))
	if want == "" {
		return 0
	}
	sa.mu.Lock()
	defer sa.mu.Unlock()
	var hit int64
	for _, al := range sa.albums[artistID] {
		if music.NormKey(al.Title) == want {
			if hit != 0 {
				return 0
			}
			hit = al.ID
		}
	}
	return hit
}

// lock serialises the work for one key, so two workers meeting one new artist add it once.
func (sa *scanArtists) lock(key string) func() {
	sa.mu.Lock()
	l := sa.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		sa.locks[key] = l
	}
	sa.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// find resolves an artist by MBID (when the tags carry one) or name. id 0 with why says
// why not; transient says MusicBrainz couldn't be asked. remote false keeps to the library
// (and artists this scan added): nothing is looked up on MusicBrainz or added.
func (sa *scanArtists) find(ctx context.Context, name, mbid string, remote bool) (id int64, why string, transient bool) {
	if music.IsVariousArtistsName(name) || music.IsVariousArtistsMBID(mbid) {
		// "Various Artists" is a placeholder, not an artist to add: adding it would list
		// every compilation MusicBrainz has. A compilation's album is chosen by hand.
		return 0, "A various-artists compilation: choose its album with Match….", false
	}
	if !remote {
		return sa.local(name), "", false
	}
	if mbid != "" {
		if a := sa.answer(ctx, "m:"+strings.ToLower(mbid), func() scanArtistAnswer { return sa.byMBID(ctx, mbid, name) }); a.id > 0 || a.transient {
			return a.id, a.why, a.transient
		}
	}
	if music.NormKey(name) == "" {
		return 0, "", false
	}
	a := sa.answer(ctx, "n:"+music.NormKey(name), func() scanArtistAnswer { return sa.byName(ctx, name) })
	return a.id, a.why, a.transient
}

// local is the one library artist whose name or alias this is, or one this scan already
// resolved by the name (0: none, or more than one).
func (sa *scanArtists) local(name string) int64 {
	key := music.NormKey(name)
	if key == "" {
		return 0
	}
	sa.mu.Lock()
	defer sa.mu.Unlock()
	if a, ok := sa.resolved["n:"+key]; ok {
		return a.id
	}
	var hit int64
	n := 0
	for _, id := range sa.ix.ArtistsNamed(name) {
		if _, ok := sa.inLib[id]; ok {
			hit, n = id, n+1
		}
	}
	if n != 1 {
		return 0
	}
	return hit
}

// answer returns the cached answer for key, working it out once.
func (sa *scanArtists) answer(_ context.Context, key string, work func() scanArtistAnswer) scanArtistAnswer {
	sa.mu.Lock()
	if a, ok := sa.resolved[key]; ok {
		sa.mu.Unlock()
		return a
	}
	sa.mu.Unlock()
	defer sa.lock(key)()
	sa.mu.Lock()
	if a, ok := sa.resolved[key]; ok {
		sa.mu.Unlock()
		return a
	}
	sa.mu.Unlock()
	a := work()
	if !a.transient { // a MusicBrainz outage is asked about again by the next folder
		sa.mu.Lock()
		sa.resolved[key] = a
		sa.mu.Unlock()
	}
	return a
}

// byName: a library artist whose name or alias this is, else MusicBrainz's single exact
// match on its name or an alias, added unmonitored.
func (sa *scanArtists) byName(ctx context.Context, name string) scanArtistAnswer {
	var inLib []int64
	sa.mu.Lock()
	for _, id := range sa.ix.ArtistsNamed(name) {
		if _, ok := sa.inLib[id]; ok {
			inLib = append(inLib, id)
		}
	}
	sa.mu.Unlock()
	switch len(inLib) {
	case 1:
		return scanArtistAnswer{id: inLib[0]}
	case 0:
	default:
		return scanArtistAnswer{why: fmt.Sprintf("%d artists in your library are called %q, so the scan can't tell which this is.", len(inLib), name)}
	}
	results, err := sa.c.music.Lookup(ctx, name)
	if err != nil {
		if mbDown(err) || ctx.Err() != nil {
			return scanArtistAnswer{why: "MusicBrainz couldn't be reached", transient: true}
		}
		return scanArtistAnswer{why: fmt.Sprintf("Looking %q up on MusicBrainz failed: %v", name, err)}
	}
	key := music.NormKey(name)
	match := map[string]bool{}
	for _, r := range results {
		hit := music.NormKey(r.Name) == key
		for _, al := range r.Aliases {
			hit = hit || music.NormKey(al.Name) == key
		}
		if hit {
			match[r.MBID] = true
		}
	}
	switch len(match) {
	case 0:
		return scanArtistAnswer{why: fmt.Sprintf("No artist called %q is in your library or on MusicBrainz.", name)}
	case 1:
	default:
		// MusicBrainz has several "Nirvana": there's no basis to choose, so don't.
		return scanArtistAnswer{why: fmt.Sprintf("%d artists on MusicBrainz are called %q, so the scan can't tell which this is. Add the right one, then match the folder.", len(match), name)}
	}
	var mbid string
	for m := range match {
		mbid = m
	}
	return sa.add(ctx, mbid, name)
}

// byMBID: the library artist with this MBID, else that artist added unmonitored.
func (sa *scanArtists) byMBID(ctx context.Context, mbid, name string) scanArtistAnswer {
	if a, err := sa.c.music.ArtistByMBID(ctx, mbid); err == nil {
		sa.mu.Lock()
		_, lib := sa.inLib[a.ID]
		sa.mu.Unlock()
		if lib {
			return scanArtistAnswer{id: a.ID}
		}
	}
	return sa.add(ctx, mbid, name)
}

// add adds an artist the scan found, UNMONITORED: it already exists on disk, and switching
// every scanned artist on would send the searcher after their whole discography the moment
// the scan finished.
func (sa *scanArtists) add(ctx context.Context, mbid, name string) scanArtistAnswer {
	c := sa.c
	added, err := c.music.AddArtist(ctx, mbid, "", false)
	isNew := err == nil
	if errors.Is(err, music.ErrExists) {
		added, err = c.music.ArtistByMBID(ctx, mbid)
	}
	if err != nil {
		if mbDown(err) || ctx.Err() != nil {
			return scanArtistAnswer{why: "MusicBrainz couldn't be reached", transient: true}
		}
		c.log.Warn("music scan: adding a scanned artist failed", "artist", name, "err", err)
		return scanArtistAnswer{why: fmt.Sprintf("Adding %q from MusicBrainz failed: %v", name, err)}
	}
	albums, err := c.music.Albums(ctx, added.ID)
	if err != nil {
		return scanArtistAnswer{why: err.Error()}
	}
	sa.mu.Lock()
	sa.inLib[added.ID] = added.Name
	sa.albums[added.ID] = nil
	var list []music.ScanAlbum
	for _, al := range albums {
		list = append(list, music.ScanAlbum{ID: al.ID, ArtistID: added.ID, Title: al.Title, MBID: al.MBID})
	}
	sa.addAlbums(list)
	if isNew {
		sa.newCount++
	}
	sa.mu.Unlock()
	if isNew {
		c.log.Info("music scan: catalogued a new artist", "artist", added.Name)
	}
	return scanArtistAnswer{id: added.ID}
}

// --- reviews -----------------------------------------------------------------------------

// scanReviewPrefix marks a review the library scan wrote: its "download" is a folder that
// is already in the music folder. Such a review is never rejected (that would delete
// library files) and its Match… records the files where they are.
const scanReviewPrefix = "music-scan:"

// scanReviewKey is a scan review's key (import_reviews.hash) for a folder.
func scanReviewKey(path string) string {
	sum := sha1.Sum([]byte(path))
	return scanReviewPrefix + hex.EncodeToString(sum[:])[:24]
}

// IsLibraryScanReview reports whether a review is a library-scan folder rather than a
// download.
func IsLibraryScanReview(r Review) bool { return strings.HasPrefix(r.Hash, scanReviewPrefix) }

// pendingScanReviews lists the pending scan reviews by folder.
func (c *Coordinator) pendingScanReviews(ctx context.Context) (map[string]int64, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, content_path FROM import_reviews WHERE status = 'pending' AND `+store.LikeASCII(c.db, "hash", "?", false), scanReviewPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			return nil, err
		}
		out[path] = id
	}
	return out, rows.Err()
}

// addScanReviews writes the scan's new reviews in one transaction-free batch (each insert
// skips a folder already pending), then tells open pages once. Unlike a download's review
// there is no grab to hold and no per-folder alert: Needs you shows the scan's folders as
// one item.
func (c *Coordinator) addScanReviews(ctx context.Context, list []Review) {
	added := 0
	for _, r := range list {
		res, err := c.db.ExecContext(ctx, `
			INSERT INTO import_reviews (hash, name, content_path, media_type, expected_id, expected_title, parsed_title, reason, reason_code, size_bytes, indexer, match_json)
			SELECT ?, ?, ?, 'music', ?, ?, ?, ?, ?, ?, '', ?
			 WHERE NOT EXISTS (SELECT 1 FROM import_reviews WHERE hash = ? AND status = 'pending')`,
			r.Hash, r.Name, r.ContentPath, r.ExpectedID, r.ExpectedTitle, r.ParsedTitle, r.Reason, r.ReasonCode, r.SizeBytes, string(r.Match), r.Hash)
		if err != nil {
			c.log.Warn("music scan: couldn't hold a folder for review", "folder", r.ContentPath, "err", err)
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}
	if added == 0 {
		return
	}
	c.log.Info("music scan: folders held for review", "count", added)
	c.reviewsChanged()
	if c.bus != nil {
		c.bus.Publish("import.held", map[string]any{"kind": "music", "scan": true, "count": added,
			"name": fmt.Sprintf("%d folder(s) from the music library scan", added)})
	}
}

// --- measuring ---------------------------------------------------------------------------

// musicMeasurePerScan caps how many already-recorded tracks one scan measures, so a big
// library recorded before tracks were probed catches up over a few scans.
const musicMeasurePerScan = 200

// measureUnmeasuredTracks probes up to limit music files that haven't been measured with
// every fact (copied over by the model migration, recorded while ffprobe couldn't run, or
// by a scan, which reads only the headers) and records their real tier, bitrate, codec,
// sample rate, bit depth, channels and length. ffprobe only reads, at idle priority;
// nothing is moved or rewritten. A file that's gone or unreadable is skipped and stays
// unmeasured; a cursor carries each pass on from where the last one stopped, so files like
// that never hold the rest back, and it starts over from the top once it reaches the end.
func (c *Coordinator) measureUnmeasuredTracks(ctx context.Context, limit int) int {
	if c.probeAudioFn == nil && !mediainfo.Available() {
		return 0
	}
	files, err := c.music.UnmeasuredFiles(ctx, c.measureCursor.Load(), limit)
	if err != nil {
		c.log.Warn("music scan: couldn't list unmeasured files", "err", err)
		return 0
	}
	if len(files) < limit {
		c.measureCursor.Store(0) // the end: the next pass starts from the top again
	}
	n := 0
	for _, f := range files {
		if ctx.Err() != nil {
			break
		}
		if len(files) == limit {
			c.measureCursor.Store(f.ID)
		}
		info, err := c.probeAudioIdle(ctx, f.Path)
		if err != nil {
			continue
		}
		q := music.TierFromProbe(info)
		if q == music.QualityUnknown || info.BitrateKbps <= 0 {
			continue
		}
		if err := c.music.SetFileMeasured(ctx, f.ID, f.Path, q, info); err != nil {
			c.log.Warn("music scan: recording a measured tier failed", "path", f.Path, "err", err)
			continue
		}
		n++
	}
	return n
}

// probeAudioIdle is probeAudio for background work (the scan's measuring pass, the verify
// pass): ffprobe at idle CPU and I/O priority, so it never competes with playback.
func (c *Coordinator) probeAudioIdle(ctx context.Context, path string) (mediainfo.AudioInfo, error) {
	if c.probeAudioFn != nil {
		return c.probeAudioFn(ctx, path)
	}
	if !mediainfo.Available() {
		return mediainfo.AudioInfo{}, errNoFFprobe
	}
	return mediainfo.ProbeAudioIdle(ctx, path)
}

// --- the discography import's folders ------------------------------------------------------

// albumFolder is one on-disk folder holding an album's audio files (the discography
// import's unit).
type albumFolder struct {
	path   string
	artist string // from the parent directory, or the "Artist - Album" folder name
	album  string
	year   int
	files  []music.AudioFile
}

// matchAlbumByTitle finds the artist's album whose title matches the folder, exact
// normalized comparison only.
func matchAlbumByTitle(albums []music.Album, title string) (music.Album, bool) {
	want := music.NormKey(title)
	if want == "" {
		return music.Album{}, false
	}
	for _, al := range albums {
		if music.NormKey(al.Title) == want {
			return al, true
		}
	}
	return music.Album{}, false
}

// findAlbumFolders walks a folder (a discography download) and returns every folder
// holding audio, read as <Artist>/<Album>/tracks, or "Artist - Album" directly under it.
func findAlbumFolders(root string) []albumFolder {
	byDir := map[string][]music.AudioFile{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		// The recycle bin (.arrmada-recycle) and other hidden folders aren't albums: a
		// deleted album scanned from the bin would come straight back.
		if err == nil && d.IsDir() && p != root && library.SkipScanDir(d.Name()) {
			return filepath.SkipDir
		}
		if err != nil || d.IsDir() || !music.IsAudioFile(p) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() < minTrackBytes {
			return nil // artwork, a jingle or a broken file — not a track
		}
		dir := filepath.Dir(p)
		byDir[dir] = append(byDir[dir], music.AudioFile{Path: p, SizeBytes: info.Size()})
		return nil
	})

	out := make([]albumFolder, 0, len(byDir))
	for dir, files := range byDir {
		f := albumFolder{path: dir, files: files}
		f.artist, f.album, f.year = folderNames(root, dir)
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// sameDir compares two paths for equality, tolerating separator and trailing-slash noise.
func sameDir(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
