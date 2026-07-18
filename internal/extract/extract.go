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

// ExtractAll finds archives directly inside dir and extracts them into dir. For
// multi-part RAR sets only the first volume is opened (rardecode follows the
// chain). Returns how many archives were extracted.
func ExtractAll(dir string) (int, error) {
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
			if err := extractZip(full, dir); err != nil {
				return count, fmt.Errorf("extract %s: %w", name, err)
			}
			count++
		case strings.HasSuffix(lower, ".rar"):
			if !isFirstRarVolume(name) {
				continue
			}
			if err := extractRar(full, dir); err != nil {
				return count, fmt.Errorf("extract %s: %w", name, err)
			}
			count++
		}
	}
	return count, nil
}

// ExtractTree extracts archives found anywhere under dir (recursively), so a season
// pack whose episodes each live in their own subfolder of RARs is fully unpacked.
// Best-effort: a failure in one folder doesn't stop the rest. Returns the total count.
func ExtractTree(dir string) (int, error) {
	total := 0
	var firstErr error
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		n, e := ExtractAll(p)
		total += n
		if e != nil && firstErr == nil {
			firstErr = e
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

func extractZip(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		in, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFile(in, filepath.Join(destDir, filepath.Base(f.Name)))
		in.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractRar(archivePath, destDir string) error {
	rc, err := rardecode.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer rc.Close()

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
		if err := writeFile(rc, filepath.Join(destDir, filepath.Base(hdr.Name))); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(in io.Reader, dst string) error {
	// Idempotent: if we've already extracted this file, don't rewrite it. A season pack
	// gets re-scanned while it still has missing episodes, and re-copying multi-GB videos
	// every pass would be pointless churn.
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		return nil
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
