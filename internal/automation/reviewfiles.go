package automation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// ErrWrongReason means an action was asked of a review it can't help: Retry import on a
// download that never failed to import, say.
var ErrWrongReason = errors.New("that action doesn't apply to this review")

// ResolutionRetried: the review was cleared so the import sweep tries the download again.
const ResolutionRetried = "retried"

// reviewFileCap bounds a review's file list. A season pack is tens of files; anything
// near this is a whole-series dump, and the list says it was cut short.
const reviewFileCap = 500

// EpisodeGuess is what a file's own name says about its episode numbering.
type EpisodeGuess struct {
	Season   int   `json:"season,omitempty"`
	Episodes []int `json:"episodes,omitempty"`
	Absolute []int `json:"absolute,omitempty"`
}

// ReviewFile is one file inside a held download.
type ReviewFile struct {
	RelPath string       `json:"rel_path"`
	Size    int64        `json:"size"`
	Video   bool         `json:"video"`
	Guess   EpisodeGuess `json:"guess"`
}

// ReviewFiles lists the files inside a held download, with what each name says about its
// episode numbering, so a person can see what arrived and map a pack by hand.
//
// It walks only the review's own content path — no path comes from the request — never
// follows a symlink, and checks every entry against that folder, so a link planted inside
// a download can't turn this into a listing of anything else on the host.
func (c *Coordinator) ReviewFiles(ctx context.Context, id int64) (files []ReviewFile, truncated bool, err error) {
	r, err := c.getReview(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if r.ContentPath == "" {
		return nil, false, ErrDownloadGone
	}
	root := filepath.Clean(r.ContentPath)
	fi, err := os.Lstat(root)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %s", ErrDownloadGone, r.ContentPath)
	}
	files = []ReviewFile{}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return files, false, nil // the download itself is a link: nothing to list
	}
	add := func(rel string, size int64) {
		name := filepath.Base(rel)
		p := parser.Parse(name)
		files = append(files, ReviewFile{
			RelPath: filepath.ToSlash(rel), Size: size,
			Video: isVideoName(name),
			Guess: EpisodeGuess{Season: p.Season, Episodes: p.Episodes, Absolute: p.AbsoluteEpisodes},
		})
	}
	if !fi.IsDir() {
		add(filepath.Base(root), fi.Size())
		return files, false, nil
	}
	errStop := errors.New("stop")
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner is skipped, not fatal
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.Type()&fs.ModeSymlink != 0 || !(d.IsDir() || d.Type().IsRegular()) {
			return nil // links and devices are never listed or followed
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
		if len(files) >= reviewFileCap {
			truncated = true
			return errStop
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		add(rel, info.Size())
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errStop) {
		return nil, false, walkErr
	}
	sort.SliceStable(files, func(i, j int) bool { return naturalLess(files[i].RelPath, files[j].RelPath) })
	return files, truncated, nil
}

// isVideoName reports whether a file name has a video extension.
func isVideoName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range videoExts {
		if ext == e {
			return true
		}
	}
	return false
}

// RetryReviewImport clears an "import keeps failing" review so the import sweep tries the
// download again straight away: the importer's back-off for it is dropped and the review
// is resolved as 'retried'. If the import fails again it comes back to Review on its own.
func (c *Coordinator) RetryReviewImport(ctx context.Context, id int64) error {
	r, err := c.pendingReview(ctx, id)
	if err != nil {
		return err
	}
	if r.ReasonCode != ReasonImportFailed {
		return fmt.Errorf("%w: only a download whose import kept failing can be retried", ErrWrongReason)
	}
	if c.importRetry != nil && r.Hash != "" {
		c.importRetry(r.Hash)
	}
	// Back in flight: it's an ordinary finished download waiting for the sweep again.
	c.setGrabStatusByHash(ctx, r.Hash, r.Name, r.MediaType, grabStatusGrabbed)
	return c.resolveReview(ctx, id, ResolutionRetried)
}

// SetImportRetry installs what Retry import calls to drop the importer's back-off for a
// download (library.Manager.RetryNow). Call it at startup.
func (c *Coordinator) SetImportRetry(f func(hash string)) { c.importRetry = f }

// SearchTitle searches one library item now, whatever its kind — what "Reject & find
// another" runs once the rejected release is blocklisted.
func (c *Coordinator) SearchTitle(ctx context.Context, kind string, id int64) (SearchOutcome, error) {
	switch kind {
	case "movie":
		return c.SearchMovie(ctx, id)
	case "series":
		return c.SearchSeriesNow(ctx, id)
	case "book":
		return c.SearchBookNow(ctx, id)
	case "music":
		return c.SearchAlbumNow(ctx, id)
	}
	return SearchOutcome{}, fmt.Errorf("unknown media type %q", kind)
}
