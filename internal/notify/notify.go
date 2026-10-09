// Package notify sends the owner's alerts through Apprise (80+ services from a single URL
// scheme): grabs and imports, requests, Plex watch events, and whatever else the event
// catalog (catalog.go) declares. It's a CRUD store of connections — each an Apprise URL
// plus the events it subscribes to — and a dispatcher that fans each event out to the
// connections that want it. Apprise is bundled in the image; delivery shells out to the
// `apprise` CLI.
package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/store"
)

// ErrNotFound is returned when a connection id doesn't exist.
var ErrNotFound = errors.New("notification connection not found")

// Connection is one configured notification target — an Apprise URL plus the catalog
// events it subscribes to.
type Connection struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // free-form label / service hint (informational)
	// URL is an Apprise URL (discord://, tgram://, mailto://, ntfy://, …). It often holds a
	// token or password, so it never goes out in JSON: the API answers with URLHint.
	URL     string   `json:"-"`
	Events  []string `json:"events"` // catalog keys, sorted
	Enabled bool     `json:"enabled"`
}

// Subscribes reports whether the connection wants an event.
func (c Connection) Subscribes(key string) bool {
	for _, e := range c.Events {
		if e == key {
			return true
		}
	}
	return false
}

// Service stores connections and delivers notifications to them via Apprise.
type Service struct {
	db      *sql.DB
	bus     *eventbus.Bus
	log     *slog.Logger
	apprise string // path to the apprise binary ("" if not found)
	// transport sends one message to one Apprise URL. It's the apprise CLI; tests swap
	// it for a recorder (SetTransport).
	transport Transport

	// The delivery queue (queue.go): wake nudges the worker, now and poll are the clock
	// and the idle interval (tests shorten them).
	wake chan struct{}
	now  func() time.Time
	poll time.Duration
}

// Transport sends one message to one Apprise URL. An error's text must not quote the
// URL (Send scrubs its own).
type Transport func(ctx context.Context, url, title, body string) error

// NewService wires the notification service.
func NewService(db *sql.DB, bus *eventbus.Bus, log *slog.Logger) *Service {
	s := &Service{db: db, bus: bus, log: log, wake: make(chan struct{}, 1), now: time.Now, poll: defaultQueuePoll}
	if p, err := exec.LookPath("apprise"); err == nil {
		s.apprise = p
	} else {
		log.Warn("notify: apprise binary not found — notifications will not send")
	}
	s.transport = func(ctx context.Context, url, title, body string) error {
		if s.apprise == "" {
			return fmt.Errorf("apprise is not installed")
		}
		return Send(ctx, s.apprise, title, body, url)
	}
	return s
}

// SetTransport replaces how messages leave the server. For tests.
func (s *Service) SetTransport(t Transport) { s.transport = t }

// AppriseBin returns the path to the apprise binary ("" if not installed) — used by other
// modules (e.g. per-user request-ready pushes) to send directly.
func (s *Service) AppriseBin() string { return s.apprise }

const cols = `id, name, kind, url, enabled`

func scanConn(row interface{ Scan(...any) error }) (Connection, error) {
	var (
		c     Connection
		enabl int
	)
	if err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.URL, &enabl); err != nil {
		return Connection{}, err
	}
	c.Enabled = enabl != 0
	c.Events = []string{}
	return c, nil
}

// List returns all connections with their subscriptions (two queries, however many
// connections there are).
func (s *Service) List(ctx context.Context) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM notifications ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	at := map[int64]int{}
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		at[c.ID] = len(out)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	subs, err := s.db.QueryContext(ctx, `SELECT connection_id, event_key FROM notification_subscriptions ORDER BY connection_id, event_key`)
	if err != nil {
		return nil, err
	}
	defer subs.Close()
	for subs.Next() {
		var id int64
		var key string
		if err := subs.Scan(&id, &key); err != nil {
			return nil, err
		}
		if i, ok := at[id]; ok {
			out[i].Events = append(out[i].Events, key)
		}
	}
	return out, subs.Err()
}

