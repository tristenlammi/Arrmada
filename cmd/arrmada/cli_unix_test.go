//go:build linux

package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// TestCLIHelperProcess runs the CLI in a child process for the privilege test below:
// dropping root can't be undone, so it mustn't happen in the test binary itself.
func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv("ARRMADA_CLI_HELPER") != "1" {
		t.Skip("helper process for TestCLIBackupAsRootWritesAsOwner")
	}
	os.Exit(runCLI(strings.Fields(os.Getenv("ARRMADA_CLI_ARGS"))))
}

// Run as root (a plain `docker exec`), the CLI must become the database's owner before
// it opens anything, so the backup and SQLite's -wal/-shm belong to PUID:PGID.
func TestCLIBackupAsRootWritesAsOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root (the race run in Docker)")
	}
	const uid, gid = 99, 100
	dir := testDataDir(t)
	// The test's temp folders are root-only; the child has to reach the data dir after
	// switching user.
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	}); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIHelperProcess$")
	cmd.Env = append(os.Environ(), "ARRMADA_CLI_HELPER=1", "ARRMADA_DATA_DIR="+dir, "ARRMADA_CLI_ARGS=backup --kind manual")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("backup as root: %v\n%s", err, out)
	}

	err = filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		var st syscall.Stat_t
		if err := syscall.Stat(p, &st); err != nil {
			return err
		}
		if st.Uid != uid || st.Gid != gid {
			t.Errorf("%s is owned by %d:%d, want %d:%d", p, st.Uid, st.Gid, uid, gid)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(store.BackupsDir(dir)); len(entries) != 1 {
		t.Errorf("backups folder holds %d files, want the one backup", len(entries))
	}
}
