package eventbus

import "strconv"

// Topics the UI listens for instead of polling. Who receives each is decided by the
// realtime policy (internal/realtime/policy.go): a plain topic is staff-only, and a
// user.<id>.* topic goes to that one user. So:
//   - queue.progress is staff-only. It carries hashes and progress numbers, never titles.
//   - request.updated is staff-only, with ids and status (no titles).
//   - each requester (and subscriber) hears about their own request on
//     user.<id>.request.updated, with the request's id, status and media type only.
const (
	TopicQueueProgress  = "queue.progress"
	TopicRequestUpdated = "request.updated"
)

// UserTopic is the per-user form of an event, delivered to that user alone.
func UserTopic(userID int64, event string) string {
	return "user." + strconv.FormatInt(userID, 10) + "." + event
}
