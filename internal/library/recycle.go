package library

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RecycleMetaExt is the sidecar extension holding a recycled file's original path +
// deletion time, so the recycle bin can restore it and age it correctly.
const RecycleMetaExt = ".arrmeta"

// RecycleMeta is what a recycled file's sidecar records.
type RecycleMeta struct {
	Orig    string `json:"orig"`
	Deleted int64  `json:"deleted"` // epoch seconds the file was recycled
}

// RecycleFile moves a file into the recycle bin at recycleDir, keeping its immediate
// parent folder name so recycled files stay identifiable, and de-duplicating on name
// collision. It returns the destination path. A move across filesystems falls back to
// copy+remove; a missing source is treated as already gone (empty dst, nil error).
//
// It stamps the destination's mtime with the deletion time (so retention ages from when
// it was recycled, not when the content was last modified) and writes a sidecar with the
// original path so the bin can restore it.
func RecycleFile(recycleDir, path string) (string, error) {
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
	if err := os.Rename(path, dst); err != nil {
		// Cross-device rename fails with EXDEV — fall back to copy then remove.
		if cerr := copyFile(path, dst); cerr != nil {
			return "", cerr
		}
		if rerr := os.Remove(path); rerr != nil {
			return "", rerr
		}
	}
	now := time.Now()
	_ = os.Chtimes(dst, now, now) // mtime = deletion time, for accurate retention
	if b, err := json.Marshal(RecycleMeta{Orig: path, Deleted: now.Unix()}); err == nil {
		_ = os.WriteFile(dst+RecycleMetaExt, b, 0o644)
	}
	return dst, nil
}

// ReadRecycleMeta reads a recycled file's sidecar (empty RecycleMeta when absent).
func ReadRecycleMeta(recycledPath string) RecycleMeta {
	var m RecycleMeta
	if b, err := os.ReadFile(recycledPath + RecycleMetaExt); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}