// Get returns one connection.
func (s *Service) Get(ctx context.Context, id int64) (Connection, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM notifications WHERE id = ?`, id)
	c, err := scanConn(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	if err != nil {
		return Connection{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT event_key FROM notification_subscriptions WHERE connection_id = ? ORDER BY event_key`, id)
	if err != nil {
		return Connection{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return Connection{}, err
		}
		c.Events = append(c.Events, key)
	}
	return c, rows.Err()
}

// legacyFlags are the old per-event columns, kept in step for the four events they
// described so a rolled-back build still alerts as configured.
func legacyFlags(c Connection) (grab, imp, stream, buffering int) {
	return b2i(c.Subscribes("release.grabbed")),
		b2i(c.Subscribes("movie.imported") || c.Subscribes("episodes.imported")),
		b2i(c.Subscribes("plex.stream.started")),
		b2i(c.Subscribes("plex.buffering"))
}

// Create stores a new connection and its subscriptions together.
func (s *Service) Create(ctx context.Context, c Connection) (Connection, error) {
	grab, imp, stream, buf := legacyFlags(c)
	var id int64
	err := store.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO notifications (name, kind, url, on_grab, on_import, on_stream, on_buffering, enabled) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			c.Name, c.Kind, c.URL, grab, imp, stream, buf, b2i(c.Enabled))
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return writeEvents(ctx, tx, id, c.Events)
	})
	if err != nil {
		return Connection{}, err
	}
	return s.Get(ctx, id)
}

// Update changes a connection and replaces its subscriptions, together.
func (s *Service) Update(ctx context.Context, id int64, c Connection) error {
	grab, imp, stream, buf := legacyFlags(c)
	return store.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE notifications SET name = ?, kind = ?, url = ?, on_grab = ?, on_import = ?, on_stream = ?, on_buffering = ?, enabled = ? WHERE id = ?`,
			c.Name, c.Kind, c.URL, grab, imp, stream, buf, b2i(c.Enabled), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if !c.Enabled {
			// Switched off: what's still waiting would never go, so say so in its log.
			if err := failQueued(ctx, tx, id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM notification_subscriptions WHERE connection_id = ?`, id); err != nil {
			return err
		}
		return writeEvents(ctx, tx, id, c.Events)
	})
}

