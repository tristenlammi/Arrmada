package insights

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// newDataTestService builds an in-package Service backed by a migrated temp DB. Only the repo is wired
// (settings/bus/geo are unused by the data-layer paths under test).
func newDataTestService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Service{repo: &repo{db: st.DB()}}
}

func TestNormalizeDecision(t *testing.T) {
	cases := map[string]string{
		"direct play":   "direct_play",
		"Direct Play":   "direct_play",
		" direct_play":  "direct_play",
		"copy":          "direct_stream",
		"COPY":          "direct_stream",
		"direct stream": "direct_stream",
		"transcode":     "transcode",
		"Transcode ":    "transcode",
		"":              "",
		"weird":         "weird",
	}
	for in, want := range cases {
		if got := normalizeDecision(in); got != want {
			t.Errorf("normalizeDecision(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestImportHistorySkipsBadDuration verifies an in-progress/clock-skewed row (stopped<=started)
// is skipped rather than recorded — otherwise it poisons every SUM(watched) aggregate with a huge
// negative wall time.
func TestImportHistorySkipsBadDuration(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)

	rows := []ImportedSession{
		// In-progress at import time: stopped=0, duration=0 → un-computable, must be skipped.
		{UserID: 1, UserName: "Alice", RatingKey: "100", MediaType: "movie", Title: "Dune", StartedAt: 1_750_000_000},
		// Good row: 2h movie.
		{UserID: 1, UserName: "Alice", RatingKey: "101", MediaType: "movie", Title: "Arrival", StartedAt: 1_750_100_000, StoppedAt: 1_750_107_200},
		// stopped before started (clock skew) → skipped.
		{UserID: 1, UserName: "Alice", RatingKey: "102", MediaType: "movie", Title: "Bad", StartedAt: 1_750_200_000, StoppedAt: 1_750_100_000},
	}
	imported, skipped := s.ImportHistory(ctx, rows)
	if imported != 1 || skipped != 2 {
		t.Fatalf("imported=%d skipped=%d, want imported=1 skipped=2", imported, skipped)
	}

	// The single recorded movie should report a non-negative, sane watch time.
	titles, err := s.repo.topTitles(ctx, "movie", "title", 0, true, 10)
	if err != nil {
		t.Fatalf("topTitles: %v", err)
	}
	if len(titles) != 1 {
		t.Fatalf("got %d title rows, want 1", len(titles))
	}
	if titles[0].Title != "Arrival" || titles[0].Secs != 7200 {
		t.Fatalf("row = %+v, want Arrival / 7200s", titles[0])
	}
}

// TestWatchedSumClampsNegative verifies the aggregate hardening: even if a corrupt row somehow
// exists in the table, SUM(MAX(0,watched)) never goes negative.
func TestWatchedSumClampsNegative(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)

	// A good 1h play plus a directly-inserted corrupt row (stopped before started).
	if _, err := s.repo.insertSession(ctx, sessionRecord{
		UserID: "1", UserName: "Alice", RatingKey: "1", MediaType: "movie", Title: "Good",
		StartedAt: 1_750_000_000, StoppedAt: 1_750_003_600,
	}); err != nil {
		t.Fatalf("insert good: %v", err)
	}
	if _, err := s.repo.insertSession(ctx, sessionRecord{
		UserID: "1", UserName: "Alice", RatingKey: "1", MediaType: "movie", Title: "Good",
		StartedAt: 1_750_000_000, StoppedAt: 0, // corrupt: stopped=0 → hugely negative watched
	}); err != nil {
		t.Fatalf("insert corrupt: %v", err)
	}
	titles, err := s.repo.topTitles(ctx, "movie", "title", 0, true, 10)
	if err != nil {
		t.Fatalf("topTitles: %v", err)
	}
	if len(titles) != 1 {
		t.Fatalf("got %d rows, want 1", len(titles))
	}
	// Two plays counted, but the corrupt row contributes 0 (clamped) → total stays at the good 3600s.
	if titles[0].Plays != 2 {
		t.Errorf("plays = %d, want 2 (count includes the corrupt row)", titles[0].Plays)
	}
	if titles[0].Secs != 3600 {
		t.Errorf("secs = %d, want 3600 (corrupt row clamped to 0, not negative)", titles[0].Secs)
	}
}

// TestUsersDedup verifies two sessions sharing a started_at don't fan a user out into duplicate
// rows in the Users aggregate, and that plays/last-session are correct.
func TestUsersDedup(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)

	if err := s.repo.upsertUser(ctx, "1", "Alice", "avatar.png", 1_750_000_000); err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	// Two sessions at the SAME started_at (two devices, same second).
	for _, rk := range []string{"a", "b"} {
		if _, err := s.repo.insertSession(ctx, sessionRecord{
			UserID: "1", UserName: "Alice", RatingKey: rk, MediaType: "movie", Title: "Dune",
			Platform: "Roku", IPAddress: "1.2.3.4", StartedAt: 1_750_000_000, StoppedAt: 1_750_003_600,
		}); err != nil {
			t.Fatalf("insert %s: %v", rk, err)
		}
	}
	users, err := s.repo.users(ctx)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("got %d user rows, want exactly 1 (no fan-out)", len(users))
	}
	if users[0].TotalPlays != 2 {
		t.Errorf("total plays = %d, want 2", users[0].TotalPlays)
	}
	if users[0].LastPlatform != "Roku" {
		t.Errorf("last platform = %q, want Roku (one representative last-session row)", users[0].LastPlatform)
	}
}

