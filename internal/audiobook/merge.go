// Package audiobook merges a folder of chapter files (e.g. 100+ MP3s) into a single
// chapterized .m4b using ffmpeg. Each source file becomes one chapter, titled from its
// filename. Requires ffmpeg + ffprobe on PATH (bundled in the Docker image).
package audiobook

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Available reports whether ffmpeg and ffprobe are on PATH.
func Available() bool {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return false
	}
	_, err := exec.LookPath("ffprobe")
	return err == nil
}

// Merge concatenates the given audio files (in order) into a single AAC .m4b at outPath,
// with one chapter per input file. It does not delete the sources — the caller decides.
func Merge(ctx context.Context, files []string, outPath string) error {
	if !Available() {
		return fmt.Errorf("audiobook merge needs ffmpeg — not installed")
	}
	if len(files) < 2 {
		return fmt.Errorf("need at least two files to merge")
	}

	tmp, err := os.MkdirTemp("", "arrmada-merge-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	// 1) concat list for ffmpeg's concat demuxer.
	var list strings.Builder
	for _, f := range files {
		// The concat demuxer needs single-quotes escaped as '\''.
		list.WriteString("file '" + strings.ReplaceAll(f, "'", `'\''`) + "'\n")
	}
	listPath := filepath.Join(tmp, "list.txt")
	if err := os.WriteFile(listPath, []byte(list.String()), 0o644); err != nil {
		return err
	}

	// 2) chapter metadata (ffmetadata) — cumulative durations, title = filename.
	var meta strings.Builder
	meta.WriteString(";FFMETADATA1\n")
	var startMs int64
	for _, f := range files {
		durMs := probeDurationMs(ctx, f)
		endMs := startMs + durMs
		title := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		meta.WriteString("[CHAPTER]\nTIMEBASE=1/1000\n")
		meta.WriteString("START=" + strconv.FormatInt(startMs, 10) + "\n")
		meta.WriteString("END=" + strconv.FormatInt(endMs, 10) + "\n")
		meta.WriteString("title=" + sanitizeMeta(title) + "\n")
		startMs = endMs
	}
	metaPath := filepath.Join(tmp, "chapters.ffmeta")
	if err := os.WriteFile(metaPath, []byte(meta.String()), 0o644); err != nil {
		return err
	}

	// 3) transcode to a single AAC .m4b with the chapter metadata.
	args := []string{
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "concat", "-safe", "0", "-i", listPath,
		"-i", metaPath, "-map_metadata", "1",
		"-map", "0:a", "-c:a", "aac", "-b:a", "64k",
		"-movflags", "+faststart", outPath,
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg merge failed: %v — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// probeDurationMs returns a file's duration in milliseconds (0 if it can't be read).
func probeDurationMs(ctx context.Context, path string) int64 {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "quiet",
		"-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return int64(secs * 1000)
}

func sanitizeMeta(s string) string {
	// ffmetadata treats =, ;, #, \ and newlines specially — escape with a backslash.
	repl := strings.NewReplacer("\\", "\\\\", "=", "\\=", ";", "\\;", "#", "\\#", "\n", " ")
	return repl.Replace(s)
}
