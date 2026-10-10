package notify

import (
	"context"
	"strings"
	"testing"
)

// The "New request" message names who asked for what, carries their note, says when it's
// asked again after a decline, and opens the request on the Requests page.
func TestRequestCreatedFormat(t *testing.T) {
	d := mustLookup(t, "request.created")
	if d.Group != GroupRequests || !d.DefaultOn || d.Topic != "" {
		t.Fatalf("request.created = group %q default %v topic %q; want requests, on, emitted directly", d.Group, d.DefaultOn, d.Topic)
	}
	m, ok := d.Format(samples["request.created"])
	if !ok || m.Title != "New request" || m.Link != "/requests?tab=needs&id=42" {
		t.Fatalf("message = %+v, %v", m, ok)
	}
	if want := "Sam requested Dune (2021). It's waiting for your approval.\nNote: “for movie night”"; m.Body != want {
		t.Errorf("body = %q, want %q", m.Body, want)
	}
	m, _ = d.Format(map[string]any{"id": 3, "title": "The Bear", "year": 2022, "seasons": "S1–2", "requested_by_name": "Ann", "rerequest": true, "decline_reason": "Not for now"})
	if !strings.HasPrefix(m.Body, "Ann asked again for S1–2 of The Bear (2022), which was declined before.") || !strings.Contains(m.Body, "Declined because: Not for now") {
		t.Errorf("re-request body = %q", m.Body)
	}
}

// PushUsersFor names the users whose enabled "This device" connection takes an event, so
// a producer that pushes to staff itself doesn't buzz them twice.
func TestPushUsersFor(t *testing.T) {
	s, _, _ := newTestService(t)
	ctx := context.Background()
	for _, c := range []Connection{
		{Name: "phone", Kind: KindWebPush, Config: PushConfigFor(5), Events: []string{"request.created"}, Enabled: true},
		{Name: "off", Kind: KindWebPush, Config: PushConfigFor(6), Events: []string{"request.created"}, Enabled: false},
		{Name: "other event", Kind: KindWebPush, Config: PushConfigFor(7), Events: []string{"book.imported"}, Enabled: true},
		{Name: "apprise", URL: "ntfy://a", Events: []string{"request.created"}, Enabled: true},
	} {
		if _, err := s.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.PushUsersFor(ctx, "request.created")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[5] {
		t.Errorf("PushUsersFor = %v, want only user 5", got)
	}
}
