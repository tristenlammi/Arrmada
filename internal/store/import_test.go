package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// A backup from a newer Arrmada is refused on import and leaves nothing in the folder;
// the same file is taken in by a build that knows its schema, gzipped or not.
func TestImportBackupChecksSchema(t *testing.T) {
	src := t.TempDir()
	st, err := openWith(t, src, restoreV2, Options{})
	if err != nil {
		t.Fatal(err)
	}
	path, err := st.Snapshot(context.Background(), BackupManual)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if _, _, err := importBackup(dir, bytes.NewReader(raw), 1<<30, versionsIn(restoreV1)); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("err = %v, want ErrNewerSchema", err)
	}
	if files := backupFiles(t, dir); len(files) != 0 {
		t.Errorf("a refused import left %v", files)
	}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	name, info, err := importBackup(dir, &gz, 1<<30, versionsIn(restoreV2))
	if err != nil {
		t.Fatal(err)
	}
	if k, _, ok := ParseBackupName(name); !ok || k != BackupUploaded || info.SchemaVersion != "0002_extra" {
		t.Errorf("imported %q, %+v", name, info)
	}

	// Over the size cap: refused, nothing left behind.
	if _, _, err := importBackup(dir, bytes.NewReader(raw), int64(len(raw)/2), versionsIn(restoreV2)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over the cap: err = %v, want ErrTooLarge", err)
	}
	for _, f := range backupFiles(t, dir) {
		if strings.HasSuffix(f, ".part") {
			t.Errorf("left behind: %s", f)
		}
	}
}
