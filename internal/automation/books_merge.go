package automation

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/audiobook"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/library"
)

// ErrAlreadyMerging is returned when a merge for the same book is already running.
var ErrAlreadyMerging = errors.New("this audiobook is already being merged")

const (
	// mergeBackupDir holds merge sources when the recycle bin is off, under the audiobooks
	// root: hidden, and both book scans (FindBookFiles, FindBookFoldersIn) skip hidden
	// folders, so none treats it as a book.
	mergeBackupDir = ".arrmada-merge-backup"
	// mergeBackupKeep is how long those sources are kept before the daily prune drops them.
	mergeBackupKeep = 14 * 24 * time.Hour
)

// MergingAudiobook reports whether a merge is running for this book, so the handler can
// refuse a second one straight away instead of from a background goroutine.
func (c *Coordinator) MergingAudiobook(bookID int64) bool {
	_, busy := c.merging.Load(bookID)
	return busy
}

// CheckAudiobookMerge reports why a book can't be merged ("nothing to merge", no such
// book), so the handler can say so straight away instead of answering 202 for a merge
// that would stop before it started.
func (c *Coordinator) CheckAudiobookMerge(ctx context.Context, bookID int64) error {
	_, _, err := c.audiobookMergeSources(ctx, bookID)
	return err
}

// audiobookMergeSources loads the book and its chapter files in play order.
func (c *Coordinator) audiobookMergeSources(ctx context.Context, bookID int64) (books.Book, []string, error) {
	if c.books == nil {
		return books.Book{}, nil, errBooksNotReady
	}
	b, err := c.books.Get(ctx, bookID)
	if err != nil {
		return b, nil, err
	}
	if b.Audiobook == nil || b.Audiobook.FileCount <= 1 {
		return b, nil, errString("nothing to merge — the audiobook is a single file")
	}
	var paths []string
	for _, f := range library.FindBookFiles(b.Audiobook.Path) {
		if library.IsAudiobookFile(f.Path) {
			paths = append(paths, f.Path)
		}
	}
	// NATURAL order, not lexical. sort.Strings puts "Chapter 10" before "Chapter 2", so a
	// book with ten or more unpadded chapter files was concatenated in the wrong order.
	sortNatural(paths)
	if len(paths) < 2 {
		return b, nil, errString("nothing to merge — fewer than two audio files in the book folder")
	}
	return b, paths, nil
}

