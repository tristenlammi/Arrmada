package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/listening"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Merging two rows that are one book.
//
// Nothing merges by itself (dedupe.go): a person picks the row to keep in the duplicates
// review and Merge folds the other into it. It loses nothing. Every edition, audiobook
// version, listening place, bookmark, request, catalogue key, timeline entry, grab and
// blocklist row the dropped row had ends up on the kept one, all in one transaction —
// a failure anywhere leaves both rows exactly as they were. Merge never touches a file:
// an ebook the kept book already has a copy of stays on disk and is reported.

// ErrMergeSame is returned when asked to merge a book into itself.
var ErrMergeSame = errors.New("a book can't be merged into itself")

// ErrMergeUnavailable is returned when the listening store isn't wired in: merging then
// would orphan everyone's places in the dropped book, so it is refused instead.
var ErrMergeUnavailable = errors.New("merging isn't available: the listening store isn't connected")

// MergedCopyLabel names the audio version a dropped book's audiobook becomes when the
// kept book already has one.
const MergedCopyLabel = "Merged copy"

// ItemRekeyer moves listening rows (places, bookmarks, sessions, timelines) from one
// audiobook item key to another inside a transaction. listening.Store is the one;
// tests pass a fake, so books needn't run the listening store to test a merge.
type ItemRekeyer interface {
	// HoldReports keeps sync writes out from before the transaction starts until after it
	// commits, so a report that read a place under the old key can't write it back there.
	HoldReports() (release func())
	RekeyItemsTx(ctx context.Context, tx *store.Transaction, moves map[string]string) (listening.RekeyCounts, error)
}

// MergeAnnouncer queues the follow-up work for the kept book (the audiobook catalogue
// refresh, requesters' "ready") inside the merge transaction, so it can't be lost.
type MergeAnnouncer func(ctx context.Context, q store.Execer, keepID int64) error

// SetMergeHooks wires what Merge needs from outside the package: the listening store
// that re-keys places, and the outbox that announces the kept book.
func (s *Service) SetMergeHooks(items ItemRekeyer, announce MergeAnnouncer) {
	s.items, s.announceMerge = items, announce
}

// MergeOptions tune one merge.
type MergeOptions struct {
	// Editions reports which editions a book quality profile wants
	// (quality.Service.BookEditions). With it, the timeline notes an edition the dropped
	// book wanted that the kept book's profile doesn't; nil skips the note.
	Editions func(ctx context.Context, profile string) (ebook, audiobook bool)
}

// MergeResult says what a merge did. Counts and the kept book's own paths only: nothing
// in it says who listened to what.
type MergeResult struct {
	KeepID     int64  `json:"keep_id"`
	DropID     int64  `json:"drop_id"`
	DropTitle  string `json:"drop_title"`
	MovedEbook bool   `json:"moved_ebook"`
	// MovedAudiobook: the dropped book's audiobook became the kept book's (it had none).
	MovedAudiobook bool `json:"moved_audiobook"`
	// MergedCopyID is the audio version the dropped audiobook became when the kept book
	// already had one (0 = none).
	MergedCopyID int64 `json:"merged_copy_id,omitempty"`
	// Versions is how many of the dropped book's audio versions moved over.
	Versions int `json:"versions"`
	// LeftFiles are files of the dropped book left on disk untracked: an ebook the kept
	// book already has. Nothing is ever deleted.
	LeftFiles []string `json:"left_files"`
	// Note is anything the person should know (an edition only the dropped book wanted).
	Note string `json:"note,omitempty"`
	// Moved counts the rows re-pointed per table.
	Moved map[string]int `json:"moved"`
	// Listening counts the listening rows moved. For the log only: never serialised, so
	// no staff screen learns even how many people had listened to a book.
	Listening listening.RekeyCounts `json:"-"`
}

