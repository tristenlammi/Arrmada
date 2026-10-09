package requests

import (
	"context"

	"github.com/tristenlammi/arrmada/internal/eventbus"
)

// Statuses request.updated carries besides the stored ones.
const (
	eventDeleted   = "deleted"   // withdrawn or removed
	eventAvailable = "available" // ready in the library
)

// parties is everyone a request belongs to: its requester and each subscriber. A
// subscriber list that can't be read leaves just the requester — a missed live update
// only costs a poll.
func (s *Service) parties(ctx context.Context, req Request) []int64 {
	seen := map[int64]bool{}
	var out []int64
	add := func(uid int64) {
		if uid > 0 && !seen[uid] {
			seen[uid] = true
			out = append(out, uid)
		}
	}
	add(req.RequestedBy)
	if subs, err := s.repo.Subscribers(ctx, req.ID); err == nil {
		for _, sub := range subs {
			add(sub.UserID)
		}
	}
	return out
}

// publishUpdated tells open pages a request changed, so they refresh instead of polling:
// staff on request.updated, and each of users on their own user.<id>.request.updated.
// Ids and status only — no titles — and a requester never hears about anyone else's
// request (the realtime policy delivers a user topic to that user alone).
func (s *Service) publishUpdated(req Request, status string, users []int64) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(eventbus.TopicRequestUpdated, map[string]any{
		"id": req.ID, "status": status, "media_type": req.MediaType, "requested_by": req.RequestedBy,
	})
	for _, uid := range users {
		s.bus.Publish(eventbus.UserTopic(uid, "request.updated"), map[string]any{
			"id": req.ID, "status": status, "media_type": req.MediaType,
		})
	}
}
