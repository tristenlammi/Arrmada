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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/tristenlammi/arrmada/internal/netsafe"
	"github.com/tristenlammi/arrmada/internal/safego"
	"github.com/tristenlammi/arrmada/internal/store"
)

const (
	keyPublic  = "webpush_vapid_public"
	keyPrivate = "webpush_vapid_private"
	sendTTL    = 6 * 3600 // seconds the push service may retry an offline device
	// MaxPerUser is how many devices one account keeps subscribed; a new one past it
	// drops that account's oldest.
	MaxPerUser = 10
	// sendTimeout bounds one push to one device.
	sendTimeout = 15 * time.Second
)

// A subscription's endpoint is a URL the browser hands over, and the server then POSTs to
// it. Taken as given, any signed-in account could make Arrmada post to any address, the
// LAN included. So only the browsers' own push services are accepted, on subscribe and
// again before every send (rows saved before this check are covered too): Chrome, Edge,
// Opera, Samsung Internet and Brave use Firebase Cloud Messaging, Firefox Mozilla's
// autopush, Safari Apple's, and legacy Edge Windows' notification service.
var (
	pushHosts        = map[string]bool{"fcm.googleapis.com": true, "android.googleapis.com": true}
	pushHostSuffixes = []string{".push.services.mozilla.com", ".push.apple.com", ".notify.windows.com"}
)

// ErrBadEndpoint is a subscription whose endpoint isn't a browser push service.
var ErrBadEndpoint = errors.New("that isn't a browser push service address")

// ValidEndpoint checks a subscription endpoint: https on the standard port, no login in
// it, at one of the browsers' push services.
func ValidEndpoint(endpoint string) error {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return ErrBadEndpoint
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if pushHosts[host] {
		return nil
	}
	// Chrome sometimes hands out an FCM endpoint on another Google host
	// (https://jmt17.google.com/fcm/send/…): taken for FCM's own path only.
	if p := u.EscapedPath(); strings.HasSuffix(host, ".google.com") && strings.HasPrefix(p, "/fcm/") && !strings.Contains(p, "..") {
		return nil
	}
	for _, sfx := range pushHostSuffixes {
		if strings.HasSuffix(host, sfx) {
			return nil
		}
	}
	return ErrBadEndpoint
}

