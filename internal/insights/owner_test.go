package insights

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/settings"
)

// quietLog is a logger that goes nowhere — newDataTestService wires only the repo, and the
// merge path logs what it moved.
func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A Plex Media Server calls its own owner user id "1" in /status/sessions — a placeholder,
// not an account id. Tautulli and plex.tv both use the real account id, so the Users tab
// listed one person twice with their plays and watch time split between the rows.
func TestMergeOwnerSessionsFoldsThePlaceholder(t *testing.T) {
	s := newDataTestService(t)
	s.log = quietLog()
	ctx := context.Background()

	// The two halves of the same person: an imported history under the real account id,
	// and live-polled sessions under the server's placeholder.
	if err := s.repo.upsertUser(ctx, "8421", "RegularBloke", "avatar", 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.repo.upsertUser(ctx, ownerPlaceholderID, "RegularBloke", "", 2000); err != nil {
		t.Fatal(err)
	}
	for i, uid := range []string{"8421", "8421", ownerPlaceholderID} {
		if _, err := s.repo.insertSession(ctx, sessionRecord{
			SessionKey: string(rune('a' + i)), UserID: uid, UserName: "RegularBloke",
			StartedAt: 100, StoppedAt: 100 + 600, WatchedMS: 600 * 1000,
		}); err != nil {
			t.Fatal(err)
		}
	}

	s.mergeOwnerSessions(ctx, "8421", "RegularBloke", "avatar")

	users, err := s.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		names := make([]string, len(users))
		for i, u := range users {
			names[i] = u.ID + "/" + u.Username
		}
		t.Fatalf("got %d user rows %v, want 1 — the owner must not appear twice", len(users), names)
	}
	if users[0].ID != "8421" {
		t.Errorf("surviving id = %q, want the real account id 8421", users[0].ID)
	}
	if users[0].TotalPlays != 3 {
		t.Errorf("plays = %d, want 3 — all three sessions belong to one person", users[0].TotalPlays)
	}
	if want := int64(3 * 600); users[0].TotalSecs != want {
		t.Errorf("watch time = %ds, want %ds", users[0].TotalSecs, want)
	}

	// Idempotent: nothing is left under the placeholder, so a second run is a no-op.
	s.mergeOwnerSessions(ctx, "8421", "RegularBloke", "avatar")
	if again, err := s.Users(ctx); err != nil || len(again) != 1 {
		t.Errorf("second merge changed things: %d users, err %v", len(again), err)
	}
}

// The merge must never run on a guess. An unresolved owner leaves the id alone: a duplicate
// user row is a cosmetic annoyance, whereas merging the wrong two people is data loss.
func TestCanonicalUserIDLeavesIDsAloneWhenOwnerIsUnknown(t *testing.T) {
	s := newDataTestService(t)
	s.settings = settings.NewService(s.repo.db)
	ctx := context.Background()

	if got := s.canonicalUserID(ctx, ownerPlaceholderID); got != ownerPlaceholderID {
		t.Errorf("with no token, id = %q, want it untouched", got)
	}
	// A real account id is never rewritten, resolved owner or not.
	if got := s.canonicalUserID(ctx, "8421"); got != "8421" {
		t.Errorf("id = %q, want 8421 — only the placeholder is remapped", got)
	}

	// With the id cached, the placeholder maps onto it without any network call.
	if err := s.settings.Set(ctx, keyOwnerID, "8421"); err != nil {
		t.Fatal(err)
	}
	if got := s.canonicalUserID(ctx, ownerPlaceholderID); got != "8421" {
		t.Errorf("id = %q, want the cached owner id 8421", got)
	}
}

// Without a backoff, an unreachable plex.tv would be dialled on every poll — every five
// seconds by default — for as long as it stayed down.
func TestOwnerLookupBacksOff(t *testing.T) {
	s := &Service{}
	if !s.ownerRetryDue() {
		t.Fatal("the first lookup must be allowed")
	}
	if s.ownerRetryDue() {
		t.Error("a second lookup immediately after must be refused")
	}
	s.ownerLookupAt = time.Now().Add(-ownerLookupBackoff - time.Second)
	if !s.ownerRetryDue() {
		t.Error("a lookup past the backoff must be allowed again")
	}
}
