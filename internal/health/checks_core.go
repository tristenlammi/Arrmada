package health

import (
	"context"
	"fmt"
	"time"

	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/libroots"
)

// The core checks: what has to be in place for anything to download and import at all.
// Each is built over a narrow function rather than a service, so tests can feed it fakes
// and the HTTP layer decides where the values come from.

// IndexersCheck reports when no indexer is enabled. enabled returns how many are; an
// error reading them says nothing (a database hiccup isn't "no indexers").
func IndexersCheck(enabled func(ctx context.Context) (int, error)) Check {
	return Check{
		Key: "indexers", Name: "Indexers", Category: CategoryIndexers,
		Run: func(ctx context.Context) []Finding {
			n, err := enabled(ctx)
			if err != nil || n > 0 {
				return nil
			}
			return []Finding{{
				Key: "indexers.none", Level: LevelError, Fix: FixIndexers,
				Message: "No indexers are enabled — Arrmada can't search for releases.",
			}}
		},
	}
}

// PausedIndexer is an enabled indexer that background searches are leaving alone after
// repeated failures (see internal/connstatus).
type PausedIndexer struct {
	ID        int64
	Name      string
	Failures  int
	Until     time.Time
	LastError string // already redacted
}

// IndexerStatusCheck names each enabled indexer that is backing off after repeated
// failures, with its error and when it is next tried. One paused among working ones is a
// warning; every enabled one paused is an error, because then nothing is searched
// automatically at all. A single failure isn't reported: a public tracker that times out
// now and then is still working. states returns how many indexers are enabled and which
// of them are paused; an error says nothing.
func IndexerStatusCheck(states func(ctx context.Context) (enabled int, paused []PausedIndexer, err error)) Check {
	return indexerStatusCheck(states, time.Now)
}

func indexerStatusCheck(states func(ctx context.Context) (int, []PausedIndexer, error), now func() time.Time) Check {
	return Check{
		Key: "indexers.status", Name: "Indexer status", Category: CategoryIndexers,
		Run: func(ctx context.Context) []Finding {
			enabled, paused, err := states(ctx)
			if err != nil || len(paused) == 0 {
				return nil
			}
			level, tail := LevelWarning, "automatic searches skip it until then"
			if len(paused) >= enabled {
				level, tail = LevelError, "every indexer is paused, so nothing is being searched automatically"
			}
			out := make([]Finding, 0, len(paused))
			for _, p := range paused {
				msg := fmt.Sprintf("%s has failed %d times in a row and is paused until %s — %s", p.Name, p.Failures, clock(p.Until, now()), tail)
				if p.LastError != "" {
					msg += ". Last error: " + p.LastError
				}
				out = append(out, Finding{Key: fmt.Sprintf("indexers.paused.%d", p.ID), Level: level, Fix: FixIndexerStatus, Message: msg})
			}
			return out
		},
	}
}

// LibraryState is what the library-folders check judges: the folders in use (only the
// modules that are on), every folder setting (for the data-folder test, which applies
// whether or not the module is on), and Arrmada's data folder.
type LibraryState struct {
	Folders []Folder
	All     []Folder
	DataDir string
}

// folderCheckEvery paces the checks that touch the library shares. They run whether or
// not anyone is looking, and on Unraid a look at a share can spin up a sleeping array
// disk; a share going missing is still noticed within minutes.
const folderCheckEvery = 5 * time.Minute

