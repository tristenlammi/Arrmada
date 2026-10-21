package music

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Reads and writes of the music model (MUS-28). Every read here assembles what it returns
// in a fixed number of queries, however many discs, tracks or files an album has: a
// 200-track box set costs the same four queries as a single.

const editionCols = `id, album_id, mbid, title, COALESCE(artist_credit_id, 0), status, release_date, country,
	label, catalog_number, barcode, packaging, disambiguation, media_summary, track_count, disc_count,
	source, legacy, tracks_fetched, fetched_at, stale_since`

func scanEdition(row interface{ Scan(...any) error }) (Edition, error) {
	var (
		r                  Edition
		legacy, fetched    int
		fetchedAt, staleAt sql.NullString
	)
	if err := row.Scan(&r.ID, &r.AlbumID, &r.MBID, &r.Title, &r.CreditID, &r.Status, &r.ReleaseDate, &r.Country,
		&r.Label, &r.CatalogNumber, &r.Barcode, &r.Packaging, &r.Disambiguation, &r.MediaSummary,
		&r.TrackCount, &r.DiscCount, &r.Source, &legacy, &fetched, &fetchedAt, &staleAt); err != nil {
		return Edition{}, err
	}
	r.Legacy, r.TracksFetched = legacy != 0, fetched != 0
	r.FetchedAt, r.StaleSince = fetchedAt.String, staleAt.String
	return r, nil
}

// fileCols are a music_track_files row's columns, in scanFile's order.
const fileCols = `id, album_id, COALESCE(release_track_id, 0), path, size_bytes, mtime_unix, container, codec,
	lossless, bitrate_kbps, bitrate_mode, sample_rate_hz, bit_depth, channels, duration_ms, quality,
	probed_at, tag_state, source_release, download_hash, missing_since, added_at`

func scanFile(row interface{ Scan(...any) error }) (TrackFile, error) {
	var (
		f                        TrackFile
		lossless                 int
		quality                  string
		probed, missing, addedAt sql.NullString
	)
	if err := row.Scan(&f.ID, &f.AlbumID, &f.ReleaseTrackID, &f.Path, &f.SizeBytes, &f.MtimeUnix, &f.Container,
		&f.Codec, &lossless, &f.BitrateKbps, &f.BitrateMode, &f.SampleRateHz, &f.BitDepth, &f.Channels,
		&f.DurationMs, &quality, &probed, &f.TagState, &f.SourceRelease, &f.DownloadHash, &missing, &addedAt); err != nil {
		return TrackFile{}, err
	}
	f.Lossless, f.Quality = lossless != 0, Quality(quality)
	f.ProbedAt, f.MissingSince, f.AddedAt = probed.String, missing.String, addedAt.String
	return f, nil
}

// Editions lists an album's editions (summaries, no track lists), the target first.
func (r *Repo) Editions(ctx context.Context, albumID int64) ([]Edition, error) {
	var target sql.NullInt64
	if err := r.db.QueryRowContext(ctx, `SELECT target_release_id FROM albums WHERE id = ?`, albumID).Scan(&target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+editionCols+` FROM music_releases WHERE album_id = ? ORDER BY release_date = '', release_date, id`, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Edition
	for rows.Next() {
		rel, err := scanEdition(rows)
		if err != nil {
			return nil, err
		}
		rel.Target = target.Valid && rel.ID == target.Int64
		out = append(out, rel)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Target && !out[j].Target })
	return out, nil
}

