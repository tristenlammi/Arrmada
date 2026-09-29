package audioserver

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// A database laid out like Audiobookshelf's: users (with bookmarks), library items with
// their paths (under Audiobookshelf's own mount, /audiobooks), books and saved places.
func TestImportFromAudiobookshelf(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "absdatabase.sqlite")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE users (id TEXT, username TEXT, bookmarks JSON)`,
		`CREATE TABLE libraryItems (id TEXT, mediaId TEXT, mediaType TEXT, path TEXT, isFile INTEGER, title TEXT, authorNamesFirstLast TEXT)`,
		`CREATE TABLE books (id TEXT, title TEXT)`,
		`CREATE TABLE mediaProgresses (id TEXT, userId TEXT, mediaItemId TEXT, mediaItemType TEXT, duration REAL, currentTime REAL,
		   isFinished INTEGER, finishedAt TEXT, updatedAt TEXT)`,
		`INSERT INTO users VALUES ('u-1', 'reader', '[{"libraryItemId":"li-1","title":"Loot box","time":4321,"createdAt":1}]'),
		                         ('u-2', 'someone-else', '[]')`,
		`INSERT INTO libraryItems VALUES ('li-1', 'bk-1', 'book', '/audiobooks/Matt Dinniman/Dungeon Crawler Carl', 0, 'Dungeon Crawler Carl', 'Matt Dinniman'),
		                                ('li-2', 'bk-2', 'book', '/audiobooks/Nobody/Missing Book', 0, 'Missing Book', 'Nobody')`,
		`INSERT INTO books VALUES ('bk-1', 'Dungeon Crawler Carl'), ('bk-2', 'Missing Book')`,
		`INSERT INTO mediaProgresses VALUES ('p1', 'u-1', 'bk-1', 'book', 36000, 12345.6, 0, NULL, '2026-08-01 10:00:00.000 +00:00'),
		                                   ('p2', 'u-1', 'bk-2', 'book', 1000, 10, 0, NULL, '2026-08-01 10:00:00.000 +00:00'),
		                                   ('p3', 'u-2', 'bk-1', 'book', 36000, 50, 0, NULL, '2026-08-01 10:00:00.000 +00:00')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	pv, err := h.srv.PreviewImport(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Progress != 3 || pv.Matched != 2 || pv.Bookmarks != 1 || len(pv.UnmatchedBooks) != 1 {
		t.Fatalf("preview: %+v", pv)
	}
	var reader, other *ImportUser
	for i := range pv.Users {
		switch pv.Users[i].ABSUsername {
		case "reader":
			reader = &pv.Users[i]
		case "someone-else":
			other = &pv.Users[i]
		}
	}
	if reader == nil || reader.UserID == 0 || other == nil || other.UserID != 0 {
		t.Fatalf("user matching: %+v", pv.Users)
	}

	res, err := h.srv.ApplyImport(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 1 || res.Bookmarks != 1 || res.Skipped != 2 {
		t.Fatalf("import result: %+v", res)
	}
	key := itemKeyFor(h.book.ID, 0)
	p, ok, _ := h.srv.listen.Progress(ctx, reader.UserID, key)
	if !ok || p.Position < 12345 || p.Position > 12346 {
		t.Fatalf("imported place: %+v", p)
	}
	// Running it again keeps the place (not newer) rather than duplicating anything.
	res, _ = h.srv.ApplyImport(ctx, path, nil)
	if res.Imported != 0 || res.Kept != 1 {
		t.Fatalf("second import: %+v", res)
	}
}
