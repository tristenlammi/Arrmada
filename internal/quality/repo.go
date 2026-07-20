package quality

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ErrNotFound is returned when a profile id doesn't exist.
var ErrNotFound = errors.New("quality profile not found")

// Repo persists user-defined quality profiles.
type Repo struct{ db *sql.DB }

// NewRepo builds the repository.
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

const profileCols = `id, media_type, name, base, allowed_resolutions, min_source, bitrate_cap_mbps,
	small_bias, min_format_score, format_scores, custom_formats, keywords, rejected, min_seeders, stall_minutes, max_source,
	upgrades_enabled, upgrade_min_percent`

func (r *Repo) scan(row interface{ Scan(...any) error }) (StoredProfile, error) {
	var (
		sp                                                    StoredProfile
		allowedJSON, scoresJSON, cfJSON, kwJSON, rejectedJSON string
		upgradesEnabled                                       int
	)
	err := row.Scan(&sp.ID, &sp.MediaType, &sp.Name, &sp.Base, &allowedJSON, &sp.MinSource,
		&sp.BitrateCapMbps, &sp.SmallBias, &sp.MinFormatScore, &scoresJSON, &cfJSON,
		&kwJSON, &rejectedJSON, &sp.MinSeeders, &sp.StallMinutes, &sp.MaxSource,
		&upgradesEnabled, &sp.UpgradeMinPercent)
	if err != nil {
		return StoredProfile{}, err
	}
	sp.UpgradesEnabled = upgradesEnabled != 0
	_ = json.Unmarshal([]byte(allowedJSON), &sp.AllowedResolutions)
	_ = json.Unmarshal([]byte(scoresJSON), &sp.FormatScores)
	_ = json.Unmarshal([]byte(cfJSON), &sp.CustomFormats)
	_ = json.Unmarshal([]byte(kwJSON), &sp.Keywords)
	_ = json.Unmarshal([]byte(rejectedJSON), &sp.Rejected)
	if sp.FormatScores == nil {
		sp.FormatScores = map[string]int{}
	}
	return sp, nil
}

// List returns all user profiles for a media type.
func (r *Repo) List(ctx context.Context, mediaType string) ([]StoredProfile, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+profileCols+` FROM quality_profiles WHERE media_type = ? ORDER BY id`, mediaType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredProfile
	for rows.Next() {
		sp, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// Get returns one profile by id.
func (r *Repo) Get(ctx context.Context, id int64) (StoredProfile, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+profileCols+` FROM quality_profiles WHERE id = ?`, id)
	sp, err := r.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredProfile{}, ErrNotFound
	}
	return sp, err
}

// Create inserts a profile and returns it with its new id.
func (r *Repo) Create(ctx context.Context, sp StoredProfile) (StoredProfile, error) {
	allowed, scores, cf, kw, rej := marshalJSON(sp)
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO quality_profiles (media_type, name, base, allowed_resolutions, min_source,
			bitrate_cap_mbps, small_bias, min_format_score, format_scores, custom_formats,
			keywords, rejected, min_seeders, stall_minutes, max_source, upgrades_enabled, upgrade_min_percent)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sp.MediaType, sp.Name, sp.Base, allowed, sp.MinSource, sp.BitrateCapMbps, sp.SmallBias,
		sp.MinFormatScore, scores, cf, kw, rej, sp.MinSeeders, sp.StallMinutes, sp.MaxSource,
		boolToInt(sp.UpgradesEnabled), sp.UpgradeMinPercent)
	if err != nil {
		return StoredProfile{}, err
	}
	id, _ := res.LastInsertId()
	return r.Get(ctx, id)
}

// Update writes an existing profile.
func (r *Repo) Update(ctx context.Context, id int64, sp StoredProfile) error {
	allowed, scores, cf, kw, rej := marshalJSON(sp)
	res, err := r.db.ExecContext(ctx,
		`UPDATE quality_profiles SET name = ?, base = ?, allowed_resolutions = ?, min_source = ?,
			bitrate_cap_mbps = ?, small_bias = ?, min_format_score = ?, format_scores = ?, custom_formats = ?,
			keywords = ?, rejected = ?, min_seeders = ?, stall_minutes = ?, max_source = ?,
			upgrades_enabled = ?, upgrade_min_percent = ?
		 WHERE id = ?`,
		sp.Name, sp.Base, allowed, sp.MinSource, sp.BitrateCapMbps, sp.SmallBias, sp.MinFormatScore,
		scores, cf, kw, rej, sp.MinSeeders, sp.StallMinutes, sp.MaxSource,
		boolToInt(sp.UpgradesEnabled), sp.UpgradeMinPercent, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a profile.
func (r *Repo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM quality_profiles WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// getSetting reads a key/value setting ("" if absent).
func (r *Repo) getSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// setSetting upserts a key/value setting.
func (r *Repo) setSetting(ctx context.Context, key, value string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		key, value)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func marshalJSON(sp StoredProfile) (allowed, scores, cf, keywords, rejected string) {
	a, _ := json.Marshal(sp.AllowedResolutions)
	s, _ := json.Marshal(sp.FormatScores)
	c, _ := json.Marshal(sp.CustomFormats)
	k, _ := json.Marshal(sp.Keywords)
	rj, _ := json.Marshal(sp.Rejected)
	return string(a), string(s), string(c), string(k), string(rj)
}
