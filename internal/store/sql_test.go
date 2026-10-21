package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestSQLExpressionHelpers(t *testing.T) {
	for _, tt := range []struct {
		dialect Dialect
		time    string
		fold    string
	}{
		{SQLite, "strftime('%Y-%m-%d %H:%M:%S', s.expires_at)", "lower(?)"},
		{Postgres, "to_char(s.expires_at, 'YYYY-MM-DD HH24:MI:SS')", "ascii_lower(?)"},
	} {
		db := &Database{dialect: tt.dialect}
		if got := TimestampText(db, "s.expires_at"); got != tt.time {
			t.Errorf("%s time expression = %q", tt.dialect, got)
		}
		if got := FoldASCII(db, "?"); got != tt.fold {
			t.Errorf("%s fold expression = %q", tt.dialect, got)
		}
		min, max := "MIN(a, b)", "MAX(a, b)"
		if tt.dialect == Postgres {
			min, max = "LEAST(a, b)", "GREATEST(a, b)"
		}
		if got := ScalarMin(db, "a", "b"); got != min {
			t.Errorf("%s scalar min = %q", tt.dialect, got)
		}
		if got := ScalarMax(db, "a", "b"); got != max {
			t.Errorf("%s scalar max = %q", tt.dialect, got)
		}
	}
}

func TestSQLiteUTCNowText(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	checkUTCNowText(t, context.Background(), st.DB())
}

func TestPostgresUTCNowText(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	checkUTCNowText(t, ctx, db)
}

func TestSQLiteDateText(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	checkDateText(t, context.Background(), st.DB())
}

func TestPostgresDateText(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	checkDateText(t, ctx, db)
}

func checkDateText(t *testing.T, ctx context.Context, db Execer) {
	t.Helper()
	for _, tt := range []struct{ value, want string }{
		{"2024-02-29", "2024-02-29"},
		{"2026-01-02 12:30:00", "2026-01-02"},
		{"2026-01-01T00:30:00+02:00", "2025-12-31"},
		{"2026-99-99", ""}, {"not-a-date", ""}, {"", ""},
	} {
		var got sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT `+DateText(db, "?"), tt.value).Scan(&got); err != nil || got.String != tt.want || got.Valid != (tt.want != "") {
			t.Errorf("date(%q) = %+v, want %q, err %v", tt.value, got, tt.want, err)
		}
	}
	var today string
	if err := db.QueryRowContext(ctx, `SELECT `+UTCDateText(db)).Scan(&today); err != nil || today != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("UTC date = %q, %v", today, err)
	}
}

func TestSQLiteLikeASCII(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	checkLikeASCII(t, context.Background(), st.DB())
}

func TestPostgresLikeASCII(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	checkLikeASCII(t, ctx, db)
}

func checkLikeASCII(t *testing.T, ctx context.Context, db Execer) {
	t.Helper()
	for _, tt := range []struct {
		value, pattern string
		escape, want   bool
	}{
		{"Heat", "%heat%", false, true},
		{"É", "é", false, false},
		{"100%_", `100\%\_`, true, true},
		{`C:\path\file`, `%c:\path%`, false, true},
		{"100X", "100_", false, true},
		{"100%", `100\%`, false, false},
	} {
		var got bool
		if err := db.QueryRowContext(ctx, `SELECT `+LikeASCII(db, "?", "?", tt.escape), tt.value, tt.pattern).Scan(&got); err != nil || got != tt.want {
			t.Errorf("%q LIKE %q (escape %v) = %v, %v", tt.value, tt.pattern, tt.escape, got, err)
		}
	}
}

func checkUTCNowText(t *testing.T, ctx context.Context, db Execer) {
	t.Helper()
	for modifier, offset := range map[string]time.Duration{
		"-1 day":       -24 * time.Hour,
		"-6 hours":     -6 * time.Hour,
		"+300 seconds": 300 * time.Second,
	} {
		var nowText, shiftedText string
		if err := db.QueryRowContext(ctx, `SELECT `+UTCNowText(db, "")+`, `+UTCNowText(db, "?"), modifier).Scan(&nowText, &shiftedText); err != nil {
			t.Fatal(err)
		}
		now, err := time.Parse("2006-01-02 15:04:05", nowText)
		if err != nil {
			t.Fatal(err)
		}
		shifted, err := time.Parse("2006-01-02 15:04:05", shiftedText)
		if err != nil {
			t.Fatal(err)
		}
		if got := shifted.Sub(now); got != offset {
			t.Errorf("%s offset = %s, want %s", modifier, got, offset)
		}
		if age := time.Since(now); age < -time.Second || age > 10*time.Second {
			t.Errorf("database clock is not current UTC: %s", nowText)
		}
	}
}
