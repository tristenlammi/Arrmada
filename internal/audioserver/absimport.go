package audioserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite" // read the uploaded Audiobookshelf database
)

// Bringing people's places over from Audiobookshelf, so switching servers doesn't itself
// lose everyone's place. The admin uploads Audiobookshelf's database file
// (config/absdatabase.sqlite); users are matched by username, books by their folder and
// then by title and author. Nothing is written until the admin confirms, and a place
// Arrmada already has that's newer is never replaced.

// ImportPreview describes what an import would do.
type ImportPreview struct {
	Users          []ImportUser `json:"users"`
	Progress       int          `json:"progress"`        // saved places found
	Matched        int          `json:"matched"`         // of those, on books Arrmada has
	Bookmarks      int          `json:"bookmarks"`       // bookmarks on matched books
	UnmatchedBooks []string     `json:"unmatched_books"` // titles with progress that Arrmada doesn't have
}

// ImportUser is one Audiobookshelf account and the Arrmada account it maps to.
type ImportUser struct {
	ABSID        string `json:"abs_id"`
	ABSUsername  string `json:"abs_username"`
	UserID       int64  `json:"user_id"` // 0 = not matched; skipped unless the admin picks one
	Username     string `json:"username,omitempty"`
	ProgressRows int    `json:"progress_rows"`
}

// ImportResult is what an import did.
type ImportResult struct {
	Imported  int `json:"imported"`
	Kept      int `json:"kept"` // Arrmada already had a newer place
	Bookmarks int `json:"bookmarks"`
	Skipped   int `json:"skipped"` // unmatched user or book
}

type absProgress struct {
	userID     string
	mediaID    string
	duration   float64
	current    float64
	finished   bool
	finishedAt int64
	updatedAt  int64
}

type absBook struct {
	mediaID string
	itemID  string
	path    string
	isFile  bool
	title   string
	author  string
	itemKey string // matched Arrmada item
}

type absData struct {
	users     []ImportUser
	progress  []absProgress
	books     map[string]*absBook // by media id
	bookmarks map[string][]absBookmark
}

type absBookmark struct {
	LibraryItemID string  `json:"libraryItemId"`
	Title         string  `json:"title"`
	Time          float64 `json:"time"`
	CreatedAt     int64   `json:"createdAt"`
}

// PreviewImport reads an Audiobookshelf database and reports what would be imported.
func (s *Server) PreviewImport(ctx context.Context, dbPath string) (ImportPreview, error) {
	d, err := s.readABS(ctx, dbPath)
	if err != nil {
		return ImportPreview{}, err
	}
	var p ImportPreview
	p.Users = d.users
	unmatched := map[string]bool{}
	for _, pr := range d.progress {
		p.Progress++
		b := d.books[pr.mediaID]
		if b != nil && b.itemKey != "" {
			p.Matched++
		} else if b != nil {
			unmatched[b.title] = true
		}
	}
	byItem := map[string]string{}
	for _, b := range d.books {
		if b.itemKey != "" {
			byItem[b.itemID] = b.itemKey
		}
	}
	for _, list := range d.bookmarks {
		for _, bm := range list {
			if byItem[bm.LibraryItemID] != "" {
				p.Bookmarks++
			}
		}
	}
	for t := range unmatched {
		p.UnmatchedBooks = append(p.UnmatchedBooks, t)
	}
	return p, nil
}

// ApplyImport imports places and bookmarks. userMap overrides the username matching
// (Audiobookshelf user id → Arrmada user id; 0 skips that user).
func (s *Server) ApplyImport(ctx context.Context, dbPath string, userMap map[string]int64) (ImportResult, error) {
	d, err := s.readABS(ctx, dbPath)
	if err != nil {
		return ImportResult{}, err
	}
	target := map[string]int64{}
	for _, u := range d.users {
		target[u.ABSID] = u.UserID
		if v, ok := userMap[u.ABSID]; ok {
			target[u.ABSID] = v
		}
	}
	var res ImportResult
	for _, pr := range d.progress {
		uid := target[pr.userID]
		b := d.books[pr.mediaID]
		if uid == 0 || b == nil || b.itemKey == "" {
			res.Skipped++
			continue
		}
		ok, err := s.listen.ImportProgress(ctx, uid, b.itemKey, pr.current, pr.duration, pr.finished, pr.finishedAt, pr.updatedAt)
		if err != nil {
			return res, err
		}
		if ok {
			res.Imported++
		} else {
			res.Kept++
		}
	}
	byItem := map[string]string{}
	for _, b := range d.books {
		if b.itemKey != "" {
			byItem[b.itemID] = b.itemKey
		}
	}
	for absUser, list := range d.bookmarks {
		uid := target[absUser]
		if uid == 0 {
			continue
		}
		for _, bm := range list {
			key := byItem[bm.LibraryItemID]
			if key == "" {
				continue
			}
			if _, err := s.listen.AddBookmark(ctx, uid, key, bm.Time, bm.Title); err == nil {
				res.Bookmarks++
			}
		}
	}
	s.log.Info("audiobook server: imported from Audiobookshelf", "places", res.Imported, "kept_newer", res.Kept,
		"bookmarks", res.Bookmarks, "skipped", res.Skipped)
	return res, nil
}

