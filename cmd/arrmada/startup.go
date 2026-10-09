package main

import (
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/health"
	"github.com/tristenlammi/arrmada/internal/libroots"
)

// logEnvironment records the facts that every "why is it doing that?" turns out to
// depend on, at a point in startup where they're all known.
//
// This exists because they kept being invisible. The encode window silently ran on
// UTC for weeks because the container's clock was never printed anywhere; a merge
// failed because ffmpeg wasn't on PATH and the only symptom was the failure itself.
// One block at boot, in the log the user already exports, answers all of it.
func logEnvironment(log *slog.Logger, cfg config.Config) {
	now := time.Now()
	zone, offset := now.Zone()
	log.Info("environment: clock",
		"local_time", now.Format(time.RFC3339),
		"zone", zone,
		"utc_offset_minutes", offset/60,
		"tz_env", os.Getenv("TZ"),
	)
	log.Info("environment: runtime",
		"go", runtime.Version(),
		"os", runtime.GOOS,
		"arch", runtime.GOARCH,
		"cpus", runtime.NumCPU(),
		"uid", os.Getuid(),
		"gid", os.Getgid(),
	)

	// External binaries. Absent ones are the interesting case, so log both ways
	// rather than only complaining.
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if path, err := exec.LookPath(bin); err != nil {
			log.Warn("environment: helper binary missing", "binary", bin,
				"impact", "conversion, subtitle extraction and audiobook merging are unavailable")
		} else {
			log.Info("environment: helper binary", "binary", bin, "path", path)
		}
	}

	// The library folders are logged later (logFolders), once the folders picked in the
	// app are known; here, before the database opens, only the data folder is.
	logFolder(log, "data", cfg.DataDir)
}

// logFolders records each folder the app will work in — the ones picked in the app,
// with the environment's as the fallback — so a missing mount or a read-only share is
// in the log the user already exports. Call after ApplySavedLibraryDirs.
func logFolders(log *slog.Logger, cfg config.Config, booksOn, musicOn bool) {
	folders := health.LibraryFolders(cfg, booksOn, musicOn)
	for _, f := range folders {
		logFolder(log, f.Role, f.Path)
	}

	// The download disk guard measures the downloads folder alone. If that folder is
	// on the same filesystem as a library, the guard is watching the whole array rather
	// than a torrent drive, and a threshold tuned for a cache pool means something quite
	// different.
	var shared []string
	for _, f := range folders {
		if f.Role == "downloads" {
			continue
		}
		if same, ok := health.SameFilesystem(cfg.DownloadsDir, f.Path); ok && same {
			shared = append(shared, f.Role)
		}
	}
	if len(shared) > 0 {
		log.Warn("environment: downloads share a filesystem with library folders",
			"downloads", cfg.DownloadsDir, "libraries", strings.Join(shared, ","),
			"impact", "the download disk guard will measure the whole volume, not a separate torrent drive")
	}
}

// logFolder logs one folder: present, a folder, writable, and how full its disk is.
func logFolder(log *slog.Logger, role, path string) {
	if path == "" {
		return
	}
	attrs := []any{"role", role, "path", path}
	st, err := os.Stat(path)
	switch {
	case err != nil:
		// Not fatal here — some roots are created on first import. But a missing
		// mount looks exactly like an empty library, and this is the difference.
		log.Warn("environment: folder is not present", append(attrs, "err", err)...)
		return
	case !st.IsDir():
		log.Warn("environment: path is not a folder", attrs...)
		return
	}
	if u, ok := diskspace.Of(path); ok {
		attrs = append(attrs, "free_gb", byteGB(u.FreeBytes), "used_pct", int(u.UsedPct))
	}
	// Proven by writing, not inferred from mode bits, which say nothing useful under a
	// read-only bind mount or a PUID mismatch.
	if err := libroots.ProbeWritable(path); err != nil {
		log.Warn("environment: folder is not writable", append(attrs, "err", err)...)
		return
	}
	log.Info("environment: folder", attrs...)
}

// byteGB reports whole-GB-with-one-decimal. The raw division printed
// "free_gb=22374.6357421875", which is fifteen characters of noise in a line meant to
// be skimmed.
func byteGB(b uint64) float64 {
	mb := b / (1024 * 1024)
	return float64(mb*10/1024) / 10
}