// MergeAudiobook combines a multi-file audiobook into a single chapterized .m4b (one
// chapter per source file). Runs synchronously — callers should background it.
//
// Nothing about it can lose the book. ffmpeg writes to a hidden temp file in the same
// folder; the result is checked against the sources' total length before it gets its
// real name; and the sources then go to the recycle bin — or, with the bin off, to a
// backup folder kept for 14 days — never straight to os.Remove. Any failure removes the
// temp file, leaves the sources exactly as they were, and records why on the book.
func (c *Coordinator) MergeAudiobook(ctx context.Context, bookID int64) error {
	if _, busy := c.merging.LoadOrStore(bookID, true); busy {
		// No event: the merge already running will record its own outcome, and the page
		// is watching for that one.
		return ErrAlreadyMerging
	}
	defer c.merging.Delete(bookID)
	if c.books == nil {
		return errBooksNotReady
	}
	// Every outcome is written with a context that outlives ctx. The page waits for a
	// 'merged' or 'merge-failed' event, and the case most likely to fail — running out
	// of the time budget on a long re-encode — is exactly when ctx is already done.
	bk := context.WithoutCancel(ctx)
	title := ""
	fail := func(reason string) error {
		c.books.AddEvent(bk, bookID, "merge-failed", reason)
		c.log.Warn("book: audiobook merge failed — the original files are untouched", "book_id", bookID, "title", title, "reason", reason)
		return errString("merge failed: " + reason)
	}
	b, paths, err := c.audiobookMergeSources(ctx, bookID)
	if err != nil {
		if errors.Is(err, books.ErrNotFound) {
			return err // nothing to record it on
		}
		return fail(err.Error())
	}
	title = b.Title
	bookDir := b.Audiobook.Path

	final := mergeTarget(bookDir, sanitizeName(b.Title))
	tmp := filepath.Join(bookDir, "."+filepath.Base(final)+".merging")
	_ = os.Remove(tmp) // a leftover from a crashed run

	merge, duration := c.mergeFn, c.durationFn
	if merge == nil {
		merge = audiobook.Merge
		// Say which way it's going before spending an hour on it: a remux is the audio
		// untouched, an encode is a second lossy generation and worth knowing about.
		if plan := audiobook.PlanFor(ctx, paths); plan.Copy {
			c.log.Info("book: merging audiobook — copying the audio untouched", "title", b.Title, "files", len(paths))
		} else {
			c.log.Info("book: merging audiobook — re-encoding (the sources can't be copied into an m4b as-is)",
				"title", b.Title, "files", len(paths), "target_kbps", plan.BitrateBPS/1000,
				"sample_rate", plan.SampleRate, "channels", plan.Channels)
		}
	}
	decoded := c.decodedDurationFn
	if duration == nil {
		duration = audiobook.Duration
		if decoded == nil {
			decoded = audiobook.DecodedDuration
		}
	}

	if _, err := merge(ctx, paths, tmp, audiobook.MergeOptions{Title: b.Title, Author: b.Author}); err != nil {
		_ = os.Remove(tmp)
		if ctx.Err() != nil {
			return fail("it ran out of time (" + ctx.Err().Error() + ")")
		}
		return fail(err.Error())
	}
	if reason := verifyMerge(ctx, duration, decoded, paths, tmp); reason != "" {
		_ = os.Remove(tmp)
		if ctx.Err() != nil {
			return fail("it ran out of time while checking the result (" + ctx.Err().Error() + ")")
		}
		return fail(reason)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fail("couldn't put the merged file in place: " + err.Error())
	}

	// The merged file is in place and checked; only now do the sources move — and they
	// move, they are never deleted outright.
	where, failed := c.retireMergeSources(bookID, bookDir, paths)
	var size int64
	if fi, err := os.Stat(final); err == nil {
		size = fi.Size()
	}
	detail := fmt.Sprintf("Merged %d chapter files into %s. The original files %s. Apps that downloaded this audiobook need to download it again.",
		len(paths), filepath.Base(final), where)
	if len(failed) > 0 {
		detail += " Couldn't move " + strings.Join(failed, ", ") + " — still in the book folder."
	}
	c.log.Info("book: merged audiobook", "title", b.Title, "out", final, "sources_left", len(failed))
	// The sources have moved, so the book must point at the merged file whatever
	// became of ctx in the meantime. It still reads as 'merged' if that fails — the
	// merge itself worked — with what to do about it.
	markErr := c.books.MarkImported(bk, bookID, books.KindAudiobook, final, "M4B", size, 1)
	if markErr != nil {
		detail += " The book couldn't be pointed at the new file (" + markErr.Error() + ") — rescan it."
	}
	c.books.AddEvent(bk, b.ID, "merged", detail)
	c.bookImported(bk, b.ID, books.KindAudiobook) // listening apps see the merged file now
	if c.bus != nil {
		c.bus.Publish("book.imported", map[string]any{"title": b.Title, "id": b.ID, "edition": "audiobook"})
	}
	return markErr
}

// mergeTarget picks the merged file's name: "<title>.m4b", or "<title> (merged).m4b" when
// a file — usually one of the sources — already has that name, so nothing is overwritten.
func mergeTarget(dir, title string) string {
	p := filepath.Join(dir, title+".m4b")
	for i := 1; ; i++ {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
		suffix := " (merged)"
		if i > 1 {
			suffix = fmt.Sprintf(" (merged %d)", i)
		}
		p = filepath.Join(dir, title+suffix+".m4b")
	}
}

// verifyMerge checks the output against the sources: every source's length must be known,
// and the output must be within max(1%, 5 s) of their sum. Returns why not, or "".
//
// A quick probe of a VBR MP3 without a Xing/VBRI header is only an estimate from its
// bitrate, so when the figures disagree and decoded is set, the MP3 sources are measured
// again by decoding them before the merge is refused.
func verifyMerge(ctx context.Context, duration, decoded func(context.Context, string) (float64, error), sources []string, out string) string {
	sum, reason := sumDurations(ctx, duration, sources)
	if reason != "" {
		return reason
	}
	got, err := duration(ctx, out)
	if err != nil || got <= 0 {
		return "the merged file has no readable length"
	}
	within := func(sum float64) bool { return math.Abs(got-sum) <= math.Max(sum*0.01, 5) }
	if within(sum) {
		return ""
	}
	if decoded != nil && hasMP3(sources) {
		exact := func(ctx context.Context, p string) (float64, error) {
			if strings.EqualFold(filepath.Ext(p), ".mp3") {
				return decoded(ctx, p)
			}
			return duration(ctx, p)
		}
		if again, reason := sumDurations(ctx, exact, sources); reason == "" {
			if within(again) {
				return ""
			}
			sum = again
		}
	}
	return fmt.Sprintf("the merged file is %s long but the chapters add up to %s", fmtDur(got), fmtDur(sum))
}

