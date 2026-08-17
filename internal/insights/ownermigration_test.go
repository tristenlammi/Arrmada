package insights

import (
	"context"
	"os"
	"testing"
)

// Migration 0073 repairs history recorded before the owner's real account id was known. It
// runs UPDATE and DELETE over the user's own data, so exercise the statements themselves
// rather than trusting that they parse.
//
// The migration has already run (on an empty DB, where it's a no-op) by the time a test
// store opens, so replay it against a seeded database.
func replayOwnerMerge(t *testing.T, s *Service) {
	t.Helper()
	sql, err := os.ReadFile("../store/migrations/0073_merge_owner_user.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := s.repo.db.ExecContext(context.Background(), string(sql)); err != nil {
		t.Fatalf("run migration: %v", err)
	}
}

func seedSession(t *testing.T, s *Service, key, userID string) {
	t.Helper()
	if _, err := s.repo.insertSession(context.Background(), sessionRecord{
		SessionKey: key, UserID: userID, UserName: "RegularBloke",
		StartedAt: 100, StoppedAt: 700, WatchedMS: 600 * 1000,
	}); err != nil {
		t.Fatal(err)
	}
}

// The case in the wild: imported history under the real account id, live-polled sessions
// under the server's "1" placeholder, one username across both.
func TestMigrationMergesTheOwnerByUsername(t *testing.T) {
	s := newDataTestService(t)
	ctx := context.Background()
	if err := s.repo.upsertUser(ctx, "8421", "RegularBloke", "avatar", 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.repo.upsertUser(ctx, ownerPlaceholderID, "RegularBloke", "", 5000); err != nil {
		t.Fatal(err)
	}
	seedSession(t, s, "a", "8421")
	seedSession(t, s, "b", ownerPlaceholderID)

	replayOwnerMerge(t, s)

	users, err := s.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].ID != "8421" {
		t.Fatalf("got %d users (first %+v), want one row under the real id", len(users), users)
	}
	if users[0].TotalPlays != 2 {
		t.Errorf("plays = %d, want 2 — both sessions are the same person", users[0].TotalPlays)
	}
	// The placeholder's last_seen was the more recent of the two; merging must not lose it.
	if users[0].LastSeen == 0 {
		t.Error("last seen was lost in the merge")
	}
}

// Nothing to pair with: the migration must leave the placeholder alone and wait for the
// runtime resolve, which knows the real id instead of inferring it from a name.
func TestMigrationLeavesALonePlaceholderAlone(t *testing.T) {
	s := newDataTestService(t)
	ctx := context.Background()
	if err := s.repo.upsertUser(ctx, ownerPlaceholderID, "RegularBloke", "", 5000); err != nil {
		t.Fatal(err)
	}
	seedSession(t, s, "a", ownerPlaceholderID)

	replayOwnerMerge(t, s)

	users, err := s.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].ID != ownerPlaceholderID {
		t.Fatalf("got %+v, want the placeholder untouched — there was nothing to merge into", users)
	}
	if users[0].TotalPlays != 1 {
		t.Errorf("plays = %d, want 1 — the session must not be orphaned", users[0].TotalPlays)
	}
}

// Two accounts sharing a username is the case where guessing would destroy data. The
// migration must decline rather than pick one.
func TestMigrationDeclinesAnAmbiguousMatch(t *testing.T) {
	s := newDataTestService(t)
	ctx := context.Background()
	for _, id := range []string{"8421", "9999", ownerPlaceholderID} {
		if err := s.repo.upsertUser(ctx, id, "RegularBloke", "", 1000); err != nil {
			t.Fatal(err)
		}
	}
	seedSession(t, s, "a", ownerPlaceholderID)

	replayOwnerMerge(t, s)

	users, err := s.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 3 {
		t.Fatalf("got %d users, want all 3 kept — an ambiguous name must not be merged on", len(users))
	}
	var placeholderPlays int
	for _, u := range users {
		if u.ID == ownerPlaceholderID {
			placeholderPlays = u.TotalPlays
		}
	}
	if placeholderPlays != 1 {
		t.Errorf("the placeholder's session moved somewhere on a guess (plays = %d)", placeholderPlays)
	}
}
