package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// RecycleMetaExt is the sidecar extension holding a recycled file's original path +
// deletion time, so the recycle bin can restore it and age it correctly.
const RecycleMetaExt = ".arrmeta"

// PartialSuffix ends the name of a copy still being written (copyFile's temp file). The
// bin manager never counts one as a recycled item, and clears old ones.
const PartialSuffix = ".arrmada-tmp"

// ErrRecycleDisabled is returned when the recycle bin is switched off. Callers decide
// whether that means "hard-delete" or "refuse" — it must never mean "move it somewhere
// arbitrary and report success".
var ErrRecycleDisabled = errors.New("recycle bin is disabled")

// RecycleMeta is what a recycled file's sidecar records.
type RecycleMeta struct {
	Orig    string `json:"orig"`
	Deleted int64  `json:"deleted"` // epoch seconds the file was recycled
}

// The file operations RecycleFile uses, swappable so tests can force the cross-device
// path (rename failing with EXDEV) or prove it's never taken.
var (
	renameFn = os.Rename
	copyFn   = copyVerified
	removeFn = os.Remove
)

// RecycleFile moves a file into the recycle bin at recycleDir, keeping its immediate
// parent folder name so recycled files stay identifiable, and de-duplicating on name
// collision. It returns the destination path. A missing source is treated as already
// gone (empty dst, nil error).
//
// It's a rename. Only when the OS says the bin is on another filesystem (EXDEV) does it
// copy: into a temp file the bin never counts, checked against the original, renamed
// into place, and only then is the original removed. If the original can't be removed
// the copy is taken back out, so a failure always leaves things as they were.
//
// It stamps the destination's mtime with the deletion time (so retention ages from when
// it was recycled, not when the content was last modified) and writes a sidecar with the
// original path so the bin can restore it.
func RecycleFile(recycleDir, path string) (string, error) {
	// An empty recycleDir means the bin is switched off. Without this guard filepath.Join
	// produced a RELATIVE destination, so the file was moved into the process working
	// directory and reported as successfully recycled — losing it on the next container
	// update. Callers that support "off" must hard-delete deliberately instead.
	if recycleDir == "" {
		return "", ErrRecycleDisabled
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", nil
	}
	dstDir := filepath.Join(recycleDir, filepath.Base(filepath.Dir(path)))
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dstDir, filepath.Base(path))
	for i := 1; ; i++ {
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		}
		ext := filepath.Ext(path)
		dst = filepath.Join(dstDir, strings.TrimSuffix(filepath.Base(path), ext)+fmt.Sprintf(".%d", i)+ext)
	}
	now := time.Now()
	stamp := func() {
		// mtime = deletion time, for accurate retention; the sidecar records the
		// origin (restore) and the authoritative deletion time. Failures are logged —
		// silently losing either quietly breaks retention/restore.
		if err := os.Chtimes(dst, now, now); err != nil {
			slog.Warn("recycle: stamping deletion time failed — retention will age from content mtime", "dst", dst, "err", err)
		}
		b, err := json.Marshal(RecycleMeta{Orig: path, Deleted: now.Unix()})
		if err == nil {
			err = os.WriteFile(dst+RecycleMetaExt, b, 0o644)
		}
		if err != nil {
			slog.Warn("recycle: writing sidecar failed — item won't be restorable", "dst", dst, "err", err)
		}
	}
	err := renameFn(path, dst)
	if err == nil {
		stamp()
		return dst, nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return "", err
	}
	// The bin is on another filesystem. Every library folder has its own bin now, so
	// this is a file outside them all, or an ARRMADA_RECYCLE_DIR on another drive.
	n, err := copyFn(path, dst)
	if err != nil {
		return "", fmt.Errorf("couldn't copy %s into the recycle bin on another drive: %w", filepath.Base(path), err)
	}
	stamp()
	if err := removeFn(path); err != nil && !os.IsNotExist(err) {
		// The original is still in place, so the copy would be a duplicate that a
		// restore can't put back. Take it out again: nothing was deleted.
		_ = os.Remove(dst)
		_ = os.Remove(dst + RecycleMetaExt)
		return "", fmt.Errorf("copied %s into the recycle bin but couldn't remove the original, so the copy was taken back out: %w", filepath.Base(path), err)
	}
	slog.Warn(fmt.Sprintf("cross-device recycle — copied %.2f GB because the bin is on another drive", float64(n)/(1<<30)),
		"from", path, "to", dst, "bytes", n)
	return dst, nil
}