// TestUpsertUserKeepsAvatarAndLastSeen verifies importing an old session with an empty avatar
// neither clobbers a good avatar nor moves last_seen backwards.
func TestUpsertUserKeepsAvatarAndLastSeen(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)

	if err := s.repo.upsertUser(ctx, "1", "Alice", "good-avatar.png", 1_750_000_000); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Import an older session carrying no avatar.
	if err := s.repo.upsertUser(ctx, "1", "Alice", "", 1_700_000_000); err != nil {
		t.Fatalf("import: %v", err)
	}
	var thumb string
	var lastSeen int64
	if err := s.repo.db.QueryRowContext(ctx, `SELECT thumb, last_seen_at FROM plex_users WHERE id='1'`).
		Scan(&thumb, &lastSeen); err != nil {
		t.Fatalf("query: %v", err)
	}
	if thumb != "good-avatar.png" {
		t.Errorf("thumb = %q, want good-avatar.png (empty import must not clobber it)", thumb)
	}
	if lastSeen != 1_750_000_000 {
		t.Errorf("last_seen = %d, want 1750000000 (must not move backwards)", lastSeen)
	}
}

// TestTopNamesRepresentativeID verifies grouping carries a real user_id for drill-down links.
func TestTopNamesRepresentativeID(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)

	for i := 0; i < 3; i++ {
		if _, err := s.repo.insertSession(ctx, sessionRecord{
			UserID: "42", UserName: "Alice", RatingKey: "x", MediaType: "movie", Platform: "Roku",
			StartedAt: 1_750_000_000 + int64(i), StoppedAt: 1_750_003_600 + int64(i),
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	names, err := s.repo.topNames(ctx, "user_id", "user_name", 0, false, 10)
	if err != nil {
		t.Fatalf("topNames: %v", err)
	}
	if len(names) != 1 {
		t.Fatalf("got %d rows, want 1", len(names))
	}
	if names[0].ID != "42" || names[0].Name != "Alice" || names[0].Plays != 3 {
		t.Errorf("row = %+v, want id=42 name=Alice plays=3", names[0])
	}
}

// TestPruneBandwidth verifies old bandwidth samples are deleted and recent ones kept.
func TestPruneBandwidth(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)

	_ = s.repo.insertBandwidth(ctx, 1_700_000_000, 100, 50, 50)  // old
	_ = s.repo.insertBandwidth(ctx, 1_750_000_000, 200, 80, 120) // recent
	n, err := s.repo.pruneBandwidth(ctx, 1_720_000_000)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	var remaining int
	if err := s.repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bandwidth_samples`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Errorf("remaining = %d, want 1", remaining)
	}
}

// TestImportSerializationGuard verifies the process-wide guard blocks a second concurrent import.
func TestImportSerializationGuard(t *testing.T) {
	s := newDataTestService(t)
	if !s.TryStartImport() {
		t.Fatal("first TryStartImport should succeed")
	}
	if s.TryStartImport() {
		s.StopImport()
		t.Fatal("second TryStartImport should fail while one is running")
	}
	s.StopImport()
	if !s.TryStartImport() {
		t.Fatal("TryStartImport should succeed after StopImport")
	}
	s.StopImport()
}
