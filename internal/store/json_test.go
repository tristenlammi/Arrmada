package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestSQLiteJSONHelpers(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	checkJSONHelpers(t, context.Background(), st.DB())
}

func TestPostgresJSONHelpers(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	checkJSONHelpers(t, ctx, db)
}

func checkJSONHelpers(t *testing.T, ctx context.Context, db Pool) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `INSERT INTO movies (id, tmdb_id, title) VALUES (1, 1, 'Movie')`); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		raw    string
		length int
		title  string
	}{
		{`{"title":"Unicode 日本語","number":7.5}`, 0, "Unicode 日本語"},
		{`[{"a":1},2,null]`, 3, ""},
		{`null`, 0, ""},
		{`42`, 0, ""},
		{`broken`, 0, ""},
		{``, 0, ""},
	} {
		if _, err := db.ExecContext(ctx, `UPDATE movies SET extra_json = ? WHERE id = 1`, tt.raw); err != nil {
			t.Fatal(err)
		}
		var length int
		var title sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT `+JSONScalar(db, "extra_json", "title")+`, `+JSONArrayLength(db, "extra_json")+` FROM movies WHERE id = 1`).Scan(&title, &length); err != nil {
			t.Fatalf("reading %q: %v", tt.raw, err)
		}
		if title.String != tt.title || length != tt.length {
			t.Errorf("reading %q = %q, %d", tt.raw, title.String, length)
		}
		if err := WithTx(ctx, db, func(tx *Transaction) error {
			return SetJSONFields(ctx, tx, "movies", "extra_json", 1, map[string]any{"changed": true})
		}); err != nil {
			t.Fatal(err)
		}
		var after string
		if err := db.QueryRowContext(ctx, `SELECT extra_json FROM movies WHERE id = 1`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if json.Valid([]byte(tt.raw)) && tt.title == "" && after != tt.raw {
			t.Errorf("valid scalar/array changed: %q -> %q", tt.raw, after)
		}
	}
	const original = `{"unknown":123456789012345678901234567890}`
	if _, err := db.ExecContext(ctx, `UPDATE movies SET extra_json = ? WHERE id = 1`, original); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("abort JSON update")
	err := WithTx(ctx, db, func(tx *Transaction) error {
		if err := SetJSONFields(ctx, tx, "movies", "extra_json", 1, map[string]any{"changed": true}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("rollback error = %v", err)
	}
	if err := WithTx(ctx, db, func(tx *Transaction) error {
		return SetJSONFields(ctx, tx, "movies", "extra_json", 1, map[string]any{"invalid": math.NaN()})
	}); err == nil {
		t.Fatal("invalid JSON value accepted")
	}
	var after string
	if err := db.QueryRowContext(ctx, `SELECT extra_json FROM movies WHERE id = 1`).Scan(&after); err != nil || after != original {
		t.Fatalf("failed JSON update changed source: %q, %v", after, err)
	}
}