// Edition reads one edition in full: its discs, each disc's tracks with their recording,
// and each track's file. Four queries, whatever its size.
func (r *Repo) Edition(ctx context.Context, releaseID int64) (Edition, error) {
	rel, err := scanEdition(r.db.QueryRowContext(ctx, `SELECT `+editionCols+` FROM music_releases WHERE id = ?`, releaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return Edition{}, ErrNotFound
	}
	if err != nil {
		return Edition{}, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, release_id, position, format, title, track_count FROM music_media WHERE release_id = ? ORDER BY position`, releaseID)
	if err != nil {
		return Edition{}, err
	}
	byMedium := map[int64]int{}
	for rows.Next() {
		var m Medium
		if err := rows.Scan(&m.ID, &m.ReleaseID, &m.Position, &m.Format, &m.Title, &m.TrackCount); err != nil {
			rows.Close()
			return Edition{}, err
		}
		byMedium[m.ID] = len(rel.Media)
		rel.Media = append(rel.Media, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Edition{}, err
	}

	files, err := r.AlbumFiles(ctx, rel.AlbumID)
	if err != nil {
		return Edition{}, err
	}
	fileOf := make(map[int64]*TrackFile, len(files))
	for i := range files {
		if files[i].ReleaseTrackID != 0 {
			fileOf[files[i].ReleaseTrackID] = &files[i]
		}
	}
	rows, err = r.db.QueryContext(ctx, `
		SELECT rt.id, rt.medium_id, rt.recording_id, rec.mbid, rt.mbid, rt.position, rt.number, rt.title,
		       COALESCE(rt.artist_credit_id, 0), rt.length_ms, rt.monitored
		  FROM music_media m
		  JOIN music_release_tracks rt ON rt.medium_id = m.id
		  JOIN music_recordings rec ON rec.id = rt.recording_id
		 WHERE m.release_id = ?
		 ORDER BY m.position, rt.position`, releaseID)
	if err != nil {
		return Edition{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var t ReleaseTrack
		var mon int
		if err := rows.Scan(&t.ID, &t.MediumID, &t.RecordingID, &t.RecordingMBID, &t.MBID, &t.Position, &t.Number,
			&t.Title, &t.CreditID, &t.LengthMs, &mon); err != nil {
			return Edition{}, err
		}
		t.Monitored = mon != 0
		t.File = fileOf[t.ID]
		if i, ok := byMedium[t.MediumID]; ok {
			rel.Media[i].Tracks = append(rel.Media[i].Tracks, t)
		}
	}
	return rel, rows.Err()
}

// AlbumFiles lists every file row of an album: its target edition's, other editions', and
// its extras (files on no track of the target edition), by path.
func (r *Repo) AlbumFiles(ctx context.Context, albumID int64) ([]TrackFile, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+fileCols+` FROM music_track_files WHERE album_id = ? ORDER BY path`, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackFile
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Credits reads the given credits with their names, in one query.
func (r *Repo) Credits(ctx context.Context, ids []int64) (map[int64]Credit, error) {
	out := map[int64]Credit{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			args = append(args, id)
		}
	}
	if len(args) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT c.id, c.display, n.artist_id, a.mbid, n.credited_name, n.join_phrase
		  FROM music_artist_credits c
		  JOIN music_artist_credit_names n ON n.credit_id = c.id
		  JOIN artists a ON a.id = n.artist_id
		 WHERE c.id IN (?`+strings.Repeat(", ?", len(args)-1)+`)
		 ORDER BY c.id, n.position`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var display string
		var n CreditName
		if err := rows.Scan(&id, &display, &n.ArtistID, &n.ArtistMBID, &n.Name, &n.JoinPhrase); err != nil {
			return nil, err
		}
		c := out[id]
		c.ID, c.Display = id, display
		c.Names = append(c.Names, n)
		out[id] = c
	}
	return out, rows.Err()
}

// ensureCreditTx stores a credit (or finds the identical one already stored) and returns
// its id. Every name must name an artist row.
func ensureCreditTx(ctx context.Context, tx *store.Transaction, names []CreditName) (int64, error) {
	if len(names) == 0 {
		return 0, errors.New("music: a credit needs at least one artist")
	}
	key := CreditKey(names)
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM music_artist_credits WHERE credit_key = ?`, key).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO music_artist_credits (display, credit_key) VALUES (?, ?) RETURNING id`,
		CreditDisplay(names), key).Scan(&id); err != nil {
		return 0, err
	}
	for i, n := range names {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO music_artist_credit_names (credit_id, position, artist_id, credited_name, join_phrase)
			 VALUES (?, ?, ?, ?, ?)`, id, i, n.ArtistID, n.Name, n.JoinPhrase); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// artistCreditTx is an artist's own one-name credit (credited under its own name), stored
// on first use.
func artistCreditTx(ctx context.Context, tx *store.Transaction, artistID int64) (int64, error) {
	var n CreditName
	if err := tx.QueryRowContext(ctx, `SELECT id, mbid, name FROM artists WHERE id = ?`, artistID).
		Scan(&n.ArtistID, &n.ArtistMBID, &n.Name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return ensureCreditTx(ctx, tx, []CreditName{n})
}

// EditionInput is an edition to store: the release's own facts and its track list.
type EditionInput struct {
	Edition Edition // AlbumID, ID and Media are ignored; Title and CreditID default to the album's
	Tracks  []EditionTrack
}

// EditionTrack is one track of an EditionInput.
type EditionTrack struct {
	Disc          int // from 1
	DiscFormat    string
	DiscTitle     string
	Position      int
	Number        string // as printed; the position when empty
	Title         string
	MBID          string // the release track's MBID
	RecordingMBID string
	LengthMs      int
	CreditID      int64 // 0: the release's credit
	// Credit is the track's own credit by MBIDs (MUS-30), stored when CreditID is 0; an
	// empty one means the release's credit.
	Credit      []CreditInput
	ISRCs       []string // the recording's ISRCs, kept in music_external_ids
	Unmonitored bool
}

// AddEdition stores an edition of an album with its discs, recordings and tracks, in one
// transaction, and returns its id. It doesn't make it the target (SetTargetEdition does).
func (r *Repo) AddEdition(ctx context.Context, albumID int64, in EditionInput) (int64, error) {
	var id int64
	err := store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		var err error
		id, err = addEditionTx(ctx, tx, albumID, in)
		return err
	})
	return id, err
}

func addEditionTx(ctx context.Context, tx *store.Transaction, albumID int64, in EditionInput) (int64, error) {
	var albumTitle string
	var albumCredit sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT title, artist_credit_id FROM albums WHERE id = ?`, albumID).
		Scan(&albumTitle, &albumCredit); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	rel := in.Edition
	if rel.Title == "" {
		rel.Title = albumTitle
	}
	credit := nullID(rel.CreditID)
	if rel.CreditID == 0 {
		credit = albumCredit
	}
	if rel.Source == "" {
		rel.Source = "musicbrainz"
	}
	if rel.MBID != "" {
		// MusicBrainz releases belong to one release group, so an MBID already stored
		// elsewhere is a data problem upstream; store this edition without it rather than
		// fail the listing.
		var other int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM music_releases WHERE mbid = ?`, rel.MBID).Scan(&other); err == nil {
			rel.MBID = ""
		} else if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	discs := map[int]bool{}
	for _, t := range in.Tracks {
		discs[discOf(t.Disc)] = true
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO music_releases (album_id, mbid, title, artist_credit_id, status, release_date, country, label,
		                            catalog_number, barcode, packaging, disambiguation, media_summary, track_count,
		                            disc_count, source, legacy, tracks_fetched, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		RETURNING id`,
		albumID, rel.MBID, rel.Title, credit, rel.Status, rel.ReleaseDate, rel.Country, rel.Label,
		rel.CatalogNumber, rel.Barcode, rel.Packaging, rel.Disambiguation, rel.MediaSummary, len(in.Tracks),
		len(discs), rel.Source, b2i(rel.Legacy), b2i(rel.TracksFetched || len(in.Tracks) > 0)).Scan(&id); err != nil {
		return 0, err
	}
	if err := putTracksTx(ctx, tx, id, in.Tracks); err != nil {
		return 0, err
	}
	return id, nil
}

