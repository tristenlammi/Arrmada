package download

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/diskspace"
)

// The path half of a download client's Test: where the client saves, whether Arrmada can
// see that folder (after the client's path mappings), whether it can see the newest
// finished download, and whether the save folder shares a filesystem with the libraries
// (hardlinks need that). A client that downloads fine but whose paths Arrmada can't see
// never imports anything, and nothing said so until now.

// PathEnv is where Arrmada keeps things, for a path check.
type PathEnv struct {
	// Downloads is Arrmada's own downloads folder: where a missing mapping most likely points.
	Downloads string
	// Libraries are the library folders imports go to, for the same-filesystem check.
	Libraries []string
	// DataDir is the database folder; a suggested mapping never points there.
	DataDir string
	// FolderDownloads maps a library folder (cleaned) to its own downloads folder (GitHub
	// #11). Torrents for that library save there, so the same-filesystem check judges the
	// library against it rather than the client's default save folder. SABnzbd's leftover
	// guard never deletes a job folder that holds one: torrents seed in it.
	FolderDownloads map[string]string
}

// sameDeviceFn compares two folders' filesystem ids (a test swaps it: temp dirs share one).
var sameDeviceFn = diskspace.SameDevice

// ownDownloads is every library folder's own downloads folder, once each.
func (e PathEnv) ownDownloads() []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range e.FolderDownloads {
		if d = strings.TrimSpace(d); d != "" && !seen[filepath.Clean(d)] {
			seen[filepath.Clean(d)] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// PathCheck is one path the client reported and what Arrmada made of it.
type PathCheck struct {
	// What is "save_folder" (the client's default save path) or "newest_download" (the
	// content of its most recently added finished torrent).
	What    string `json:"what"`
	Remote  string `json:"remote"`
	Local   string `json:"local"`
	Visible bool   `json:"visible"`
	// Gone marks a newest download that's missing although its folder is visible through
	// the same mapping: the files were moved or deleted, which no mapping would fix.
	Gone bool `json:"gone,omitempty"`
	// Category names the category a "category_folder" check is for (a usenet client can
	// give a category a folder of its own, outside its completed folder).
	Category string `json:"category,omitempty"`
	// Advisory marks a path Arrmada doesn't need to see — qBittorrent's incomplete folder
	// ("incomplete_folder"): imports wait for the move out of it. Shown for information,
	// it never makes the report not OK, and the page doesn't draw it as a failure.
	Advisory bool `json:"advisory,omitempty"`
}

// PathReport is the path check's answer.
type PathReport struct {
	Checks []PathCheck `json:"checks"`
	// OtherDrive lists library folders on a different filesystem from the save folder:
	// imports into them are copied, not hardlinked.
	OtherDrive []string `json:"other_drive,omitempty"`
	// Suggestion is a mapping that would make the unseen paths visible, when one can be
	// worked out from what Arrmada can see.
	Suggestion *PathMap `json:"suggestion,omitempty"`
	// OK is true when every reported path is visible.
	OK      bool   `json:"ok"`
	Summary string `json:"summary"`
}

// savePathReader is a client that can say where it saves downloads.
type savePathReader interface {
	DefaultSavePath(ctx context.Context, dc Client) (string, error)
}

// ErrNoPathCheck: this kind of client can't report its paths.
var ErrNoPathCheck = errors.New("this download client can't report where it saves")

// TestPaths runs the path check against a stored client. It only reads: the client's
// preferences and torrent list, and whether folders exist here.
func (s *Service) TestPaths(ctx context.Context, id int64, env PathEnv) (PathReport, error) {
	c, err := s.repo.Get(ctx, id)
	if err != nil {
		return PathReport{}, err
	}
	impl, ok := s.registry.For(c.Kind)
	if !ok {
		return PathReport{}, ErrNoPathCheck
	}
	spr, ok := impl.(savePathReader)
	if !ok {
		return PathReport{}, ErrNoPathCheck
	}
	save, err := spr.DefaultSavePath(ctx, c)
	if err != nil {
		return PathReport{}, err
	}
	// The newest finished torrent shows whether real downloads land where Arrmada looks;
	// a list that can't be read only costs the report that line.
	var newest *Item
	if items, err := impl.List(ctx, c); err == nil {
		newest = newestFinished(items)
	}
	rep := checkPaths(c, save, newest, env)
	// qBittorrent can download into an incomplete folder and move each torrent to its save
	// folder when it finishes. Said in the report, with the setting to change; a client
	// that can't say only costs the report that line.
	if ipr, ok := impl.(incompletePathReader); ok {
		if temp, on, err := ipr.IncompletePath(ctx, c); err == nil && on {
			addIncompleteFolder(&rep, c, temp)
		}
	}
	// SABnzbd saves into its completed folder (save above), except for categories given a
	// folder of their own: each of Arrmada's must be visible too.
	if cfr, ok := impl.(categoryFolderReader); ok {
		folders, err := cfr.CategoryFolders(ctx, c)
		if err != nil {
			return PathReport{}, err
		}
		addCategoryFolders(&rep, c, folders)
	}
	return rep, nil
}

// incompletePathReader is a client that can download into a folder of its own before
// moving each finished download to its save folder (qBittorrent's "Default Download Path
// (incomplete torrents)"): the folder as the client names it, and whether it's on.
type incompletePathReader interface {
	IncompletePath(ctx context.Context, dc Client) (path string, on bool, err error)
}

// addIncompleteFolder adds the client's incomplete folder to the report, and says what it
// means for imports.
//
// It's advisory, not a failure: Arrmada never reads a download until its client has moved
// it out (download.Item.ReadyToImport), so the folder needn't be mounted. But it's worth
// saying. Until that wait existed, a season pack imported mid-move was read from a folder
// Arrmada couldn't see, blocklisted as "no video", and grabbed again. The move is also a
// full copy of every finished download whenever the two folders are separate mounts, and
// it's the setup the bundled qBittorrent avoids (SetSavePath turns it off). So the report
// names the setting that turns it off.
func addIncompleteFolder(rep *PathReport, c Client, temp string) {
	pc := folderCheck(c, "incomplete_folder", temp)
	pc.Advisory = true
	rep.Checks = append(rep.Checks, pc)
	rep.Summary = strings.TrimSuffix(rep.Summary, ".") + fmt.Sprintf(
		". %s downloads into %s first and moves each torrent to its save folder when it finishes."+
			" Arrmada waits for that move before importing, so it needn't see %s — but turning off"+
			" \"Default Download Path (incomplete torrents)\" in %s's Downloads settings means there's no move"+
			" to wait for, and finished downloads aren't copied a second time.",
		c.Name, pc.Remote, pc.Remote, c.Name)
}

// categoryFolderReader is a client whose categories can have folders of their own
// (SABnzbd), as the client names them, keyed by category.
type categoryFolderReader interface {
	CategoryFolders(ctx context.Context, dc Client) (map[string]string, error)
}

// addCategoryFolders adds a check per category folder to the report, in name order; one
// Arrmada can't see makes the report not OK.
func addCategoryFolders(rep *PathReport, c Client, folders map[string]string) {
	names := make([]string, 0, len(folders))
	for name := range folders {
		names = append(names, name)
	}
	sort.Strings(names)
	var unseen []string
	for _, name := range names {
		pc := folderCheck(c, "category_folder", folders[name])
		pc.Category = name
		rep.Checks = append(rep.Checks, pc)
		if !pc.Visible {
			unseen = append(unseen, fmt.Sprintf("%s (%s)", pc.Remote, name))
		}
	}
	if len(unseen) == 0 {
		return
	}
	rep.OK = false
	rep.Summary = strings.TrimSuffix(strings.TrimSuffix(rep.Summary, "."), " ✓")
	rep.Summary += fmt.Sprintf(". Arrmada can't see the category %s %s. Mount %s into the Arrmada container, or add a path mapping.",
		plural(len(unseen), "folder", "folders"), strings.Join(unseen, ", "), plural(len(unseen), "it", "them"))
}

// newestFinished is the most recently added complete torrent with a content path, leaving
// out ones the client itself says have lost their files, and ones it's still moving out of
// its incomplete folder: their content path names that folder until the move is done, and
// reporting it as a download Arrmada "can't see" would be a false failure.
func newestFinished(items []Item) *Item {
	var best *Item
	for i := range items {
		it := &items[i]
		if !it.ReadyToImport() || it.ContentPath == "" || it.RawState == "missingFiles" {
			continue
		}
		if best == nil || it.AddedOn > best.AddedOn {
			best = it
		}
	}
	return best
}

// checkPaths builds the report from what the client said. Split from TestPaths so it can
// be tested against folders in a temp dir.
func checkPaths(c Client, save string, newest *Item, env PathEnv) PathReport {
	var rep PathReport
	saveLocal := c.PathMaps.ToLocal(save)
	saveCheck := PathCheck{What: "save_folder", Remote: save, Local: saveLocal, Visible: isDir(saveLocal)}
	rep.Checks = append(rep.Checks, saveCheck)
	var dl *PathCheck
	if newest != nil {
		local := c.PathMaps.ToLocal(newest.ContentPath)
		rep.Checks = append(rep.Checks, PathCheck{What: "newest_download", Remote: newest.ContentPath, Local: local, Visible: exists(local)})
		dl = &rep.Checks[len(rep.Checks)-1]
	}
	if dl != nil && !dl.Visible && saveCheck.Visible {
		if rp, ok := parseRemote(dl.Remote); ok {
			if sp, ok := parseRemote(save); ok {
				_, dl.Gone = rp.under(sp)
			}
		}
	}
	rep.OK = saveCheck.Visible && (dl == nil || dl.Visible || dl.Gone)

	// Only a torrent client saves into a library folder's own downloads folder; a usenet
	// client's jobs always go to its own folders, so it's judged against those.
	torrent := c.Kind.Transport() == TransportTorrent
	for _, root := range env.Libraries {
		if root == "" || !isDir(root) {
			continue
		}
		// A library with its own downloads folder gets its torrents there, not in the
		// client's default folder: that's the pair that has to hardlink.
		from := saveLocal
		if own := strings.TrimSpace(env.FolderDownloads[filepath.Clean(root)]); torrent && own != "" {
			from = own
		} else if !saveCheck.Visible {
			continue
		}
		if !isDir(from) {
			continue
		}
		if same, ok := sameDeviceFn(from, root); ok && !same {
			rep.OtherDrive = append(rep.OtherDrive, root)
		}
	}
	if !rep.OK {
		rep.Suggestion = suggestMapping(c, saveCheck, dl, env)
	}
	rep.Summary = summarise(c.Name, saveCheck, dl, rep)
	return rep
}

// suggestMapping works out a mapping that would make the unseen paths visible. From the
// newest download: the longest tail of its path that exists under Arrmada's downloads
// folder ("/downloads/complete/Movie" with /media/downloads/complete/Movie present gives
// /downloads → /media/downloads). Otherwise the save folder → Arrmada's downloads folder,
// the usual shape of a seedbox mount. Nil when nothing fits or the mapping would be refused.
func suggestMapping(c Client, save PathCheck, dl *PathCheck, env PathEnv) *PathMap {
	if env.Downloads == "" || !isDir(env.Downloads) {
		return nil
	}
	var sug *PathMap
	if dl != nil && !dl.Visible {
		if rp, ok := parseRemote(dl.Remote); ok {
			parts := strings.Split(strings.Trim(rp.p, "/"), "/")
			// The newest download may have saved into a library folder's own downloads
			// folder rather than the Downloads folder: look for it in each.
			locals := []string{env.Downloads}
			if c.Kind.Transport() == TransportTorrent {
				locals = append(locals, env.ownDownloads()...)
			}
			for _, local := range locals {
				if sug != nil || !isDir(local) {
					continue
				}
				for k := len(parts) - 1; k >= 1 && sug == nil; k-- {
					tail := path.Join(parts[len(parts)-k:]...)
					if !exists(filepath.Join(local, filepath.FromSlash(tail))) {
						continue
					}
					prefix := rp
					prefix.p = strings.TrimSuffix(rp.p, "/"+tail)
					if pr, ok := parseRemote(prefix.String()); ok && prefix.p != rp.p && !pr.isRoot() {
						sug = &PathMap{Remote: prefix.String(), Local: local}
					}
				}
			}
		}
	}
	if sug == nil && !save.Visible {
		sug = &PathMap{Remote: save.Remote, Local: env.Downloads}
	}
	if sug == nil {
		return nil
	}
	// The suggestion must pass the same rules a saved mapping does, alongside the client's
	// other mappings (one for the same remote folder is what it replaces).
	others := make([]PathMap, 0, len(c.PathMaps)+1)
	norm, _ := parseRemote(sug.Remote)
	for _, pm := range c.PathMaps {
		if pm.normRemote() != norm.p {
			others = append(others, pm)
		}
	}
	valid, err := ValidatePathMaps(append(others, *sug), env.DataDir)
	if err != nil {
		return nil
	}
	out := valid[len(valid)-1]
	return &out
}

func summarise(name string, save PathCheck, dl *PathCheck, rep PathReport) string {
	var b strings.Builder
	switch {
	case save.Visible && save.Local == save.Remote:
		fmt.Fprintf(&b, "%s saves to %s, which Arrmada can see ✓", name, save.Remote)
	case save.Visible:
		fmt.Fprintf(&b, "%s saves to %s; Arrmada sees it as %s ✓", name, save.Remote, save.Local)
	case save.Local != save.Remote:
		fmt.Fprintf(&b, "%s saves to %s (mapped to %s), which Arrmada can't see", name, save.Remote, save.Local)
	default:
		fmt.Fprintf(&b, "%s saves to %s, which Arrmada can't see", name, save.Remote)
	}
	if dl != nil && !dl.Visible && !dl.Gone {
		fmt.Fprintf(&b, ". Its newest finished download, %s, isn't visible either", dl.Local)
		if save.Visible {
			b.Reset()
			fmt.Fprintf(&b, "%s's newest finished download is at %s, which Arrmada can't see", name, dl.Local)
		}
	}
	if !rep.OK {
		if rep.Suggestion != nil {
			fmt.Fprintf(&b, ". Add the path mapping %s → %s", rep.Suggestion.Remote, rep.Suggestion.Local)
		} else {
			b.WriteString(". Mount that folder into the Arrmada container, or add a path mapping to where it's mounted")
		}
	}
	if len(rep.OtherDrive) > 0 {
		fmt.Fprintf(&b, ". %s is on a different drive, so imports there are copied rather than hardlinked", strings.Join(rep.OtherDrive, ", "))
	}
	if out := b.String(); !strings.HasSuffix(out, "✓") {
		return out + "."
	}
	return b.String()
}

func exists(p string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func isDir(p string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