// pushClient sends the pushes: public addresses only, a bounded wait, and no redirects
// (a push service answers; it never hands the message on).
var pushClient = &http.Client{
	Timeout:       sendTimeout,
	Transport:     netsafe.PublicTransport(),
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Settings is the tiny slice of the settings service this package needs.
type Settings interface {
	Get(ctx context.Context, key, def string) string
	Set(ctx context.Context, key, value string) error
}

// Service manages VAPID keys, subscriptions and sending.
type Service struct {
	db       store.Pool
	settings Settings
	log      *slog.Logger
	// httpClient, when set, replaces pushClient (tests).
	httpClient webpush.HTTPClient
}

func (s *Service) client() webpush.HTTPClient {
	if s.httpClient != nil {
		return s.httpClient
	}
	return pushClient
}

func New(db store.Pool, settings Settings, log *slog.Logger) *Service {
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
//
// Only a browser push service's endpoint is taken (ErrBadEndpoint), and an account keeps
// at most MaxPerUser devices: subscribing one more drops its oldest.
func (s *Service) Subscribe(ctx context.Context, userID int64, endpoint, p256dh, auth string) error {
	if err := ValidEndpoint(endpoint); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth) VALUES (?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET user_id=excluded.user_id, p256dh=excluded.p256dh, auth=excluded.auth`,
		userID, endpoint, p256dh, auth); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM push_subscriptions WHERE user_id = ? AND id NOT IN (
			SELECT id FROM push_subscriptions WHERE user_id = ? ORDER BY id DESC LIMIT ?)`,
		userID, userID, MaxPerUser)
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
	// PlexURL, on a 'ready' notice for a title the owner's Plex has, is the title's
	// app.plex.tv page: the worker offers "Watch on Plex" and a plain tap opens it.
	PlexURL string `json:"plex_url,omitempty"`
	// WatchURL and WatchLabel, on a 'ready' notice for a title a media server has, are
	// the reader's own server's page for it and that server's name ("Jellyfin"): the
	// worker offers "Watch on <label>" and a plain tap opens it. Workers from before
	// Jellyfin read plex_url instead.
	WatchURL   string `json:"watch_url,omitempty"`
	WatchLabel string `json:"watch_label,omitempty"`
}

// Message is one push: its words, the app address a tap opens, and (optionally) the
// title's Watch pages — Plex's, and the reader's own server's.
type Message struct {
	Title      string
	Body       string
	URL        string
	PlexURL    string
	WatchURL   string
	WatchLabel string
}

func (m Message) payload() ([]byte, error) {
	return json.Marshal(Payload(m))
}

// SendToUser pushes to every device the user has registered. Failures are logged,
// never returned — a dead push service must not fail the caller's flow — and
// subscriptions the push service says are gone (404/410) are pruned.
func (s *Service) SendToUser(ctx context.Context, userID int64, m Message) {
	_, _ = s.SendMessage(ctx, userID, m)
}

// ErrNoDevices is SendToUserResult for a user with no device subscribed.
var ErrNoDevices = errors.New("no devices subscribed — turn on push on the device first")

// SendToUserResult is SendToUser for a caller that keeps a delivery log (admin push
// alerts): it says how many devices took the message, and when none did, why.
func (s *Service) SendToUserResult(ctx context.Context, userID int64, title, body, url string) (int, error) {
	return s.SendMessage(ctx, userID, Message{Title: title, Body: body, URL: url})
}

// SendMessage is SendToUserResult for a whole Message.
func (s *Service) SendMessage(ctx context.Context, userID int64, m Message) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT endpoint, p256dh, auth FROM push_subscriptions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
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
		return 0, ErrNoDevices
	}

	priv := s.settings.Get(ctx, keyPrivate, "")
	pub := s.settings.Get(ctx, keyPublic, "")
	if priv == "" || pub == "" {
		return 0, ErrNoDevices // no keys yet → nobody could have subscribed anyway
	}
	msg, err := m.payload()
	if err != nil {
		return 0, err
	}
	sent := 0
	var lastErr error
	for _, x := range subs {
		if ValidEndpoint(x.endpoint) != nil {
			// Saved before endpoints were checked, and not a push service: never posted to.
			lastErr = errors.New("the device's subscription isn't a browser push service — turn push on again there")
			continue
		}
		resp, err := webpush.SendNotificationWithContext(ctx, msg, &webpush.Subscription{
			Endpoint: x.endpoint,
			Keys:     webpush.Keys{P256dh: x.p256dh, Auth: x.auth},
		}, &webpush.Options{
			HTTPClient:      s.client(),
			Subscriber:      "https://github.com/tristenlammi/Arrmada",
			VAPIDPublicKey:  pub,
			VAPIDPrivateKey: priv,
			TTL:             sendTTL,
			Urgency:         webpush.UrgencyNormal,
		})
		if err != nil {
			s.log.Debug("push: send failed", "err", err)
			lastErr = errors.New("the push service couldn't be reached")
			continue
		}
		switch {
		case resp.StatusCode == 404 || resp.StatusCode == 410:
			// The browser revoked the subscription (uninstalled PWA, cleared
			// site data) — the push service says it's gone for good.
			_, _ = s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint = ?`, x.endpoint)
			lastErr = errors.New("the device's subscription has expired — turn push on again there")
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			sent++
		default:
			lastErr = fmt.Errorf("the push service answered %d", resp.StatusCode)
		}
		resp.Body.Close()
	}
	if sent == 0 && lastErr != nil {
		return 0, lastErr
	}
	return sent, nil
}

// timeoutCtx bounds a send batch kicked off from an event handler.
func timeoutCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// SendToUserAsync fires the sends on a goroutine with its own deadline — event
// handlers (import fan-out) must never block on push services.
func (s *Service) SendToUserAsync(userID int64, m Message) {
	safego.Go(s.log, "push: send", func() {
		ctx, cancel := timeoutCtx()
		defer cancel()
		s.SendToUser(ctx, userID, m)
	})
}