// Merge folds dropID into keepID in one transaction (see the top of this file) and
// returns what moved. The caller needn't publish anything afterwards: the announcer
// queues the kept book's follow-up work inside the same transaction.
func (s *Service) Merge(ctx context.Context, keepID, dropID int64, opts MergeOptions) (MergeResult, error) {
	res := MergeResult{KeepID: keepID, DropID: dropID, LeftFiles: []string{}, Moved: map[string]int{}}
	if keepID == dropID {
		return res, ErrMergeSame
	}
	if s.items == nil {
		return res, ErrMergeUnavailable
	}
	// The edition note needs the quality profiles, which are read before the
	// transaction: nothing but this merge's own writes should run inside it.
	note, err := s.editionNote(ctx, keepID, dropID, opts)
	if err != nil {
		return res, err
	}
	res.Note = note

	release := s.items.HoldReports()
	defer release()
	err = store.WithTx(ctx, s.repo.db, func(tx *store.Transaction) error {
		res.Moved = map[string]int{}
		res.LeftFiles = []string{}
		return s.mergeTx(ctx, tx, keepID, dropID, &res)
	})
	if err != nil {
		return MergeResult{KeepID: keepID, DropID: dropID, LeftFiles: []string{}, Moved: map[string]int{}}, err
	}
	s.forgetUnmatched(dropID) // the upgrade banner mustn't list a row that's gone
	// Ids and counts only: which books, never who listened to them.
	s.log.Info("books: merged", "keep_id", keepID, "drop_id", dropID, "versions", res.Versions,
		"left_files", len(res.LeftFiles), "requests", res.Moved["requests"], "places", res.Listening.Progress,
		"place_conflicts", res.Listening.Conflicts, "bookmarks", res.Listening.Bookmarks)
	return res, nil
}