func (s *Server) readABS(ctx context.Context, dbPath string) (*absData, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("no uploaded database: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='mediaProgresses'`).Scan(&n); err != nil || n == 0 {
		return nil, fmt.Errorf("that doesn't look like an Audiobookshelf database (no mediaProgresses table)")
	}
	d := &absData{books: map[string]*absBook{}, bookmarks: map[string][]absBookmark{}}

	// Users, matched to Arrmada accounts by username.
	rows, err := db.QueryContext(ctx, `SELECT id, username, COALESCE(bookmarks, '[]') FROM users`)
	if err != nil {
		return nil, fmt.Errorf("reading users: %w", err)
	}
	for rows.Next() {
		var id, name, bms string
		if rows.Scan(&id, &name, &bms) != nil {
			continue
		}
		u := ImportUser{ABSID: id, ABSUsername: name}
		if au, err := s.users.UserByUsername(ctx, name); err == nil {
			u.UserID, u.Username = au.ID, au.Username
		}
		d.users = append(d.users, u)
		var list []absBookmark
		if json.Unmarshal([]byte(bms), &list) == nil && len(list) > 0 {
			d.bookmarks[id] = list
		}
	}
	rows.Close()

	// Books: the library item (path) joined to the book (title, author).
	rows, err = db.QueryContext(ctx, `SELECT li.id, li.mediaId, COALESCE(li.path, ''), COALESCE(li.isFile, 0),
		COALESCE(b.title, li.title, ''), COALESCE(li.authorNamesFirstLast, '')
		FROM libraryItems li LEFT JOIN books b ON b.id = li.mediaId WHERE li.mediaType = 'book'`)
	if err != nil {
		// Older databases lack title/authorNamesFirstLast on libraryItems.
		rows, err = db.QueryContext(ctx, `SELECT li.id, li.mediaId, COALESCE(li.path, ''), COALESCE(li.isFile, 0),
			COALESCE(b.title, ''), '' FROM libraryItems li LEFT JOIN books b ON b.id = li.mediaId WHERE li.mediaType = 'book'`)
		if err != nil {
			return nil, fmt.Errorf("reading books: %w", err)
		}
	}
	for rows.Next() {
		var b absBook
		var isFile int
		if rows.Scan(&b.itemID, &b.mediaID, &b.path, &isFile, &b.title, &b.author) != nil {
			continue
		}
		b.isFile = isFile != 0
		d.books[b.mediaID] = &b
	}
	rows.Close()

	// Saved places.
	rows, err = db.QueryContext(ctx, `SELECT userId, mediaItemId, COALESCE(duration, 0), COALESCE(currentTime, 0),
		COALESCE(isFinished, 0), COALESCE(finishedAt, ''), COALESCE(updatedAt, '')
		FROM mediaProgresses WHERE mediaItemType = 'book'`)
	if err != nil {
		return nil, fmt.Errorf("reading progress: %w", err)
	}
	for rows.Next() {
		var p absProgress
		var fin int
		var finAt, upd string
		if rows.Scan(&p.userID, &p.mediaID, &p.duration, &p.current, &fin, &finAt, &upd) != nil {
			continue
		}
		p.finished = fin != 0
		p.finishedAt, p.updatedAt = parseABSTime(finAt), parseABSTime(upd)
		d.progress = append(d.progress, p)
	}
	rows.Close()

	if err := s.matchABSBooks(ctx, d); err != nil {
		return nil, err
	}
	for i := range d.users {
		for _, p := range d.progress {
			if p.userID == d.users[i].ABSID {
				d.users[i].ProgressRows++
			}
		}
	}
	return d, nil
}

// matchABSBooks finds each Audiobookshelf book among Arrmada's audiobooks: by its
// author/title folders first (the mount points differ between the two apps, so only
// the last two folder names are compared), then by title and author.
func (s *Server) matchABSBooks(ctx context.Context, d *absData) error {
	items, err := s.items(ctx)
	if err != nil {
		return err
	}
	byFolder := map[string]string{}
	byTitleAuthor := map[string]string{}
	byTitle := map[string][]string{}
	for _, it := range items {
		dir := it.Path
		if st, err := os.Stat(dir); err == nil && !st.IsDir() {
			dir = filepath.Dir(dir)
		}
		byFolder[folderKey(dir)] = it.Key
		byTitleAuthor[norm(it.Title)+"|"+norm(it.Book.Author)] = it.Key
		byTitle[norm(it.Title)] = append(byTitle[norm(it.Title)], it.Key)
	}
	for _, b := range d.books {
		dir := b.path
		if b.isFile {
			dir = filepath.Dir(dir)
		}
		if k := byFolder[folderKey(dir)]; k != "" {
			b.itemKey = k
			continue
		}
		if k := byTitleAuthor[norm(b.title)+"|"+norm(b.author)]; k != "" {
			b.itemKey = k
			continue
		}
		if ks := byTitle[norm(b.title)]; len(ks) == 1 {
			b.itemKey = ks[0]
		}
	}
	return nil
}

// folderKey is the last two folder names, normalised ("Matt Dinniman/Dungeon Crawler Carl").
func folderKey(dir string) string {
	dir = filepath.ToSlash(strings.TrimRight(dir, `/\`))
	parts := strings.Split(dir, "/")
	if len(parts) >= 2 {
		return norm(parts[len(parts)-2]) + "/" + norm(parts[len(parts)-1])
	}
	return norm(dir)
}

func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseABSTime reads Sequelize's SQLite date text ("2025-03-01 10:20:30.123 +00:00") or
// a number of milliseconds.
func parseABSTime(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999 -07:00", "2006-01-02 15:04:05 -07:00",
		"2006-01-02 15:04:05.999", "2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli()
		}
	}
	var ms int64
	if _, err := fmt.Sscan(s, &ms); err == nil && ms > 1e11 {
		return ms
	}
	return 0
}
