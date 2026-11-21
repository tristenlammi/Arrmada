package series

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/parser"
)

// BorrowedFile is a file recorded for a show that looks like another library show's.
type BorrowedFile struct {
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
	Path    string `json:"path"`
	// Owner is the title of the show the file's name says it is.
	Owner string `json:"owner"`
}

// BorrowedFiles lists the files recorded for show seriesID that look like another library
// show's, for a person to look over and confirm (ForgetBorrowedFiles). Nothing is ever
// forgotten without that: by name alone a sequel's files kept under the first show's name
// ("Bleach - S17E01" for "Bleach: Thousand-Year Blood War", in Bleach's folder) look just
// like this, and they are the show's own.
//
// What it is for: an early library scan could match a folder to the wrong show ("Power"
// taken for "Blossoms of Power"), which recorded the folder's files as its own. Once the
// right show was added too, the folder read as shared, nothing could be imported for the
// wrong one, and nothing in the app could clear it.
//
// A file is listed when all of these hold:
//   - its name carries a title, and that title isn't this show's in any way (its title,
//     display title, override or any alias, whatever the year or country);
//   - another show in the library has the very same file recorded, or has its own files
//     recorded in the folder the file is in (its show folder, above a season folder);
//   - and the name is that other show's (FitRelease, with its year and country checks).
func (s *Service) BorrowedFiles(ctx context.Context, seriesID int64) ([]BorrowedFile, error) {
	show, err := s.repo.Get(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	show.Aliases = s.repo.Aliases(ctx, seriesID)
	out := []BorrowedFile{}
	var others map[int64]Series
	for _, e := range s.repo.RecordedEpisodeFiles(ctx, seriesID) {
		rel := library.ParseEpisodeFile(e.FilePath)
		if parser.TitleKey(rel.Title) == "" || namesShow(rel, show) {
			continue
		}
		owners := s.repo.OtherSeriesWithFile(ctx, seriesID, e.FilePath)
		if dir := s.ShowFolderFor(e.FilePath); dir != "" {
			owners = append(owners, s.repo.OtherSeriesUnder(ctx, seriesID, dir, 50)...)
		}
		if len(owners) == 0 {
			continue
		}
		if others == nil {
			others = map[int64]Series{}
			if all, err := s.List(ctx); err == nil {
				for _, o := range all {
					others[o.ID] = o
				}
			}
		}
		for _, id := range owners {
			if o, ok := others[id]; ok && FitRelease(rel, o).OK {
				out = append(out, BorrowedFile{Season: e.SeasonNumber, Episode: e.EpisodeNumber, Path: e.FilePath, Owner: o.Title})
				break
			}
		}
	}
	return out, nil
}

// ForgetBorrowedFiles forgets, for show seriesID, the files among paths that BorrowedFiles
// lists right now — what the person was shown and confirmed, and still true — and returns
// how many episodes it cleared. No file is touched: only this show's claim on it goes, and
// the episode becomes missing.
func (s *Service) ForgetBorrowedFiles(ctx context.Context, seriesID int64, paths []string) (int, error) {
	listed, err := s.BorrowedFiles(ctx, seriesID)
	if err != nil {
		return 0, err
	}
	asked := map[string]bool{}
	for _, p := range paths {
		asked[p] = true
	}
	cleared := 0
	owners := map[string]bool{}
	for _, b := range listed {
		if !asked[b.Path] {
			continue
		}
		// Only the record that was judged: one an import replaced meanwhile stays.
		n, err := s.repo.ClearEpisodeFileAt(ctx, seriesID, b.Season, b.Episode, b.Path)
		if err != nil || n == 0 {
			continue
		}
		cleared++
		owners[b.Owner] = true
		s.log.Info("series: forgot a file recorded for the wrong show, asked for by hand; the file is untouched",
			"series_id", seriesID, "episode", fmt.Sprintf("S%02dE%02d", b.Season, b.Episode),
			"file", filepath.Base(b.Path), "belongs_to", b.Owner)
	}
	if cleared > 0 {
		names := make([]string, 0, len(owners))
		for o := range owners {
			names = append(names, o)
		}
		sort.Strings(names)
		s.folderPaths.Delete(seriesID)
		s.AddEvent(ctx, seriesID, "repaired", fmt.Sprintf("Forgot %d episode file(s) that are %s's, asked for by hand. Nothing on disk was touched; those episodes are missing again.", cleared, strings.Join(names, ", ")))
	}
	return cleared, nil
}

// namesShow reports whether a file's parsed title is one of show sr's names at all — its
// title, display title, override or an alias — whether or not the year and country agree.
func namesShow(rel parser.Release, sr Series) bool {
	if fitsShow(rel, sr) || FitRelease(rel, sr).Title {
		return true
	}
	key := parser.TitleKey(rel.Title)
	for _, t := range []string{sr.DisplayTitle, sr.TitleOverride} {
		if t != "" && parser.TitleKey(t) == key {
			return true
		}
	}
	return false
}
