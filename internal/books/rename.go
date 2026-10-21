package books

import (
	"context"
	"database/sql"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// PathMove is one file a rename moved on disk.
type PathMove struct {
	From, To string
}

// RepointEdition records that a book edition's files were moved by a rename: the edition
// (versionID 0) or audiobook version now lives at path — its file, or its folder when it
// has several — and every moved file's probed audio details (audio_file_meta, keyed by
// path) follow it, so the audiobook server knows its length and chapters without probing
// it again, and keeps the file id listening apps already hold (the row's ino). One
// transaction with the timeline entry and the end of the rename's journal entry, so the
// record never describes half a rename.
//
// Listening places, bookmarks and history are keyed by the book's (and version's) id, not
// by a path, so a rename leaves them exactly as they were. meta_updated_at moves, which
// tells listening apps to re-read the item — by hand as well as by the path trigger, since
// a rename that only renames a multi-file audiobook's tracks leaves the folder path as it
// was.
func (s *Service) RepointEdition(ctx context.Context, bookID, versionID int64, kind, path string, moves []PathMove, detail string) error {
	return store.WithTx(ctx, s.repo.db, func(tx *store.Transaction) error {
		var (
			res sql.Result
			err error
		)
		if versionID > 0 {
			res, err = tx.ExecContext(ctx, `UPDATE book_audio_versions SET path = ? WHERE id = ? AND book_id = ?`, path, versionID, bookID)
		} else {
			col := "ebook_path"
			if kind == KindAudiobook {
				col = "audiobook_path"
			}
			res, err = tx.ExecContext(ctx, `UPDATE books SET `+col+` = ? WHERE id = ?`, path, bookID)
		}
		if err := affected(res, err); err != nil {
			return err
		}
		for _, m := range moves {
			if m.From == m.To {
				continue
			}
			// A stale destination gives way only when the source metadata exists.
			// Delete then update under the writer lock retains the source's ino,
			// matching SQLite's former UPDATE OR REPLACE behavior on both engines.
			if _, err := tx.ExecContext(ctx, `DELETE FROM audio_file_meta WHERE path = ?
				AND EXISTS (SELECT 1 FROM audio_file_meta source WHERE source.path = ?)`, m.To, m.From); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE audio_file_meta SET path = ? WHERE path = ?`, m.To, m.From); err != nil {
				return err
			}
		}
		now := time.Now().UnixMilli()
		if _, err := tx.ExecContext(ctx,
			`UPDATE books SET meta_updated_at = CASE WHEN ? > meta_updated_at THEN ? ELSE meta_updated_at + 1 END WHERE id = ?`, now, now, bookID); err != nil {
			return err
		}
		if err := clearJournal(ctx, tx, bookID, versionID, kind); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO book_events (book_id, event, detail) VALUES (?, ?, ?)`, bookID, "renamed", detail)
		return err
	})
}

// JournalRename writes down the moves a rename is about to make, before the first one,
// with the edition's recorded path before (oldPath) and after (newPath). RepointEdition
// ends the entry with the record; ClearRenameJournal ends it when the rename was undone.
// An entry still here at start-up is a rename a crash cut short.
func (s *Service) JournalRename(ctx context.Context, bookID, versionID int64, kind, oldPath, newPath string, moves []PathMove) error {
	now := time.Now().UnixMilli()
	return store.WithTx(ctx, s.repo.db, func(tx *store.Transaction) error {
		if err := clearJournal(ctx, tx, bookID, versionID, kind); err != nil {
			return err
		}
		for i, m := range moves {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO book_rename_journal (book_id, version_id, kind, seq, from_path, to_path, old_edition_path, new_edition_path, created_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				bookID, versionID, kind, i, m.From, m.To, oldPath, newPath, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// clearJournal ends an edition's journal entry inside tx (or straight away with s.repo.db).
func clearJournal(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, bookID, versionID int64, kind string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM book_rename_journal WHERE book_id = ? AND version_id = ? AND kind = ?`, bookID, versionID, kind)
	return err
}

// FileID is an audiobook file's id for listening apps, to keep (see PinFileIDs).
type FileID struct {
	Path, ID string
}

// PinFileIDs stores the id of each file that has none stored yet — a file probed before
// ids were stored, or never listed — before a rename moves it, so the id it was always
// given (from its old name) is the one it keeps. A stored id is never changed.
func (s *Service) PinFileIDs(ctx context.Context, ids []FileID) error {
	return store.WithTx(ctx, s.repo.db, func(tx *store.Transaction) error {
		for _, f := range ids {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO audio_file_meta (path, size, mtime, ino) VALUES (?, -1, 0, ?)
				 ON CONFLICT(path) DO UPDATE SET ino = excluded.ino WHERE audio_file_meta.ino = ''`, f.Path, f.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// ClearRenameJournal ends an edition's journal entry (the rename was undone).
func (s *Service) ClearRenameJournal(ctx context.Context, bookID, versionID int64, kind string) error {
	return clearJournal(ctx, s.repo.db, bookID, versionID, kind)
}

// UnfinishedRename is one edition's rename a crash cut short: the moves it planned, in
// order. Its record still names the old place (the record and the end of the entry are
// one transaction), so the way back is to undo the moves that happened.
type UnfinishedRename struct {
	BookID, VersionID int64
	Kind              string
	// OldPath and NewPath are the edition's recorded path before and after the rename.
	OldPath, NewPath string
	Moves            []PathMove
}

// UnfinishedRenames lists the journal's entries.
func (s *Service) UnfinishedRenames(ctx context.Context) ([]UnfinishedRename, error) {
	rows, err := s.repo.db.QueryContext(ctx,
		`SELECT book_id, version_id, kind, from_path, to_path, old_edition_path, new_edition_path
		   FROM book_rename_journal ORDER BY book_id, version_id, kind, seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnfinishedRename
	for rows.Next() {
		var (
			bookID, versionID int64
			kind, oldP, newP  string
			m                 PathMove
		)
		if err := rows.Scan(&bookID, &versionID, &kind, &m.From, &m.To, &oldP, &newP); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].BookID != bookID || out[n-1].VersionID != versionID || out[n-1].Kind != kind {
			out = append(out, UnfinishedRename{BookID: bookID, VersionID: versionID, Kind: kind, OldPath: oldP, NewPath: newP})
		}
		out[len(out)-1].Moves = append(out[len(out)-1].Moves, m)
	}
	return out, rows.Err()
}