// LibraryFoldersCheck checks each folder the user picked is there and writable, and that
// none sits inside the data folder. Write probes go through probes, which trusts a pass
// for a while, for the same sleeping-disk reason. The timeout is generous because a disk
// spinning up takes several seconds to answer.
func LibraryFoldersCheck(src func(ctx context.Context) LibraryState, probes *ProbeCache) Check {
	return Check{
		Key: "library", Name: "Library folders", Category: CategoryStorage, Interval: folderCheckEvery, Timeout: 20 * time.Second,
		Run: func(ctx context.Context) []Finding {
			st := src(ctx)
			var out []Finding
			// Media in or above the data folder mixes it with the database, backups and
			// logs. Saving such a folder is refused now, but an older save or the
			// environment can still carry one and the app keeps running on it — so it's red.
			underData := map[string]bool{}
			for _, f := range st.All {
				if f.Path != "" && libroots.UnderDataDir(f.Path, st.DataDir) && !underData[f.Path] {
					underData[f.Path] = true
					out = append(out, Finding{
						Key: "library." + f.Role, Level: LevelError, Fix: FixLibraryFolders,
						Message: fmt.Sprintf("The %s folder (%s) is inside (or contains) Arrmada's data folder — move it to its own mount.", f.Label, f.Path),
					})
				}
			}
			for _, f := range st.Folders {
				if ctx.Err() != nil {
					break
				}
				if underData[f.Path] {
					continue // already reported, and nothing is probed inside the data folder
				}
				if level, msg := FolderProblem(f, probes.ProbeFolder(f.Path)); msg != "" {
					out = append(out, Finding{Key: "library." + f.Role, Level: level, Message: msg, Fix: FixLibraryFolders})
				}
			}
			return out
		},
	}
}

// FolderProblem turns a folder probe into a health line, or "" when the folder is fine.
// A missing folder whose parent can be written is only a warning — imports create it —
// but a missing parent means the share isn't mounted at all.
func FolderProblem(f Folder, st FolderState) (level, msg string) {
	switch {
	case !st.Exists && st.Err != nil:
		return LevelError, fmt.Sprintf("Arrmada can't look at the %s folder %s: %v.", f.Label, f.Path, st.Err)
	case !st.Exists && st.ParentExists && st.ParentWritable:
		return LevelWarning, fmt.Sprintf("The %s folder %s doesn't exist yet. Arrmada will create it when it's first needed; if it should be an existing share, check the container's volume mapping.", f.Label, f.Path)
	case !st.Exists && st.ParentExists:
		return LevelError, fmt.Sprintf("The %s folder %s doesn't exist, and Arrmada can't create it (check the container's volume mapping and the share's permissions).", f.Label, f.Path)
	case !st.Exists:
		return LevelError, fmt.Sprintf("The %s folder %s isn't there — the share isn't mounted.", f.Label, f.Path)
	case !st.IsDir:
		return LevelError, fmt.Sprintf("The %s folder %s is a file, not a folder.", f.Label, f.Path)
	case !st.Writable:
		return LevelError, fmt.Sprintf("Arrmada can't write to the %s folder %s (check PUID/PGID and the share's permissions).", f.Label, f.Path)
	}
	return "", ""
}

// RecycleBinProblem is one recycle bin the health panel should mention (the recycle bin
// service's Problems, without importing it).
type RecycleBinProblem struct {
	Dir        string
	Legacy     bool // the old shared bin from before bins lived on each library folder
	OtherDrive bool // on a different drive from the library it serves
	LegacyFull bool // the old shared bin, still holding deleted files
}

// RecycleDriveCheck reports the bins worth a line: a per-library bin on another drive from
// the library it serves turns every delete into a full copy, and the old shared bin still
// holding files only drains when someone empties it or it ages out. The legacy bin never
// gets the "every delete" line — only stray files go there now, so it would overstate it.
func RecycleDriveCheck(problems func() []RecycleBinProblem) Check {
	return Check{
		Key: "recycle.drive", Name: "Recycle bins", Category: CategoryStorage, Interval: folderCheckEvery, Timeout: 10 * time.Second,
		Run: func(ctx context.Context) []Finding {
			var out []Finding
			for _, p := range problems() {
				if p.OtherDrive && !p.Legacy {
					out = append(out, Finding{
						Key: "recycle.drive." + p.Dir, Level: LevelWarning, Fix: FixRecycleBin,
						Message: fmt.Sprintf("The recycle bin %s is on a different drive from your library, so every delete is a full copy, which is slow and fills that drive.", p.Dir),
					})
				}
				if p.LegacyFull {
					out = append(out, Finding{
						Key: "recycle.legacy", Level: LevelWarning, Fix: FixRecycleBin,
						Message: fmt.Sprintf("The old shared recycle bin (%s) still holds deleted files; new deletes no longer go there. Empty it under Recycle bin once you've checked what's in it.", p.Dir),
					})
				}
			}
			return out
		},
	}
}