// mergeTx is Merge's transaction. Every statement goes through tx.
func (s *Service) mergeTx(ctx context.Context, tx *store.Transaction, keepID, dropID int64, res *MergeResult) error {
	keep, err := bookTx(ctx, tx, keepID)
	if err != nil {
		return err
	}
	drop, err := bookTx(ctx, tx, dropID)
	if err != nil {
		return err
	}
	res.DropTitle = drop.Title
	keepVersions, err := versionsTx(ctx, tx, keepID)
	if err != nil {
		return err
	}
	dropVersions, err := versionsTx(ctx, tx, dropID)
	if err != nil {
		return err
	}
	moves := map[string]string{}

	// 1. The ebook: moved when the kept book has none, otherwise left on disk.
	if drop.Ebook != nil {
		if keep.Ebook == nil {
			if err := setEditionTx(ctx, tx, keepID, KindEbook, *drop.Ebook); err != nil {
				return err
			}
			res.MovedEbook = true
		} else {
			res.LeftFiles = append(res.LeftFiles, drop.Ebook.Path)
		}
	}

	// 2. The audiobook: moved when the kept book has none, otherwise kept as an audio
	// version of the kept book, unmonitored so the sweep never searches for it.
	// A narrator typed for the dropped audiobook travels with the file in both branches.
	dropMain := listening.ItemKey(dropID, 0)
	moves[dropMain] = listening.ItemKey(keepID, 0)
	taken := map[string]bool{}
	for _, v := range keepVersions {
		taken[strings.ToLower(v.Label)] = true
	}
	if drop.Audiobook != nil {
		if keep.Audiobook == nil {
			if err := setEditionTx(ctx, tx, keepID, KindAudiobook, *drop.Audiobook); err != nil {
				return err
			}
			// A typed narrator is a hand edit, with the files' tags ('') underneath, so
			// Reset (or clearing it) hands the moved audiobook back to its own tags.
			if drop.AudiobookNarrator != "" {
				edits := `json_set(edits_json, '$.narrator', '')`
				if store.DialectOf(tx) == store.Postgres {
					if err := store.SetJSONFields(ctx, tx, "books", "edits_json", keepID, map[string]any{"narrator": ""}); err != nil {
						return err
					}
					edits = "edits_json"
				}
				now := time.Now().UnixMilli()
				if _, err := tx.ExecContext(ctx,
					`UPDATE books SET audiobook_narrator = ?, edits_json = `+edits+`,
					        edited_at = CASE WHEN ? > edited_at THEN ? ELSE edited_at + 1 END WHERE id = ?`,
					drop.AudiobookNarrator, now, now, keepID); err != nil {
					return err
				}
			}
			res.MovedAudiobook = true
		} else {
			label := uniqueLabel(MergedCopyLabel, taken)
			res.MergedCopyID, err = store.InsertID(ctx, tx,
				`INSERT INTO book_audio_versions (book_id, label, terms, monitored, path, format, size, files, narrator)
				 VALUES (?, ?, '', 0, ?, ?, ?, ?, ?)`,
				keepID, label, drop.Audiobook.Path, drop.Audiobook.Format, drop.Audiobook.SizeBytes, max1(drop.Audiobook.FileCount),
				drop.AudiobookNarrator)
			if err != nil {
				return err
			}
			moves[dropMain] = listening.ItemKey(keepID, res.MergedCopyID)
		}
	}

	// 3. The dropped book's own audio versions move as they are (ids kept), renamed only
	// when the kept book already has a version by that name.
	for _, v := range dropVersions {
		label := uniqueLabel(v.Label, taken)
		if _, err := tx.ExecContext(ctx, `UPDATE book_audio_versions SET book_id = ?, label = ? WHERE id = ?`,
			keepID, label, v.ID); err != nil {
			return err
		}
		moves[listening.ItemKey(dropID, v.ID)] = listening.ItemKey(keepID, v.ID)
		res.Versions++
	}

	// 4. Everyone's places, bookmarks, sessions and timelines follow their items, and the
	// old ids redirect for apps that still have them.
	if res.Listening, err = s.items.RekeyItemsTx(ctx, tx, moves); err != nil {
		return fmt.Errorf("move listening places: %w", err)
	}

	// 5. Everything else that names the dropped row.
	for _, rp := range []struct{ name, q string }{
		{"requests", `UPDATE requests SET book_id = ? WHERE book_id = ?`},
		{"aliases", `UPDATE book_keys SET book_id = ? WHERE book_id = ?`},
		{"events", `UPDATE book_events SET book_id = ? WHERE book_id = ?`},
		{"grabs", `UPDATE grabs SET movie_id = ? WHERE media_type = 'book' AND movie_id = ?`},
		{"blocklist", `UPDATE blocklist SET movie_id = ? WHERE media_type = 'book' AND movie_id = ?`},
		{"reviews", `UPDATE import_reviews SET expected_id = ? WHERE media_type = 'book' AND expected_id = ?`},
		{"search_attempts", `UPDATE search_attempts SET media_id = ? WHERE media_type = 'book' AND media_id = ?`},
	} {
		r, err := tx.ExecContext(ctx, rp.q, keepID, dropID)
		if err != nil {
			return fmt.Errorf("re-point %s: %w", rp.name, err)
		}
		n, _ := r.RowsAffected()
		res.Moved[rp.name] = int(n)
	}
	if err := carryDupeReviewsTx(ctx, tx, keepID, dropID); err != nil {
		return fmt.Errorf("re-point duplicate reviews: %w", err)
	}

	// 6. What the kept book lacks, it takes from the dropped one.
	if err := carryOverTx(ctx, tx, keep, drop); err != nil {
		return err
	}

	// 7. The dropped row goes; its key stays one of the kept book's, so whatever still
	// carries it (a Discover card, a request) finds the kept book.
	if _, err := tx.ExecContext(ctx, `DELETE FROM books WHERE id = ?`, dropID); err != nil {
		return err
	}
	if err := addKey(ctx, tx, drop.OLKey, keepID, ""); err != nil {
		return fmt.Errorf("keep the dropped book's key: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO book_events (book_id, event, detail) VALUES (?, 'merged', ?)`,
		keepID, mergedDetail(drop, *res)); err != nil {
		return err
	}
	if s.announceMerge != nil {
		if err := s.announceMerge(ctx, tx, keepID); err != nil {
			return fmt.Errorf("queue the kept book's refresh: %w", err)
		}
	}
	return nil
}

// carryOverTx gives the kept book what it lacks from the dropped one, as the old fold
// did: monitored if either was, and the series, description, cover, subjects and
// author row when the kept book has none. The kept book's quality profile stays.
//
// Gaps are filled with the dropped book's CATALOGUE values (drop.Matched()), never its hand
// edits, which travel separately (carryEditsTx); and a detail the kept book edited by hand
// is never filled, even when the edit is empty (a series deliberately cleared).
func carryOverTx(ctx context.Context, tx *store.Transaction, keep, drop Book) error {
	dm := drop.Matched()
	monitored := keep.Monitored || drop.Monitored
	filled := keep // the kept book after the gaps are filled, for carryEditsTx
	seriesName, seriesPos, seriesKey := keep.SeriesName, keep.SeriesPosition, keep.SeriesKey
	if !keep.Edited(FieldSeriesName) && strings.TrimSpace(seriesName) == "" && strings.TrimSpace(dm.SeriesName) != "" {
		seriesName, seriesKey = dm.SeriesName, dm.SeriesKey
		if !keep.Edited(FieldSeriesPosition) {
			seriesPos = dm.SeriesPosition
		}
	}
	filled.SeriesName, filled.SeriesPosition = seriesName, seriesPos
	description := keep.Description
	if !keep.Edited(FieldDescription) {
		description = orStr(keep.Description, dm.Description)
	}
	filled.Description = description
	cover := orStr(keep.CoverURL, drop.CoverURL)
	subjects := keep.Subjects
	if len(subjects) == 0 && !keep.Edited(FieldSubjects) {
		subjects = dm.Subjects
	}
	filled.Subjects = subjects
	// The author row: the kept book's, else the dropped one's (NULL when neither has one).
	var authorID any
	if keep.AuthorID > 0 {
		authorID = keep.AuthorID
	} else if drop.AuthorID > 0 {
		authorID = drop.AuthorID
	}
	// The release day (BOOK-18): the kept book's, else the dropped one's. meta_checked_at
	// deliberately stays the kept book's own: the stamp says when the KEPT key was looked
	// up, and an empty one only means the refresh job looks at it soon.
	releaseDate := orStr(keep.ReleaseDate, drop.ReleaseDate)
	if _, err := tx.ExecContext(ctx,
		`UPDATE books SET monitored = ?, series_name = ?, series_position = ?, series_key = ?,
		        description = ?, cover_url = ?, subjects_json = ?, release_date = ?, author_id = ? WHERE id = ?`,
		b2i(monitored), seriesName, seriesPos, seriesKey, description, cover, subjectsJSON(subjects), cleanReleaseDate(releaseDate), authorID, keep.ID); err != nil {
		return err
	}
	return carryEditsTx(ctx, tx, filled, drop)
}

// carryEditsTx gives the kept book the dropped one's hand edits (0334_book_edits) for the
// fields the kept book hasn't edited itself: an edit is something the person typed, and a
// merge shouldn't lose it. What goes underneath is the kept book's catalogue value — its
// snapshot from before the merge with carryOverTx's gaps filled from the dropped book's
// catalogue values, never from an edit — so Reset brings the catalogue's back. The kept
// book's own edits win, and the edits_json read here is the kept book's current one, so a
// narrator edit step 2 just wrote stays.
//
// The narrator isn't carried: it belongs to the dropped book's audiobook and travels with
// that file (step 2), as the moved standard audiobook's narrator or the merged copy's.
// Put on the kept book, it would name the wrong reader for the kept book's own audiobook.
func carryEditsTx(ctx context.Context, tx *store.Transaction, keep Book, drop Book) error {
	if len(drop.Edits) == 0 {
		return nil
	}
	cur, err := getTx(ctx, tx, keep.ID)
	if err != nil {
		return err
	}
	edits := Edits{}
	for k, v := range cur.Edits {
		edits[k] = v
	}
	var sets []string
	var args []any
	for _, f := range editFields {
		if f.name == FieldNarrator || !drop.Edited(f.name) || keep.Edited(f.name) {
			continue
		}
		edits[f.name] = f.get(&keep)
		sets = append(sets, f.column+" = ?")
		args = append(args, f.arg(f.get(&drop)))
	}
	if len(sets) == 0 {
		return nil
	}
	raw, err := encodeEdits(edits)
	if err != nil {
		return err
	}
	sets = append(sets, "edits_json = ?", "edited_at = "+store.ScalarMax(tx, "?", "edited_at + 1"))
	args = append(args, raw, time.Now().UnixMilli(), keep.ID)
	_, err = tx.ExecContext(ctx, `UPDATE books SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	return err
}

// editionNote says when the dropped book wanted an edition the kept book's profile
// doesn't, so the person can widen the profile.
func (s *Service) editionNote(ctx context.Context, keepID, dropID int64, opts MergeOptions) (string, error) {
	keep, err := s.repo.Get(ctx, keepID)
	if err != nil {
		return "", err
	}
	drop, err := s.repo.Get(ctx, dropID)
	if err != nil {
		return "", err
	}
	if opts.Editions == nil || keep.QualityProfile == drop.QualityProfile {
		return "", nil
	}
	ke, ka := opts.Editions(ctx, keep.QualityProfile)
	de, da := opts.Editions(ctx, drop.QualityProfile)
	var missing []string
	if de && !ke {
		missing = append(missing, "the ebook")
	}
	if da && !ka {
		missing = append(missing, "the audiobook")
	}
	if len(missing) == 0 {
		return "", nil
	}
	return fmt.Sprintf("“%s” also wanted %s, which the kept book's profile doesn't; change its profile to search for it.",
		drop.Title, strings.Join(missing, " and ")), nil
}

// mergedDetail is the kept book's timeline entry for a merge.
func mergedDetail(drop Book, res MergeResult) string {
	var moved []string
	if res.MovedEbook {
		moved = append(moved, "the ebook")
	}
	if res.MovedAudiobook {
		moved = append(moved, "the audiobook")
	}
	if res.MergedCopyID > 0 {
		moved = append(moved, "the audiobook as a “"+MergedCopyLabel+"” version")
	}
	if res.Versions == 1 {
		moved = append(moved, "1 audio version")
	} else if res.Versions > 1 {
		moved = append(moved, fmt.Sprintf("%d audio versions", res.Versions))
	}
	d := fmt.Sprintf("Merged “%s” (%s)", drop.Title, drop.OLKey)
	if len(moved) > 0 {
		d += ": moved " + strings.Join(moved, ", ")
	}
	if len(res.LeftFiles) > 0 {
		d += "; left on disk: " + strings.Join(res.LeftFiles, ", ")
	}
	if res.Note != "" {
		d += ". " + res.Note
	}
	return d
}

// uniqueLabel returns label, or "label 2", "label 3"… when the book already has a
// version by that name, and marks the one returned as taken.
func uniqueLabel(label string, taken map[string]bool) string {
	out := label
	for n := 2; taken[strings.ToLower(out)]; n++ {
		out = fmt.Sprintf("%s %d", label, n)
	}
	taken[strings.ToLower(out)] = true
	return out
}

// bookTx loads one book row inside a transaction.
func bookTx(ctx context.Context, tx *store.Transaction, id int64) (Book, error) {
	return getTx(ctx, tx, id)
}

// versionsTx loads a book's audio versions inside a transaction.
func versionsTx(ctx context.Context, tx *store.Transaction, bookID int64) ([]AudioVersion, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+versionCols+` FROM book_audio_versions WHERE book_id = ? ORDER BY id`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AudioVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// setEditionTx records an edition's file on a book inside a transaction.
func setEditionTx(ctx context.Context, tx *store.Transaction, id int64, kind string, f BookFile) error {
	col := "ebook"
	if kind == KindAudiobook {
		col = "audiobook"
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE books SET `+col+`_path = ?, `+col+`_format = ?, `+col+`_size = ?, `+col+`_files = ?, has_file = 1 WHERE id = ?`,
		f.Path, f.Format, f.SizeBytes, max1(f.FileCount), id)
	return err
}

func subjectsJSON(subjects []string) string {
	if len(subjects) == 0 {
		return ""
	}
	raw, err := json.Marshal(subjects)
	if err != nil {
		return ""
	}
	return string(raw)
}
