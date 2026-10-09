package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// runBackfill re-runs a migration's data statements (everything but its ALTERs, which
// the fresh store already applied) over rows seeded the way an older build left them.
func runBackfill(t *testing.T, st *store.Store, file string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("migrations", file))
	if err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			body.WriteString(line + "\n")
		}
	}
	for _, stmt := range strings.Split(body.String(), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" || strings.HasPrefix(strings.ToUpper(stmt), "ALTER") {
			continue
		}
		if _, err := st.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v\n%s", file, err, stmt)
		}
	}
}

// 0132 marks grabs waiting in Review as 'held' and settled-review grabs stuck at
// 'grabbed' as 'dismissed' — and touches nothing else.
func TestGrabLifecycleBackfill(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	grab := func(title, hash, status string) {
		exec(`INSERT INTO grabs (movie_id, title, info_hash, status) VALUES (1, ?, ?, ?)`, title, hash, status)
	}
	grab("pending-review", "AAAA", "grabbed")        // → held (case differs from the review's hash)
	grab("resolved-imported", "bbbb", "grabbed")     // → dismissed
	grab("resolved-not-recorded", "cccc", "grabbed") // resolved, but never recorded as handled: stays
	grab("plain", "dddd", "grabbed")                 // no review: stays
	grab("already-imported", "eeee", "imported")     // pending review, but closed out: stays
	grab("no-hash", "", "grabbed")                   // nothing to match on: stays
	exec(`INSERT INTO import_reviews (hash, name, status) VALUES ('aaaa', 'x', 'pending'), ('BBBB', 'x', 'resolved'),
		('cccc', 'x', 'resolved'), ('eeee', 'x', 'pending'), ('', 'x', 'pending')`)
	exec(`INSERT INTO imports (download_hash, source_path, target_path, title) VALUES ('BbBb', '', '', 'x')`)

	runBackfill(t, st, "0132_grab_lifecycle.sql")

	want := map[string]string{
		"pending-review": "held", "resolved-imported": "dismissed", "resolved-not-recorded": "grabbed",
		"plain": "grabbed", "already-imported": "imported", "no-hash": "grabbed",
	}
	for title, w := range want {
		var got string
		if err := db.QueryRow(`SELECT status FROM grabs WHERE title = ?`, title).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != w {
			t.Errorf("%s: status %q, want %q", title, got, w)
		}
	}
	var resolution string
	if err := db.QueryRow(`SELECT resolution FROM import_reviews LIMIT 1`).Scan(&resolution); err != nil || resolution != "" {
		t.Errorf("resolution column: %q, %v", resolution, err)
	}
}