// DiskGuardState is the disk guard as the health check sees it. Holding counts only held
// torrents the client still has (a torrent deleted while held would otherwise keep
// promising a resume that can never happen).
type DiskGuardState struct {
	Enabled   bool
	UsedPct   float64
	PausePct  int
	ResumePct int
	Holding   int
}

// DiskGuardCheck says plainly when the disk guard is holding downloads: it's the single
// most confusing reason for "nothing is downloading", because everything else looks fine.
func DiskGuardCheck(state func(ctx context.Context) (DiskGuardState, bool)) Check {
	return Check{
		Key: "disk.guard", Name: "Disk guard", Category: CategoryDownloads, Timeout: 10 * time.Second,
		Run: func(ctx context.Context) []Finding {
			g, ok := state(ctx)
			if !ok || !g.Enabled || g.Holding <= 0 {
				return nil
			}
			return []Finding{{
				Key: "disk.guard", Level: LevelWarning, Fix: FixDiskGuard,
				Message: fmt.Sprintf(
					"Downloads are paused: the downloads volume is %.1f%% full (pause at %d%%). "+
						"%d torrent%s will resume automatically once it drops below %d%%.",
					g.UsedPct, g.PausePct, g.Holding, plural(g.Holding), g.ResumePct),
			}}
		},
	}
}

// DiskFreeCheck warns when the downloads volume is nearly full.
func DiskFreeCheck(dir func() string) Check {
	return diskFreeCheck(dir, diskspace.FreeGB)
}

func diskFreeCheck(dir func() string, free func(string) (float64, bool)) Check {
	return Check{
		Key: "disk.free", Name: "Free space", Category: CategoryStorage,
		Run: func(ctx context.Context) []Finding {
			gb, ok := free(dir())
			switch {
			case !ok:
				return nil
			case gb < 2:
				return []Finding{{Key: "disk.free", Level: LevelError, Message: fmt.Sprintf("Very low free disk space (%.1f GB) on the downloads volume.", gb)}}
			case gb < 10:
				return []Finding{{Key: "disk.free", Level: LevelWarning, Message: fmt.Sprintf("Low free disk space (%.1f GB) on the downloads volume.", gb)}}
			}
			return nil
		},
	}
}

// AudiobookServerCheck reports an audiobook server that's switched on but couldn't start
// (usually its port is taken). state returns whether it's on, whether it's running and
// the last start error.
func AudiobookServerCheck(state func(ctx context.Context) (on, running bool, lastErr string)) Check {
	return Check{
		Key: "audiobooks.server", Name: "Audiobook server", Category: CategoryIntegrations,
		Run: func(ctx context.Context) []Finding {
			on, running, lastErr := state(ctx)
			if !on || running {
				return nil
			}
			msg := "The audiobook server is switched on but isn't running, so listening apps can't connect."
			if lastErr != "" {
				msg += " " + lastErr
			}
			return []Finding{{Key: "audiobooks.server", Level: LevelError, Message: msg, Fix: FixAudiobookServer}}
		},
	}
}

// BackupsCheck passes on the backup service's own warning when nightly backups have
// stopped (usually a full disk on the data volume).
func BackupsCheck(warning func(ctx context.Context) string) Check {
	return Check{
		Key: "backups", Name: "Database backups", Category: CategoryStorage,
		Run: func(ctx context.Context) []Finding {
			if msg := warning(ctx); msg != "" {
				return []Finding{{Key: "backups.age", Level: LevelWarning, Message: msg, Fix: FixBackups}}
			}
			return nil
		},
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
