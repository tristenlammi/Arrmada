package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tristenlammi/arrmada/internal/attention"
	"github.com/tristenlammi/arrmada/internal/auth"
)

// oneReview is an attention source with a single held review.
type oneReview struct{}

func (oneReview) Name() string { return "reviews" }
func (oneReview) Collect(context.Context, *attention.Frame) ([]attention.Item, error) {
	return []attention.Item{{Key: "review:1", Kind: attention.KindReview, Title: "Dune is held", Link: "/review"}}, nil
}

// The Needs-you feed is staff-only: a requester's shell shows none of these counts, so
// the endpoint refuses them outright. Staff get the snapshot as it stands.
func TestAttentionIsStaffOnly(t *testing.T) {
	svc := attention.New(nil, nil, nil, oneReview{})
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := newRouteServer(t, func(d *Deps) { d.Attention = svc })
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)

	if rec := s.do("GET", "/api/v1/attention", kid); rec.Code != http.StatusForbidden {
		t.Fatalf("requester: HTTP %d, want 403", rec.Code)
	}
	if rec := s.do("GET", "/api/v1/attention", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: HTTP %d, want 401", rec.Code)
	}
	rec := s.do("GET", "/api/v1/attention", mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager: HTTP %d: %s", rec.Code, rec.Body)
	}
	var got attentionPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.At == nil || got.Stale || got.Counts.Reviews != 1 || got.Counts.Total != 1 || len(got.Items) != 1 || len(got.Groups) != 1 {
		t.Errorf("payload = %s", rec.Body)
	}
	if got.Items[0].Key != "review:1" || got.Groups[0].Title != "1 import needs review" {
		t.Errorf("payload = %s", rec.Body)
	}
}

// Before the first refresh (or with no feed wired) the answer is an empty, stale snapshot
// with arrays, never null.
func TestAttentionBeforeFirstRefresh(t *testing.T) {
	s := newRouteServer(t, nil)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	rec := s.do("GET", "/api/v1/attention", mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["stale"] != true || got["at"] != nil || got["items"] == nil || got["groups"] == nil {
		t.Errorf("payload = %s", rec.Body)
	}
}