// putTracksTx writes an edition's tracks: a track already at a (disc, position) takes the
// listing's title, MBID and length and keeps its recording, monitoring and file; a new one
// gets its recording (shared by MBID when it has one) and starts monitored. The edition's
// and its discs' counts are brought up to date.
func putTracksTx(ctx context.Context, tx *store.Transaction, releaseID int64, tracks []EditionTrack) error {
	media := map[int]int64{}
	for _, t := range tracks {
		disc := discOf(t.Disc)
		if _, ok := media[disc]; ok {
			continue
		}
		var mid int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO music_media (release_id, position, format, title) VALUES (?, ?, ?, ?)
			ON CONFLICT(release_id, position) DO UPDATE SET
			  format = CASE WHEN excluded.format != '' THEN excluded.format ELSE music_media.format END,
			  title = CASE WHEN excluded.title != '' THEN excluded.title ELSE music_media.title END
			RETURNING id`, releaseID, disc, t.DiscFormat, t.DiscTitle).Scan(&mid); err != nil {
			return err
		}
		media[disc] = mid
	}
	for _, t := range tracks {
		mid := media[discOf(t.Disc)]
		number := t.Number
		if number == "" {
			number = fmt.Sprint(t.Position)
		}
		if t.CreditID == 0 && len(t.Credit) > 0 {
			id, err := creditFromInputsTx(ctx, tx, t.Credit)
			if err != nil {
				return err
			}
			t.CreditID = id
		}
		var existing, recID int64
		err := tx.QueryRowContext(ctx, `SELECT id, recording_id FROM music_release_tracks WHERE medium_id = ? AND position = ?`,
			mid, t.Position).Scan(&existing, &recID)
		switch {
		case err == nil:
			if _, err := tx.ExecContext(ctx,
				`UPDATE music_release_tracks SET mbid = ?, number = ?, title = ?, length_ms = ?,
				        artist_credit_id = COALESCE(?, artist_credit_id) WHERE id = ?`,
				t.MBID, number, t.Title, t.LengthMs, nullID(t.CreditID), existing); err != nil {
				return err
			}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		default:
			if recID, err = recordingTx(ctx, tx, t); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO music_release_tracks (medium_id, recording_id, mbid, position, number, title, artist_credit_id,
				                                  length_ms, monitored)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				mid, recID, t.MBID, t.Position, number, t.Title, nullID(t.CreditID), t.LengthMs, b2i(!t.Unmonitored)); err != nil {
				return err
			}
		}
		for _, isrc := range t.ISRCs {
			if isrc = strings.ToUpper(strings.TrimSpace(isrc)); isrc == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO music_external_ids (entity_type, entity_id, source, value) VALUES ('recording', ?, 'isrc', ?)
				ON CONFLICT DO NOTHING`, recID, isrc); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE music_media SET track_count = (SELECT COUNT(*) FROM music_release_tracks rt WHERE rt.medium_id = music_media.id)
		 WHERE release_id = ?`, releaseID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE music_releases SET
		  track_count = (SELECT COALESCE(SUM(track_count), 0) FROM music_media WHERE release_id = music_releases.id),
		  disc_count = (SELECT COUNT(*) FROM music_media WHERE release_id = music_releases.id),
		  tracks_fetched = 1
		 WHERE id = ?`, releaseID)
	return err
}

