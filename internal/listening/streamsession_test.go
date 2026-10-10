package listening

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A stream link works for an open, recently used session only: not once it's closed or
// idle too long, never for an offline upload, and never for an id it wasn't given.
func TestStreamSession(t *testing.T) {
	s, _, uid, clock := testStore(t)
	ctx := context.Background()
	sess, _, err := s.OpenSession(ctx, uid, "b1", "dev1", "iPhone", "Audiobookshelf")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.StreamSession(ctx, sess.ID, 48*time.Hour)
	if err != nil || got.UserID != uid || got.ItemKey != "b1" {
		t.Fatalf("open session: %+v %v", got, err)
	}
	if _, err := s.StreamSession(ctx, "00000000-0000-4000-8000-000000000000", 48*time.Hour); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("unknown id: %v", err)
	}

	*clock = clock.Add(49 * time.Hour)
	if _, err := s.StreamSession(ctx, sess.ID, 48*time.Hour); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("idle session: %v", err)
	}
	// Playing again (a sync) brings it back within the window.
	if _, err := s.Sync(ctx, uid, sess.ID, at(30), 5, 3600, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamSession(ctx, sess.ID, 48*time.Hour); err != nil {
		t.Fatalf("after a sync: %v", err)
	}
	if _, err := s.Sync(ctx, uid, sess.ID, at(40), 5, 3600, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamSession(ctx, sess.ID, 48*time.Hour); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("closed session: %v", err)
	}

	if _, err := s.SyncOffline(ctx, uid, OfflineSession{ID: "off-1", ItemKey: "b1", Position: 50, Duration: 3600,
		StartedAt: clock.UnixMilli(), UpdatedAt: clock.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamSession(ctx, "off-1", 48*time.Hour); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("offline upload: %v", err)
	}
}
