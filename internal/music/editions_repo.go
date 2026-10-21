package music

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Storing editions and moving file rows between them (MUS-30). Rows only: no file on
// disk is ever touched here.

// CreditInput is one artist of a credit as MusicBrainz gives it, by MBID. An artist that
// isn't stored yet (featured on one song, say) gets a row outside the library
// (in_library = 0), so the credit can name it.
type CreditInput struct {
	ArtistMBID string
	ArtistName string // the artist's own name
	SortName   string
	Name       string // as credited
	JoinPhrase string
}

// EditionSummary is an edition as one browse call describes it: its facts and its discs,
// no tracks.
type EditionSummary struct {
	Edition Edition // MBID, Title, Status, ReleaseDate, Country, Label, CatalogNumber, Barcode, Packaging, Disambiguation, MediaSummary
	Credit  []CreditInput
	Media   []Medium // Position, Format, Title, TrackCount
}

// ErrEditionNotListed is returned when an edition's tracks aren't stored yet.
var ErrEditionNotListed = errors.New("that edition's tracks aren't known yet")

// creditFromInputsTx stores a credit given by MBIDs (finding or adding each artist) and
// returns its id; 0 for an empty credit.
func creditFromInputsTx(ctx context.Context, tx *store.Transaction, parts []CreditInput) (int64, error) {
	if len(parts) == 0 {
		return 0, nil
	}
	names := make([]CreditName, 0, len(parts))
	for _, p := range parts {
		mbid := strings.TrimSpace(p.ArtistMBID)
		if mbid == "" {
			return 0, nil // a credit we can't key isn't stored: the edition's or album's stands
		}
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM artists WHERE mbid = ?`, mbid).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			name := p.ArtistName
			if name == "" {
				name = p.Name
			}
			err = tx.QueryRowContext(ctx, `
				INSERT INTO artists (mbid, name, sort_name, monitored, in_library) VALUES (?, ?, ?, 0, 0)
				RETURNING id`, mbid, name, p.SortName).Scan(&id)
		}
		if err != nil {
			return 0, err
		}
		credited := p.Name
		if credited == "" {
			credited = p.ArtistName
		}
		names = append(names, CreditName{ArtistID: id, ArtistMBID: mbid, Name: credited, JoinPhrase: p.JoinPhrase})
	}
	return ensureCreditTx(ctx, tx, names)
}

// StoreEditionSummaries stores every edition of an album a browse returned: a new one is
// added with its discs (no tracks yet), one already stored has its facts brought up to
// date, and its discs too while its tracks aren't. The album is stamped as listed.
// Editions are matched by MBID; one whose MBID another album already holds is skipped.
func (r *Repo) StoreEditionSummaries(ctx context.Context, albumID int64, eds []EditionSummary) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		var albumTitle string
		var albumCredit sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT title, artist_credit_id FROM albums WHERE id = ?`, albumID).
			Scan(&albumTitle, &albumCredit); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		for _, in := range eds {
			e := in.Edition
			if strings.TrimSpace(e.MBID) == "" {
				continue
			}
			if e.Title == "" {
				e.Title = albumTitle
			}
			if e.Source == "" {
				e.Source = "musicbrainz"
			}
			credit, err := creditFromInputsTx(ctx, tx, in.Credit)
			if err != nil {
				return err
			}
			creditVal := nullID(credit)
			if credit == 0 {
				creditVal = albumCredit
			}
			tracks := 0
			for _, m := range in.Media {
				tracks += m.TrackCount
			}
			var id, owner int64
			var fetched int
			err = tx.QueryRowContext(ctx, `SELECT id, album_id, tracks_fetched FROM music_releases WHERE mbid = ?`, e.MBID).
				Scan(&id, &owner, &fetched)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				if err := tx.QueryRowContext(ctx, `
					INSERT INTO music_releases (album_id, mbid, title, artist_credit_id, status, release_date, country, label,
					                            catalog_number, barcode, packaging, disambiguation, media_summary,
					                            track_count, disc_count, source, tracks_fetched, fetched_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, CURRENT_TIMESTAMP) RETURNING id`,
					albumID, e.MBID, e.Title, creditVal, e.Status, e.ReleaseDate, e.Country, e.Label, e.CatalogNumber,
					e.Barcode, e.Packaging, e.Disambiguation, e.MediaSummary, tracks, len(in.Media), e.Source).Scan(&id); err != nil {
					return err
				}
				fetched = 0
			case err != nil:
				return err
			case owner != albumID:
				continue // MusicBrainz files a release under one group; this one is another album's
			default:
				if _, err := tx.ExecContext(ctx, `
					UPDATE music_releases SET title = ?, artist_credit_id = COALESCE(?, artist_credit_id), status = ?,
					       release_date = ?, country = ?, label = ?, catalog_number = ?, barcode = ?, packaging = ?,
					       disambiguation = ?, media_summary = ?, stale_since = NULL
					 WHERE id = ?`,
					e.Title, nullID(credit), e.Status, e.ReleaseDate, e.Country, e.Label, e.CatalogNumber, e.Barcode,
					e.Packaging, e.Disambiguation, e.MediaSummary, id); err != nil {
					return err
				}
			}
			if fetched != 0 {
				continue // the discs and counts come from the stored track list
			}
			for _, m := range in.Media {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO music_media (release_id, position, format, title, track_count) VALUES (?, ?, ?, ?, ?)
					ON CONFLICT(release_id, position) DO UPDATE SET
					  format = excluded.format, title = excluded.title, track_count = excluded.track_count`,
					id, discOf(m.Position), m.Format, m.Title, m.TrackCount); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE music_releases SET track_count = ?, disc_count = ? WHERE id = ?`, tracks, len(in.Media), id); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE albums SET editions_checked_at = CURRENT_TIMESTAMP WHERE id = ?`, albumID)
		return err
	})
}

// StoreEditionTracks stores an edition's full track list (with credits and ISRCs) and
// its facts. A track already at a (disc, position) keeps its recording, monitoring and
// file (see putTracksTx).
func (r *Repo) StoreEditionTracks(ctx context.Context, releaseID int64, sum EditionSummary, tracks []EditionTrack) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM music_releases WHERE id = ?`, releaseID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		credit, err := creditFromInputsTx(ctx, tx, sum.Credit)
		if err != nil {
			return err
		}
		e := sum.Edition
		if _, err := tx.ExecContext(ctx, `
			UPDATE music_releases SET artist_credit_id = COALESCE(?, artist_credit_id),
			       barcode = CASE WHEN ? != '' THEN ? ELSE barcode END,
			       label = CASE WHEN ? != '' THEN ? ELSE label END,
			       catalog_number = CASE WHEN ? != '' THEN ? ELSE catalog_number END,
			       fetched_at = CURRENT_TIMESTAMP, stale_since = NULL
			 WHERE id = ?`,
			nullID(credit), e.Barcode, e.Barcode, e.Label, e.Label, e.CatalogNumber, e.CatalogNumber, releaseID); err != nil {
			return err
		}
		return putTracksTx(ctx, tx, releaseID, tracks)
	})
}

