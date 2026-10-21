package store

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

var backupVersionText = regexp.MustCompile(`^[0-9]{4}_[a-z0-9_]+$`)

// InspectBackupVersion reads only migration metadata, never executes archive
// SQL, and does not connect to a database for PostgreSQL archives.
func InspectBackupVersion(ctx context.Context, path, binDir string) (string, error) {
	if BackupBackend(filepath.Base(path)) != Postgres {
		db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
		if err != nil {
			return "", err
		}
		defer db.Close()
		db.SetMaxOpenConns(1)
		var ver sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&ver); err != nil {
			return "", err
		}
		return ver.String, nil
	}
	// Only a small table is decoded. Keep output bounded even if an archive
	// lies about that table's contents; a list request must not fill memory.
	var metadata bytes.Buffer
	if err := nativePostgresTool(ctx, postgresTools(PostgresOptions{BinDir: binDir}).restore, []string{}, &limitedBackupMetadata{w: &metadata, remaining: 1024 * 1024},
		"--data-only", "--no-owner", "--no-privileges", "--table=schema_migrations", "--file=-", path); err != nil {
		return "", err
	}
	r := newArchiveDataReader(&metadata)
	name, columns, data, err := r.next()
	if err != nil || name != "schema_migrations" || strings.Join(columns, ",") != "version,checksum,applied_at" {
		return "", errors.New("archive has no recognizable migration metadata")
	}
	scanner := bufio.NewScanner(data)
	latest := ""
	for scanner.Scan() {
		cells := strings.Split(scanner.Text(), "\t")
		if len(cells) != 3 || !backupVersionText.MatchString(cells[0]) {
			return "", errors.New("archive has invalid migration metadata")
		}
		if cells[0] > latest {
			latest = cells[0]
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if !data.terminated {
		return "", errors.New("archive migration metadata is truncated")
	}
	if _, _, _, err := r.next(); err != io.EOF {
		return "", errors.New("archive has unexpected migration metadata")
	}
	return latest, nil
}

type limitedBackupMetadata struct {
	w         io.Writer
	remaining int
}

func (w *limitedBackupMetadata) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, errors.New("archive migration metadata is too large")
	}
	w.remaining -= len(p)
	return w.w.Write(p)
}
