package store

import (
	"context"
	"testing"
)

func TestSQLiteNumberedParameters(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	checkNumberedParameters(t, context.Background(), st.DB())
}

func TestPostgresNumberedParameters(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	checkNumberedParameters(t, ctx, db)
}

func checkNumberedParameters(t *testing.T, ctx context.Context, db Pool) {
	t.Helper()
	const query = `SELECT CAST(?2 AS bigint), CAST(?1 AS bigint), CAST(?2 AS bigint), CAST(? AS bigint)`
	var a, b, c, d int64
	if err := db.QueryRowContext(ctx, query, 11, 22, 33).Scan(&a, &b, &c, &d); err != nil || a != 22 || b != 11 || c != 22 || d != 33 {
		t.Fatalf("numbered parameters = %d/%d/%d/%d, %v", a, b, c, d, err)
	}
	prepared, err := db.PrepareContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if err := prepared.QueryRowContext(ctx, 44, 55, 66).Scan(&a, &b, &c, &d); err != nil || a != 55 || b != 44 || c != 55 || d != 66 {
		t.Fatalf("prepared numbered parameters = %d/%d/%d/%d, %v", a, b, c, d, err)
	}
	var length int
	var prefix bool
	if err := db.QueryRowContext(ctx, `SELECT length(?1), substr(?1, 1, length(?2)) = ?2`, "/library/file", "/library/").Scan(&length, &prefix); err != nil || length != 13 || !prefix {
		t.Fatalf("numbered text/path parameters = %d/%v, %v", length, prefix, err)
	}
}
