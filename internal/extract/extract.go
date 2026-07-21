// Package extract unpacks archives in a completed download (RAR, including
// multi-part sets, and ZIP) so the importer can find the media inside. This is
// the Unpackerr functionality, folded into Arrmada's pipeline.
package extract

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nwaples/rardecode/v2"
)

var reRarPart = regexp.MustCompile(`(?i)\.part(\d+)\.rar$`)

// maxUnpackedBytes caps the total bytes a single extraction call tree (ExtractAll /
// ExtractTree / ExtractArchive) may unpack — a zip-bomb guard. A var so tests can
// shrink it.
var maxUnpackedBytes int64 = 200 << 30 // 200 GiB

// ErrUnpackLimit is returned when an extraction would exceed the unpack cap.
var ErrUnpackLimit = errors.New("archive unpack limit exceeded")

// budget tracks how many bytes an extraction call tree may still write.
type budget struct{ remaining int64 }

// IsArchive reports whether path looks like an archive the extractor can open.
func IsArchive(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".rar")
}

// ExtractArchive extracts a single archive file into destDir (flattened, like the
// directory extractors). A non-first RAR volume is a no-op — rardecode follows the
// chain from the first volume.
func ExtractArchive(archivePath, destDir string) error {
	b := &budget{remaining: maxUnpackedBytes}
	lower := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(archivePath, destDir, b)
	case strings.HasSuffix(lower, ".rar"):
		if !isFirstRarVolume(filepath.Base(archivePath)) {
			return nil
		}
		return extractRar(archivePath, destDir, b)
	}
	return nil
}

// ExtractAll finds archives directly inside dir and extracts them into dir. For
// multi-part RAR sets only the first volume is opened (rardecode follows the
// chain). Returns how many archives were extracted.
func ExtractAll(dir string) (int, error) {
	return extractAll(dir, &budget{remaining: maxUnpackedBytes})
}

func extractAll(dir string, b *budget) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		full := filepath.Join(dir, name)

		switch {
		case strings.HasSuffix(lower, ".zip"):
			if err := extractZip(full, dir, b); err != nil {
				return count, fmt.Errorf("extract %s: %w", name, err)
			}
			count++
		case strings.HasSuffix(lower, ".rar"):
			if !isFirstRarVolume(name) {
				continue
			}
			if err := extractRar(full, dir, b); err != nil {
				return count, fmt.Errorf("extract %s: %w", name, err)
			}
			count++
		}
	}
	return count, nil
}

// ExtractTree extracts archives found anywhere under dir (recursively), so a season
// pack whose episodes each live in their own subfolder of RARs is fully unpacked.
// Best-effort: a failure in one folder doesn't stop the rest — except blowing the
// shared unpack cap, which aborts the walk. Returns the total count.
func ExtractTree(dir string) (int, error) {
	total := 0
	var firstErr error
	b := &budget{remaining: maxUnpackedBytes} // one cap for the whole tree
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		n, e := extractAll(p, b)
		total += n
		if e != nil && firstErr == nil {
			firstErr = e
		}
		if errors.Is(e, ErrUnpackLimit) {
			return filepath.SkipAll // the cap is shared — nothing further can succeed
		}
		return nil
	})
	return total, firstErr
}

// isFirstRarVolume reports whether name is the first volume of a RAR set. Plain
// ".rar" (old-style .rar/.r00/.r01…) is first; ".partNN.rar" only when NN == 1.
func isFirstRarVolume(name string) bool {
	if m := reRarPart.FindStringSubmatch(name); m != nil {
		return strings.TrimLeft(m[1], "0") == "1"
	}
	return true
}

// flatName returns the flattened output name for an archive entry, disambiguating
// deterministically when two entries collide on basename ("a/movie.mkv" and
// "b/movie.mkv" become "movie.mkv" and "movie.2.mkv") instead of silently dropping
// the second. Entry order in an archive is stable, so re-extraction is idempotent.
// filepath.Base keeps this zip-slip safe.
func flatName(used map[string]int, entryName string) string {
	base := filepath.Base(entryName)
	n := used[base]
	used[base] = n + 1
	if n == 0 {
		return base
	}
	ext := filepath.Ext(base)
	return fmt.Sprintf("%s.%d%s", strings.TrimSuffix(base, ext), n+1, ext)
}

func extractZip(archivePath, destDir string, b *budget) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()

	used := map[string]int{}
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		in, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFile(in, filepath.Join(destDir, flatName(used, f.Name)), b)
		in.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractRar(archivePath, destDir string, b *budget) error {
	rc, err := rardecode.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer rc.Close()

	used := map[string]int{}
	for {
		hdr, err := rc.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if hdr.IsDir {
			continue
		}
		if err := writeFile(rc, filepath.Join(destDir, flatName(used, hdr.Name)), b); err != nil {
			return err
		}
	}
	return nil
}

// writeFile extracts one entry to dst atomically: it writes a temp file in the
// destination directory and renames it into place on success, so a completed dst is
// always a fully-extracted file and a crash never leaves a plausible-looking partial.
func writeFile(in io.Reader, dst string, b *budget) error {
	// Idempotent: if we've already extracted this file, don't rewrite it. A season pack
	// gets re-scanned while it still has missing episodes, and re-copying multi-GB videos
	// every pass would be pointless churn. Safe: completed files only exist via the
	// rename below, so an existing dst is never a partial.
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		return nil
	}
	out, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".*.arrmada-tmp")
	if err != nil {
		return err
	}
	tmp := out.Name()
	fail := func(e error) error {
		out.Close()
		_ = os.Remove(tmp)
		return e
	}
	// Copy at most the remaining budget (+1 so exceeding is detectable) — the
	// zip-bomb guard.
	n, err := io.Copy(out, io.LimitReader(in, b.remaining+1))
	b.remaining -= n
	if b.remaining < 0 {
		return fail(fmt.Errorf("%w extracting %s", ErrUnpackLimit, filepath.Base(dst)))
	}
	if err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil { // CreateTemp makes 0600
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