// editionState is what EnsureEditions needs to know to decide whether it has work to do:
// when the album's editions were listed, and whether its target is the migration's legacy
// edition or still has no track list.
type editionState struct {
	CheckedAt     time.Time
	HasTarget     bool
	TargetLegacy  bool
	TargetFetched bool
}

func (r *Repo) editionState(ctx context.Context, albumID int64) (editionState, error) {
	var (
		st              editionState
		checked         sql.NullString
		legacy, fetched sql.NullInt64
		target          sql.NullInt64
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT al.editions_checked_at, al.target_release_id, r.legacy, r.tracks_fetched
		  FROM albums al LEFT JOIN music_releases r ON r.id = al.target_release_id
		 WHERE al.id = ?`, albumID).Scan(&checked, &target, &legacy, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return st, ErrNotFound
	}
	if err != nil {
		return st, err
	}
	if checked.Valid {
		st.CheckedAt = parseDBTime(checked.String)
	}
	st.HasTarget = target.Valid
	st.TargetLegacy = legacy.Int64 != 0
	st.TargetFetched = fetched.Int64 != 0
	return st, nil
}

// markEditionsChecked stamps when an album's editions were last looked for.
func (r *Repo) markEditionsChecked(ctx context.Context, albumID int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE albums SET editions_checked_at = ? WHERE id = ?`,
		at.UTC().Format("2006-01-02 15:04:05"), albumID)
	return err
}

