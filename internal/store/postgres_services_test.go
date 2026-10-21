package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

func TestPostgresConfigurationRepositoriesAndJobRunner(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.MigratePostgres(ctx, db, store.PostgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	profile, err := quality.NewRepo(db).Create(ctx, quality.StoredProfile{
		MediaType: "movie", Name: "PostgreSQL profile", AllowedResolutions: []string{"1080p"}, UpgradesEnabled: true,
	})
	if err != nil || profile.ID <= 8 || profile.Name != "PostgreSQL profile" || !profile.UpgradesEnabled {
		t.Fatalf("quality profile = %+v, %v", profile, err)
	}
	clientRepo := download.NewRepo(db)
	client, err := clientRepo.Create(ctx, download.Client{Name: "Downloader", Kind: download.KindQbittorrent, URL: "http://not-contacted.invalid", Enabled: true})
	if err != nil || client.ID <= 0 {
		t.Fatalf("download client = %+v, %v", client, err)
	}
	loadedClient, err := clientRepo.Get(ctx, client.ID)
	if err != nil || loadedClient.Name != client.Name || !loadedClient.Enabled {
		t.Fatalf("loaded download client = %+v, %v", loadedClient, err)
	}
	indexerRepo := indexer.NewRepo(db)
	idx, err := indexerRepo.Create(ctx, indexer.Indexer{Name: "Indexer", Kind: indexer.KindTorznab, URL: "http://not-contacted.invalid", Enabled: true})
	if err != nil || idx.ID <= 0 {
		t.Fatalf("indexer = %+v, %v", idx, err)
	}
	loadedIndexer, err := indexerRepo.Get(ctx, idx.ID)
	if err != nil || loadedIndexer.Name != idx.Name || !loadedIndexer.Enabled {
		t.Fatalf("loaded indexer = %+v, %v", loadedIndexer, err)
	}
	runner, err := jobs.New(ctx, db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runner.Shutdown(time.Second) })
	job, err := runner.Run(ctx, jobs.Spec{Kind: "postgres.test", Target: "test", Trigger: "system", Fn: func(context.Context, *jobs.Progress) (any, error) {
		return map[string]bool{"done": true}, nil
	}})
	if err != nil || job.ID <= 0 || job.FinishedAt == nil || string(job.Result) != `{"done":true}` {
		t.Fatalf("persisted job = %+v, %v", job, err)
	}
	listed, err := runner.List(ctx, jobs.Filter{Kind: "postgres.test"})
	if err != nil || len(listed) != 1 || listed[0].ID != job.ID {
		t.Fatalf("job list = %+v, %v", listed, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO artists (id, mbid, name) VALUES (1, 'artist', 'Artist')`); err != nil {
		t.Fatal(err)
	}
	albums := music.NewRepo(db)
	first, err := albums.UpsertAlbum(ctx, music.Album{ArtistID: 1, MBID: "first", Title: "First"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := albums.UpsertAlbum(ctx, music.Album{ArtistID: 1, MBID: "second", Title: "Second"})
	if err != nil || second == first {
		t.Fatalf("second album id = %d, %v", second, err)
	}
	updated, err := albums.UpsertAlbum(ctx, music.Album{ArtistID: 1, MBID: "first", Title: "Updated"})
	if err != nil || updated != first {
		t.Fatalf("updated album id = %d, want %d, err %v", updated, first, err)
	}
}

func TestPostgresAccountMergePreservesTargetDuplicates(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.MigratePostgres(ctx, db, store.PostgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(db)
	target, err := svc.CreateUser(ctx, "owner", "test-password", auth.RoleAdmin, false)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := svc.FindOrCreatePlexUser(ctx, "plex-duplicate", "duplicate", auth.RoleRequester, auth.AutoApproval{})
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []*auth.User{target, duplicate} {
		for _, statement := range []string{
			`INSERT INTO user_notifications (user_id, title, ref) VALUES (?, 'original', 'shared')`,
			`INSERT INTO listen_progress (user_id, item_key, updated_at) VALUES (?, 'shared', 1)`,
			`INSERT INTO listen_bookmarks (user_id, item_key, time, title, created_at) VALUES (?, 'shared', 3, 'original', 1)`,
			`INSERT INTO audio_passwords (user_id, hash, updated_at) VALUES (?, 'original', 1)`,
		} {
			if _, err := db.ExecContext(ctx, statement, user.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE user_notifications SET title = 'target wins' WHERE user_id = ?`, target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO user_notifications (user_id, title, ref) VALUES (?, 'moves', 'unique')`, duplicate.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.MergeDuplicate(ctx, target.ID, duplicate.ID); err != nil {
		t.Fatal(err)
	}
	var title string
	var count int
	if err := db.QueryRowContext(ctx, `SELECT title FROM user_notifications WHERE user_id = ? AND ref = 'shared'`, target.ID).Scan(&title); err != nil || title != "target wins" {
		t.Fatalf("target duplicate overwritten: %q, %v", title, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM user_notifications WHERE user_id = ?`, target.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("nonduplicate data lost: %d, %v", count, err)
	}
	linked, err := svc.UserByPlexID(ctx, "plex-duplicate")
	if err != nil || linked == nil || linked.ID != target.ID {
		t.Fatalf("external identity not moved: %+v, %v", linked, err)
	}
	if _, err := svc.UserByID(ctx, duplicate.ID); err == nil {
		t.Fatal("duplicate account still exists")
	}
}

func TestPostgresAuthenticationSessionsAndAPIKeys(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.MigratePostgres(ctx, db, store.PostgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(db)
	u, err := svc.CreateUser(ctx, "Admin@example.com", "test-password", auth.RoleAdmin, false)
	if err != nil || u.ID <= 0 {
		t.Fatalf("create admin = %+v, %v", u, err)
	}
	if _, err := svc.CreateUser(ctx, "admin@example.com", "test-password", auth.RoleAdmin, false); !errors.Is(err, auth.ErrUserExists) {
		t.Fatalf("duplicate user not refused: %v", err)
	}
	// SQLite folds ASCII only. Distinct non-ASCII names must not be merged by
	// a PostgreSQL server's locale-dependent lower() implementation.
	for _, name := range []string{"ÉOwner", "éOwner"} {
		created, err := svc.CreateUser(ctx, name, "test-password", auth.RoleRequester, false)
		if err != nil {
			t.Fatal(err)
		}
		lookup := "ÉOWNER"
		if name == "éOwner" {
			lookup = "éOWNER"
		}
		found, err := svc.UserByUsername(ctx, lookup)
		if err != nil || found.ID != created.ID {
			t.Fatalf("ASCII-only username lookup %q = %+v, %v", lookup, found, err)
		}
	}
	signedIn, err := svc.Authenticate(ctx, "ADMIN@example.com", "test-password")
	if err != nil || signedIn.ID != u.ID {
		t.Fatalf("password sign-in = %+v, %v", signedIn, err)
	}
	token, expires, err := svc.CreateSessionFrom(ctx, u.ID, auth.SessionClient{UserAgent: "test browser", IP: "192.0.2.1"})
	if err != nil || token == "" || !expires.After(time.Now()) {
		t.Fatalf("create session = %q, %v, %v", token, expires, err)
	}
	current, exp, err := svc.ValidateSessionInfo(ctx, token)
	if err != nil || current.ID != u.ID || exp.IsZero() {
		t.Fatalf("session validation = %+v, %v, %v", current, exp, err)
	}
	sessions, err := svc.ListSessions(ctx, u.ID, token)
	if err != nil || len(sessions) != 1 || !sessions[0].Current || sessions[0].CreatedAt.IsZero() {
		t.Fatalf("device list = %+v, %v", sessions, err)
	}
	activity, err := svc.SessionActivityByUser(ctx)
	if err != nil || activity[u.ID].Sessions != 1 || activity[u.ID].LastSeen.IsZero() {
		t.Fatalf("session activity = %+v, %v", activity, err)
	}
	plex, err := svc.FindOrCreatePlexUser(ctx, "external-1", "Plex user", auth.RoleRequester, auth.AutoApproval{})
	if err != nil || plex.ID <= 0 || plex.ID == u.ID {
		t.Fatalf("linked account = %+v, %v", plex, err)
	}
	rawKey, key, err := svc.CreateAPIKey(ctx, u.ID, auth.NewAPIKey{Name: "test key", Scope: auth.KeyScopeRead, ExpiresInDays: 30})
	if err != nil || key.ID <= 0 || rawKey == "" {
		t.Fatalf("create API key = %+v, %v", key, err)
	}
	use, err := svc.ValidateAPIKey(ctx, rawKey, "192.0.2.2")
	if err != nil || use.User.ID != u.ID || use.KeyID != key.ID || use.Scope != auth.KeyScopeRead {
		t.Fatalf("API key validation = %+v, %v", use, err)
	}
	keys, err := svc.ListAPIKeys(ctx, u.ID)
	if err != nil || len(keys) != 1 || keys[0].CreatedAt.IsZero() || keys[0].LastUsedAt == nil || keys[0].ExpiresAt == nil {
		t.Fatalf("API key list = %+v, %v", keys, err)
	}
	if revoked, err := svc.RevokeAPIKey(ctx, u.ID, key.ID); err != nil || !revoked {
		t.Fatalf("revoke API key = %v, %v", revoked, err)
	}
	if _, err := svc.ValidateAPIKey(ctx, rawKey, "192.0.2.2"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("revoked API key accepted: %v", err)
	}
	if err := svc.DeleteSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ValidateSessionInfo(ctx, token); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("revoked session accepted: %v", err)
	}
}

// Exercise a real repository, not just the driver's SQL support. The settings
// service keeps its existing SQL and memory/write-through guarantees on both
// backends; it only needs the shared query boundary instead of a concrete pool.
func TestPostgresSettingsPersistenceAndFailedWrites(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := settings.Open(ctx, db); err == nil {
		t.Fatal("opened settings without a readable table")
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE settings (
		key text PRIMARY KEY, value text NOT NULL,
		updated_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatal(err)
	}
	svc, err := settings.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	key := "literal ? key'; --"
	if err := svc.Set(ctx, key, "original"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Set(ctx, key, "updated"); err != nil {
		t.Fatal(err)
	}
	reopened, err := settings.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get(ctx, key, "missing"); got != "updated" {
		t.Fatalf("persisted value = %q", got)
	}

	var writers sync.WaitGroup
	for i := range 20 {
		writers.Go(func() {
			if err := svc.Set(ctx, key, fmt.Sprint(i)); err != nil {
				t.Errorf("concurrent settings write: %v", err)
			}
		})
	}
	writers.Wait()
	var persisted string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if got := svc.Get(ctx, key, "missing"); got != persisted {
		t.Fatalf("memory %q differs from database %q", got, persisted)
	}
	// No failed database operation may replace the last successfully loaded
	// settings in memory with defaults or an unsaved value.
	if _, err := db.ExecContext(ctx, `DROP TABLE settings`); err != nil {
		t.Fatal(err)
	}
	if err := svc.Set(ctx, key, "not saved"); err == nil {
		t.Fatal("write to missing settings table succeeded")
	}
	if err := svc.Reload(ctx); err == nil {
		t.Fatal("reload of missing settings table succeeded")
	}
	if got := svc.Get(ctx, key, "missing"); got != persisted {
		t.Fatalf("failed write/reload changed memory to %q", got)
	}
}
