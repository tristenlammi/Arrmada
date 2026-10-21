package store

import (
	"bytes"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The URL points only at an explicitly provisioned disposable TLS cluster.
// Its trusted certificate must name localhost, not the 127.0.0.1 negative case.
func TestPostgresVerifiedTLSAndCompressedRecovery(t *testing.T) {
	address := os.Getenv("ARRMADA_TEST_POSTGRES_TLS_URL")
	if address == "" {
		t.Skip("set ARRMADA_TEST_POSTGRES_TLS_URL to a disposable verify-full localhost cluster")
	}
	u, err := url.Parse(address)
	if err != nil || u.Hostname() != "localhost" || u.Query().Get("sslmode") != "verify-full" || u.Query().Get("sslrootcert") == "" {
		t.Fatal("TLS test requires localhost, verify-full and an explicit CA")
	}
	t.Setenv("ARRMADA_TEST_POSTGRES_ADMIN_URL", address)
	tools := archiveTestTools(t)
	ctx, _, source, _ := postgresFixture(t)
	db, err := openPostgresPool(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var encrypted bool
	if err := db.QueryRowContext(ctx, `SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()`).Scan(&encrypted); err != nil || !encrypted {
		t.Fatalf("Go connection did not negotiate TLS: %v", err)
	}
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	value := strings.Repeat("compressed native recovery / é / ", 20000)
	if _, err := db.ExecContext(ctx, `INSERT INTO settings (key,value) VALUES ('compressed-tls',?)`, value); err != nil {
		t.Fatal(err)
	}
	env, err := postgresClientEnv(source)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := nativePostgresTool(ctx, tools.dump, env, &archive, "--no-password", "--format=custom", "--compress=gzip:6", "--schema="+`"`+source.Schema+`"`, "--no-owner", "--no-privileges"); err != nil {
		t.Fatal(err)
	}
	if archive.Len() >= len(value) {
		t.Fatal("gzip archive did not compress repetitive data")
	}
	path := filepath.Join(t.TempDir(), "compressed.pgdump")
	if err := os.WriteFile(path, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, target, _ := postgresFixture(t)
	if err := restorePostgres(ctx, target, path, t.TempDir(), tools); err != nil {
		t.Fatal(err)
	}
	recovered, err := openPostgresPool(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	var got string
	if err := recovered.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='compressed-tls'`).Scan(&got); err != nil || got != value {
		t.Fatalf("compressed TLS recovery lost data: %v", err)
	}
	badURL, err := url.Parse(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	badURL.Host = net.JoinHostPort("127.0.0.1", badURL.Port())
	bad := source
	bad.URL = badURL.String()
	if conn, err := openPostgresPool(ctx, bad); err == nil {
		conn.Close()
		t.Fatal("Go accepted mismatched TLS hostname")
	}
	badEnv, err := postgresClientEnv(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := nativePostgresTool(ctx, tools.dump, badEnv, &bytes.Buffer{}, "--no-password", "--format=custom", "--schema="+`"`+source.Schema+`"`); err == nil {
		t.Fatal("native backup accepted mismatched TLS hostname")
	}
	badURL, err = url.Parse(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := badURL.Query()
	query.Set("sslrootcert", filepath.Join(t.TempDir(), "missing-ca.pem"))
	badURL.RawQuery = query.Encode()
	bad.URL = badURL.String()
	if conn, err := openPostgresPool(ctx, bad); err == nil {
		conn.Close()
		t.Fatal("Go accepted missing TLS trust root")
	}
	badEnv, err = postgresClientEnv(bad)
	// Configuration parsing may already reject an unreadable root; otherwise
	// the native client must refuse it, never fall back to plaintext.
	if err == nil {
		if err := nativePostgresTool(ctx, tools.dump, badEnv, &bytes.Buffer{}, "--no-password", "--format=custom"); err == nil {
			t.Fatal("native backup accepted missing TLS trust root")
		}
	}
}