// parseDBTime reads a SQLite CURRENT_TIMESTAMP ("2006-01-02 15:04:05") or RFC 3339 time.
func parseDBTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// editionTracks are an edition's tracks as identities, for moving files between
// editions.
func editionTracksTx(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, releaseID int64) ([]trackIdent, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT rt.id, rt.recording_id, rt.mbid, rec.mbid, m.position, rt.position, rt.title
		  FROM music_media m
		  JOIN music_release_tracks rt ON rt.medium_id = m.id
		  JOIN music_recordings rec ON rec.id = rt.recording_id
		 WHERE m.release_id = ? ORDER BY m.position, rt.position`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []trackIdent
	for rows.Next() {
		var t trackIdent
		if err := rows.Scan(&t.ID, &t.RecordingID, &t.MBID, &t.RecordingMBID, &t.Disc, &t.Position, &t.Title); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SwitchResult is what moving an album to another edition did.
type SwitchResult struct {
	From, To int64
	// Moved is how many file rows now sit on the new edition's tracks; Kept how many fit
	// none of its tracks and stay where they were (on the old edition's track, listed as
	// the album's extras), so switching back finds them again.
	Moved, Kept int
}

// SwitchEdition makes an edition the album's target and moves the album's file rows onto
// its tracks (mapTracks: by recording, release-track id, position and title, then title).
// Rows only: every file stays where it is on disk, and no file row is ever dropped: one
// that fits no track of the new edition keeps its old track. The monitoring of the old
// target's tracks carries over to the tracks they map to. pinned sets (or clears) the pin.
func (r *Repo) SwitchEdition(ctx context.Context, albumID, releaseID int64, pinned bool) (SwitchResult, error) {
	return r.switchEdition(ctx, albumID, releaseID, pinned, false)
}

// ErrWouldStrand is RekeyEdition refusing an edition some of the target's files fit no
// track of.
var ErrWouldStrand = errors.New("some of the album's files fit no track of that edition")

// RekeyEdition is SwitchEdition for the migration's legacy edition: it only switches when
// every file on the current target finds its track on the new edition, so a re-key never
// leaves the album reading as missing tracks it has (which the sweep would then search
// for). Otherwise it changes nothing and returns ErrWouldStrand.
func (r *Repo) RekeyEdition(ctx context.Context, albumID, releaseID int64) (SwitchResult, error) {
	return r.switchEdition(ctx, albumID, releaseID, false, true)
}

func (r *Repo) switchEdition(ctx context.Context, albumID, releaseID int64, pinned, allOrNothing bool) (SwitchResult, error) {
	res := SwitchResult{To: releaseID}
	err := store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		var owner int64
		if err := tx.QueryRowContext(ctx, `SELECT album_id FROM music_releases WHERE id = ?`, releaseID).Scan(&owner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if owner != albumID {
			return ErrNotFound
		}
		var cur sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT target_release_id FROM albums WHERE id = ?`, albumID).Scan(&cur); err != nil {
			return err
		}
		res.From = cur.Int64
		to, err := editionTracksTx(ctx, tx, releaseID)
		if err != nil {
			return err
		}
		if len(to) == 0 {
			return ErrEditionNotListed
		}
		// Tracks of the new edition that already hold a file (from an earlier switch) are
		// taken; the rest are free.
		taken := map[int64]bool{}
		type held struct {
			fileID int64
			track  trackIdent
			onCur  bool
		}
		var files []held
		rows, err := tx.QueryContext(ctx, `
			SELECT f.id, rt.id, rt.recording_id, rt.mbid, rec.mbid, m.position, rt.position, rt.title, m.release_id
			  FROM music_track_files f
			  JOIN music_release_tracks rt ON rt.id = f.release_track_id
			  JOIN music_recordings rec ON rec.id = rt.recording_id
			  JOIN music_media m ON m.id = rt.medium_id
			 WHERE f.album_id = ?
			 ORDER BY m.release_id, m.position, rt.position`, albumID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var h held
			var rel int64
			if err := rows.Scan(&h.fileID, &h.track.ID, &h.track.RecordingID, &h.track.MBID, &h.track.RecordingMBID,
				&h.track.Disc, &h.track.Position, &h.track.Title, &rel); err != nil {
				rows.Close()
				return err
			}
			if rel == releaseID {
				taken[h.track.ID] = true
				continue
			}
			h.onCur = cur.Valid && rel == cur.Int64
			files = append(files, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		free := make([]trackIdent, 0, len(to))
		for _, t := range to {
			if !taken[t.ID] {
				free = append(free, t)
			}
		}
		// The current target's files first, then other editions' (an earlier switch's
		// leftovers): the target's are the album as people know it.
		from := make([]trackIdent, 0, len(files))
		fileOf := map[int64]int64{}
		for pass := 0; pass < 2; pass++ {
			for _, h := range files {
				if h.onCur == (pass == 0) {
					from = append(from, h.track)
					fileOf[h.track.ID] = h.fileID
				}
			}
		}
		moved := mapTracks(from, free)
		var before albumTally
		if allOrNothing {
			for _, h := range files {
				if _, ok := moved[h.track.ID]; h.onCur && !ok {
					return ErrWouldStrand
				}
			}
			if cur.Valid {
				if before, err = tallyTx(ctx, tx, albumID, cur.Int64); err != nil {
					return err
				}
			}
		}
		for fromTrack, toTrack := range moved {
			if _, err := tx.ExecContext(ctx, `UPDATE music_track_files SET release_track_id = ? WHERE id = ?`,
				toTrack, fileOf[fromTrack]); err != nil {
				return fmt.Errorf("move a file row to the new edition: %w", err)
			}
		}
		res.Moved = len(moved)
		res.Kept = len(files) - len(moved)
		// Carry the old target's per-track monitoring over.
		if cur.Valid && cur.Int64 != releaseID {
			old, err := editionTracksTx(ctx, tx, cur.Int64)
			if err != nil {
				return err
			}
			mon := map[int64]int{}
			mrows, err := tx.QueryContext(ctx, `
				SELECT rt.id, rt.monitored FROM music_release_tracks rt JOIN music_media m ON m.id = rt.medium_id
				 WHERE m.release_id = ?`, cur.Int64)
			if err != nil {
				return err
			}
			for mrows.Next() {
				var id int64
				var m int
				if err := mrows.Scan(&id, &m); err != nil {
					mrows.Close()
					return err
				}
				mon[id] = m
			}
			mrows.Close()
			carried := map[int64]bool{}
			for fromTrack, toTrack := range mapTracks(old, to) {
				carried[toTrack] = true
				if _, err := tx.ExecContext(ctx, `UPDATE music_release_tracks SET monitored = ? WHERE id = ?`,
					mon[fromTrack], toTrack); err != nil {
					return err
				}
			}
			if allOrNothing {
				// A re-key only renames what the album already had: a track of the new
				// edition the old listing didn't have, and no file covers, isn't newly
				// wanted (a deluxe's bonus tracks don't start being searched for).
				filed := map[int64]bool{}
				for _, toTrack := range moved {
					filed[toTrack] = true
				}
				for id := range taken {
					filed[id] = true
				}
				for _, t := range to {
					if !carried[t.ID] && !filed[t.ID] {
						if _, err := tx.ExecContext(ctx, `UPDATE music_release_tracks SET monitored = 0 WHERE id = ?`, t.ID); err != nil {
							return err
						}
					}
				}
			}
		}
		if allOrNothing && cur.Valid {
			// And it never makes the album read as less complete than it was: no fewer
			// tracks held, no more wanted ones missing.
			after, err := tallyTx(ctx, tx, albumID, releaseID)
			if err != nil {
				return err
			}
			if after.have < before.have || after.tracks-after.have > before.tracks-before.have {
				return ErrWouldStrand
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE albums SET target_release_id = ?, edition_pinned = ? WHERE id = ?`,
			releaseID, b2i(pinned), albumID)
		return err
	})
	return res, err
}

// albumTally is what an edition says about an album's completeness: its wanted tracks
// and how many of those hold a file on disk.
type albumTally struct{ tracks, have int }

// tallyTx counts an edition of an album the way the library does (Repo.albumCounts):
// wanted tracks are monitored ones, or every one when the album isn't monitored.
func tallyTx(ctx context.Context, tx *store.Transaction, albumID, releaseID int64) (albumTally, error) {
	var t albumTally
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(EXISTS (SELECT 1 FROM music_track_files f
		                                       WHERE f.release_track_id = rt.id AND f.missing_since IS NULL)), 0)
		  FROM music_media m
		  JOIN music_release_tracks rt ON rt.medium_id = m.id
		  JOIN albums al ON al.id = ?
		 WHERE m.release_id = ? AND `+wantedTrack, albumID, releaseID).Scan(&t.tracks, &t.have)
	return t, err
}

// EditionFileCounts counts each of an album's editions' file rows (for hiding a legacy
// edition once nothing is left on it).
func (r *Repo) EditionFileCounts(ctx context.Context, albumID int64) (map[int64]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT m.release_id, COUNT(*) FROM music_track_files f
		  JOIN music_release_tracks rt ON rt.id = f.release_track_id
		  JOIN music_media m ON m.id = rt.medium_id
		 WHERE f.album_id = ? GROUP BY m.release_id`, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