// sumDurations adds up the sources' lengths, or says which one couldn't be measured.
func sumDurations(ctx context.Context, duration func(context.Context, string) (float64, error), sources []string) (float64, string) {
	var sum float64
	for _, p := range sources {
		d, err := duration(ctx, p)
		if err != nil || d <= 0 {
			return 0, fmt.Sprintf("couldn't measure %s, so the result can't be checked", filepath.Base(p))
		}
		sum += d
	}
	return sum, ""
}

func hasMP3(paths []string) bool {
	for _, p := range paths {
		if strings.EqualFold(filepath.Ext(p), ".mp3") {
			return true
		}
	}
	return false
}

func fmtDur(secs float64) string {
	return (time.Duration(secs * float64(time.Second))).Round(time.Second).String()
}

// retireMergeSources moves the merged-away chapter files out of the book: to the recycle
// bin when it's on, otherwise into a dated backup folder under the audiobooks root (a
// same-filesystem rename) that the daily prune clears after 14 days. A file that can't be
// moved stays where it is. It returns where they went and which couldn't be moved.
func (c *Coordinator) retireMergeSources(bookID int64, bookDir string, paths []string) (string, []string) {
	var failed []string
	if c.recycle != "" {
		bin := library.SingleBin(c.recycle)
		for _, p := range paths {
			if _, err := library.RemoveToBin(bin, p); err != nil {
				c.log.Warn("book: merge source left in place", "path", p, "err", err)
				failed = append(failed, filepath.Base(p))
			}
		}
		pruneEmptySubdirs(bookDir)
		return "went to the recycle bin", failed
	}
	root := c.mergeBackupRoot(bookDir)
	dir := filepath.Join(root, mergeBackupDir, fmt.Sprintf("%d-%s", bookID, time.Now().UTC().Format("20060102T150405Z")))
	for _, p := range paths {
		rel, err := filepath.Rel(bookDir, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = filepath.Base(p)
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err == nil {
			err = os.Rename(p, dst)
		}
		if err != nil {
			if _, statErr := os.Stat(p); statErr == nil {
				c.log.Warn("book: merge source left in place", "path", p, "err", err)
				failed = append(failed, filepath.Base(p))
			}
		}
	}
	pruneEmptySubdirs(bookDir)
	return "are kept in " + filepath.Join(filepath.Base(root), mergeBackupDir) + " for 14 days", failed
}

// mergeBackupRoot is the audiobooks root when known (one place for the prune to look),
// else the folder above the book.
func (c *Coordinator) mergeBackupRoot(bookDir string) string {
	if c.imp != nil {
		if r := c.imp.AudiobookRoot(); r != "" {
			return r
		}
	}
	return filepath.Dir(bookDir)
}

// pruneEmptySubdirs removes empty folders inside dir (e.g. "CD1" after its files moved),
// never dir itself.
func pruneEmptySubdirs(dir string) {
	var subs []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != dir {
			subs = append(subs, p)
		}
		return nil
	})
	sort.Slice(subs, func(i, j int) bool { return len(subs[i]) > len(subs[j]) })
	for _, s := range subs {
		_ = os.Remove(s) // only succeeds when empty
	}
}

var reMergeBackup = regexp.MustCompile(`^\d+-(\d{8}T\d{6}Z)$`)

// PruneMergeBackups drops merge backup folders older than 14 days. Daily job.
func (c *Coordinator) PruneMergeBackups(ctx context.Context) error {
	if c.imp == nil {
		return nil
	}
	root := c.imp.AudiobookRoot()
	if root == "" {
		return nil
	}
	n := pruneMergeBackups(filepath.Join(root, mergeBackupDir), time.Now(), mergeBackupKeep)
	if n > 0 {
		c.log.Info("book: dropped old merge backups", "folders", n)
	}
	return nil
}

// pruneMergeBackups removes backup folders in dir made more than keep before now (the
// time is in the folder's name; the folder's mtime stands in for a name it can't read).
func pruneMergeBackups(dir string, now time.Time, keep time.Duration) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		var made time.Time
		if m := reMergeBackup.FindStringSubmatch(e.Name()); m != nil {
			made, _ = time.Parse("20060102T150405Z", m[1])
		}
		if made.IsZero() {
			if fi, err := e.Info(); err == nil {
				made = fi.ModTime()
			}
		}
		if !made.IsZero() && now.Sub(made) > keep {
			if os.RemoveAll(filepath.Join(dir, e.Name())) == nil {
				removed++
			}
		}
	}
	return removed
}
