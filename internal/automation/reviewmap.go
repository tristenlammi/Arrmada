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
	"github.com/tristenlammi/arrmada/internal/pathguard"
	"github.com/tristenlammi/arrmada/internal/series"
)

// ErrBadMapping means a hand mapping can't be applied as given: a path outside the
// download, a file that isn't a video, an episode the show doesn't have, or one episode
// claimed twice. Nothing is imported when any mapping is bad.
var ErrBadMapping = errors.New("that mapping can't be used")

// ResolutionMapped: imported by mapping its files to episodes by hand.
const ResolutionMapped = "mapped"

// FileMapping says which episode(s) one file of a held download is.
type FileMapping struct {
	RelPath  string `json:"rel_path"`
	Season   int    `json:"season"`
	Episodes []int  `json:"episodes"`
}

// ReviewMapPlan is a checked hand mapping, ready to import.
type ReviewMapPlan struct {
	review Review
	show   series.Series
	files  []plannedFile
}

type plannedFile struct {
	path     string
	season   int
	episodes []int
}

// Files is how many files the plan imports.
func (p ReviewMapPlan) Files() int { return len(p.files) }

func badMapping(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrBadMapping}, args...)...)
}

// PlanReviewMap checks a hand mapping of a held series download's files to episodes of
// seriesID before anything is touched. Every path must resolve inside the review's own
// download folder — through symlinks too — and be a video file; every episode must be one
// the show has; no episode may be claimed by two files.
func (c *Coordinator) PlanReviewMap(ctx context.Context, id, seriesID int64, mappings []FileMapping) (ReviewMapPlan, error) {
	r, err := c.pendingReview(ctx, id)
	if err != nil {
		return ReviewMapPlan{}, err
	}
	if r.MediaType != "series" {
		return ReviewMapPlan{}, fmt.Errorf("%w: only a show's download can be mapped to episodes", ErrWrongReason)
	}
	if c.series == nil || c.imp == nil {
		return ReviewMapPlan{}, moduleOffError{reviewModuleLabel["series"]}
	}
	if seriesID <= 0 {
		seriesID = r.ExpectedID
	}
	if seriesID <= 0 {
		return ReviewMapPlan{}, needsTargetError{kindLabel("series")}
	}
	s, err := c.series.Get(ctx, seriesID)
	if errors.Is(err, series.ErrNotFound) {
		return ReviewMapPlan{}, badMapping("that show isn't in your library any more — choose another")
	}
	if err != nil {
		return ReviewMapPlan{}, err
	}
	if r.ContentPath == "" {
		return ReviewMapPlan{}, ErrDownloadGone
	}
	root := filepath.Clean(r.ContentPath)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return ReviewMapPlan{}, fmt.Errorf("%w: %s", ErrDownloadGone, r.ContentPath)
	}
	if len(mappings) == 0 {
		return ReviewMapPlan{}, badMapping("map at least one file to an episode")
	}
	plan := ReviewMapPlan{review: r, show: s}
	claimed := map[[2]int]string{}
	seenFile := map[string]bool{}
	for _, m := range mappings {
		rel := strings.TrimSpace(m.RelPath)
		if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
			return ReviewMapPlan{}, badMapping("%q isn't a file inside this download", m.RelPath)
		}
		var abs string
		if rootInfo.IsDir() {
			abs = filepath.Join(root, filepath.FromSlash(rel))
		} else if filepath.ToSlash(rel) == filepath.Base(root) {
			abs = root // a single-file download maps its one file
		} else {
			return ReviewMapPlan{}, badMapping("%q isn't a file inside this download", m.RelPath)
		}
		within, err := filepath.Rel(root, abs)
		if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
			return ReviewMapPlan{}, badMapping("%q is outside this download", m.RelPath)
		}
		// Resolved through every link, both sides: a link inside the download that
		// points elsewhere on the host is refused, not followed.
		base := root
		if !rootInfo.IsDir() {
			base = filepath.Dir(root)
		}
		if !pathguard.Within(abs, base) {
			return ReviewMapPlan{}, badMapping("%q leads outside this download", m.RelPath)
		}
		fi, err := os.Lstat(abs)
		if err != nil || fi.Mode()&fs.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return ReviewMapPlan{}, badMapping("%q isn't a file in this download", m.RelPath)
		}
		if !isVideoName(abs) {
			return ReviewMapPlan{}, badMapping("%q isn't a video file", m.RelPath)
		}
		if seenFile[abs] {
			return ReviewMapPlan{}, badMapping("%q is mapped twice", m.RelPath)
		}
		seenFile[abs] = true
		if m.Season < 0 || len(m.Episodes) == 0 {
			return ReviewMapPlan{}, badMapping("give %q a season and at least one episode", m.RelPath)
		}
		eps := append([]int(nil), m.Episodes...)
		sort.Ints(eps)
		for i, ep := range eps {
			if ep <= 0 || (i > 0 && ep == eps[i-1]) {
				return ReviewMapPlan{}, badMapping("%q lists episode %d more than once or below 1", m.RelPath, ep)
			}
			if !c.series.EpisodeExists(ctx, s.ID, m.Season, ep) {
				return ReviewMapPlan{}, badMapping("%s has no S%02dE%02d", s.Title, m.Season, ep)
			}
			key := [2]int{m.Season, ep}
			if other, dup := claimed[key]; dup {
				return ReviewMapPlan{}, badMapping("S%02dE%02d is given to both %q and %q", m.Season, ep, other, m.RelPath)
			}
			claimed[key] = m.RelPath
		}
		plan.files = append(plan.files, plannedFile{path: abs, season: m.Season, episodes: eps})
	}
	return plan, nil
}

