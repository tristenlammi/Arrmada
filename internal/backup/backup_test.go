package backup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// fakeSettings is an in-memory settings store.
type fakeSettings map[string]string

func (f fakeSettings) Get(_ context.Context, key, def string) string {
	if v, ok := f[key]; ok {
		return v
	}
	return def
}

func (f fakeSettings) GetBool(_ context.Context, key string, def bool) bool {
	if v, ok := f[key]; ok {
		return v == "true"
	}
	return def
}

// newService opens a scratch database in a temp data dir; nothing touches a real one.
func newService(t *testing.T, set fakeSettings) (*Service, string) {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, set, slog.New(slog.NewTextHandler(io.Discard, nil))), dataDir
}

// touch writes a placeholder file under a backup name taken at at.
func touch(t *testing.T, dir string, kind store.BackupKind, at time.Time) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, store.BackupName(kind, at))
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDueNightly(t *testing.T) {
	loc := time.Local
	at5 := time.Date(2026, 10, 9, 5, 0, 0, 0, loc) // after the 04:00 hour
	at3 := time.Date(2026, 10, 9, 3, 0, 0, 0, loc) // before it
	cases := []struct {
		name   string
		newest time.Time
		now    time.Time
		want   bool
	}{
		{"fresh install", time.Time{}, at5, true},
		{"19h old", at5.Add(-19 * time.Hour), at5, false},
		{"21h old, before the hour", at3.Add(-21 * time.Hour), at3, false},
		{"21h old, after the hour", at5.Add(-21 * time.Hour), at5, true},
		{"37h old, before the hour", at3.Add(-37 * time.Hour), at3, true},
	}
	for _, c := range cases {
		if got := dueNightly(c.newest, c.now, 4); got != c.want {
			t.Errorf("%s: dueNightly = %v, want %v", c.name, got, c.want)
		}
	}
}

// Nine nightlies prune to seven, and the store's pre-migrate copies and anything else in
// the folder are left alone.
func TestBackupCreateListPrune(t *testing.T) {
	s, _ := newService(t, fakeSettings{})
	ctx := context.Background()
	base := time.Now().Add(-30 * 24 * time.Hour)
	for i := 0; i < 8; i++ {
		touch(t, s.Dir(), store.BackupNightly, base.Add(time.Duration(i)*24*time.Hour))
	}
	pre := touch(t, s.Dir(), store.BackupPreMigrate, base)
	other := filepath.Join(s.Dir(), "notes.txt")
	if err := os.WriteFile(other, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := s.Create(ctx, store.BackupNightly) // the ninth
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind != store.BackupNightly || b.SizeBytes == 0 {
		t.Errorf("created %+v", b)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nightly := 0
	for _, x := range list {
		if x.Kind == store.BackupNightly {
			nightly++
		}
	}
	if nightly != 7 {
		t.Errorf("%d nightlies kept, want 7", nightly)
	}
	if list[0].Name != b.Name {
		t.Errorf("list isn't newest first: %s before %s", list[0].Name, b.Name)
	}
	if _, err := os.Stat(pre); err != nil {
		t.Error("a pre-migrate copy was pruned by the backup service")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("a file that isn't a backup was touched")
	}
	if _, err := s.Create(ctx, store.BackupPreMigrate); err == nil {
		t.Error("pre-migrate copies belong to the upgrade, not the backup service")
	}
}

// The configured retention wins over the default.
func TestNightlyRetentionSetting(t *testing.T) {
	s, _ := newService(t, fakeSettings{KeyKeepNightly: "2"})
	for i := 0; i < 3; i++ {
		touch(t, s.Dir(), store.BackupNightly, time.Now().Add(-time.Duration(10-i)*24*time.Hour))
	}
	if _, err := s.Create(context.Background(), store.BackupNightly); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List(context.Background())
	if len(list) != 2 {
		t.Errorf("%d backups kept, want 2", len(list))
	}
}

// Running the hourly task twice in a row makes one nightly, not two; switched off it makes none.
func TestDailyBackupIdempotent(t *testing.T) {
	s, _ := newService(t, fakeSettings{})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := s.RunNightly(ctx); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := s.List(ctx)
	if len(list) != 1 || list[0].Kind != store.BackupNightly {
		t.Fatalf("backups = %+v, want exactly one nightly", list)
	}

	off, _ := newService(t, fakeSettings{KeyEnabled: "false"})
	if err := off.RunNightly(ctx); err != nil {
		t.Fatal(err)
	}
	if list, _ := off.List(ctx); len(list) != 0 {
		t.Errorf("backups switched off, yet %d were made", len(list))
	}
}

// Each backup reports the newest migration it holds, read from the copy itself.
func TestListReadsSchemaVersion(t *testing.T) {
	s, _ := newService(t, fakeSettings{})
	ctx := context.Background()
	b, err := s.Create(ctx, store.BackupManual)
	if err != nil {
		t.Fatal(err)
	}
	if b.SchemaVersion == "" {
		t.Fatal("no schema version read from the backup")
	}
	var live string
	if err := s.store.DB().QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if b.SchemaVersion != live {
		t.Errorf("schema version = %q, want %q", b.SchemaVersion, live)
	}
}

// Delete takes only exact backup names: never a path, the live database, or another file.
func TestDeleteRejectsNonBackupNames(t *testing.T) {
	s, dataDir := newService(t, fakeSettings{})
	for _, name := range []string{"../arrmada.db", "arrmada.db", filepath.Join(dataDir, "arrmada.db"), "arrmada-nightly-x.db", "notes.txt"} {
		if err := s.Delete(name); !errors.Is(err, ErrBadName) {
			t.Errorf("Delete(%q) = %v, want ErrBadName", name, err)
		}
	}
	b, err := s.Create(context.Background(), store.BackupManual)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(b.Name); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(context.Background()); len(list) != 0 {
		t.Errorf("backup not deleted: %+v", list)
	}
}

// The health line appears only once nightlies are on, the app has been up a day, and the
// newest is over 48 hours old.
func TestHealthWarning(t *testing.T) {
	s, _ := newService(t, fakeSettings{})
	ctx := context.Background()
	touch(t, s.Dir(), store.BackupNightly, time.Now().Add(-72*time.Hour))
	if msg := s.HealthWarning(ctx, time.Hour); msg != "" {
		t.Errorf("warned after an hour of uptime: %q", msg)
	}
	if msg := s.HealthWarning(ctx, 25*time.Hour); msg == "" {
		t.Error("no warning with a 3-day-old newest nightly")
	}
	touch(t, s.Dir(), store.BackupNightly, time.Now().Add(-time.Hour))
	if msg := s.HealthWarning(ctx, 25*time.Hour); msg != "" {
		t.Errorf("warned with a fresh nightly: %q", msg)
	}
	off, _ := newService(t, fakeSettings{KeyEnabled: "false"})
	if msg := off.HealthWarning(ctx, 100*time.Hour); msg != "" {
		t.Errorf("warned with backups switched off: %q", msg)
	}
}

// Deleting an account with nothing to lose takes its own kind of safety copy. That kind
// was missing from the backup names, so every such delete was refused.
func TestPreDeleteEmptyUserKind(t *testing.T) {
	s, _ := newService(t, fakeSettings{})
	b, err := s.Create(context.Background(), store.BackupPreDeleteEmptyUser)
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind != store.BackupPreDeleteEmptyUser {
		t.Errorf("kind = %q", b.Kind)
	}
}
