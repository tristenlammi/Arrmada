package download

import (
	"context"
	"database/sql"
	"errors"
)

// ErrNotFound is returned when a client id doesn't exist.
var ErrNotFound = errors.New("download client not found")

// Repo persists download clients in SQLite.
type Repo struct{ db *sql.DB }

// NewRepo builds a repository over the given pool.
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

const clientCols = `id, name, kind, url, username, password, category, enabled`

func (r *Repo) scan(row interface{ Scan(...any) error }) (Client, error) {
	var (
		c  Client
		en int
	)
	if err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.URL, &c.Username, &c.Password, &c.Category, &en); err != nil {
		return Client{}, err
	}
	c.Enabled = en != 0
	return c, nil
}

// List returns all clients.
func (r *Repo) List(ctx context.Context) ([]Client, error) {
	return r.query(ctx, `SELECT `+clientCols+` FROM download_clients ORDER BY id`)
}

// ListEnabled returns only enabled clients.
func (r *Repo) ListEnabled(ctx context.Context) ([]Client, error) {
	return r.query(ctx, `SELECT `+clientCols+` FROM download_clients WHERE enabled = 1 ORDER BY id`)
}

func (r *Repo) query(ctx context.Context, q string, args ...any) ([]Client, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Client
	for rows.Next() {
		c, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns one client by id.
func (r *Repo) Get(ctx context.Context, id int64) (Client, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+clientCols+` FROM download_clients WHERE id = ?`, id)
	c, err := r.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Client{}, ErrNotFound
	}
	return c, err
}

// Create inserts a client and returns it with its id.
func (r *Repo) Create(ctx context.Context, c Client) (Client, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO download_clients (name, kind, url, username, password, category, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.Kind, c.URL, c.Username, c.Password, c.Category, boolToInt(c.Enabled))
	if err != nil {
		return Client{}, err
	}
	c.ID, _ = res.LastInsertId()
	return c, nil
}

// Delete removes a client by id.
func (r *Repo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM download_clients WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