// ApplyReviewMap imports a checked hand mapping: each file is placed as its first episode
// and recorded for every episode it covers (a double episode marks both), replacing any
// file those episodes had through the recycle bin. The quality gate isn't applied — the
// person chose these files. When every file landed, the download is closed out like any
// review import (Convert, Subtitles and the requester hear about it through the outbox,
// its grab becomes 'imported' so it seeds out) and the review resolves as 'mapped'.
// Returns how many episodes got a file.
func (c *Coordinator) ApplyReviewMap(ctx context.Context, p ReviewMapPlan) (int, error) {
	s, r := p.show, p.review
	// A big pack is applied as a job; someone may have settled the review meanwhile.
	if _, err := c.pendingReview(ctx, r.ID); err != nil {
		return 0, err
	}
	folder := c.series.ExistingFolderName(ctx, s.ID)
	releaseName := filepath.Base(r.ContentPath)
	release := parser.Parse(releaseName)
	var placed []series.EpisodeRef
	var failures []string
	for _, f := range p.files {
		if err := ctx.Err(); err != nil {
			return len(placed), err
		}
		sourceName := filepath.Base(f.path)
		if parser.Parse(sourceName).Resolution == "" && release.Resolution != "" {
			sourceName = releaseName // the pack names the quality once, on its folder
		}
		ei, ok, err := c.imp.ImportEpisodeAs(folder, s.Title, s.Year, f.season, f.episodes[0], f.path)
		if err != nil || !ok {
			if err == nil {
				err = errors.New("empty file")
			}
			failures = append(failures, fmt.Sprintf("%s: %v", filepath.Base(f.path), err))
			continue
		}
		for _, ep := range f.episodes {
			if err := c.series.SupersedeEpisodeFile(ctx, s.ID, f.season, ep, ei.TargetPath, ei.SizeBytes, sourceName); err != nil {
				failures = append(failures, fmt.Sprintf("S%02dE%02d: %v", f.season, ep, err))
				continue
			}
			placed = append(placed, series.EpisodeRef{Season: f.season, Episode: ep})
		}
	}
	n := len(placed)
	if n > 0 {
		c.series.AddEvent(ctx, s.ID, "imported", fmt.Sprintf("Imported %d episode%s from review (mapped by hand): %s", n, plural(n), r.Name))
		c.seriesImported(ctx, s.ID, placed)
	}
	if len(failures) > 0 {
		// The review stays open so the rest can be tried again; what landed stays landed.
		c.log.Warn("review: some mapped files couldn't be placed", "release", r.Name, "placed", n, "failed", len(failures))
		return n, fmt.Errorf("%w: placed %d episode%s, but %d couldn't be: %s", ErrNothingToImport,
			n, plural(n), len(failures), strings.Join(failures, "; "))
	}
	c.closeReviewImported(ctx, r)
	return n, c.resolveReview(ctx, r.ID, ResolutionMapped)
}

// ImportReviewMapped is PlanReviewMap then ApplyReviewMap.
func (c *Coordinator) ImportReviewMapped(ctx context.Context, id, seriesID int64, mappings []FileMapping) (int, error) {
	p, err := c.PlanReviewMap(ctx, id, seriesID, mappings)
	if err != nil {
		return 0, err
	}
	return c.ApplyReviewMap(ctx, p)
}
