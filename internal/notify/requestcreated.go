package notify

import (
	"context"
	"fmt"
	"strings"
)

// formatRequestCreated writes the "New request" alert. The payload comes from requests
// (announceCreated): id, title, year, seasons (a label such as "S1–2", "" for a whole show
// or anything else), requested_by_name, note and rerequest. The same words go to staff
// inboxes and their Web Push, so everyone reads one message.
func formatRequestCreated(d map[string]any) (Message, bool) {
	title := withYear(str(d, "title"), num(d, "year"))
	if title == "" {
		return Message{}, false
	}
	what := title
	if s := str(d, "seasons"); s != "" {
		what = s + " of " + title
	}
	who := str(d, "requested_by_name")
	if who == "" {
		who = "Someone"
	}
	var b strings.Builder
	if again, _ := d["rerequest"].(bool); again {
		fmt.Fprintf(&b, "%s asked again for %s, which was declined before. It's waiting for your approval.", who, what)
		if why := str(d, "decline_reason"); why != "" {
			fmt.Fprintf(&b, " Declined because: %s", why)
		}
	} else {
		fmt.Fprintf(&b, "%s requested %s. It's waiting for your approval.", who, what)
	}
	if note := strings.TrimSpace(str(d, "note")); note != "" {
		fmt.Fprintf(&b, "\nNote: “%s”", note)
	}
	m := Message{Title: "New request", Body: b.String(), Link: "/requests?tab=needs"}
	if id := num(d, "id"); id > 0 {
		m.Link = fmt.Sprintf("/requests?tab=needs&id=%d", id)
	}
	return m, true
}

// PushUsersFor is who already gets an event as Web Push through an enabled "This device"
// connection subscribed to it, by user id. A producer that also pushes to staff directly
// (a new request) skips them, so nobody's phone buzzes twice for one thing.
func (s *Service) PushUsersFor(ctx context.Context, key string) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT n.config FROM notifications n
		 JOIN notification_subscriptions sub ON sub.connection_id = n.id AND sub.event_key = ?
		 WHERE n.enabled != 0 AND n.kind = ?`, key, KindWebPush)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var config string
		if err := rows.Scan(&config); err != nil {
			return nil, err
		}
		if uid := (Connection{Config: []byte(config)}).PushUserID(); uid > 0 {
			out[uid] = true
		}
	}
	return out, rows.Err()
}
