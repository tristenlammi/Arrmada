// Package push delivers Web Push notifications to users' browsers and installed
// PWAs — the "ready to watch" ping on a phone, with no extra app.
//
// The server holds one VAPID keypair (generated on first use, persisted in
// settings); each browser/device that opts in registers a push subscription
// (endpoint + client keys). Payloads are encrypted per subscription by the
// webpush library. Delivery is best-effort over Google/Apple/Mozilla's push
// services; subscriptions those services report gone are pruned.
package push

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

const (
	keyPublic  = "webpush_vapid_public"
	keyPrivate = "webpush_vapid_private"
	sendTTL    = 6 * 3600 // seconds the push service may retry an offline device
)

// Settings is the tiny slice of the settings service this package needs.
type Settings interface {
	Get(ctx context.Context, key, def string) string
	Set(ctx context.Context, key, value string) error
}

// Service manages VAPID keys, subscriptions and sending.
type Service struct {
	db       *sql.DB
	settings Settings
	log      *slog.Logger
}

func New(db *sql.DB, settings Settings, log *slog.Logger) *Service {
	return &Service{db: db, settings: settings, log: log}
}

// PublicKey returns the VAPID public key the browser needs to subscribe,
// generating and persisting the pair on first use.
func (s *Service) PublicKey(ctx context.Context) (string, error) {
	if pub := s.settings.Get(ctx, keyPublic, ""); pub != "" {
		return pub, nil
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", err
	}
	if err := s.settings.Set(ctx, keyPrivate, priv); err != nil {
		return "", err
	}
	if err := s.settings.Set(ctx, keyPublic, pub); err != nil {
		return "", err
	}
	s.log.Info("push: generated VAPID keypair")
	return pub, nil
}

// Subscribe registers (or refreshes) one browser/device subscription for a user.
// The endpoint is unique per subscription: re-subscribing after a permission
// round-trip simply re-points the row (including to a different user on a shared
// device — last sign-in wins, which is what a shared browser should do).
func (s *Service) Subscribe(ctx context.Context, userID int64, endpoint, p256dh, auth string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth) VALUES (?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET user_id=excluded.user_id, p256dh=excluded.p256dh, auth=excluded.auth`,
		userID, endpoint, p256dh, auth)
	return err
}

// Unsubscribe removes a subscription. Scoped to the user so one account can't
// silence another's device.
func (s *Service) Unsubscribe(ctx context.Context, userID int64, endpoint string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE user_id = ? AND endpoint = ?`, userID, endpoint)
	return err
}

// HasSubscription reports whether the user has this endpoint registered (the UI
// uses it to show the toggle's real state).
func (s *Service) HasSubscription(ctx context.Context, userID int64, endpoint string) bool {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM push_subscriptions WHERE user_id = ? AND endpoint = ? LIMIT 1`, userID, endpoint).Scan(&one)
	return err == nil
}

// Payload is what the service worker receives and renders.
type Payload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"` // where a tap takes the user
}

// SendToUser pushes to every device the user has registered. Failures are logged,
// never returned — a dead push service must not fail the caller's flow — and
// subscriptions the push service says are gone (404/410) are pruned.
func (s *Service) SendToUser(ctx context.Context, userID int64, title, body, url string) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT endpoint, p256dh, auth FROM push_subscriptions WHERE user_id = ?`, userID)
	if err != nil {
		return
	}
	type sub struct{ endpoint, p256dh, auth string }
	var subs []sub
	for rows.Next() {
		var x sub
		if rows.Scan(&x.endpoint, &x.p256dh, &x.auth) == nil {
			subs = append(subs, x)
		}
	}
	rows.Close()
	if len(subs) == 0 {
		return
	}

	priv := s.settings.Get(ctx, keyPrivate, "")
	pub := s.settings.Get(ctx, keyPublic, "")
	if priv == "" || pub == "" {
		return // no keys yet → nobody could have subscribed anyway
	}
	msg, err := json.Marshal(Payload{Title: title, Body: body, URL: url})
	if err != nil {
		return
	}
	for _, x := range subs {
		resp, err := webpush.SendNotification(msg, &webpush.Subscription{
			Endpoint: x.endpoint,
			Keys:     webpush.Keys{P256dh: x.p256dh, Auth: x.auth},
		}, &webpush.Options{
			Subscriber:      "https://github.com/tristenlammi/Arrmada",
			VAPIDPublicKey:  pub,
			VAPIDPrivateKey: priv,
			TTL:             sendTTL,
			Urgency:         webpush.UrgencyNormal,
		})
		if err != nil {
			s.log.Debug("push: send failed", "err", err)
			continue
		}
		if resp.StatusCode == 404 || resp.StatusCode == 410 {
			// The browser revoked the subscription (uninstalled PWA, cleared
			// site data) — the push service says it's gone for good.
			_, _ = s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint = ?`, x.endpoint)
		}
		resp.Body.Close()
	}
}

// timeoutCtx bounds a send batch kicked off from an event handler.
func timeoutCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// SendToUserAsync fires the sends on a goroutine with its own deadline — event
// handlers (import fan-out) must never block on push services.
func (s *Service) SendToUserAsync(userID int64, title, body, url string) {
	go func() {
		ctx, cancel := timeoutCtx()
		defer cancel()
		s.SendToUser(ctx, userID, title, body, url)
	}()
}
