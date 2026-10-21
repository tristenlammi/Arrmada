package movies

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/outbox"
	"github.com/tristenlammi/arrmada/internal/store"
)

// A rename is planned for every track first and shown to the person; only then does
// anything move. The plan is read again when it's applied, and a track whose file or
// proposed name changed in between is left alone. Nothing is ever moved onto an existing
// file: a target that's taken is a conflict, reported and skipped.

// What a planned track rename will do (RenameItem.Status).
const (
	RenameOK       = "ok"       // the file moves to Proposed
	RenameSame     = "same"     // already named right
	RenameConflict = "conflict" // something else has the new name; the file stays put
	RenameMissing  = "missing"  // the recorded file isn't on disk
)

// Why a track was left alone.
const (
	reasonTaken        = "a different file already exists there"
	reasonOtherTrack   = "another version of this movie has that name"
	reasonSameName     = "another version of this movie would get the same name"
	reasonNotOnDisk    = "the file isn't on disk"
	reasonChanged      = "changed since the preview"
	reasonAlreadyNamed = "already named correctly"
	reasonMovieGone    = "the movie is no longer in the library"
)

// RenameItem is one track's planned rename. Media says what kind of title it is
// ("movie"), so series can share the organize endpoints' shape later.
type RenameItem struct {
	Media     string `json:"media"`
	MovieID   int64  `json:"movie_id"`
	VersionID int64  `json:"version_id"` // 0 = the default track
	Title     string `json:"title"`
	Year      int    `json:"year,omitempty"`
	Label     string `json:"label"`
	Current   string `json:"current"`
	Proposed  string `json:"proposed"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	// Extras are the other files that move with this one (an "ok" item only): its paired
	// subtitles and, on the first item leaving a folder that is provably the movie's own,
	// that folder's metadata and artwork. Applying moves no more than this list.
	Extras []RenameExtra `json:"extras,omitempty"`
	// Kept says why the old folder's other files stay where they are, when the file
	// leaves a folder that holds more than the movie's files and subtitles.
	Kept string `json:"kept,omitempty"`
}

// RenameResult is what applying one planned item did.
type RenameResult struct {
	MovieID   int64  `json:"movie_id"`
	VersionID int64  `json:"version_id"`
	Label     string `json:"label"`
	From      string `json:"from"`
	To        string `json:"to"`
	Renamed   bool   `json:"renamed"`
	Reason    string `json:"reason,omitempty"` // why it was left alone
}

// libraryPlanThreshold: asking for more titles than this reads the whole library in two
// queries instead of two per title.
const libraryPlanThreshold = 50

// RenamePlan lists every track of the given movies (all of them when ids is empty) with
// where its file is and where the naming scheme puts it. It changes nothing: one stat
// per file, one per proposed name. Ids that aren't in the library are skipped.
func (s *Service) RenamePlan(ctx context.Context, ids []int64) ([]RenameItem, error) {
	var out []RenameItem
	recs := s.newRenameRecords(ctx) // one for the whole plan: each record is read and resolved once
	if len(ids) == 0 || len(ids) > libraryPlanThreshold {
		byMovie, all, err := s.LibraryVersionRows(ctx)
		if err != nil {
			return nil, err
		}
		want := map[int64]bool{}
		for _, id := range ids {
			want[id] = true
		}
		for _, m := range all {
			if len(ids) > 0 && !want[m.ID] {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			out = append(out, s.planMovie(m, byMovie[m.ID], recs)...)
		}
		return out, nil
	}
	for _, id := range ids {
		m, versions, err := s.trackRows(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, s.planMovie(m, versions, recs)...)
	}
	return out, nil
}

// trackRows reads a movie and all its tracks from the database. Unlike VersionRows it
// fails when the extra tracks can't be read: a plan missing a track could hand that
// track's name to another.
func (s *Service) trackRows(ctx context.Context, id int64) (Movie, []Version, error) {
	m, err := s.repo.Get(ctx, id)
	if err != nil {
		return Movie{}, nil, err
	}
	extras, err := s.repo.ListVersions(ctx, id)
	if err != nil {
		return Movie{}, nil, err
	}
	return m, append([]Version{defaultVersionRow(m)}, extras...), nil
}

// planMovie plans one movie's tracks. A file two tracks both record is planned once,
// under the first; applying it repoints both. recs are the pass's records of every film,
// read only when there is a clean rename (planExtras).
func (s *Service) planMovie(m Movie, versions []Version, recs *renameRecords) []RenameItem {
	owned := map[string]int64{} // every file this movie's tracks record → its first track
	for _, v := range versions {
		if v.HasFile && v.FilePath != "" {
			if _, ok := owned[v.FilePath]; !ok {
				owned[v.FilePath] = v.ID
			}
		}
	}
	// One stat per file, shared by the naming (is the cached probe still this file's?) and
	// the judging (is the file there?).
	stats := map[string]fileStat{}
	for path := range owned {
		fi, err := s.statFile(path)
		stats[path] = fileStat{fi, err}
	}
	targets := s.trackTargets(m, versions, stats)
	var items []RenameItem
	claimed := map[string]bool{}
	for _, v := range versions {
		if !v.HasFile || v.FilePath == "" || owned[v.FilePath] != v.ID {
			continue
		}
		it := RenameItem{Media: "movie", MovieID: m.ID, VersionID: v.ID, Title: m.Title, Year: m.Year,
			Label: v.Label, Current: v.FilePath, Proposed: targets[v.ID]}
		it.Status, it.Reason = s.judgeRename(it, stats[v.FilePath], owned, claimed)
		if it.Status == RenameOK {
			claimed[it.Proposed] = true
		}
		items = append(items, it)
	}
	s.planExtras(m, versions, items, recs)
	return items
}

// fileStat is one stat of a track's file.
type fileStat struct {
	fi  os.FileInfo
	err error
}

// judgeRename decides one item's status against the disk and the movie's other tracks.
// st is the stat of the item's current file.
func (s *Service) judgeRename(it RenameItem, st fileStat, owned map[string]int64, claimed map[string]bool) (string, string) {
	cur, err := st.fi, st.err
	if err == nil && cur == nil {
		err = fs.ErrNotExist
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return RenameMissing, reasonNotOnDisk
		}
		return RenameMissing, "couldn't read the file: " + err.Error()
	}
	if it.Proposed == "" || it.Proposed == it.Current {
		return RenameSame, ""
	}
	if owner, ok := owned[it.Proposed]; ok && owner != it.VersionID {
		return RenameConflict, reasonOtherTrack
	}
	if claimed[it.Proposed] {
		return RenameConflict, reasonSameName
	}
	if fi, err := os.Lstat(it.Proposed); err == nil {
		// The same file under another name (a hardlink, or a case-only change on a
		// case-insensitive disk) isn't in the way: Move just drops the old name.
		if !os.SameFile(cur, fi) {
			return RenameConflict, reasonTaken
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return RenameConflict, "couldn't check the new name: " + err.Error()
	}
	return RenameOK, ""
}

// trackTargets is where each track's file belongs under the naming scheme, keyed by
// version id. Each name comes from the one per-track builder imports use (TrackTarget:
// the scheme, the track's edition as Plex's {edition-…}, the movie's ids, and its label
// only to keep clear of another track's file), so a rename lands every track exactly
// where an import would have put it. Tracks are named in order, default first, and each
// one's planned name is kept clear of by the tracks after it, as their files would be.
//
// The quality, codec and group can also come from the track's recorded release, which
// still has what a name written under an older, shorter scheme dropped. That release is
// the download's name while an import named the file from the media file inside it, so a
// rename can now propose a fuller name than the import gave — and a release that
// contradicts the file's name is ignored (library.RenameSource.Release has the rule).
func (s *Service) trackTargets(m Movie, versions []Version, stats map[string]fileStat) map[int64]string {
	out := map[int64]string{}
	var planned []string
	for _, v := range versions {
		if !v.HasFile || v.FilePath == "" {
			continue
		}
		t := s.TrackNaming(m, versions, v)
		t.Protected = append(t.Protected, planned...)
		target := s.imp.MovieRenameTarget(t, renameSource(v, stats[v.FilePath].fi), filepath.Ext(v.FilePath))
		out[v.ID] = target
		planned = append(planned, target)
	}
	return out
}

// renameSource is what track v's file is renamed from: its name, its recorded release, and
// the cached probe when it still describes the file stat'd as fi (same size and mtime).
// RepointMovieFile keeps the release current through a Convert swap and a rename, and
// re-reads a changed file.
func renameSource(v Version, fi os.FileInfo) library.RenameSource {
	src := library.RenameSource{FileName: filepath.Base(v.FilePath), SourceRelease: v.SourceRelease}
	if f := v.File; f != nil && f.Probed && fi != nil && cacheCurrent(f, fi) {
		src.Resolution, src.Codec = f.Resolution, f.Codec
	}
	return src
}

// PlanToken fingerprints the renames a plan would make, and every file moving with them,
// so applying it can tell whether the library changed since the preview it was asked from.
func PlanToken(items []RenameItem) string {
	var lines []string
	for _, it := range items {
		if it.Status == RenameOK {
			parts := []string{strconv.FormatInt(it.MovieID, 10), strconv.FormatInt(it.VersionID, 10), it.Current, it.Proposed}
			for _, x := range it.Extras {
				parts = append(parts, x.Kind, x.From, x.To)
			}
			lines = append(lines, strings.Join(parts, "\x00"))
		}
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:16])
}

// ApplyRename carries out planned renames. Each movie's plan is read again first: an item
// whose file or proposed name changed since it was planned, or that is no longer a clean
// move, is left alone and reported. Files move without ever replacing another file
// (library.Importer.Move) and take along what the item's Extras list — its subtitles and,
// leaving the movie's own folder, that folder's metadata and artwork — when the fresh plan
// still lists it (renamefolder.go); an emptied old folder is removed. Every moved track's
// record changes in one transaction with its movie.changed row, so Convert, Subtitles and
// a Plex scan follow the new name.
func (s *Service) ApplyRename(ctx context.Context, items []RenameItem) ([]RenameResult, error) {
	return s.ApplyRenameEach(ctx, items, nil)
}

// ApplyRenameEach is ApplyRename, calling after (when set) with each movie's results as it
// finishes. One pass reads the library's records once and keeps them current as it moves
// files, so a later movie sees where an earlier one went. A cancelled ctx stops it between
// movies, never in the middle of one's files.
func (s *Service) ApplyRenameEach(ctx context.Context, items []RenameItem, after func(movieID int64, res []RenameResult)) ([]RenameResult, error) {
	recs := s.newRenameRecords(ctx)
	var order []int64
	byMovie := map[int64][]RenameItem{}
	for _, it := range items {
		if _, ok := byMovie[it.MovieID]; !ok {
			order = append(order, it.MovieID)
		}
		byMovie[it.MovieID] = append(byMovie[it.MovieID], it)
	}
	var out []RenameResult
	for _, id := range order {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		res, err := s.applyMovieRename(ctx, id, byMovie[id], recs)
		out = append(out, res...)
		if after != nil {
			after(id, res)
		}
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *Service) applyMovieRename(ctx context.Context, id int64, asked []RenameItem, recs *renameRecords) ([]RenameResult, error) {
	skip := func(it RenameItem, reason string) RenameResult {
		return RenameResult{MovieID: it.MovieID, VersionID: it.VersionID, Label: it.Label, From: it.Current, To: it.Proposed, Reason: reason}
	}
	var out []RenameResult
	m, versions, err := s.trackRows(ctx, id)
	if errors.Is(err, ErrNotFound) {
		for _, it := range asked {
			out = append(out, skip(it, reasonMovieGone))
		}
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	fresh := map[int64]RenameItem{}
	for _, it := range s.planMovie(m, versions, recs) {
		fresh[it.VersionID] = it
	}
	// Only what the preview listed may move with a file, and only while the fresh plan
	// still lists it too.
	allowed := map[string]bool{}
	for _, it := range asked {
		for _, x := range it.Extras {
			allowed[extraKey(x)] = true
		}
	}
	var moves []RenameItem
	for _, it := range asked {
		now, ok := fresh[it.VersionID]
		switch {
		case !ok || now.Current != it.Current || now.Proposed != it.Proposed:
			out = append(out, skip(it, reasonChanged))
		case now.Status == RenameSame:
			out = append(out, skip(it, reasonAlreadyNamed))
		case now.Status != RenameOK:
			out = append(out, skip(it, now.Reason))
		default:
			moves = append(moves, now)
		}
	}

	// Which new folders this rename creates, looked at before anything moves.
	created := map[string]bool{}
	for _, it := range moves {
		nd := filepath.Dir(it.Proposed)
		if _, err := os.Lstat(nd); errors.Is(err, fs.ErrNotExist) {
			created[nd] = true
		}
	}
	moved := 0
	left := map[string][]renameMove{} // old folder → the files that left it
	var dirs []string
	for _, it := range moves {
		r, ok := s.moveTrackFile(ctx, m, versions, it, allowed)
		out = append(out, r)
		if ok {
			moved++
			recs.moved(m.ID, it.Current, it.Proposed)
			if od := filepath.Dir(it.Current); od != filepath.Dir(it.Proposed) {
				if _, seen := left[od]; !seen {
					dirs = append(dirs, od)
				}
				left[od] = append(left[od], renameMove{it.Current, it.Proposed})
			}
		}
	}
	// The tracks have moved and their records are written: the folder files follow (or
	// stay) whatever happens to ctx now, so a cancelled job never strands them half way.
	for _, od := range dirs {
		s.leaveFolder(m, versions, od, left[od], recs, created[filepath.Dir(left[od][0].to)], allowed)
	}
	if moved > 0 {
		s.log.Info("movie renamed", "movie_id", id, "files", moved)
	}
	return out, nil
}

// moveTrackFile moves one planned file and repoints every track recording it. If the
// record can't be written the file is put back, so the record and the disk never
// disagree.
func (s *Service) moveTrackFile(ctx context.Context, m Movie, versions []Version, it RenameItem, allowed map[string]bool) (RenameResult, bool) {
	res := RenameResult{MovieID: m.ID, VersionID: it.VersionID, Label: it.Label, From: it.Current, To: it.Proposed}
	if err := s.imp.Move(it.Current, it.Proposed); err != nil {
		if errors.Is(err, library.ErrTargetExists) {
			res.Reason = reasonTaken
		} else {
			res.Reason = "couldn't move the file: " + err.Error()
		}
		return res, false
	}
	subs := s.moveSubs(it.Extras, allowed)

	// The tracks that follow this file, with their cached facts moved along (read before
	// the transaction: a stale cache means a probe, and that can take seconds).
	type follow struct {
		v     Version
		media string
		size  int64
	}
	var follows []follow
	for _, v := range versions {
		if !v.HasFile || v.FilePath != it.Current {
			continue
		}
		info := s.movedMedia(v.File, it.Proposed)
		size := v.SizeBytes
		if info != nil && !info.Missing {
			size = info.SizeBytes
		}
		follows = append(follows, follow{v, mediaJSONOf(info), size})
	}
	err := s.repo.inTx(ctx, func(tx *store.Transaction, r *Repo) error {
		for _, f := range follows {
			if f.v.IsDefault {
				if err := setDefaultFileIn(ctx, r, m.ID, it.Proposed, f.media); err != nil {
					return err
				}
			} else if err := r.SetVersionFile(ctx, f.v.ID, it.Proposed, f.size, f.media); err != nil {
				return err
			}
			detail := filepath.Base(it.Current) + " → " + filepath.Base(it.Proposed)
			if !f.v.IsDefault {
				detail += " (" + f.v.Label + ")"
			}
			if err := r.AddEvent(ctx, m.ID, "renamed", detail); err != nil {
				return err
			}
			if err := s.enqueue(ctx, tx, outbox.TopicMovieChanged,
				outbox.MovieChanged{MovieID: m.ID, VersionID: f.v.ID, Change: outbox.ChangeRenamed, OldPath: it.Current, Path: it.Proposed},
				movieKey(m.ID)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if berr := s.imp.Move(it.Proposed, it.Current); berr == nil {
			for _, x := range subs {
				_ = s.imp.Move(x.To, x.From)
			}
			res.Reason = "couldn't record the new name, so the file was put back: " + err.Error()
		} else {
			s.log.Error("movie rename: the record couldn't be written and the file couldn't be put back — the movie points at the old name",
				"movie_id", m.ID, "old", it.Current, "new", it.Proposed, "err", err, "move_back_err", berr)
			res.Reason = fmt.Sprintf("couldn't record the new name (%v); the file is at %s — refresh the movie to pick it up", err, it.Proposed)
		}
		return res, false
	}
	for _, f := range follows {
		s.publish("movie.renamed", map[string]any{"id": m.ID, "version_id": f.v.ID, "old_path": it.Current, "new_path": it.Proposed})
	}
	res.Renamed = true
	return res, true
}

// Rename applies every clean rename for one movie (the plan's "ok" items). It returns
// the first item it couldn't apply as an error; the rest still ran.
func (s *Service) Rename(ctx context.Context, id int64) error {
	if _, err := s.repo.Get(ctx, id); err != nil {
		return err
	}
	plan, err := s.RenamePlan(ctx, []int64{id})
	if err != nil {
		return err
	}
	var todo []RenameItem
	for _, it := range plan {
		if it.Status == RenameOK {
			todo = append(todo, it)
		}
	}
	res, err := s.ApplyRename(ctx, todo)
	if err != nil {
		return err
	}
	for _, r := range res {
		if !r.Renamed {
			return fmt.Errorf("%s: %s", filepath.Base(r.From), r.Reason)
		}
	}
	return nil
}
