package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/tristenlammi/arrmada/internal/realtime"
)

// handleWS upgrades the connection to a websocket and streams broadcast events
// (fed by the event bus via the realtime hub) until the client disconnects.
func (a *api) handleWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		a.deps.Log.Debug("websocket accept failed", "err", err)
		return
	}
	defer c.CloseNow()

	// What the socket carries depends on who opened it: staff get every topic, other
	// accounts only the heartbeat and their own user.<id>.* events (realtime/policy.go).
	u, _ := userFrom(r)
	client := a.deps.Realtime.Connect(realtime.Viewer{UserID: u.ID, Staff: isStaffRequest(r)})
	defer a.deps.Realtime.Disconnect(client)

	// CloseRead drains inbound frames, answers pings, and cancels ctx on close.
	ctx := c.CloseRead(r.Context())

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-client.Send():
			if !ok {
				return
			}
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
