package download

import (
	"context"
	"database/sql"
	"errors"

	"github.com/tristenlammi/arrmada/internal/store"
)

// ErrNotFound is returned when a client id doesn't exist.
var ErrNotFound = errors.New("download client not found")

// Repo persists download clients in SQLite.
type Repo struct{ db store.Pool }

// NewRepo builds a repository over the given pool.
func NewRepo(db store.Pool) *Repo { return &Repo{db: db} }

const clientCols = `id, name, kind, url, username, password, category, enabled, priority, bundled, path_maps`

func (r *Repo) scan(row interface{ Scan(...any) error }) (Client, error) {
	var (
		c       Client
		en, bun int
		maps    string
	)
	if err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.URL, &c.Username, &c.Password, &c.Category, &en, &c.Priority, &bun, &maps); err != nil {
		return Client{}, err
	}
	c.Enabled = en != 0
	c.Bundled = bun != 0
	c.PathMaps = decodePathMaps(maps)
	return c, nil
}

// List returns all clients in the order new downloads try them (priority, then id).
func (r *Repo) List(ctx context.Context) ([]Client, error) {
	return r.query(ctx, `SELECT `+clientCols+` FROM download_clients ORDER BY priority, id`)
}

// ListEnabled returns only enabled clients, in the order new downloads try them: the
// lowest priority number first, then the oldest.
func (r *Repo) ListEnabled(ctx context.Context) ([]Client, error) {
	return r.query(ctx, `SELECT `+clientCols+` FROM download_clients WHERE enabled = 1 ORDER BY priority, id`)
}

// Bundled returns the row marked as the packaged qBittorrent, switched off or not, and
// false when there's none.
func (r *Repo) Bundled(ctx context.Context) (Client, bool, error) {
	return r.BundledOf(ctx, KindQbittorrent)
}

// BundledOf returns the row marked as the packaged client of one kind (qBittorrent, or the
// optional SABnzbd), switched off or not, and false when there's none. Each kind has its
// own: the bundled SABnzbd being present says nothing about the bundled qBittorrent.
func (r *Repo) BundledOf(ctx context.Context, kind Kind) (Client, bool, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+clientCols+` FROM download_clients WHERE bundled = 1 AND kind = ? ORDER BY id LIMIT 1`, kind)
	c, err := r.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Client{}, false, nil
	}
	if err != nil {
		return Client{}, false, err
	}
	return c, true, nil
}

// MarkBundled flags one row as the packaged client of its kind.
func (r *Repo) MarkBundled(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE download_clients SET bundled = 1 WHERE id = ?`, id)
	return err
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

// Create inserts a client and returns it with its id. A zero priority means the default.
func (r *Repo) Create(ctx context.Context, c Client) (Client, error) {
	if c.Priority <= 0 {
		c.Priority = DefaultPriority
	}
	id, err := store.InsertID(ctx, r.db,
		`INSERT INTO download_clients (name, kind, url, username, password, category, enabled, priority, bundled, path_maps)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.Kind, c.URL, c.Username, c.Password, c.Category, boolToInt(c.Enabled), c.Priority, boolToInt(c.Bundled), encodePathMaps(c.PathMaps))
	if err != nil {
		return Client{}, err
	}
	c.ID = id
	return c, nil
}

// Update changes a client's name, URL, username, enabled flag, priority and path mappings
// in place. The password is replaced only when one is given — the browser never holds the
// stored one, so a blank field means "keep it". Kind, category and the bundled flag are
// left alone, and a zero priority keeps the stored one. ErrNotFound when the id doesn't
// exist.
func (r *Repo) Update(ctx context.Context, c Client) error {
	maps := encodePathMaps(c.PathMaps)
	q := `UPDATE download_clients SET name = ?, url = ?, username = ?, enabled = ?,
		priority = CASE WHEN ? > 0 THEN ? ELSE priority END, path_maps = ? WHERE id = ?`
	args := []any{c.Name, c.URL, c.Username, boolToInt(c.Enabled), c.Priority, c.Priority, maps, c.ID}
	if c.Password != "" {
		q = `UPDATE download_clients SET name = ?, url = ?, username = ?, enabled = ?,
			priority = CASE WHEN ? > 0 THEN ? ELSE priority END, path_maps = ?, password = ? WHERE id = ?`
		args = []any{c.Name, c.URL, c.Username, boolToInt(c.Enabled), c.Priority, c.Priority, maps, c.Password, c.ID}
	}
	res, err := r.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
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