// recordingTx is the recording a new track points at: the stored one with its MBID, or a
// new one.
func recordingTx(ctx context.Context, tx *store.Transaction, t EditionTrack) (int64, error) {
	var id int64
	if t.RecordingMBID != "" {
		err := tx.QueryRowContext(ctx, `
			INSERT INTO music_recordings (mbid, title, artist_credit_id, length_ms) VALUES (?, ?, ?, ?)
			ON CONFLICT(mbid) WHERE mbid != '' DO UPDATE SET
			  length_ms = CASE WHEN excluded.length_ms > 0 THEN excluded.length_ms ELSE music_recordings.length_ms END
			RETURNING id`, t.RecordingMBID, t.Title, nullID(t.CreditID), t.LengthMs).Scan(&id)
		return id, err
	}
	err := tx.QueryRowContext(ctx,
		`INSERT INTO music_recordings (title, artist_credit_id, length_ms) VALUES (?, ?, ?) RETURNING id`,
		t.Title, nullID(t.CreditID), t.LengthMs).Scan(&id)
	return id, err
}

// SetTargetEdition makes an edition the album's target (the one its completeness and
// imports are measured against); pinned keeps automatic switching (MUS-30/31) off it. The
// edition must be the album's own.
func (r *Repo) SetTargetEdition(ctx context.Context, albumID, releaseID int64, pinned bool) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE albums SET target_release_id = ?, edition_pinned = ?
		 WHERE id = ? AND EXISTS (SELECT 1 FROM music_releases r WHERE r.id = ? AND r.album_id = albums.id)`,
		releaseID, b2i(pinned), albumID, releaseID)
	return affected(res, err)
}

// ExternalIDs lists the identifiers stored for one entity.
func (r *Repo) ExternalIDs(ctx context.Context, entityType string, id int64) ([]ExternalID, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT entity_type, entity_id, source, value FROM music_external_ids
		 WHERE entity_type = ? AND entity_id = ? ORDER BY source, value`, entityType, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExternalID
	for rows.Next() {
		var e ExternalID
		if err := rows.Scan(&e.EntityType, &e.EntityID, &e.Source, &e.Value); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddExternalID stores an identifier for an entity; storing it twice is a no-op.
func (r *Repo) AddExternalID(ctx context.Context, e ExternalID) error {
	if e.EntityType == "" || e.Source == "" || strings.TrimSpace(e.Value) == "" {
		return errors.New("music: an external id needs a type, a source and a value")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO music_external_ids (entity_type, entity_id, source, value) VALUES (?, ?, ?, ?)
		ON CONFLICT DO NOTHING`, e.EntityType, e.EntityID, e.Source, strings.TrimSpace(e.Value))
	return err
}

// MigrationNotes lists what the model migration couldn't place one-to-one for an album
// (two tracks claiming one file, a file flag with no path...), oldest first.
func (r *Repo) MigrationNotes(ctx context.Context, albumID int64) ([]MigrationNote, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, COALESCE(legacy_track_id, 0), path, kind, note, resolved_at IS NOT NULL FROM music_migration_notes
		 WHERE album_id = ? ORDER BY id`, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MigrationNote
	for rows.Next() {
		var n MigrationNote
		if err := rows.Scan(&n.ID, &n.LegacyTrackID, &n.Path, &n.Kind, &n.Note, &n.Resolved); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MigrationNote is one thing the model migration kept aside rather than dropping.
type MigrationNote struct {
	ID int64 `json:"id"`
	// LegacyTrackID is the old track's id, which its release track kept.
	LegacyTrackID int64  `json:"legacy_track_id,omitempty"`
	Path          string `json:"path,omitempty"`
	Kind          string `json:"kind"`
	Note          string `json:"note"`
	Resolved      bool   `json:"resolved,omitempty"`
}

// discOf is a listing's disc number, 1 when it doesn't say.
func discOf(d int) int {
	if d < 1 {
		return 1
	}
	return d
}

// nullID stores 0 as NULL, for the optional foreign keys.
func nullID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: id > 0} }
