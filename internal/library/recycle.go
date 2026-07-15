package library

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RecycleFile moves a file into the recycle bin at recycleDir, keeping its immediate
// parent folder name so recycled files stay identifiable, and de-duplicating on name
// collision. It returns the destination path. A move across filesystems falls back to
// copy+remove; a missing source is treated as already gone (empty dst, nil error).
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
	return dst, nil
}
