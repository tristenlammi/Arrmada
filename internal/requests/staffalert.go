package requests

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/notify"
	"github.com/tristenlammi/arrmada/internal/series"
)

// EventRequestCreated is the alert catalog's "New request" event.
const EventRequestCreated = "request.created"

// StaffAlerts is how a new request reaches staff, wired from main. Every field is
// optional; an unset one just skips that channel.
type StaffAlerts struct {
	// Emit queues a catalog alert for the admin's alert connections, exactly once per
	// dedupe key (notify.Service.EmitOnce).
	Emit func(ctx context.Context, key, dedupe string, data map[string]any) error
	// Staff lists the enabled managers and admins, who each get an inbox entry and a Web
	// Push.
	Staff func(ctx context.Context) ([]int64, error)
	// PushedByAlerts is who already gets the event as Web Push through an alert connection
	// ("This device"); they aren't pushed a second time here.
	PushedByAlerts func(ctx context.Context, key string) (map[int64]bool, error)
}

// SetStaffAlerts wires the "New request" alert, staff inbox and staff Web Push.
func (s *Service) SetStaffAlerts(a StaffAlerts) { s.staffAlerts = a }

// alertStaff tells staff about a new ask waiting for their approval: the admin's alert
// connections (Apprise, "This device"), and each manager's and admin's inbox and Web Push
// (the push only for those who haven't turned off "Someone requests something").
// Called only from announceCreated, once per new pending row or re-opened declined one.
//
// The dedupe key and the inbox reference name the ask: the row's creation time, plus how
// many times it has been asked for again after a decline, so a re-request alerts again
// while a repeat of the same ask can never queue twice.
func (s *Service) alertStaff(ctx context.Context, req Request) {
	created := parseSQLiteTime(req.CreatedAt)
	if created.IsZero() {
		created = time.Now()
	}
	ask := fmt.Sprintf("%d:%d", req.ID, created.Unix())
	if req.ReRequest > 0 {
		ask += fmt.Sprintf(":%d", req.ReRequest)
	}
	data := map[string]any{
		"id": req.ID, "media_type": req.MediaType, "tmdb_id": req.TMDBID, "title": req.Title, "year": req.Year,
		"requested_by_name": req.RequestedByName, "note": req.Note,
		"rerequest": req.ReRequest > 0, "decline_reason": req.DeclineReason,
	}
	if req.MediaType == "series" && len(req.Seasons) > 0 {
		data["seasons"] = series.SeasonsLabel(req.Seasons)
	}
	a := s.staffAlerts
	if a.Emit != nil {
		if err := a.Emit(ctx, EventRequestCreated, EventRequestCreated+":"+ask, data); err != nil {
			s.log.Warn("request: couldn't queue the new-request alert", "request", req.ID, "err", err)
		}
	}
	if a.Staff == nil {
		return
	}
	def, ok := notify.Lookup(EventRequestCreated)
	if !ok {
		return
	}
	m, ok := def.Format(data)
	if !ok {
		return
	}
	staff, err := a.Staff(ctx)
	if err != nil {
		s.log.Warn("request: couldn't list staff for the new-request notice", "request", req.ID, "err", err)
		return
	}
	pushed := map[int64]bool{}
	if a.PushedByAlerts != nil {
		if p, err := a.PushedByAlerts(ctx, EventRequestCreated); err == nil {
			pushed = p
		}
	}
	ref := "request:" + strings.Replace(ask, ":", ":new:", 1)
	now := time.Now().Unix()
	for _, uid := range staff {
		if uid <= 0 || uid == req.RequestedBy {
			continue // nobody is told about their own request
		}
		inserted, err := s.repo.addUserNotification(ctx, uid, m.Title, m.Body, req.MediaType, ref, now)
		if err != nil {
			s.log.Warn("request: couldn't add the new-request notice", "request", req.ID, "user", uid, "err", err)
			continue
		}
		// Their "Someone requests something" choice silences the push; the inbox keeps the
		// notice, as it does for every other choice.
		if inserted && s.push != nil && !pushed[uid] && s.wantsPush(ctx, uid, PrefNewRequest) {
			s.push.SendToUserAsync(uid, m.Title, m.Body, m.Link)
		}
	}
}
