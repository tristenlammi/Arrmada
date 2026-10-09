package library

import "testing"

// SwapRecycleOps replaces RecycleFile's rename, copy and remove for one test (nil keeps
// the real one), so tests in package library_test can force the cross-device path.
func SwapRecycleOps(t testing.TB, rename func(oldpath, newpath string) error, cp func(src, dst string) (int64, error), remove func(string) error) {
	t.Helper()
	r, c, rm := renameFn, copyFn, removeFn
	t.Cleanup(func() { renameFn, copyFn, removeFn = r, c, rm })
	if rename != nil {
		renameFn = rename
	}
	if cp != nil {
		copyFn = cp
	}
	if remove != nil {
		removeFn = remove
	}
}

// CopyVerified is copyVerified, for tests that wrap it.
var CopyVerified = copyVerified
