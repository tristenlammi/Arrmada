// Package notify sends outbound notifications (Discord, generic webhook) when
// Arrmada grabs or imports a release. It's a small subsystem: a CRUD store of
// connections plus a bus subscriber that fans events out to them.
package notify

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
)

// ErrNotFound is returned when a connection id doesn't exist.
var ErrNotFound = errors.New("notification connection not found")

// Kinds of notification transport.
const (
	KindDiscord = "discord"
	KindWebhook = "webhook"
)

// Connection is one configured notification target.
type Connection struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // discord | webhook
	URL      string `json:"url"`
	OnGrab   bool   `json:"on_grab"`
	OnImport bool   `json:"on_import"`
	Enabled  bool   `json:"enabled"`
}

// Service stores connections and delivers notifications to them.
type Service struct {
	db   *sql.DB
	bus  *eventbus.Bus
	log  *slog.Logger
	http *http.Client
}

// NewService wires the notification service.
func NewService(db *sql.DB, bus *eventbus.Bus, log *slog.Logger) *Service {
	return &Service{db: db, bus: bus, log: log, http: &http.Client{Timeout: 15 * time.Second}}
}

const cols = `id, name, kind, url, on_grab, on_import, enabled`

func scanConn(row interface{ Scan(...any) error }) (Connection, error) {
	var (
		c                       Connection
		onGrab, onImport, enabl int
	)
	if err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.URL, &onGrab, &onImport, &enabl); err != nil {
		return Connection{}, err
	}
	c.OnGrab, c.OnImport, c.Enabled = onGrab != 0, onImport != 0, enabl != 0
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
		`INSERT INTO notifications (name, kind, url, on_grab, on_import, enabled) VALUES (?, ?, ?, ?, ?, ?)`,
		c.Name, c.Kind, c.URL, b2i(c.OnGrab), b2i(c.OnImport), b2i(c.Enabled))
	if err != nil {
		return Connection{}, err
	}
	id, _ := res.LastInsertId()
	return s.Get(ctx, id)
}

// Update changes a connection.
func (s *Service) Update(ctx context.Context, id int64, c Connection) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE notifications SET name = ?, kind = ?, url = ?, on_grab = ?, on_import = ?, enabled = ? WHERE id = ?`,
		c.Name, c.Kind, c.URL, b2i(c.OnGrab), b2i(c.OnImport), b2i(c.Enabled), id)
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
	return s.deliver(ctx, c, "✅ Arrmada test notification — this connection works.")
}

// Run subscribes to acquisition events and delivers notifications until ctx is
// cancelled. Start it once at boot.
func (s *Service) Run(ctx context.Context) {
	grabbed, cancelG := s.bus.Subscribe("release.grabbed")
	imported, cancelI := s.bus.Subscribe("movie.downloaded")
	seriesImported, cancelS := s.bus.Subscribe("series.imported")
	defer cancelG()
	defer cancelI()
	defer cancelS()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-grabbed:
			title, _ := asString(ev.Data, "title")
			if title != "" {
				s.fan(ctx, "grab", "🎬 Grabbed: "+title)
			}
		case ev := <-imported:
			title, _ := asString(ev.Data, "title")
			if title != "" {
				s.fan(ctx, "import", "📥 Imported: "+title)
			}
		case ev := <-seriesImported:
			title, _ := asString(ev.Data, "title")
			if title != "" {
				msg := "📥 Imported: " + title
				if count, ok := asInt(ev.Data, "count"); ok && count > 0 {
					msg = fmt.Sprintf("📥 Imported: %s (%d episode%s)", title, count, plural(count))
				}
				s.fan(ctx, "import", msg)
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

// fan delivers a message to every enabled connection subscribed to the event.
func (s *Service) fan(ctx context.Context, event, message string) {
	conns, err := s.List(ctx)
	if err != nil {
		return
	}
	for _, c := range conns {
		if !c.Enabled {
			continue
		}
		if (event == "grab" && !c.OnGrab) || (event == "import" && !c.OnImport) {
			continue
		}
		if err := s.deliver(ctx, c, message); err != nil {
			s.log.Warn("notify delivery failed", "connection", c.Name, "err", err)
		}
	}
}

// deliver POSTs a message to one connection in its expected shape.
func (s *Service) deliver(ctx context.Context, c Connection, message string) error {
	if c.URL == "" {
		return fmt.Errorf("no URL configured")
	}
	var payload any
	switch c.Kind {
	case KindDiscord:
		payload = map[string]any{"content": message}
	default: // generic webhook
		payload = map[string]any{"message": message, "source": "arrmada"}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
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
