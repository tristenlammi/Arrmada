package requests

import (
	"context"
	"slices"
	"testing"
)

func seedUser(t *testing.T, s *Service, id int64, name, prefs, apprise string) {
	t.Helper()
	if _, err := s.repo.db.Exec(`INSERT INTO users (id, username, role, password_hash, notify_prefs, apprise_url) VALUES (?, ?, 'requester', 'x', ?, ?)`, id, name, prefs, apprise); err != nil {
		t.Fatalf("seed user %d: %v", id, err)
	}
}

// Empty, broken or partial choices mean "on" for everything they don't turn off: nobody
// goes quiet because of a bad value.
func TestNotifyPrefsDefaultsTrue(t *testing.T) {
	for _, raw := range []string{"", "garbage", "null", "[]", `{"approved":"no"}`} {
		p := parseNotifyPrefs(raw)
		for _, k := range NotifyPrefKeys {
			if !p.Wants(k) {
				t.Errorf("parse(%q).Wants(%q) = false, want true", raw, k)
			}
		}
	}
	p := parseNotifyPrefs(`{"approved":false}`)
	if p.Wants(PrefApproved) || !p.Wants(PrefReady) || !p.Wants("some-later-key") {
		t.Errorf("partial prefs = %v: want approved off, the rest on", p.Full())
	}
}

// Turning "approved" off stops that person's approved pushes and Apprise messages, while
// their inbox still records the notice; someone with no choices gets everything.
func TestNotifyPrefsGateDelivery(t *testing.T) {
	s, push, _ := quietFixture(t)
	var apprised []string
	s.userApprise.resolver = fixedResolver{"push.example.com": "93.184.216.34"}
	s.userApprise.send = func(_ context.Context, url, _, _ string) error { apprised = append(apprised, url); return nil }
	seedUser(t, s, 7, "quiet", `{"approved":false}`, "gotify://push.example.com/seven")
	seedUser(t, s, 8, "loud", "", "gotify://push.example.com/eight")
	ctx := context.Background()

	for i, uid := range []int64{7, 8} {
		req := Request{ID: int64(i + 1), MediaType: "movie", TMDBID: 100 + i, Title: "Dune", RequestedBy: uid, DecidedAt: 5}
		s.notifyDecision(ctx, req, true, nil)
		if refs := inboxRefs(t, s, uid); len(refs) != 1 {
			t.Errorf("user %d inbox = %v, want the approved notice", uid, refs)
		}
	}
	if got := push.sent(); !slices.Equal(got, []int64{8}) {
		t.Errorf("pushed to %v, want only user 8", got)
	}
	if !slices.Equal(apprised, []string{"gotify://push.example.com/eight"}) {
		t.Errorf("apprise sends = %v, want only user 8's", apprised)
	}

	// Their "ready" choice is still on: a ready notice reaches them.
	req := Request{ID: 3, MediaType: "movie", TMDBID: 300, Title: "Heat", RequestedBy: 7}
	if _, err := s.notifyPartiesCount(ctx, req, "Your request is ready", "“Heat” is ready to watch.", requestRef(req), "request-ready", nil); err != nil {
		t.Fatal(err)
	}
	if got := push.sent(); !slices.Equal(got, []int64{8, 7}) {
		t.Errorf("after a ready notice pushed to %v, want user 7 too", got)
	}
}

// A notice kind added later answers to no choice, so it delivers even to someone who
// turned every known one off.
func TestNotifyPrefsUnknownKindDelivers(t *testing.T) {
	s, push, _ := quietFixture(t)
	seedUser(t, s, 7, "quiet", `{"approved":false,"declined":false,"ready":false,"new_request":false}`, "")
	if prefKey("request-something-new") != "" {
		t.Fatal("an unknown kind maps to a choice")
	}
	req := Request{ID: 1, MediaType: "movie", TMDBID: 1, Title: "Dune", RequestedBy: 7}
	if _, err := s.notifyPartiesCount(context.Background(), req, "News", "Something new", "movie:1:news", "request-something-new", nil); err != nil {
		t.Fatal(err)
	}
	if got := push.sent(); !slices.Equal(got, []int64{7}) {
		t.Errorf("pushed to %v, want user 7", got)
	}
	// A season notice answers to "ready", like the final one.
	if prefKey("request-season-ready") != PrefReady {
		t.Error("a season-ready notice doesn't follow the ready choice")
	}
}

// Setting choices changes only the known keys named; the rest keep their value, and a
// missing users row (no account to store them on) reads as all on.
func TestSetNotifyPrefsKeepsOthers(t *testing.T) {
	s, _, _ := quietFixture(t)
	seedUser(t, s, 7, "kid", "", "")
	ctx := context.Background()
	if _, err := s.SetNotifyPrefs(ctx, 7, map[string]bool{"approved": false, "bogus": true}); err != nil {
		t.Fatal(err)
	}
	p, err := s.SetNotifyPrefs(ctx, 7, map[string]bool{"ready": false})
	if err != nil {
		t.Fatal(err)
	}
	want := NotifyPrefs{PrefApproved: false, PrefDeclined: true, PrefReady: false, PrefNewRequest: true}
	if len(p) != len(want) {
		t.Fatalf("prefs = %v, want %v", p, want)
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("%s = %v, want %v", k, p[k], v)
		}
	}
	var raw string
	_ = s.repo.db.QueryRow(`SELECT notify_prefs FROM users WHERE id = 7`).Scan(&raw)
	if raw != `{"approved":false,"ready":false}` {
		t.Errorf("stored %s, want only the two choices made (unknown keys dropped)", raw)
	}
	if p, err := s.NotifyPrefs(ctx, 999); err != nil || !p.Wants(PrefReady) || len(p) != len(NotifyPrefKeys) {
		t.Errorf("no users row: %v, %v; want every key on", p, err)
	}
}

// A staff member who turned off "Someone requests something" still gets the new request
// in their inbox, but no push; the others are pushed as before.
func TestStaffNewRequestPrefSilencesPush(t *testing.T) {
	s, push, _ := staffAlertFixture(t, []int64{1, 2}, nil)
	seedUser(t, s, 1, "boss", `{"new_request":false}`, "")
	seedUser(t, s, 2, "mate", "", "")
	ctx := context.Background()
	if _, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 438631, Title: "Dune", RequestedBy: 7, RequestedByName: "kid"}, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []int64{1, 2} {
		if refs := inboxRefs(t, s, uid); len(refs) != 1 {
			t.Errorf("staff %d inbox = %v, want the new request", uid, refs)
		}
	}
	if got := push.sent(); !slices.Equal(got, []int64{2}) {
		t.Errorf("pushed to %v, want only staff 2", got)
	}
}
