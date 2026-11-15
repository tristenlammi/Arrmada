package series

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tristenlammi/arrmada/internal/library"
)

// ErrNoSuchSeason is a season number the show doesn't have.
var ErrNoSuchSeason = errors.New("the show has no such season")

// SeasonDeletePlan lists one season's files without changing anything: what the delete
// dialog shows.
type SeasonDeletePlan struct {
	Paths []string `json:"paths"` // each video once, however many episodes it holds
	Bytes int64    `json:"bytes"`
	// Shared are videos that also hold an episode of another season (a file named
	// S01E10-S02E01): they stay, with every episode they hold.
	Shared []string `json:"shared,omitempty"`
}

// seasonFiles splits a season's videos into the ones only it holds and the ones another
// season shares.
func (s *Service) seasonFiles(ctx context.Context, seriesID int64, season int) (own, shared []string, seasons []Season, err error) {
	seasons, err = s.repo.SeasonsFor(ctx, seriesID)
	if err != nil {
		return nil, nil, nil, err
	}
	found := false
	inSeason, elsewhere := map[string]bool{}, map[string]bool{}
	for _, sn := range seasons {
		if sn.SeasonNumber == season {
			found = true
		}
		for _, e := range sn.Episodes {
			if !e.HasFile || e.FilePath == "" {
				continue
			}
			if e.SeasonNumber == season {
				inSeason[e.FilePath] = true
			} else {
				elsewhere[e.FilePath] = true
			}
		}
	}
	if !found {
		return nil, nil, seasons, ErrNoSuchSeason
	}
	for _, sn := range seasons {
		for _, e := range sn.Episodes {
			p := e.FilePath
			if e.SeasonNumber != season || !inSeason[p] {
				continue
			}
			delete(inSeason, p) // each once, in episode order
			if elsewhere[p] {
				shared = append(shared, p)
			} else {
				own = append(own, p)
			}
		}
	}
	return own, shared, seasons, nil
}

// SeasonDeletePlan is what DeleteSeasonFiles would move to the bin.
func (s *Service) SeasonDeletePlan(ctx context.Context, seriesID int64, season int) (SeasonDeletePlan, error) {
	own, shared, _, err := s.seasonFiles(ctx, seriesID, season)
	if err != nil {
		return SeasonDeletePlan{}, err
	}
	plan := SeasonDeletePlan{Paths: own, Shared: shared}
	if plan.Paths == nil {
		plan.Paths = []string{}
	}
	for _, p := range own {
		if fi, err := os.Stat(p); err == nil {
			plan.Bytes += fi.Size()
		}
	}
	return plan, nil
}

// DeleteSeasonFiles moves one season's files, with their subtitles, to the recycle bin and
// marks its episodes missing (#111). The show, its other seasons and the season's own
// monitoring are untouched (the caller decides that). A file holding an episode of another
// season too stays. It stops at the first file the bin refuses: what already moved reads
// as missing, the rest stays as it was.
func (s *Service) DeleteSeasonFiles(ctx context.Context, seriesID int64, season int) (DeleteSummary, error) {
	sum := DeleteSummary{Moved: []string{}, Failed: []string{}}
	own, _, seasons, err := s.seasonFiles(ctx, seriesID, season)
	if err != nil {
		return sum, err
	}
	bin := s.bin
	if bin == nil {
		bin = library.SingleBin("")
	}
	if len(own) > 0 {
		// Pre-flight, as a show's delete: a bin that can't be made fails before a file moves.
		dir, err := bin.For(own[0])
		if err == nil {
			err = os.MkdirAll(dir, 0o755)
		}
		if err != nil && !errors.Is(err, library.ErrRecycleDisabled) {
			sum.Failed = append(sum.Failed, filepath.Base(own[0]))
			s.repo.AddEvent(ctx, seriesID, "delete.failed", fmt.Sprintf("Couldn't use the recycle bin, so nothing of season %d was deleted: %v", season, err))
			return sum, fmt.Errorf("%w: %v", ErrFilesNotRemoved, err)
		}
	}

	moved := map[string]bool{}
	dirs := map[string]bool{}
	shows := map[string]bool{}
	var failure error
	for _, video := range own {
		var subs []string
		if !library.SharesBase(video) { // a same-name sibling video still pairs with them
			subs = library.Sidecars(video)
		}
		size := int64(0)
		if fi, err := os.Stat(video); err == nil {
			size = fi.Size()
		}
		if _, err := library.RemoveToBin(bin, video); err != nil {
			sum.Failed = append(sum.Failed, filepath.Base(video))
			failure = err
			break
		}
		moved[video] = true
		sum.Moved = append(sum.Moved, filepath.Base(video))
		sum.Bytes += size
		s.fileRemoved(video)
		show := s.ShowFolderFor(video)
		shows[show] = true
		// The season's own folder may go once empty; never the show's folder, which the
		// show still has.
		if d := filepath.Dir(video); d != show {
			dirs[d] = true
		}
		for _, sub := range subs {
			if _, err := library.RemoveToBin(bin, sub); err != nil {
				s.log.Warn("series: subtitle left behind", "path", sub, "err", err)
				continue
			}
			sum.Moved = append(sum.Moved, filepath.Base(sub))
			s.fileRemoved(sub)
		}
	}
	for _, sn := range seasons {
		for _, e := range sn.Episodes {
			if e.SeasonNumber == season && moved[e.FilePath] {
				if err := s.repo.ClearEpisodeFile(ctx, seriesID, e.SeasonNumber, e.EpisodeNumber); err != nil {
					s.log.Warn("series: couldn't mark a deleted episode missing", "err", err)
				}
			}
		}
	}
	s.pruneEmptyDirs(dirs)
	for d := range shows {
		if d != "" {
			s.changed(d)
		}
	}
	if failure != nil {
		s.repo.AddEvent(ctx, seriesID, "delete.failed", fmt.Sprintf("Stopped deleting season %d after %d file(s): %v", season, len(sum.Moved), failure))
		return sum, fmt.Errorf("%w: %v", ErrFilesNotRemoved, failure)
	}
	s.repo.AddEvent(ctx, seriesID, "file.deleted", fmt.Sprintf("%s: %d file(s) deleted", seasonWords(season), len(own)))
	return sum, nil
}

// seasonWords names a season in an event: "Season 2", "Specials".
func seasonWords(n int) string {
	if n == 0 {
		return "Specials"
	}
	return fmt.Sprintf("Season %d", n)
}