// copyVerified copies src to dst through a temp file in dst's folder, which the bin
// manager never counts as recycled, and only renames it into place once its size and
// content match the original. On any error nothing is left at dst.
func copyVerified(src, dst string) (int64, error) {
	si, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".*"+PartialSuffix)
	if err != nil {
		return 0, err
	}
	tmp := out.Name()
	fail := func(e error) (int64, error) {
		_ = out.Close()
		_ = os.Remove(tmp)
		return 0, e
	}
	n, err := io.Copy(out, in)
	if err != nil {
		return fail(err)
	}
	if err := out.Sync(); err != nil { // on disk before anything trusts it
		return fail(err)
	}
	if err := out.Close(); err != nil {
		return fail(err)
	}
	ti, err := os.Stat(tmp)
	if err != nil {
		return fail(err)
	}
	if ti.Size() != si.Size() {
		return fail(fmt.Errorf("the copy is %d bytes, the original %d", ti.Size(), si.Size()))
	}
	if si.Size() > 0 {
		if same, err := sameContent(src, tmp, si, ti); err != nil || !same {
			if err == nil {
				err = errors.New("the copy doesn't match the original")
			}
			return fail(err)
		}
	}
	if err := os.Chmod(tmp, 0o644); err != nil { // CreateTemp makes 0600
		return fail(err)
	}
	if err := os.Rename(tmp, dst); err != nil { // same folder: never across drives
		return fail(err)
	}
	return n, nil
}

// Bin says which recycle bin a file being deleted goes to. ErrRecycleDisabled means the
// bin is deliberately switched off; any other error means it can't take the file.
type Bin interface {
	For(path string) (string, error)
}

// BinDir is one recycle bin as the bin manager (recyclebin) sees it.
type BinDir struct {
	Dir   string
	Label string // how the UI names it ("Movies", "Old shared bin")
	// Legacy marks the old shared bin: still listed, restorable, aged and capped, but
	// new deletes only land in it when no library folder holds the file.
	Legacy bool
	// Serves lists the library folders whose deletes can land here, for the "on a
	// different drive from your library" check.
	Serves []string
}

// SingleBin is one bin for every file — today's ARRMADA_RECYCLE_DIR. "" is the bin
// switched off.
func SingleBin(dir string) Bin { return singleBin(dir) }

type singleBin string

func (b singleBin) For(string) (string, error) {
	if b == "" {
		return "", ErrRecycleDisabled
	}
	return string(b), nil
}

// RemoveToBin is the one way a library file should be deleted: it moves path into the
// bin and returns where it went. Only a bin that is deliberately off hard-deletes. If the
// bin can't take the file, nothing is deleted and the error says so — it never falls back
// to os.Remove, which is how "deleted to the recycle bin" used to mean "gone for good".
// A file that's already missing is not an error.
func RemoveToBin(bin Bin, path string) (string, error) {
	if bin == nil {
		bin = SingleBin("")
	}
	// Already gone: nothing to move, and no reason to make a bin for it (a per-library
	// bin is created on first use).
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	dir, err := bin.For(path)
	if errors.Is(err, ErrRecycleDisabled) {
		if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
			return "", rerr
		}
		return "", nil
	}
	if err == nil {
		var dst string
		if dst, err = RecycleFile(dir, path); err == nil {
			return dst, nil
		}
	}
	return "", &binError{name: filepath.Base(path), err: err}
}

// CheckBin is the pre-flight for a delete that moves several files: it makes sure the bin
// path would take is usable before the first file moves, so a broken bin fails with
// nothing touched rather than half-way through. A bin that is deliberately off passes.
// The error matches ErrBinRefused, like RemoveToBin's.
func CheckBin(bin Bin, path string) error {
	if bin == nil {
		return nil
	}
	dir, err := bin.For(path)
	if errors.Is(err, ErrRecycleDisabled) {
		return nil
	}
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		return &binError{name: filepath.Base(path), err: err}
	}
	return nil
}

// ErrBinRefused matches (errors.Is) any RemoveToBin failure, so a handler can answer
// "the bin said no" (409) apart from other failures.
var ErrBinRefused = errors.New("the recycle bin couldn't take the file")

type binError struct {
	name string
	err  error
}

func (e *binError) Error() string {
	return fmt.Sprintf("couldn't move %s to the recycle bin (%v) — nothing was deleted", e.name, e.err)
}

func (e *binError) Unwrap() []error { return []error{ErrBinRefused, e.err} }

// ReadRecycleMeta reads a recycled file's sidecar (empty RecycleMeta when absent).
func ReadRecycleMeta(recycledPath string) RecycleMeta {
	var m RecycleMeta
	if b, err := os.ReadFile(recycledPath + RecycleMetaExt); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}
