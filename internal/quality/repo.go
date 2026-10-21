package quality

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/store"
)

// ErrNotFound is returned when a profile id doesn't exist.
var ErrNotFound = errors.New("quality profile not found")

// Repo persists user-defined quality profiles.
type Repo struct{ db store.Pool }

// NewRepo builds the repository.
func NewRepo(db store.Pool) *Repo { return &Repo{db: db} }

const profileCols = `id, media_type, name, base, allowed_resolutions, min_source, bitrate_cap_mbps,
	small_bias, min_format_score, format_scores, custom_formats, keywords, rejected, min_seeders, stall_minutes, max_source,
	upgrades_enabled, upgrade_min_percent, required_formats, ideal, allow_prerelease, upgrade_trigger, unwanted, group_tiers,
	multi_counts_language`

func (r *Repo) scan(row interface{ Scan(...any) error }) (StoredProfile, error) {
	var (
		sp                                                    StoredProfile
		allowedJSON, scoresJSON, cfJSON, kwJSON, rejectedJSON string
		requiredJSON, idealJSON, unwantedJSON, tiersJSON      string
		upgradesEnabled, allowPreRelease, multiCounts         int
	)
	err := row.Scan(&sp.ID, &sp.MediaType, &sp.Name, &sp.Base, &allowedJSON, &sp.MinSource,
		&sp.BitrateCapMbps, &sp.SmallBias, &sp.MinFormatScore, &scoresJSON, &cfJSON,
		&kwJSON, &rejectedJSON, &sp.MinSeeders, &sp.StallMinutes, &sp.MaxSource,
		&upgradesEnabled, &sp.UpgradeMinPercent, &requiredJSON, &idealJSON, &allowPreRelease, &sp.UpgradeTrigger, &unwantedJSON, &tiersJSON,
		&multiCounts)
	if err != nil {
		return StoredProfile{}, err
	}
	sp.MultiCountsLanguage = multiCounts != 0
	sp.UpgradeTrigger = NormalizeTrigger(sp.UpgradeTrigger)
	sp.UpgradesEnabled = upgradesEnabled != 0
	sp.AllowPreRelease = allowPreRelease != 0
	_ = json.Unmarshal([]byte(allowedJSON), &sp.AllowedResolutions)
	_ = json.Unmarshal([]byte(scoresJSON), &sp.FormatScores)
	_ = json.Unmarshal([]byte(cfJSON), &sp.CustomFormats)
	_ = json.Unmarshal([]byte(kwJSON), &sp.Keywords)
	_ = json.Unmarshal([]byte(rejectedJSON), &sp.Rejected)
	_ = json.Unmarshal([]byte(requiredJSON), &sp.RequiredFormats)
	if idealJSON != "" {
		var ideal IdealFile
		if json.Unmarshal([]byte(idealJSON), &ideal) == nil && !ideal.Empty() {
			sp.Ideal = &ideal
		}
	}
	if unwantedJSON != "" {
		var u Unwanted
		if json.Unmarshal([]byte(unwantedJSON), &u) == nil {
			sp.Unwanted = u.normalized()
		}
	}
	if tiersJSON != "" {
		var g GroupTiers
		if json.Unmarshal([]byte(tiersJSON), &g) == nil {
			g = g.normalized()
			sp.GroupTiers = &g
		}
	}
	if sp.FormatScores == nil {
		sp.FormatScores = map[string]int{}
	}
	// A profile written before targets existed is read as one (see Migrate), so the
	// builder, the library check and the engine all see the same thing.
	sp.Migrate()
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
	required, ideal := marshalExtra(sp)
	id, err := store.InsertID(ctx, r.db,
		`INSERT INTO quality_profiles (media_type, name, base, allowed_resolutions, min_source,
			bitrate_cap_mbps, small_bias, min_format_score, format_scores, custom_formats,
			keywords, rejected, min_seeders, stall_minutes, max_source, upgrades_enabled, upgrade_min_percent,
			required_formats, ideal, allow_prerelease, upgrade_trigger, unwanted, group_tiers,
			multi_counts_language)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sp.MediaType, sp.Name, sp.Base, allowed, sp.MinSource, sp.BitrateCapMbps, sp.SmallBias,
		sp.MinFormatScore, scores, cf, kw, rej, sp.MinSeeders, sp.StallMinutes, sp.MaxSource,
		boolToInt(sp.UpgradesEnabled), sp.UpgradeMinPercent, required, ideal, boolToInt(sp.AllowPreRelease),
		NormalizeTrigger(sp.UpgradeTrigger), marshalUnwanted(sp), marshalTiers(sp),
		boolToInt(sp.MultiCountsLanguage))
	if err != nil {
		return StoredProfile{}, err
	}
	return r.Get(ctx, id)
}

// Update writes an existing profile.
func (r *Repo) Update(ctx context.Context, id int64, sp StoredProfile) error {
	allowed, scores, cf, kw, rej := marshalJSON(sp)
	required, ideal := marshalExtra(sp)
	res, err := r.db.ExecContext(ctx,
		`UPDATE quality_profiles SET name = ?, base = ?, allowed_resolutions = ?, min_source = ?,
			bitrate_cap_mbps = ?, small_bias = ?, min_format_score = ?, format_scores = ?, custom_formats = ?,
			keywords = ?, rejected = ?, min_seeders = ?, stall_minutes = ?, max_source = ?,
			upgrades_enabled = ?, upgrade_min_percent = ?, required_formats = ?, ideal = ?, allow_prerelease = ?,
			upgrade_trigger = ?, unwanted = ?, group_tiers = ?, multi_counts_language = ?
		 WHERE id = ?`,
		sp.Name, sp.Base, allowed, sp.MinSource, sp.BitrateCapMbps, sp.SmallBias, sp.MinFormatScore,
		scores, cf, kw, rej, sp.MinSeeders, sp.StallMinutes, sp.MaxSource,
		boolToInt(sp.UpgradesEnabled), sp.UpgradeMinPercent, required, ideal, boolToInt(sp.AllowPreRelease),
		NormalizeTrigger(sp.UpgradeTrigger), marshalUnwanted(sp), marshalTiers(sp),
		boolToInt(sp.MultiCountsLanguage), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Errors from DeleteAndReassign and Service.Delete.
var (
	// ErrTargetNotFound: the profile titles were to move to doesn't exist.
	ErrTargetNotFound = errors.New("the profile to move titles to doesn't exist")
	// ErrMediaMismatch: the target is for another media type — a film can't run on a book profile.
	ErrMediaMismatch = errors.New("the profile to move titles to is for another media type")
	// ErrSameProfile: the target is the profile being deleted.
	ErrSameProfile = errors.New("can't move titles to the profile being deleted")
	// ErrLastProfile: nothing else of that media type exists to move titles to.
	ErrLastProfile = errors.New("create another profile first")
)

// Reassigned counts the rows a profile delete moved onto its replacement.
type Reassigned struct {
	Movies   int `json:"movies"`
	Versions int `json:"versions"`
	Series   int `json:"series"`
	Books    int `json:"books"`
	Artists  int `json:"artists"`
	Requests int `json:"requests"`
	Grabs    int `json:"grabs"`
}

// DeleteAndReassign deletes a profile and moves everything that used it onto `to`, in
// one transaction: either every title, request and grab carries the new ref and the
// profile is gone, or nothing changed. Leaving the titles on a dangling ref instead
// would quietly run them on the default (see Effective) while the UI said otherwise.
//
// Both media types are read inside the transaction, so a target of another media type
// can never move films onto a book profile even if the profiles changed mid-request.
func (r *Repo) DeleteAndReassign(ctx context.Context, id int64, to string) (Reassigned, error) {
	var out Reassigned
	from := "custom:" + strconv.FormatInt(id, 10)
	if to == from {
		return out, ErrSameProfile
	}
	toID, ok := customID(to)
	if !ok {
		return out, ErrTargetNotFound
	}
	err := store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		return deleteAndReassignTx(ctx, tx, id, toID, from, to, &out)
	})
	if err != nil {
		return Reassigned{}, err
	}
	return out, nil
}

// deleteAndReassignTx is DeleteAndReassign's body, inside the caller's transaction.
func deleteAndReassignTx(ctx context.Context, tx *store.Transaction, id, toID int64, from, to string, out *Reassigned) error {
	var fromMedia, toMedia string
	if err := tx.QueryRowContext(ctx, `SELECT media_type FROM quality_profiles WHERE id = ?`, id).Scan(&fromMedia); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT media_type FROM quality_profiles WHERE id = ?`, toID).Scan(&toMedia); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTargetNotFound
		}
		return err
	}
	if fromMedia != toMedia {
		return ErrMediaMismatch
	}

	moves := []struct {
		n     *int
		query string
	}{
		{&out.Movies, `UPDATE movies SET quality_profile = ? WHERE quality_profile = ?`},
		{&out.Versions, `UPDATE movie_versions SET quality_profile = ? WHERE quality_profile = ?`},
		{&out.Series, `UPDATE series SET quality_profile = ? WHERE quality_profile = ?`},
		{&out.Books, `UPDATE books SET quality_profile = ? WHERE quality_profile = ?`},
		{&out.Artists, `UPDATE artists SET quality_profile = ? WHERE quality_profile = ?`},
		// Only requests still waiting on a decision: an approved one's ref is history, and
		// its title already moved with the library rows above.
		{&out.Requests, `UPDATE requests SET quality_profile = ? WHERE quality_profile = ? AND status = 'pending'`},
		// Only downloads in flight, whose stall check and import still read the ref.
		{&out.Grabs, `UPDATE grabs SET quality_profile = ? WHERE quality_profile = ? AND status = 'grabbed'`},
	}
	for _, m := range moves {
		res, err := tx.ExecContext(ctx, m.query, to, from)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		*m.n = int(n)
	}
	// The default role moves in Service.Delete, through the settings service.
	_, err := tx.ExecContext(ctx, `DELETE FROM quality_profiles WHERE id = ?`, id)
	return err
}

