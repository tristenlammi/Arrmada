package httpapi

import (
	"net/http"
	"strings"
)

// Web Push subscription endpoints. All operate on the signed-in user's own
// subscriptions; the VAPID public key is not a secret (the browser needs it).

// handlePushKey returns the server's VAPID public key, generating the pair on
// first use. An empty key response means push isn't available (e.g. key
// generation failed) and the UI hides the toggle.
func (a *api) handlePushKey(w http.ResponseWriter, r *http.Request) {
	if a.deps.Push == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"key": ""})
		return
	}
	key, err := a.deps.Push.PublicKey(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not prepare push keys")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"key": key})
}

type pushSubscribeReq struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (a *api) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if a.deps.Push == nil {
		a.writeError(w, http.StatusServiceUnavailable, "push is not available")
		return
	}
	var req pushSubscribeReq
	if !a.decodeJSON(w, r, &req) {
		return
	}
	// The endpoint must be an HTTPS push-service URL; the keys are opaque base64.
	if !strings.HasPrefix(req.Endpoint, "https://") || req.Keys.P256dh == "" || req.Keys.Auth == "" {
		a.writeError(w, http.StatusBadRequest, "invalid push subscription")
		return
	}
	if err := a.deps.Push.Subscribe(r.Context(), u.ID, req.Endpoint, req.Keys.P256dh, req.Keys.Auth); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not save the subscription")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"subscribed": true})
}

func (a *api) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if a.deps.Push == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"subscribed": false})
		return
	}
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Push.Unsubscribe(r.Context(), u.ID, req.Endpoint); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not remove the subscription")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"subscribed": false})
}
