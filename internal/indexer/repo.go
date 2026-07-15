package indexer

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// ErrNotFound is returned when an indexer id doesn't exist.
var ErrNotFound = errors.New("indexer not found")

// Repo persists indexers in SQLite.
type Repo struct {
	db *sql.DB
}

// NewRepo builds a repository over the given pool.
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

const indexerCols = `id, name, kind, url, api_key, username, password, categories, priority, min_seeders, seed_enabled, seed_ratio, seed_hours, enabled, media_types`

func (r *Repo) scan(row interface{ Scan(...any) error }) (Indexer, error) {
	var (
		idx        Indexer
		cats, mt   string
		seedEn, en int
	)
	err := row.Scan(&idx.ID, &idx.Name, &idx.Kind, &idx.URL, &idx.APIKey, &idx.Username, &idx.Password, &cats, &idx.Priority, &idx.MinSeeders, &seedEn, &idx.SeedRatio, &idx.SeedHours, &en, &mt)
	if err != nil {
		return Indexer{}, err
	}
	idx.Categories = decodeCats(cats)
	idx.MediaTypes = decodeStrs(mt)
	idx.SeedEnabled = seedEn != 0
	idx.Enabled = en != 0
	return idx, nil
}

// List returns all indexers ordered by priority.
func (r *Repo) List(ctx context.Context) ([]Indexer, error) {
	return r.query(ctx, `SELECT `+indexerCols+` FROM indexers ORDER BY priority, id`)
}

// ListEnabled returns only enabled indexers, ordered by priority.
func (r *Repo) ListEnabled(ctx context.Context) ([]Indexer, error) {
	return r.query(ctx, `SELECT `+indexerCols+` FROM indexers WHERE enabled = 1 ORDER BY priority, id`)
}

func (r *Repo) query(ctx context.Context, q string, args ...any) ([]Indexer, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Indexer
	for rows.Next() {
		idx, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, idx)
	}
	return out, rows.Err()
}

// Get returns one indexer by id.
func (r *Repo) Get(ctx context.Context, id int64) (Indexer, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+indexerCols+` FROM indexers WHERE id = ?`, id)
	idx, err := r.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Indexer{}, ErrNotFound
	}
	return idx, err
}

// Create inserts an indexer and returns it with its assigned id.
func (r *Repo) Create(ctx context.Context, idx Indexer) (Indexer, error) {
	if idx.Priority == 0 {
		idx.Priority = 25
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO indexers (name, kind, url, api_key, username, password, categories, priority, min_seeders, seed_enabled, seed_ratio, seed_hours, enabled, media_types)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idx.Name, idx.Kind, idx.URL, idx.APIKey, idx.Username, idx.Password,
		encodeCats(idx.Categories), idx.Priority, idx.MinSeeders, boolToInt(idx.SeedEnabled), idx.SeedRatio, idx.SeedHours, boolToInt(idx.Enabled), encodeStrs(idx.MediaTypes))
	if err != nil {
		return Indexer{}, err
	}
	idx.ID, _ = res.LastInsertId()
	return idx, nil
}

// Update changes an indexer's settings. Secrets (APIKey, Password) are only
// overwritten when non-empty, so the UI can send blanks to keep existing values.
func (r *Repo) Update(ctx context.Context, idx Indexer) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE indexers SET name=?, kind=?, url=?, username=?, categories=?, priority=?, min_seeders=?, seed_enabled=?, seed_ratio=?, seed_hours=?, enabled=?, media_types=? WHERE id=?`,
		idx.Name, idx.Kind, idx.URL, idx.Username, encodeCats(idx.Categories),
		idx.Priority, idx.MinSeeders, boolToInt(idx.SeedEnabled), idx.SeedRatio, idx.SeedHours, boolToInt(idx.Enabled), encodeStrs(idx.MediaTypes), idx.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if idx.APIKey != "" {
		if _, err := r.db.ExecContext(ctx, `UPDATE indexers SET api_key=? WHERE id=?`, idx.APIKey, idx.ID); err != nil {
			return err
		}
	}
	if idx.Password != "" {
		if _, err := r.db.ExecContext(ctx, `UPDATE indexers SET password=? WHERE id=?`, idx.Password, idx.ID); err != nil {
			return err
		}
	}
	return nil
}

// SetSession overwrites just the stored secret (api_key) for an indexer — used
// to persist a rotated MyAnonaMouse mam_id without touching other fields.
func (r *Repo) SetSession(ctx context.Context, id int64, session string) error {
	if session == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `UPDATE indexers SET api_key=? WHERE id=?`, session, id)
	return err
}

// Delete removes an indexer by id.
func (r *Repo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM indexers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- media-type + category CSV encoding ---

func encodeStrs(vals []string) string {
	var kept []string
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			kept = append(kept, v)
		}
	}
	return strings.Join(kept, ",")
}

func decodeStrs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func encodeCats(cats []int) string {
	if len(cats) == 0 {
		return ""
	}
	parts := make([]string, len(cats))
	for i, c := range cats {
		parts[i] = strconv.Itoa(c)
	}
	return strings.Join(parts, ",")
}

func decodeCats(s string) []int {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []int
	for _, p := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
