package main

import (
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/diskspace"
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

	for _, d := range []struct{ name, path string }{
		{"data", cfg.DataDir},
		{"library", cfg.LibraryDir},
		{"movies", cfg.MoviesDir},
		{"tv", cfg.TVDir},
		{"ebooks", cfg.EbooksDir},
		{"audiobooks", cfg.AudiobooksDir},
		{"music", cfg.MusicDir},
		{"downloads", cfg.DownloadsDir},
	} {
		if d.path == "" {
			continue
		}
		attrs := []any{"role", d.name, "path", d.path}
		st, err := os.Stat(d.path)
		switch {
		case err != nil:
			// Not fatal here — some roots are created on first import. But a missing
			// mount looks exactly like an empty library, and this is the difference.
			log.Warn("environment: folder is not present", append(attrs, "err", err)...)
			continue
		case !st.IsDir():
			log.Warn("environment: path is not a folder", attrs...)
			continue
		}
		if u, ok := diskspace.Of(d.path); ok {
			attrs = append(attrs, "free_gb", byteGB(u.FreeBytes), "used_pct", int(u.UsedPct))
		}
		if err := writable(d.path); err != nil {
			log.Warn("environment: folder is not writable", append(attrs, "err", err)...)
			continue
		}
		log.Info("environment: folder", attrs...)
	}

	// The download disk guard measures the downloads folder alone. If that folder is
	// on the same filesystem as the library, the guard is watching the whole array
	// rather than a torrent drive, and a threshold tuned for a cache pool means
	// something quite different. Two paths on one filesystem measure identically.
	if dl, ok := diskspace.Of(cfg.DownloadsDir); ok {
		if lib, ok := diskspace.Of(cfg.LibraryDir); ok && dl == lib {
			log.Warn("environment: downloads and library are on the same filesystem",
				"downloads", cfg.DownloadsDir, "library", cfg.LibraryDir,
				"impact", "the download disk guard will measure the whole volume, not a separate torrent drive")
		}
	}
}

// byteGB reports whole-GB-with-one-decimal. The raw division printed
// "free_gb=22374.6357421875", which is fifteen characters of noise in a line meant to
// be skimmed.
func byteGB(b uint64) float64 {
	mb := b / (1024 * 1024)
	return float64(mb*10/1024) / 10
}

// writable proves the folder can be written rather than inferring it from the mode
// bits, which say nothing useful under a read-only bind mount or a PUID mismatch.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".arrmada-write-check-")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}
