package automation

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/music"
)

// matchScanFolder is Match… on a folder the library scan held for review (MUS-37): its files
// are matched to the album a person chose (on the edition they chose, with
// WithMusicEdition, else the best of the album's editions) and recorded where they are.
// Nothing is staged, moved, renamed or retagged: the folder is already in the library.
func (c *Coordinator) matchScanFolder(ctx context.Context, r Review, albumID int64) error {
	if c.music == nil || c.imp == nil {
		return moduleOffError{reviewModuleLabel["music"]}
	}
	root := filepath.Clean(c.imp.MusicDir())
	if !within(root, r.ContentPath) {
		// The music folder changed since the scan: recording files outside it would put
		// the library somewhere it isn't.
		return fmt.Errorf("%w: %s isn't in the music folder (%s) any more", ErrNothingToImport, r.ContentPath, root)
	}
	al, err := c.music.GetAlbum(ctx, albumID)
	if err != nil {
		return err
	}
	files := scanFolderFiles(r.ContentPath)
	if len(files) == 0 {
		return fmt.Errorf("%w: no audio files are left in %s", ErrNothingToImport, r.ContentPath)
	}
	audio := make([]music.AudioFile, 0, len(files))
	for _, f := range files {
		audio = append(audio, music.AudioFile{Path: f.path, SizeBytes: f.size, Disc: music.DiscOfFolder(filepath.Base(filepath.Dir(f.path)))})
	}
	// Every edition of the album is weighed, not just its target (#116): the scan held the
	// folder because the target didn't fit, often because it's another pressing with other
	// track lengths. The best fit takes the files, or the edition the person chose ("Match
	// as …"), and the album moves to it unless it's pinned. It moves back if nothing is
	// recorded.
	before := al
	recorded := 0
	defer func() {
		if recorded == 0 {
			c.switchBack(context.WithoutCancel(ctx), before, albumImport{})
		}
	}()
	tracks, matched, _, err := c.matchInto(ctx, al, audio, filepath.Base(r.ContentPath), false)
	if err != nil {
		return err
	}
	if len(tracks) == 0 {
		return fmt.Errorf("%w: %q has no track listing on MusicBrainz yet", ErrNothingToImport, al.Title)
	}
	byPath := make(map[string]scanFile, len(files))
	for _, f := range files {
		byPath[f.path] = f
	}
	var out []music.ScannedTrack
	for _, t := range tracks {
		a, ok := matched[t.ID]
		if !ok || (t.HasFile && t.FilePath == a.Path) {
			continue // nothing to record: this very file is the track's already
		}
		// A track holding another file that's still there keeps it (RecordScanned); one
		// whose file is gone from its path takes this one.
		f := byPath[a.Path]
		rec := music.FileRecord{Path: f.path, SizeBytes: f.size, MtimeUnix: f.mtime, FileID: f.fileID,
			Quality: music.Quality(music.FormatOf(f.path))}
		if _, info, err := c.readScanFile(f.path); err == nil && info != nil {
			if q := music.TierFromProbe(*info); q != music.QualityUnknown {
				rec.Quality, rec.BitrateKbps = q, info.BitrateKbps
			}
			rec.Props = info
		}
		out = append(out, music.ScannedTrack{TrackID: t.ID, Rec: rec})
	}
	n, _, err := c.music.RecordScanned(ctx, out)
	if err != nil {
		return err
	}
	recorded = n
	if n == 0 {
		return fmt.Errorf("%w: no file in the folder fits a track of %q that still needs one", ErrNothingToImport, al.Title)
	}
	c.log.Info("music: matched a library folder by hand", "album", al.Title, "folder", r.ContentPath, "tracks", n)
	c.music.AddEvent(ctx, al.ArtistID, "imported", fmt.Sprintf("Matched %d track(s) of %q from %s", n, al.Title, r.Name))
	// The next scan looks at the folder afresh (its files are on record now, so that costs
	// nothing).
	if err := c.music.ForgetScanDir(ctx, r.ContentPath); err != nil {
		c.log.Warn("music: couldn't reset the scan's memory of a matched folder", "folder", r.ContentPath, "err", err)
	}
	return nil
}

// scanFolderFiles lists an album folder's audio files (its disc folders' too), as the scan
// sees them: big enough to be tracks, with size, time and inode. Bins and hidden folders
// are skipped.
func scanFolderFiles(dir string) []scanFile {
	var out []scanFile
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && library.SkipScanDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !music.IsAudioFile(p) {
			return nil
		}
		fi, err := os.Lstat(p)
		if err != nil || fi.Size() < minTrackBytes {
			return nil
		}
		out = append(out, scanFile{path: p, size: fi.Size(), mtime: fi.ModTime().Unix(), fileID: music.FileIdentity(fi)})
		return nil
	})
	return out
}
