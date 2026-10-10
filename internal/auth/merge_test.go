package auth

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// mergeFixture: an admin with no Plex link, a duplicate Plex requester (plex id 4242) who
// requested, follows, has an inbox, a phone, quota usage, audiobook places and a password,
// and a third person whose request both of them follow.
type mergeFixture struct {
	s                *Service
	db               *sql.DB
	admin, dup, kid  *User
	dupReq, adminReq int64
}

func newMergeFixture(t *testing.T) *mergeFixture {
	t.Helper()
	ctx := context.Background()
	s := newService(t)
	f := &mergeFixture{s: s, db: s.db}
	f.admin, _ = s.CreateUser(ctx, "owner", "supersecret", RoleAdmin, false)
	f.dup, _ = s.FindOrCreatePlexUser(ctx, "4242", "ownerplex", RoleRequester, AutoApproval{})
	f.kid, _ = s.CreateUser(ctx, "kid@example.com", "supersecret", RoleRequester, false)
	exec := func(q string, args ...any) int64 {
		t.Helper()
		res, err := f.db.Exec(q, args...)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	f.dupReq = exec(`INSERT INTO requests (media_type, tmdb_id, title, requested_by, requested_by_name) VALUES ('movie', 1, 'Dup asked', ?, 'ownerplex')`, f.dup.ID)
	f.adminReq = exec(`INSERT INTO requests (media_type, tmdb_id, title, requested_by, requested_by_name) VALUES ('movie', 2, 'Admin asked', ?, 'owner')`, f.admin.ID)
	kidReq := exec(`INSERT INTO requests (media_type, tmdb_id, title, requested_by, requested_by_name) VALUES ('movie', 3, 'Kid asked', ?, 'kid')`, f.kid.ID)
	exec(`INSERT INTO request_subscribers (request_id, user_id, user_name) VALUES (?, ?, 'ownerplex')`, f.adminReq, f.dup.ID) // follows what the admin owns
	exec(`INSERT INTO request_subscribers (request_id, user_id, user_name) VALUES (?, ?, 'ownerplex')`, kidReq, f.dup.ID)
	exec(`INSERT INTO request_subscribers (request_id, user_id, user_name) VALUES (?, ?, 'owner')`, kidReq, f.admin.ID) // both follow
	exec(`INSERT INTO user_notifications (user_id, title, ref) VALUES (?, 'ready', 'movie:1')`, f.dup.ID)
	exec(`INSERT INTO user_notifications (user_id, title, ref) VALUES (?, 'ready', 'movie:3')`, f.dup.ID)
	exec(`INSERT INTO user_notifications (user_id, title, ref) VALUES (?, 'ready', 'movie:3')`, f.admin.ID)
	exec(`INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth) VALUES (?, 'https://push.example/1', 'k', 'a')`, f.dup.ID)
	exec(`INSERT INTO request_usage (user_id, request_id, kind, units, created_at) VALUES (?, ?, 'movie', 1, 1)`, f.dup.ID, f.dupReq)
	exec(`INSERT INTO listen_progress (user_id, item_key, position, updated_at) VALUES (?, 'b1', 5, 1)`, f.dup.ID)
	exec(`INSERT INTO listen_progress (user_id, item_key, position, updated_at) VALUES (?, 'b2', 7, 1)`, f.dup.ID)
	exec(`INSERT INTO listen_progress (user_id, item_key, position, updated_at) VALUES (?, 'b1', 100, 1)`, f.admin.ID)
	exec(`INSERT INTO listen_log (session_id, user_id, started_at, ended_at, seconds) VALUES ('s1', ?, 1, 2, 60)`, f.dup.ID)
	exec(`INSERT INTO audio_passwords (user_id, hash, updated_at) VALUES (?, 'dup-hash', 1)`, f.dup.ID)
	if _, _, err := s.CreateSession(ctx, f.dup.ID); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *mergeFixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestMergePlexDuplicateMovesRequests(t *testing.T) {
	ctx := context.Background()
	f := newMergeFixture(t)

	p, err := f.s.PlexMergePreview(ctx, f.admin.ID, f.dup.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := MergePreview{From: "ownerplex", To: "owner", PlexUsername: "ownerplex", Requests: 1, Following: 2, Notifications: 2, PushDevices: 1, QuotaUsage: 1, AudiobookProgress: true, AudiobookPassword: true, SignedInDevices: 1}
	if p != want {
		t.Errorf("preview = %+v\nwant      %+v", p, want)
	}

	if err := f.s.MergePlexDuplicate(ctx, f.admin.ID, f.dup.ID); err != nil {
		t.Fatal(err)
	}
	a := f.admin.ID
	checks := []struct {
		name string
		q    string
		args []any
		want int
	}{
		{"dup's request is the admin's, under the admin's name", `SELECT COUNT(*) FROM requests WHERE id = ? AND requested_by = ? AND requested_by_name = 'owner'`, []any{f.dupReq, a}, 1},
		{"no follow of a request the admin owns", `SELECT COUNT(*) FROM request_subscribers WHERE request_id = ?`, []any{f.adminReq}, 0},
		{"one follow of the kid's request", `SELECT COUNT(*) FROM request_subscribers WHERE user_id = ?`, []any{a}, 1},
		{"inbox de-duplicated", `SELECT COUNT(*) FROM user_notifications WHERE user_id = ?`, []any{a}, 2},
		{"push device moved", `SELECT COUNT(*) FROM push_subscriptions WHERE user_id = ?`, []any{a}, 1},
		{"quota usage moved", `SELECT COUNT(*) FROM request_usage WHERE user_id = ?`, []any{a}, 1},
		{"admin keeps its own place in b1", `SELECT COUNT(*) FROM listen_progress WHERE user_id = ? AND item_key = 'b1' AND position = 100`, []any{a}, 1},
		{"admin gains the place in b2", `SELECT COUNT(*) FROM listen_progress WHERE user_id = ? AND item_key = 'b2'`, []any{a}, 1},
		{"listening log moved", `SELECT COUNT(*) FROM listen_log WHERE user_id = ?`, []any{a}, 1},
		{"audiobook password moved (the admin had none)", `SELECT COUNT(*) FROM audio_passwords WHERE user_id = ? AND hash = 'dup-hash'`, []any{a}, 1},
		{"duplicate gone", `SELECT COUNT(*) FROM users WHERE id = ?`, []any{f.dup.ID}, 0},
		{"its sessions signed out", `SELECT COUNT(*) FROM sessions WHERE user_id = ?`, []any{f.dup.ID}, 0},
		{"nothing left under the duplicate", `SELECT (SELECT COUNT(*) FROM user_notifications WHERE user_id = ?) + (SELECT COUNT(*) FROM request_subscribers WHERE user_id = ?)`, []any{f.dup.ID, f.dup.ID}, 0},
	}
	for _, c := range checks {
		if got := f.count(t, c.q, c.args...); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
	if l, _ := f.s.PlexLinkFor(ctx, a); !l.Linked || l.PlexUsername != "ownerplex" || f.s.PlexIDForUser(ctx, a) != "4242" {
		t.Errorf("Plex link after merge: %+v (id %q)", l, f.s.PlexIDForUser(ctx, a))
	}
}

// An injected failure part-way (a table gone) leaves both accounts untouched.
func TestMergeRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	f := newMergeFixture(t)
	if _, err := f.db.Exec(`DROP TABLE push_subscriptions`); err != nil {
		t.Fatal(err)
	}
	if err := f.s.MergePlexDuplicate(ctx, f.admin.ID, f.dup.ID); err == nil {
		t.Fatal("merge succeeded without push_subscriptions")
	}
	if got := f.count(t, `SELECT COUNT(*) FROM requests WHERE requested_by = ?`, f.dup.ID); got != 1 {
		t.Errorf("duplicate's requests = %d, want 1 (rolled back)", got)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM request_subscribers WHERE user_id = ?`, f.dup.ID); got != 2 {
		t.Errorf("duplicate's follows = %d, want 2", got)
	}
	if f.s.PlexIDForUser(ctx, f.dup.ID) != "4242" || f.s.PlexIDForUser(ctx, f.admin.ID) != "" {
		t.Error("the Plex link moved despite the failure")
	}
	if _, err := f.s.UserByID(ctx, f.dup.ID); err != nil {
		t.Error("the duplicate was deleted despite the failure")
	}
}

// Only a non-staff Plex-linked account merges, into a different account with no link.
func TestMergeRefusals(t *testing.T) {
	ctx := context.Background()
	f := newMergeFixture(t)
	mgr, _ := f.s.CreateUser(ctx, "mgr@example.com", "supersecret", RoleManager, false)
	_ = f.s.LinkPlex(ctx, mgr.ID, "555", "mgrplex")
	var refused *ErrMergeRefused
	for name, c := range map[string][2]int64{
		"into itself":            {f.dup.ID, f.dup.ID},
		"from an unlinked user":  {f.admin.ID, f.kid.ID},
		"from a staff account":   {f.admin.ID, mgr.ID},
		"into an already linked": {mgr.ID, f.dup.ID},
	} {
		if err := f.s.MergePlexDuplicate(ctx, c[0], c[1]); !errors.As(err, &refused) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	if err := f.s.MergePlexDuplicate(ctx, f.admin.ID, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}