func writeEvents(ctx context.Context, tx *sql.Tx, id int64, events []string) error {
	for _, key := range events {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO notification_subscriptions (connection_id, event_key) VALUES (?, ?)`, id, key); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes a connection; its subscriptions go with it (ON DELETE CASCADE).
func (s *Service) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM notifications WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Test sends a sample message to a connection to verify it works. It sends straight
// away rather than through the queue, so the answer is the real outcome; a saved
// connection's Test is also recorded in its delivery log.
func (s *Service) Test(ctx context.Context, c Connection) error {
	m := Message{Title: "Arrmada", Body: "✅ Test notification — this connection works.", Link: "/settings/alerts"}
	err := s.deliver(ctx, c, m)
	if c.ID > 0 {
		s.recordTest(context.WithoutCancel(ctx), c, m, err)
	}
	return err
}

// Run subscribes to every bus topic the catalog names and dispatches each event until
// ctx is cancelled. Start it once at boot, after any Register calls.
func (s *Service) Run(ctx context.Context) {
	byTopic := topics()
	names := make([]string, 0, len(byTopic))
	for t := range byTopic {
		names = append(names, t)
	}
	events, cancel := s.bus.Subscribe(names...)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			data := payload(ev.Data)
			for _, def := range byTopic[ev.Topic] {
				if m, ok := def.Format(data); ok {
					if _, err := s.Dispatch(ctx, def.Key, m); err != nil && ctx.Err() == nil {
						s.log.Warn("notify: couldn't dispatch an alert", "event", def.Key, "err", err)
					}
				}
			}
		}
	}
}

// Emit formats a catalog event from its payload and queues it — for producers that
// call the alerts directly instead of publishing a bus topic. Unknown keys and payloads
// Format turns down send nothing.
func (s *Service) Emit(ctx context.Context, key string, data map[string]any) (int, error) {
	return s.EmitOnce(ctx, key, "", data)
}

// EmitOnce is Emit with a dedupe key (see DispatchOnce).
func (s *Service) EmitOnce(ctx context.Context, key, dedupe string, data map[string]any) (int, error) {
	def, ok := Lookup(key)
	if !ok {
		return 0, fmt.Errorf("unknown alert event %q", key)
	}
	m, ok := def.Format(data)
	if !ok {
		return 0, nil
	}
	return s.DispatchOnce(ctx, key, dedupe, m)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// deliver sends a notification to one connection. The error never quotes the URL.
func (s *Service) deliver(ctx context.Context, c Connection, m Message) error {
	if c.URL == "" {
		return fmt.Errorf("no Apprise URL configured")
	}
	if err := s.transport(ctx, c.URL, m.Title, m.Body); err != nil {
		return errors.New(Redact(err.Error(), c.URL))
	}
	return nil
}

// Send delivers one notification through the apprise CLI to one or more Apprise URLs.
func Send(ctx context.Context, appriseBin, title, body string, urls ...string) error {
	if appriseBin == "" {
		return fmt.Errorf("apprise is not installed")
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, appriseBin, appriseArgs(title, body, urls)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// apprise -v can echo the URL it failed on; the reason is wanted, the token isn't.
		return fmt.Errorf("apprise: %v (%s)", err, trim(Redact(string(out), urls...)))
	}
	return nil
}

// appriseArgs builds the apprise CLI argument list. The "--" separator terminates option
// parsing so a stored URL can never be interpreted as an apprise CLI flag (e.g. a URL
// crafted to start with "-" smuggling in --config/--attach behavior).
func appriseArgs(title, body string, urls []string) []string {
	args := []string{"-v", "-t", title, "-b", body, "--"} // -v surfaces the failure reason on non-zero exit
	return append(args, urls...)
}

// appriseSchemes is the allowlist of Apprise notification URL schemes accepted by
// ValidateAppriseURL. The generic delivery schemes (json/form/xml/webhook and their TLS
// variants) can point the server at any host, internal ones included. That's fine for
// the admin's own connections; requesters' personal URLs go through the stricter
// ValidateUserAppriseURL (ssrf.go), which refuses them.
var appriseSchemes = map[string]bool{
	"discord": true, "telegram": true, "tgram": true, "slack": true,
	"mailto": true, "mailtos": true,
	"pover": true, "pushover": true,
	"gotify": true, "gotifys": true,
	"ntfy": true, "ntfys": true,
	"matrix": true, "matrixs": true,
	"signal": true, "signals": true,
	"json": true, "jsons": true,
	"form": true, "forms": true,
	"xml": true, "xmls": true,
	"webhook": true, "webhooks": true,
	"twilio":  true,
	"apprise": true, "apprises": true,
	"pbul": true, "pushbullet": true,
	"home-assistant": true, "hassio": true,
}

// ValidateAppriseURL rejects strings that are not a plausible Apprise notification URL:
// anything starting with "-" (could read as a CLI option), anything with whitespace or a
// second "scheme://" in it (apprise splits those into several URLs, which would smuggle a
// second target past these checks), and any scheme outside the allowlist above.
//
// The scheme is split off by hand rather than with url.Parse: real Apprise URLs aren't
// all RFC 3986 — a Telegram bot token ("tgram://123456:ABC…/chat") reads as a bad port.
func ValidateAppriseURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("notification URL is empty")
	}
	if strings.HasPrefix(raw, "-") {
		return errors.New("notification URL must not start with '-'")
	}
	if strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return errors.New("notification URL must not contain spaces")
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme == "" {
		return errors.New("notification URL must include a scheme (e.g. discord://…)")
	}
	scheme = strings.ToLower(scheme)
	if !appriseSchemes[scheme] {
		return fmt.Errorf("unsupported notification scheme %q", scheme)
	}
	if rest == "" {
		return errors.New("notification URL has nothing after " + scheme + "://")
	}
	if strings.Contains(rest, "://") {
		return errors.New("one connection takes one URL — add another connection for a second one")
	}
	return nil
}

func trim(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
