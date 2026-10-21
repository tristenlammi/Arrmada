package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestUniqueViolationClassification(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("UNIQUE constraint failed"), false},
		{fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23505"}), true},
		{&pgconn.PgError{Code: "23503"}, false},
		{&pgconn.PgError{Code: "23502"}, false},
		{&pgconn.PgError{Code: "23514"}, false},
	} {
		if got := IsUniqueViolation(tt.err); got != tt.want {
			t.Errorf("IsUniqueViolation(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestSQLiteUniqueViolationClassification(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "constraints.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE constraints_test (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE CHECK (name != 'invalid'));
		INSERT INTO constraints_test VALUES (1, 'one')`); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		query string
		want  bool
	}{
		{`INSERT INTO constraints_test VALUES (1, 'two')`, true},
		{`INSERT INTO constraints_test VALUES (2, 'one')`, true},
		{`INSERT INTO constraints_test VALUES (2, NULL)`, false},
		{`INSERT INTO constraints_test VALUES (2, 'invalid')`, false},
	} {
		_, err := db.Exec(tt.query)
		if err == nil || IsUniqueViolation(fmt.Errorf("repository: %w", err)) != tt.want {
			t.Errorf("%s: got %v, want unique = %v", tt.query, err, tt.want)
		}
	}
}
