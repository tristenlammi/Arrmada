package store_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/store"
)

func TestPostgresApplicationProcessStartsAndStops(t *testing.T) {
	t.Run("preprovisioned", func(t *testing.T) { checkPostgresApplicationProcess(t, false) })
	t.Run("automatic-schema", func(t *testing.T) { checkPostgresApplicationProcess(t, true) })
}

func checkPostgresApplicationProcess(t *testing.T, automaticSchema bool) {
	fixtureCtx, admin, opt, _ := store.PostgresFixture(t)
	binary := os.Getenv("ARRMADA_TEST_BINARY")
	if binary == "" {
		t.Skip("set ARRMADA_TEST_BINARY to exercise the real server process (CI supplies it)")
	}
	var database string
	if automaticSchema {
		if _, err := admin.Exec(fixtureCtx, `DROP SCHEMA `+pgx.Identifier{opt.Schema}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		if err := admin.QueryRow(fixtureCtx, `SELECT current_database()`).Scan(&database); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(fixtureCtx, `GRANT CREATE ON DATABASE `+pgx.Identifier{database}.Sanitize()+` TO `+pgx.Identifier{opt.Schema}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := admin.Exec(cleanup, `REVOKE CREATE ON DATABASE `+pgx.Identifier{database}.Sanitize()+` FROM `+pgx.Identifier{opt.Schema}.Sanitize()); err != nil {
				t.Error(err)
			}
		})
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	dir := t.TempDir()
	old := []byte("former SQLite database must not be opened")
	if err := os.WriteFile(filepath.Join(dir, "arrmada.db"), old, 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.CreateTemp(dir, "process-output-*")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		data, _ := os.ReadFile(log.Name())
		text := strings.ReplaceAll(string(data), opt.URL, "[redacted PostgreSQL URL]")
		if parsed, err := url.Parse(opt.URL); err == nil && parsed.User != nil {
			if password, ok := parsed.User.Password(); ok && password != "" {
				text = strings.ReplaceAll(text, password, "[redacted]")
			}
		}
		if len(text) > 12000 {
			text = text[len(text)-12000:]
		}
		t.Logf("isolated app startup/shutdown log (connection secrets redacted):\n%s", text)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		// Never inherit real service/database credentials into a test app.
		if !strings.HasPrefix(key, "ARRMADA_") && !strings.HasPrefix(key, "PG") {
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.Env = append(cmd.Env,
		"ARRMADA_HOST=127.0.0.1", "ARRMADA_PORT="+strconv.Itoa(port), "ARRMADA_DATA_DIR="+dir,
		"ARRMADA_DATABASE_BACKEND=postgres", "ARRMADA_POSTGRES_URL="+opt.URL, "ARRMADA_POSTGRES_SCHEMA="+opt.Schema,
		"ARRMADA_POSTGRES_BIN="+os.Getenv("ARRMADA_TEST_POSTGRES_BIN"),
		"ARRMADA_LIBRARY_DIR="+filepath.Join(dir, "library"), "ARRMADA_DOWNLOADS_DIR="+filepath.Join(dir, "downloads"))
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-exited
		}
	}()
	client := &http.Client{Timeout: time.Second}
	ready := false
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for !ready {
		select {
		case err := <-exited:
			stopped = true
			t.Fatalf("PostgreSQL app exited during startup: %v (private log: %s)", err, log.Name())
		case <-deadline.C:
			t.Fatalf("PostgreSQL app did not become healthy (private log: %s)", log.Name())
		default:
		}
		response, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/api/health")
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			ready = response.StatusCode == http.StatusOK
		}
		if !ready {
			time.Sleep(25 * time.Millisecond)
		}
	}
	// Exercise the actual maintenance entrypoints against the same live schema,
	// not just their store/service helpers. No command may open arrmada.db.
	if automaticSchema {
		if _, err := admin.Exec(ctx, `REVOKE CREATE ON DATABASE `+pgx.Identifier{database}.Sanitize()+` FROM `+pgx.Identifier{opt.Schema}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}
	runCLI := func(input string, args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = cmd.Env
		command.Stdin = strings.NewReader(input)
		return command.CombinedOutput()
	}
	if output, err := runCLI("", "schema"); err != nil || !bytes.Contains(output, []byte("0004_subtitle_job_ids")) {
		t.Fatalf("real PostgreSQL schema CLI: %v %s", err, output)
	}
	if output, err := runCLI("", "backup", "--kind", "pre-update"); err != nil || !strings.HasSuffix(strings.TrimSpace(string(output)), ".pgdump") {
		t.Fatalf("real PostgreSQL backup CLI: %v %s", err, output)
	}
	maintenance, err := store.OpenPostgresNoMigrate(ctx, dir, opt)
	if err != nil {
		t.Fatal(err)
	}
	accounts := auth.NewService(maintenance.DB())
	if _, err := accounts.CreateUser(ctx, "cli-test", "old-test-password", auth.RoleAdmin, false); err != nil {
		_ = maintenance.Close()
		t.Fatal(err)
	}
	if output, err := runCLI("new-test-password\n", "reset-password", "cli-test", "--password-stdin"); err != nil {
		_ = maintenance.Close()
		t.Fatalf("real PostgreSQL password CLI: %v %s", err, output)
	}
	if _, err := accounts.Authenticate(ctx, "cli-test", "new-test-password"); err != nil {
		_ = maintenance.Close()
		t.Fatal("CLI did not update the PostgreSQL password")
	}
	_ = maintenance.Close()
	if output, err := runCLI("", "restore", "not-a-sqlite-backup.db"); err == nil || !bytes.Contains(output, []byte("offline only")) {
		t.Fatalf("real PostgreSQL CLI allowed SQLite restore: %v %s", err, output)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		stopped = true
		if err != nil {
			t.Fatalf("graceful PostgreSQL app shutdown: %v (private log: %s)", err, log.Name())
		}
	case <-ctx.Done():
		t.Fatal("PostgreSQL application did not shut down")
	}
	output, err := os.ReadFile(log.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output, []byte("Arrmada is online")) || bytes.Contains(output, []byte("SQLSTATE")) {
		t.Fatalf("startup did not complete cleanly (private log: %s)", log.Name())
	}
	actual, err := os.ReadFile(filepath.Join(dir, "arrmada.db"))
	if err != nil || !bytes.Equal(actual, old) {
		t.Fatal("real PostgreSQL application touched the old SQLite database")
	}
}

func TestPostgresConversionCLIAuthenticatesAfterRoundTrip(t *testing.T) {
	binary := os.Getenv("ARRMADA_TEST_BINARY")
	if binary == "" {
		t.Skip("set ARRMADA_TEST_BINARY to exercise real conversion CLI")
	}
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	accounts := auth.NewService(s.DB())
	ctx := context.Background()
	if _, err := accounts.CreateUser(ctx, "conversion-cli-user", "conversion-test-password", auth.RoleAdmin, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, _, opt, _ := store.PostgresFixture(t)
	run := func(from, to, file string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, "convert-database", "--from", from, "--to", to, "--sqlite-file", file)
		for _, item := range os.Environ() {
			key, _, _ := strings.Cut(item, "=")
			if !strings.HasPrefix(key, "ARRMADA_") && !strings.HasPrefix(key, "PG") {
				cmd.Env = append(cmd.Env, item)
			}
		}
		cmd.Env = append(cmd.Env, "ARRMADA_DATA_DIR="+t.TempDir(), "ARRMADA_DATABASE_BACKEND=sqlite", "ARRMADA_POSTGRES_URL="+opt.URL, "ARRMADA_POSTGRES_SCHEMA="+opt.Schema)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("real conversion CLI %s to %s: %v %s", from, to, err, out)
		}
	}
	run("sqlite", "postgres", filepath.Join(dir, "arrmada.db"))
	pg, err := store.OpenPostgresNoMigrate(ctx, t.TempDir(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.NewService(pg.DB()).Authenticate(ctx, "conversion-cli-user", "conversion-test-password"); err != nil {
		t.Fatal("converted PostgreSQL credentials failed authentication")
	}
	if err := pg.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "arrmada.db")
	run("postgres", "sqlite", target)
	back, err := store.OpenNoMigrate(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	defer back.Close()
	if _, err := auth.NewService(back.DB()).Authenticate(ctx, "conversion-cli-user", "conversion-test-password"); err != nil {
		t.Fatal("round-trip SQLite credentials failed authentication")
	}
}
