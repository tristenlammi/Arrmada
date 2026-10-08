package realtime

import (
	"strconv"
	"strings"
)

// Viewer is who is on the other end of a websocket, as far as the topic policy cares.
type Viewer struct {
	UserID int64
	Staff  bool // manager or admin
}

// heartbeatTopic is the one topic every signed-in client gets: the UI's "connected" dot.
const heartbeatTopic = "server.heartbeat"

// allowed decides whether a viewer receives an event. The bus carries plenty a family
// account must not see — who is streaming what on Plex (managers only everywhere else),
// grabs, imports, file paths — and the hub used to send all of it to every socket.
//
// The rule:
//   - server.heartbeat goes to everyone;
//   - user.<id>.* goes to that user only. Not to staff either: these are personal
//     (request progress today), and anything touching what someone listens to must
//     never reach an admin;
//   - every other topic is staff-only.
//
// So a new topic is staff-only unless it's added here. Per-user events must be
// published as user.<id>.<what>.
func allowed(topic string, v Viewer) bool {
	if topic == heartbeatTopic {
		return true
	}
	if strings.HasPrefix(topic, "user.") {
		return strings.HasPrefix(topic, "user."+strconv.FormatInt(v.UserID, 10)+".")
	}
	return v.Staff
}
