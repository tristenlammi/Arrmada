package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/listening"
	"github.com/tristenlammi/arrmada/internal/store"
)

func TestSQLiteScalarRepositories(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	checkScalarRepositories(t, context.Background(), st.DB())
}

func TestPostgresScalarRepositories(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.MigratePostgres(ctx, db, store.PostgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	checkScalarRepositories(t, ctx, db)
}

func checkScalarRepositories(t *testing.T, ctx context.Context, db store.Pool) {
	repo := books.NewRepo(db)
	book, err := repo.Create(ctx, books.Book{OLKey: "key", Title: "Catalogue title"})
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour).UnixMilli()
	if _, err := db.ExecContext(ctx, `UPDATE books SET edited_at = ?, meta_updated_at = ? WHERE id = ?`, future, future, book.ID); err != nil {
		t.Fatal(err)
	}
	svc := books.NewService(db, nil, nil)
	result, err := svc.EditDetails(ctx, book.ID, books.EditRequest{Set: map[string]json.RawMessage{books.FieldTitle: json.RawMessage(`"Hand edit"`)}})
	if err != nil || result.Book.Title != "Hand edit" {
		t.Fatalf("book edit = %+v, %v", result, err)
	}
	if err := repo.SetMatched(ctx, book.ID, map[string]any{"title": "New catalogue title"}); err != nil {
		t.Fatal(err)
	}
	result, err = svc.EditDetails(ctx, book.ID, books.EditRequest{ResetAll: true})
	if err != nil || result.Book.Title != "New catalogue title" {
		t.Fatalf("book reset = %+v, %v", result, err)
	}
	if err := repo.TouchMeta(ctx, book.ID); err != nil {
		t.Fatal(err)
	}
	var edited, meta int64
	if err := db.QueryRowContext(ctx, `SELECT edited_at, meta_updated_at FROM books WHERE id = ?`, book.ID).Scan(&edited, &meta); err != nil || edited <= future || meta <= future {
		t.Fatalf("monotonic stamps = %d/%d, previous %d, %v", edited, meta, future, err)
	}
	listen := listening.NewStore(db)
	if _, err := db.ExecContext(ctx, `INSERT INTO users (id, username, password_hash) VALUES (1, 'listener', '')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	upload := listening.OfflineSession{ID: "offline", ItemKey: listening.ItemKey(book.ID, 0), Device: "Phone", Client: "Test",
		StartedAt: now - 600000, UpdatedAt: now - 300000, Position: 120, Duration: 3600, Listened: 120}
	if _, err := listen.SyncOffline(ctx, 1, upload); err != nil {
		t.Fatal(err)
	}
	// A repeated older upload cannot regress session/log high-water marks or
	// duplicate a listening-log row, even when its position/listening is lower.
	upload.UpdatedAt -= 100000
	upload.Position, upload.Listened = 60, 60
	if _, err := listen.SyncOffline(ctx, 1, upload); err != nil {
		t.Fatal(err)
	}
	session, err := listen.GetSession(ctx, 1, upload.ID)
	if err != nil || session.LastAt != now-300000 || session.Listened != 120 {
		t.Fatalf("session high-water marks = %+v, %v", session, err)
	}
	var count int
	var ended int64
	var seconds float64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), MAX(ended_at), MAX(seconds) FROM listen_log WHERE session_id = ?`, upload.ID).Scan(&count, &ended, &seconds); err != nil || count != 1 || ended != now-300000 || seconds != 120 {
		t.Fatalf("listening-log high-water marks = %d/%d/%g, %v", count, ended, seconds, err)
	}
}
