package indexer

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/store"
)

// ErrNotFound is returned when an indexer id doesn't exist.
var ErrNotFound = errors.New("indexer not found")

// Repo persists indexers in SQLite.
type Repo struct {
	db *sql.DB
}

// NewRepo builds a repository over the given pool.
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

const indexerCols = `id, name, kind, url, api_key, username, password, categories, priority, min_seeders, seed_enabled, seed_ratio, seed_hours, enabled, media_types, prowlarr_id, disabled_by, managed_note`

func (r *Repo) scan(row interface{ Scan(...any) error }) (Indexer, error) {
	var (
		idx        Indexer
		cats, mt   string
		seedEn, en int
	)
	err := row.Scan(&idx.ID, &idx.Name, &idx.Kind, &idx.URL, &idx.APIKey, &idx.Username, &idx.Password, &cats, &idx.Priority, &idx.MinSeeders, &seedEn, &idx.SeedRatio, &idx.SeedHours, &en, &mt, &idx.ProwlarrID, &idx.DisabledBy, &idx.ManagedNote)
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
	return createIn(ctx, r.db, idx)
}

// createIn is Create on a pool or inside a caller's transaction.
func createIn(ctx context.Context, ex store.Execer, idx Indexer) (Indexer, error) {
	if idx.Priority == 0 {
		idx.Priority = 25
	}
	res, err := ex.ExecContext(ctx,
		`INSERT INTO indexers (name, kind, url, api_key, username, password, categories, priority, min_seeders, seed_enabled, seed_ratio, seed_hours, enabled, media_types, prowlarr_id, disabled_by, managed_note)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idx.Name, idx.Kind, idx.URL, idx.APIKey, idx.Username, idx.Password,
		encodeCats(idx.Categories), idx.Priority, idx.MinSeeders, boolToInt(idx.SeedEnabled), idx.SeedRatio, idx.SeedHours, boolToInt(idx.Enabled), encodeStrs(idx.MediaTypes),
		idx.ProwlarrID, idx.DisabledBy, idx.ManagedNote)
	if err != nil {
		return Indexer{}, err
	}
	idx.ID, _ = res.LastInsertId()
	return idx, nil
}

// Update changes an indexer's settings. Secrets (APIKey, Password) are only
// overwritten when non-empty, so the UI can send blanks to keep existing values.
//
// It also notes who switched the row off. Turning it off marks it as the owner's choice,
// which a Prowlarr sync never undoes; turning it on clears that and any note a sync left.
// Saving a row that stays off (a pill click on a disabled row) changes neither, so a row
// a sync turned off can still be turned back on by the sync. The CASEs read the row's
// old enabled value: SQLite evaluates every SET expression against the row as it was.
func (r *Repo) Update(ctx context.Context, idx Indexer) error {
	en := boolToInt(idx.Enabled)
	res, err := r.db.ExecContext(ctx,
		`UPDATE indexers SET name=?, kind=?, url=?, username=?, categories=?, priority=?, min_seeders=?, seed_enabled=?, seed_ratio=?, seed_hours=?, media_types=?,
		    disabled_by = CASE WHEN ? = 1 THEN '' WHEN enabled = 1 THEN '`+DisabledByUser+`' ELSE disabled_by END,
		    managed_note = CASE WHEN ? = 1 OR enabled = 1 THEN '' ELSE managed_note END,
		    enabled = ?
		 WHERE id=?`,
		idx.Name, idx.Kind, idx.URL, idx.Username, encodeCats(idx.Categories),
		idx.Priority, idx.MinSeeders, boolToInt(idx.SeedEnabled), idx.SeedRatio, idx.SeedHours, encodeStrs(idx.MediaTypes),
		en, en, en, idx.ID)
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

// updateManaged writes what a Prowlarr sync owns on an existing row: its name, address,
// key and Prowlarr id. Whether it's on, what it's used for, its categories, priority,
// seeder floor and seed rules stay the owner's.
func updateManaged(ctx context.Context, ex store.Execer, id int64, prowlarrID int, name, url, apiKey string) error {
	_, err := ex.ExecContext(ctx,
		`UPDATE indexers SET prowlarr_id=?, name=?, url=?, api_key=CASE WHEN ?='' THEN api_key ELSE ? END WHERE id=?`,
		prowlarrID, name, url, apiKey, apiKey, id)
	return err
}

// setManagedState turns a row on or off for a Prowlarr sync, with who did it and the note
// the row shows.
func setManagedState(ctx context.Context, ex store.Execer, id int64, enabled bool, disabledBy, note string) error {
	_, err := ex.ExecContext(ctx,
		`UPDATE indexers SET enabled=?, disabled_by=?, managed_note=? WHERE id=?`,
		boolToInt(enabled), disabledBy, note, id)
	return err
}

// renameRefs points the rows that name an indexer at its new name. Grabs look up their
// seed rules by indexer name, and grab, blocklist and review rows show it, so a rename in
// Prowlarr mustn't strand them on a name that no longer exists.
func renameRefs(ctx context.Context, ex store.Execer, oldName, newName string) error {
	for _, table := range []string{"grabs", "blocklist", "import_reviews"} {
		if _, err := ex.ExecContext(ctx, `UPDATE `+table+` SET indexer=? WHERE indexer=?`, newName, oldName); err != nil {
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
