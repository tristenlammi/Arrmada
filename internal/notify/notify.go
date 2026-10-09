// Package notify sends outbound notifications via Apprise (80+ services from a single URL
// scheme) when Arrmada grabs or imports a release, or on Plex watch events. It's a small
// subsystem: a CRUD store of connections plus a bus subscriber that fans events out to them.
// Apprise is bundled in the image; delivery shells out to the `apprise` CLI.
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
)

// ErrNotFound is returned when a connection id doesn't exist.
var ErrNotFound = errors.New("notification connection not found")

// Connection is one configured notification target — an Apprise URL plus event subscriptions.
type Connection struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // free-form label / service hint (informational)
	// URL is an Apprise URL (discord://, tgram://, mailto://, ntfy://, …). It often holds a
	// token or password, so it never goes out in JSON: the API answers with URLHint.
	URL         string `json:"-"`
	OnGrab      bool   `json:"on_grab"`
	OnImport    bool   `json:"on_import"`
	OnStream    bool   `json:"on_stream"`    // Plex: a stream started
	OnBuffering bool   `json:"on_buffering"` // Plex: a stream buffered
	Enabled     bool   `json:"enabled"`
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
}

// Transport sends one message to one Apprise URL. An error's text must not quote the
// URL (Send scrubs its own).
type Transport func(ctx context.Context, url, title, body string) error

// NewService wires the notification service.
func NewService(db *sql.DB, bus *eventbus.Bus, log *slog.Logger) *Service {
	s := &Service{db: db, bus: bus, log: log}
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

const cols = `id, name, kind, url, on_grab, on_import, on_stream, on_buffering, enabled`

func scanConn(row interface{ Scan(...any) error }) (Connection, error) {
	var (
		c                                              Connection
		onGrab, onImport, onStream, onBuffering, enabl int
	)
	if err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.URL, &onGrab, &onImport, &onStream, &onBuffering, &enabl); err != nil {
		return Connection{}, err
	}
	c.OnGrab, c.OnImport, c.OnStream, c.OnBuffering, c.Enabled = onGrab != 0, onImport != 0, onStream != 0, onBuffering != 0, enabl != 0
	return c, nil
}

// List returns all connections.
func (s *Service) List(ctx context.Context) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM notifications ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns one connection.
func (s *Service) Get(ctx context.Context, id int64) (Connection, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM notifications WHERE id = ?`, id)
	c, err := scanConn(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	return c, err
}

// Create stores a new connection.
func (s *Service) Create(ctx context.Context, c Connection) (Connection, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO notifications (name, kind, url, on_grab, on_import, on_stream, on_buffering, enabled) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.Kind, c.URL, b2i(c.OnGrab), b2i(c.OnImport), b2i(c.OnStream), b2i(c.OnBuffering), b2i(c.Enabled))
	if err != nil {
		return Connection{}, err
	}
	id, _ := res.LastInsertId()
	return s.Get(ctx, id)
}

// Update changes a connection.
func (s *Service) Update(ctx context.Context, id int64, c Connection) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE notifications SET name = ?, kind = ?, url = ?, on_grab = ?, on_import = ?, on_stream = ?, on_buffering = ?, enabled = ? WHERE id = ?`,
		c.Name, c.Kind, c.URL, b2i(c.OnGrab), b2i(c.OnImport), b2i(c.OnStream), b2i(c.OnBuffering), b2i(c.Enabled), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a connection.
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

// Test sends a sample message to a connection to verify it works.
func (s *Service) Test(ctx context.Context, c Connection) error {
	return s.deliver(ctx, c, "Arrmada", "✅ Test notification — this connection works.")
}

// Run subscribes to acquisition events and delivers notifications until ctx is
// cancelled. Start it once at boot.
func (s *Service) Run(ctx context.Context) {
	grabbed, cancelG := s.bus.Subscribe("release.grabbed")
	imported, cancelI := s.bus.Subscribe("movie.downloaded")
	seriesImported, cancelS := s.bus.Subscribe("series.imported")
	streamStarted, cancelSt := s.bus.Subscribe("plex.stream.started")
	buffering, cancelB := s.bus.Subscribe("plex.buffering")
	defer cancelG()
	defer cancelI()
	defer cancelS()
	defer cancelSt()
	defer cancelB()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-streamStarted:
			user, _ := asString(ev.Data, "user")
			title, _ := asString(ev.Data, "title")
			if title != "" {
				s.fan(ctx, "stream", "Now playing", fmt.Sprintf("▶️ %s started %s", user, title))
			}
		case ev := <-buffering:
			user, _ := asString(ev.Data, "user")
			title, _ := asString(ev.Data, "title")
			if title != "" {
				s.fan(ctx, "buffering", "Buffering", fmt.Sprintf("⏳ %s’s stream is buffering — %s", user, title))
			}
		case ev := <-grabbed:
			title, _ := asString(ev.Data, "title")
			if title != "" {
				s.fan(ctx, "grab", "Grabbed", "🎬 "+title)
			}
		case ev := <-imported:
			title, _ := asString(ev.Data, "title")
			// A library scan adopting files already on disk isn't an import worth a ping
			// per film; those events carry source "scan" (and no title).
			if source, _ := asString(ev.Data, "source"); source == "scan" {
				continue
			}
			if title != "" {
				s.fan(ctx, "import", "Imported", "📥 "+title)
			}
		case ev := <-seriesImported:
			title, _ := asString(ev.Data, "title")
			if title != "" {
				body := "📥 " + title
				if count, ok := asInt(ev.Data, "count"); ok && count > 0 {
					body = fmt.Sprintf("📥 %s (%d episode%s)", title, count, plural(count))
				}
				s.fan(ctx, "import", "Imported", body)
			}
		}
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// subscribes reports whether a connection wants the given event.
func (c Connection) subscribes(event string) bool {
	switch event {
	case "grab":
		return c.OnGrab
	case "import":
		return c.OnImport
	case "stream":
		return c.OnStream
	case "buffering":
		return c.OnBuffering
	}
	return false
}

// fan delivers a message to every enabled connection subscribed to the event.
func (s *Service) fan(ctx context.Context, event, title, body string) {
	conns, err := s.List(ctx)
	if err != nil {
		return
	}
	for _, c := range conns {
		if !c.Enabled || !c.subscribes(event) {
			continue
		}
		if err := s.deliver(ctx, c, title, body); err != nil {
			s.log.Warn("notify delivery failed", "connection", c.Name, "err", err)
		}
	}
}

// deliver sends a notification to one connection. The error never quotes the URL.
func (s *Service) deliver(ctx context.Context, c Connection, title, body string) error {
	if c.URL == "" {
		return fmt.Errorf("no Apprise URL configured")
	}
	if err := s.transport(ctx, c.URL, title, body); err != nil {
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
// ValidateAppriseURL. Note: the generic delivery schemes (json/form/xml/webhook and
// their TLS variants) let a user point Arrmada's server at ANY host — including
// internal ones — so storing them is an accepted SSRF surface; the allowlist guards
// against option injection and nonsense input, not against where those schemes post.
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

func asString(data any, key string) (string, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		return "", false
	}
	s, ok := m[key].(string)
	return s, ok
}

func asInt(data any, key string) (int, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		return 0, false
	}
	n, ok := m[key].(int)
	return n, ok
}
