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

// 0133 classifies old reviews from their reason text, in order, with anything it doesn't
// recognise falling back to 'mismatch'.
func TestReviewReasonCodeBackfill(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	cases := []struct {
		reason   string
		expected int64
		want     string
	}{
		{"Import failed 5 times: mkdir /movies/X: permission denied — fix the cause", 0, "import_failed"},
		{"Parsed as \"Foo\", which matches no series in your library", 0, "unmatched"},
		{"Grabbed for a movie you deleted", 0, "unmatched"},
		{"Downloaded, but none of its 12 video files could be matched to an episode — the episode numbering isn't in a form Arrmada recognises", 7, "numbering"},
		{"Downloaded but holds no ebook or audiobook files — still archived, or unreadable", 3, "no_media"},
		{"Downloaded but holds no audio files — still archived, or unreadable", 4, "no_media"},
		{"Grabbed for \"Below Deck\" but the download looks like \"Below Deck Mediterranean\"", 2, "mismatch"},
		{"something nobody wrote a rule for", 5, "mismatch"},
	}
	for _, c := range cases {
		if _, err := db.Exec(`INSERT INTO import_reviews (name, reason, expected_id) VALUES ('x', ?, ?)`, c.reason, c.expected); err != nil {
			t.Fatal(err)
		}
	}
	// A row that already carries a code keeps it.
	if _, err := db.Exec(`INSERT INTO import_reviews (name, reason, expected_id, reason_code) VALUES ('x', 'Import failed 2 times', 1, 'numbering')`); err != nil {
		t.Fatal(err)
	}
	runBackfill(t, st, "0133_review_reason_code.sql")
	rows, err := db.Query(`SELECT reason, reason_code FROM import_reviews ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var reason, code string
		if err := rows.Scan(&reason, &code); err != nil {
			t.Fatal(err)
		}
		want := "numbering"
		if i < len(cases) {
			want = cases[i].want
		}
		if code != want {
			t.Errorf("%q: code %q, want %q", reason, code, want)
		}
		i++
	}
}