// danglingRef matches a "custom:N" ref whose profile no longer exists. "n/a" and ""
// never match: those are deliberate markers, not broken references.
const danglingRef = `quality_profile LIKE 'custom:%'
	AND CAST(substr(quality_profile, 8) AS INTEGER) NOT IN (SELECT id FROM quality_profiles)`

// repointDangling sets every dangling ref in table (narrowed by an extra WHERE clause)
// to `to`, returning how many rows changed.
func (r *Repo) repointDangling(ctx context.Context, table, where, to string, args ...any) (int, error) {
	dangling := danglingRef
	if store.DialectOf(r.db) == store.Postgres {
		// Invalid historical references are dangling, not a cast error that
		// aborts reconciliation. Profile identities are signed 64-bit values.
		dangling = store.LikeASCII(r.db, "quality_profile", "'custom:%'", false) + ` AND
			(CASE WHEN pg_input_is_valid(substr(quality_profile, 8), 'bigint')
			THEN CAST(substr(quality_profile, 8) AS bigint) ELSE 0 END) NOT IN (SELECT id FROM quality_profiles)`
	}
	q := `UPDATE ` + table + ` SET quality_profile = ? WHERE ` + dangling
	if where != "" {
		q += ` AND ` + where
	}
	res, err := r.db.ExecContext(ctx, q, append([]any{to}, args...)...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// heldByRef counts the files kept out of upgrades (upgrade_hold) per stored profile ref, for
// a video media type: movies' default files and extra tracks, or episodes by their show's
// profile. Refs are as stored — the caller resolves "n/a" and dangling ones.
func (r *Repo) heldByRef(ctx context.Context, media string) (map[string]int, error) {
	var queries []string
	switch media {
	case MediaMovie:
		queries = []string{
			`SELECT quality_profile, COUNT(*) FROM movies WHERE upgrade_hold = 1 AND has_file = 1 GROUP BY quality_profile`,
			`SELECT quality_profile, COUNT(*) FROM movie_versions WHERE upgrade_hold = 1 AND has_file = 1 GROUP BY quality_profile`,
		}
	case MediaSeries:
		queries = []string{`SELECT s.quality_profile, COUNT(*) FROM episodes e JOIN series s ON s.id = e.series_id
			WHERE e.upgrade_hold = 1 AND e.has_file = 1 GROUP BY s.quality_profile`}
	}
	out := map[string]int{}
	for _, q := range queries {
		if err := func() error {
			rows, err := r.db.QueryContext(ctx, q)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var ref string
				var n int
				if err := rows.Scan(&ref, &n); err != nil {
					return err
				}
				out[ref] += n
			}
			return rows.Err()
		}(); err != nil {
			return nil, err
		}
	}
	return out, nil
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

// marshalUnwanted encodes the Unwanted choices: "" for the defaults, so a profile that
// never touched them keeps following the shipped defaults as they evolve.
func marshalUnwanted(sp StoredProfile) string {
	u := sp.Unwanted.normalized()
	if u.IsDefault() {
		return ""
	}
	b, _ := json.Marshal(u)
	return string(b)
}

// marshalTiers encodes the release-group tiers: "" for the built-in suggestions (never set,
// or set to exactly them), so a profile that never changed them follows the defaults.
func marshalTiers(sp StoredProfile) string {
	if sp.GroupTiers == nil {
		return ""
	}
	g := sp.GroupTiers.normalized()
	if sameTiers(g, DefaultGroupTiers()) {
		return ""
	}
	b, _ := json.Marshal(g)
	return string(b)
}

// marshalExtra encodes the required formats and the ideal file ("" when not set up).
func marshalExtra(sp StoredProfile) (required, ideal string) {
	rq := sp.RequiredFormats
	if rq == nil {
		rq = []string{}
	}
	b, _ := json.Marshal(rq)
	if sp.Ideal != nil && !sp.Ideal.Empty() {
		i, _ := json.Marshal(sp.Ideal)
		ideal = string(i)
	}
	return string(b), ideal
}
