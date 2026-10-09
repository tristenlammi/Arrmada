package store

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ImportBackup streams a backup from r — a .db, or a .db.gz, told apart by its first
// bytes — into the backups folder as an uploaded backup, validates it, and returns its
// name. The file only gets a backup name once it has passed validation, so a failed or
// half-finished import is never listed. maxBytes caps the database's size after
// decompressing; the import also stops short of filling the disk the live database is on.
func ImportBackup(dataDir string, r io.Reader, maxBytes int64) (string, BackupInfo, error) {
	return importBackup(dataDir, r, maxBytes, EmbeddedVersions())
}

func importBackup(dataDir string, r io.Reader, maxBytes int64, embedded []string) (string, BackupInfo, error) {
	br := bufio.NewReader(r)
	var src io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return "", BackupInfo{}, fmt.Errorf("%w: unreadable gzip (%v)", ErrNotSQLite, err)
		}
		defer gz.Close()
		src = gz
	}
	sr := bufio.NewReader(src)
	head, err := sr.Peek(len(sqliteMagic))
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", BackupInfo{}, err // a read error (or the body's size cap), not bad content
	}
	if !bytes.Equal(head, sqliteMagic) {
		return "", BackupInfo{}, ErrNotSQLite
	}

	dir := BackupsDir(dataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", BackupInfo{}, err
	}
	final, part, f, err := claimImportName(dir)
	if err != nil {
		return "", BackupInfo{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(part)
		}
	}()

	// Never let an import fill the disk the live database is on: leave room for the
	// database to grow and for its next snapshot.
	limit, diskBound := maxBytes, false
	if u, ok := freeSpace(dir); ok {
		var live int64
		for _, p := range []string{filepath.Join(dataDir, "arrmada.db"), filepath.Join(dataDir, "arrmada.db-wal")} {
			if fi, err := os.Stat(p); err == nil {
				live += fi.Size()
			}
		}
		reserve := uint64(float64(live)*spaceHeadroom) + 256<<20
		room := int64(0)
		if u.FreeBytes > reserve {
			room = int64(u.FreeBytes - reserve)
		}
		if room < limit {
			limit, diskBound = room, true
		}
	}
	n, err := io.Copy(f, io.LimitReader(sr, limit+1))
	if err == nil && n > limit {
		if diskBound {
			err = fmt.Errorf("%w: only %s free in %s", ErrNoSpace, formatBytes(uint64(limit)), dir)
		} else {
			err = fmt.Errorf("%w (over %s)", ErrTooLarge, formatBytes(uint64(maxBytes)))
		}
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", BackupInfo{}, err
	}

	info, err := ValidateBackup(part, embedded)
	if err != nil {
		return "", info, err
	}
	if err := os.Rename(part, filepath.Join(dir, final)); err != nil {
		return "", info, err
	}
	keep = true
	syncDir(dir)
	return final, info, nil
}

// claimImportName picks a free uploaded-backup name and creates its .part file
// exclusively, so two imports in the same second can't share one.
func claimImportName(dir string) (name, part string, f *os.File, err error) {
	at := time.Now()
	for i := 0; i < 60; i++ {
		name = BackupName(BackupUploaded, at.Add(time.Duration(i)*time.Second))
		if _, serr := os.Stat(filepath.Join(dir, name)); serr == nil {
			continue
		}
		part = filepath.Join(dir, name+".part")
		f, err = os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return name, part, f, err
	}
	return "", "", nil, errors.New("no free name for the uploaded backup")
}
