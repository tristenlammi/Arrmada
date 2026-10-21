// Package libfolders lets the Movies and TV libraries have more than one folder: a
// "Kids movies" or "Anime" folder next to the main one, often its own Plex or Jellyfin
// library.
//
// The folder picked in first-run setup and Settings → Library stays the default, resolved
// by internal/libroots exactly as before (setting, then ARRMADA_MOVIES_DIR / _TV_DIR).
// This package keeps the extra folders (table library_folders), decides which folder a new
// title goes to (the first extra whose rule matches, otherwise the default) and answers
// "which folder does this title, or this file, belong to".
//
// An install that never adds a folder has no rows here; every answer is then the default
// folder, and nothing in the UI changes.
package libfolders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/libroots"
	"github.com/tristenlammi/arrmada/internal/pathguard"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The libraries that can have more than one folder.
const (
	MediaMovie  = libroots.MediaMovie
	MediaSeries = libroots.MediaSeries
)

// DefaultID is the default folder (Settings → Library's), which has no row.
const DefaultID int64 = 0

// Folder is one folder of a library. ID 0 is the default folder.
type Folder struct {
	ID       int64  `json:"id"`
	Media    string `json:"media"`
	Path     string `json:"path"`
	Label    string `json:"label"`
	Rule     string `json:"rule"`
	Position int    `json:"position"`
	Default  bool   `json:"default"`
	// Downloads is the folder's own downloads folder ("" = the Downloads folder): torrent
	// grabs for its titles save there, on the same disk, so imports hardlink (GitHub #11).
	Downloads string `json:"downloads"`
}

// Errors the settings API turns into plain messages.
var (
	ErrNotFound   = errors.New("that library folder doesn't exist")
	ErrBadMedia   = errors.New("only Movies and TV can have more than one folder")
	ErrBadRule    = errors.New("unknown folder rule")
	ErrBadPath    = errors.New("invalid folder")
	ErrInUse      = errors.New("this folder still holds titles")
	ErrDuplicates = errors.New("that folder is already a library folder")
)

// UseError is a folder that can't be removed because titles still live in it.
type UseError struct {
	Titles int
	Files  int
}

func (e *UseError) Error() string {
	return fmt.Sprintf("%d title(s) still belong to this folder (%d file(s) are in it) — move them to another folder first, then remove it", e.Titles, e.Files)
}

func (e *UseError) Unwrap() error { return ErrInUse }

// ValidMedia reports whether media can have extra folders.
func ValidMedia(media string) bool { return media == MediaMovie || media == MediaSeries }

// Service keeps the extra folders. Reads are served from memory: every write goes through
// this service, which reloads the list right after it, so the copy is never stale. The
// default folder is never cached — it's read through libroots on every use.
type Service struct {
	db      store.Pool
	roots   *libroots.Roots
	dataDir string
	now     func() time.Time
	// set saves the default folders' downloads folders (settings); nil = can't.
	set SettingWriter

	mu     sync.RWMutex
	loaded bool
	extras []Folder // ordered by media, position, id

	// moveMu guards moving: the folders a move is putting files into right now (by id),
	// so removing one waits for no move and no move starts into one being removed.
	moveMu sync.Mutex
	moving map[int64]int
}

// ErrMoving is a folder a title is being moved into right now.
var ErrMoving = errors.New("a title is being moved into this folder right now — try again once that finishes")

// beginMove marks folder id as a move's destination until the returned func is called. It
// fails when the folder is gone (removed in the meantime): the database, not the cache,
// decides, under the same lock Remove holds, so the two can't cross.
func (s *Service) beginMove(ctx context.Context, media string, id int64) (func(), error) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	ok, err := folderExists(ctx, s.db, media, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	if s.moving == nil {
		s.moving = map[int64]int{}
	}
	s.moving[id]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.moveMu.Lock()
			if s.moving[id]--; s.moving[id] <= 0 {
				delete(s.moving, id)
			}
			s.moveMu.Unlock()
		})
	}, nil
}

// querier is a store.Pool or a *store.Transaction.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// folderExists reports whether folder id of library media is still there (0, the
// default folder, always is).
func folderExists(ctx context.Context, q querier, media string, id int64) (bool, error) {
	if id == DefaultID {
		return true, nil
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM library_folders WHERE id = ? AND media = ?`, id, media).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// New builds the service. roots resolves the default folders (and downloads, for the
// overlap checks); dataDir is Arrmada's own data folder, which no media folder may touch.
func New(db store.Pool, roots *libroots.Roots, dataDir string) *Service {
	return &Service{db: db, roots: roots, dataDir: dataDir, now: time.Now}
}

// load fills the in-memory list on first use. A read error leaves it unloaded so the next
// call tries again, and answers with no extras meanwhile (everything goes to the default).
func (s *Service) load(ctx context.Context) []Folder {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	if s.loaded {
		out := s.extras
		s.mu.RUnlock()
		return out
	}
	s.mu.RUnlock()
	if err := s.reload(ctx); err != nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.extras
}

func (s *Service) reload(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, media, path, label, rule, position, downloads_path FROM library_folders ORDER BY media, position, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var out []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.Media, &f.Path, &f.Label, &f.Rule, &f.Position, &f.Downloads); err != nil {
			return err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.extras, s.loaded = out, true
	s.mu.Unlock()
	return nil
}

// Extras is every extra folder in libroots' terms, for libroots.Roots.WithExtras: the
// recycle bins, Convert's holds, manual import, the download client checks and the
// health panel all see them through it.
func (s *Service) Extras(ctx context.Context) []libroots.Extra {
	var out []libroots.Extra
	for _, f := range s.load(ctx) {
		out = append(out, libroots.Extra{Media: f.Media, ID: f.ID, Path: f.Path, Label: s.labelOf(f), Downloads: f.Downloads})
	}
	return out
}

// labelOf is a folder's name for the UI: its label, else its last path segment.
func (s *Service) labelOf(f Folder) string {
	if l := strings.TrimSpace(f.Label); l != "" {
		return l
	}
	if f.Default {
		if f.Media == MediaSeries {
			return "TV"
		}
		return "Movies"
	}
	if b := filepath.Base(filepath.Clean(f.Path)); b != "." && b != string(filepath.Separator) {
		return b
	}
	return f.Path
}

// defaultPath is the library's default folder now.
func (s *Service) defaultPath(ctx context.Context, media string) string {
	if s == nil || s.roots == nil {
		return ""
	}
	if media == MediaSeries {
		return s.roots.TV(ctx)
	}
	return s.roots.Movies(ctx)
}

// List is every folder of a library: the default first, then the extras in rule order.
// The default is listed even when it resolves to nothing, so the UI can say so.
func (s *Service) List(ctx context.Context, media string) []Folder {
	out := []Folder{{ID: DefaultID, Media: media, Path: s.defaultPath(ctx, media), Default: true, Downloads: s.defaultDownloads(ctx, media)}}
	out[0].Label = s.labelOf(out[0])
	for _, f := range s.load(ctx) {
		if f.Media == media {
			f.Label = s.labelOf(f)
			out = append(out, f)
		}
	}
	return out
}

// Multi reports whether a library has more than one folder — the only time the UI shows
// folder pickers, folder names and "Move to folder".
func (s *Service) Multi(ctx context.Context, media string) bool {
	for _, f := range s.load(ctx) {
		if f.Media == media {
			return true
		}
	}
	return false
}

// Get returns one folder of a library (id 0 is the default).
func (s *Service) Get(ctx context.Context, media string, id int64) (Folder, bool) {
	for _, f := range s.List(ctx, media) {
		if f.ID == id {
			return f, true
		}
	}
	return Folder{}, false
}

// Path is where a title assigned to folder id lives. An id that no longer exists (the
// folder was removed outside the app) falls back to the default folder: the title then
// imports where every title used to, rather than nowhere.
func (s *Service) Path(ctx context.Context, media string, id int64) string {
	if id != DefaultID {
		for _, f := range s.load(ctx) {
			if f.ID == id && f.Media == media {
				return f.Path
			}
		}
	}
	return s.defaultPath(ctx, media)
}

// Paths is every folder of a library that resolves to something, the default first.
func (s *Service) Paths(ctx context.Context, media string) []string {
	var out []string
	for _, f := range s.List(ctx, media) {
		if strings.TrimSpace(f.Path) != "" {
			out = append(out, f.Path)
		}
	}
	return out
}

// Owner is the folder holding path — the longest folder of the library it lies in — and
// whether there is one. A path that is a folder itself doesn't count as inside it.
func (s *Service) Owner(ctx context.Context, media, path string) (Folder, bool) {
	var best Folder
	found := false
	for _, f := range s.List(ctx, media) {
		if strings.TrimSpace(f.Path) == "" || !strictlyUnder(path, f.Path) {
			continue
		}
		if !found || len(filepath.Clean(f.Path)) > len(filepath.Clean(best.Path)) {
			best, found = f, true
		}
	}
	return best, found
}

// OwnerID is Owner's id, or the default when no folder holds path.
func (s *Service) OwnerID(ctx context.Context, media, path string) int64 {
	if f, ok := s.Owner(ctx, media, path); ok {
		return f.ID
	}
	return DefaultID
}

// IDForRoot is the folder whose path is root, or the default when none is.
func (s *Service) IDForRoot(ctx context.Context, media, root string) int64 {
	for _, f := range s.List(ctx, media) {
		if strings.TrimSpace(f.Path) != "" && samePath(f.Path, root) {
			return f.ID
		}
	}
	return DefaultID
}

// Pick is the folder a new title goes to: the first extra folder, in order, whose rule
// matches what's known about the title; otherwise the default.
func (s *Service) Pick(ctx context.Context, media string, facts Facts) int64 {
	for _, f := range s.load(ctx) {
		if f.Media == media && Matches(f.Rule, media, facts) {
			return f.ID
		}
	}
	return DefaultID
}

// Valid reports whether id names a folder of the library (0 always does).
func (s *Service) Valid(ctx context.Context, media string, id int64) bool {
	_, ok := s.Get(ctx, media, id)
	return ok
}

// AddInput is a new extra folder.
type AddInput struct {
	Media string `json:"media"`
	Path  string `json:"path"`
	Label string `json:"label"`
	Rule  string `json:"rule"`
}

// Add saves an extra folder at the end of its library's order. The folder must already
// exist (it's never created — a mistyped path would otherwise fill the container's own
// disk), must not be or touch the data folder, and must not be, contain or sit inside any
// other library or the downloads folder: one file must belong to exactly one folder.
func (s *Service) Add(ctx context.Context, in AddInput) (Folder, error) {
	in.Path = strings.TrimSpace(in.Path)
	in.Label = strings.TrimSpace(in.Label)
	in.Rule = strings.TrimSpace(in.Rule)
	if !ValidMedia(in.Media) {
		return Folder{}, ErrBadMedia
	}
	if !ValidRule(in.Rule) {
		return Folder{}, ErrBadRule
	}
	if err := s.checkPath(ctx, in.Path, 0); err != nil {
		return Folder{}, err
	}
	clean := filepath.Clean(in.Path)
	var id int64
	err := store.WithTx(ctx, s.db, func(tx *store.Transaction) error {
		var pos int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM library_folders WHERE media = ?`, in.Media).Scan(&pos); err != nil {
			return err
		}
		var err error
		id, err = store.InsertID(ctx, tx, `INSERT INTO library_folders (media, path, label, rule, position, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			in.Media, clean, in.Label, in.Rule, pos, s.now().Unix())
		if err != nil {
			if store.IsUniqueViolation(err) {
				return ErrDuplicates
			}
			return err
		}
		return nil
	})
	if err != nil {
		return Folder{}, err
	}
	if err := s.reload(ctx); err != nil {
		return Folder{}, err
	}
	f, _ := s.Get(ctx, in.Media, id)
	return f, nil
}

// checkPath vets a folder path for an extra folder (self is the folder being edited, 0
// for a new one).
func (s *Service) checkPath(ctx context.Context, p string, self int64) error {
	if p == "" {
		return fmt.Errorf("%w: choose a folder", ErrBadPath)
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("%w: use the full path, starting with / (for example /storage/media/kids-movies)", ErrBadPath)
	}
	if libroots.UnderDataDir(p, s.dataDir) {
		return fmt.Errorf("%w: %s is Arrmada's own data folder (or inside or above it) — media must live on its own mount", ErrBadPath, p)
	}
	fi, err := os.Stat(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: %s doesn't exist inside the container — create it, and make sure it's inside a folder mounted into the container (docker-compose.override.yml)", ErrBadPath, p)
	case err != nil:
		return fmt.Errorf("%w: Arrmada can't open %s (%v)", ErrBadPath, p, err)
	case !fi.IsDir():
		return fmt.Errorf("%w: %s is a file, not a folder", ErrBadPath, p)
	}
	for _, o := range s.others(ctx, self) {
		if pathguard.Under(p, o.path) || pathguard.Under(o.path, p) {
			if samePath(p, o.path) {
				return fmt.Errorf("%w: %s is already the %s folder", ErrDuplicates, p, o.name)
			}
			return fmt.Errorf("%w: %s overlaps the %s folder (%s) — library folders can't sit inside each other", ErrBadPath, p, o.name, o.path)
		}
	}
	return nil
}

type namedPath struct{ name, path string }

// others is every folder an extra folder must keep clear of: each library's folders
// (default and extras, except self) and the downloads folder.
func (s *Service) others(ctx context.Context, self int64) []namedPath {
	var out []namedPath
	if s.roots != nil {
		for _, r := range []namedPath{
			{"Movies", s.roots.Movies(ctx)}, {"TV", s.roots.TV(ctx)}, {"Ebooks", s.roots.Ebooks(ctx)},
			{"Audiobooks", s.roots.Audiobooks(ctx)}, {"Music", s.roots.Music(ctx)}, {"Downloads", s.roots.Downloads(ctx)},
		} {
			if strings.TrimSpace(r.path) != "" {
				out = append(out, r)
			}
		}
	}
	for _, f := range s.load(ctx) {
		if f.ID != self {
			out = append(out, namedPath{s.labelOf(f), f.Path})
		}
	}
	// A library folder's own downloads folder is no place for another library either.
	for _, d := range s.ownDownloads(ctx) {
		out = append(out, namedPath{d.name, d.path})
	}
	return out
}

// Conflict says why p can't become a library's default folder because of an extra
// folder ("" when it can). Settings → Library calls it before saving a new default, so
// the default can't land on, in or around an extra folder.
func (s *Service) Conflict(ctx context.Context, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	for _, f := range s.load(ctx) {
		if pathguard.Under(p, f.Path) || pathguard.Under(f.Path, p) {
			return fmt.Sprintf("%s overlaps the extra library folder %q (%s) — library folders can't sit inside each other", p, s.labelOf(f), f.Path)
		}
	}
	return ""
}

// LibraryConflict is Conflict for a library folder (not the Downloads folder): it also
// can't land on, in or around a library folder's own downloads folder, where torrents
// seed and the import scans of finished downloads look.
func (s *Service) LibraryConflict(ctx context.Context, p string) string {
	if msg := s.Conflict(ctx, p); msg != "" {
		return msg
	}
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	for _, d := range s.ownDownloads(ctx) {
		if pathguard.Under(p, d.path) || pathguard.Under(d.path, p) {
			return fmt.Sprintf("%s overlaps %s (%s) — a library folder can't sit inside or around a downloads folder", p, d.name, d.path)
		}
	}
	return ""
}

// UpdateInput changes an extra folder's label and/or rule (nil leaves it).
type UpdateInput struct {
	Label *string `json:"label"`
	Rule  *string `json:"rule"`
}

// Update changes an extra folder's label or rule. Its path can't change: the titles in it
// would then point at a folder that doesn't hold them. Moving the titles and adding a
// new folder is the way.
func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (Folder, error) {
	f, ok := s.byID(ctx, id)
	if !ok {
		return Folder{}, ErrNotFound
	}
	if in.Label != nil {
		f.Label = strings.TrimSpace(*in.Label)
	}
	if in.Rule != nil {
		r := strings.TrimSpace(*in.Rule)
		if !ValidRule(r) {
			return Folder{}, ErrBadRule
		}
		f.Rule = r
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE library_folders SET label = ?, rule = ? WHERE id = ?`, f.Label, f.Rule, id); err != nil {
		return Folder{}, err
	}
	if err := s.reload(ctx); err != nil {
		return Folder{}, err
	}
	out, _ := s.Get(ctx, f.Media, id)
	return out, nil
}

// Reorder sets the order rules are tried in for one library: ids lists that library's
// extra folders, first to last. Every extra folder of the library must be listed once.
func (s *Service) Reorder(ctx context.Context, media string, ids []int64) error {
	if !ValidMedia(media) {
		return ErrBadMedia
	}
	have := map[int64]bool{}
	for _, f := range s.load(ctx) {
		if f.Media == media {
			have[f.ID] = true
		}
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if !have[id] || seen[id] {
			return fmt.Errorf("%w: the order must list each of the library's extra folders once", ErrNotFound)
		}
		seen[id] = true
	}
	if len(seen) != len(have) {
		return fmt.Errorf("%w: the order must list each of the library's extra folders once", ErrNotFound)
	}
	err := store.WithTx(ctx, s.db, func(tx *store.Transaction) error {
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE library_folders SET position = ? WHERE id = ?`, i+1, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.reload(ctx)
}

// Usage counts what belongs to a folder: titles assigned to it, and library files whose
// path lies in it (whoever they're assigned to).
type Usage struct {
	Titles int `json:"titles"`
	Files  int `json:"files"`
}

// UsageOf counts a folder's titles and files.
func (s *Service) UsageOf(ctx context.Context, f Folder) (Usage, error) {
	var u Usage
	table := "movies"
	if f.Media == MediaSeries {
		table = "series"
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE library_folder_id = ?`, f.ID).Scan(&u.Titles); err != nil {
		return u, err
	}
	if strings.TrimSpace(f.Path) == "" {
		return u, nil
	}
	prefix := filepath.Clean(f.Path) + string(filepath.Separator)
	var q string
	if f.Media == MediaSeries {
		q = `SELECT COUNT(*) FROM episodes WHERE has_file = 1 AND substr(file_path, 1, length(?1)) = ?1`
	} else {
		q = `SELECT (SELECT COUNT(*) FROM movies WHERE has_file = 1 AND substr(movie_file_path, 1, length(?1)) = ?1)
			+ (SELECT COUNT(*) FROM movie_versions WHERE has_file = 1 AND substr(file_path, 1, length(?1)) = ?1)`
	}
	if err := s.db.QueryRowContext(ctx, q, prefix).Scan(&u.Files); err != nil {
		return u, err
	}
	return u, nil
}

// Counts is how many titles each folder of a library holds (by assignment), keyed by id.
func (s *Service) Counts(ctx context.Context, media string) (map[int64]int, error) {
	table := "movies"
	if media == MediaSeries {
		table = "series"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT library_folder_id, COUNT(*) FROM `+table+` GROUP BY library_folder_id`)
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

// Remove deletes an extra folder from Arrmada (the folder on disk is never touched). It's
// refused while any title is assigned to it or any library file lies in it: those titles
// would otherwise import into, and be looked for in, a folder Arrmada no longer knows.
// The check and the delete run in one transaction, so a title assigned in between can't
// slip through.
func (s *Service) Remove(ctx context.Context, id int64) error {
	f, ok := s.byID(ctx, id)
	if !ok {
		return ErrNotFound
	}
	// No move may be putting files into it, and none may start until this is done.
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	if s.moving[id] > 0 {
		return ErrMoving
	}
	err := store.WithTx(ctx, s.db, func(tx *store.Transaction) error {
		u, err := usageTx(ctx, tx, f)
		if err != nil {
			return err
		}
		if u.Titles > 0 || u.Files > 0 {
			return &UseError{Titles: u.Titles, Files: u.Files}
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM library_folders WHERE id = ?`, id)
		return err
	})
	if err != nil {
		return err
	}
	if d := strings.TrimSpace(f.Downloads); d != "" {
		s.rememberDownloads(ctx, d) // torrents there may still be imported by hand
	}
	return s.reload(ctx)
}

// usageTx is UsageOf inside a transaction.
func usageTx(ctx context.Context, tx *store.Transaction, f Folder) (Usage, error) {
	var u Usage
	prefix := filepath.Clean(f.Path) + string(filepath.Separator)
	if f.Media == MediaSeries {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM series WHERE library_folder_id = ?`, f.ID).Scan(&u.Titles); err != nil {
			return u, err
		}
		err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM episodes WHERE has_file = 1 AND substr(file_path, 1, length(?1)) = ?1`, prefix).Scan(&u.Files)
		return u, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM movies WHERE library_folder_id = ?`, f.ID).Scan(&u.Titles); err != nil {
		return u, err
	}
	err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM movies WHERE has_file = 1 AND substr(movie_file_path, 1, length(?1)) = ?1)
		+ (SELECT COUNT(*) FROM movie_versions WHERE has_file = 1 AND substr(file_path, 1, length(?1)) = ?1)`, prefix).Scan(&u.Files)
	return u, err
}

// byID is an extra folder by id.
func (s *Service) byID(ctx context.Context, id int64) (Folder, bool) {
	for _, f := range s.load(ctx) {
		if f.ID == id {
			return f, true
		}
	}
	return Folder{}, false
}

// ByID is an extra folder by id, whichever library it belongs to.
func (s *Service) ByID(ctx context.Context, id int64) (Folder, bool) {
	f, ok := s.byID(ctx, id)
	if ok {
		f.Label = s.labelOf(f)
	}
	return f, ok
}

// SortedForRules returns a library's extra folders in rule order (a copy).
func (s *Service) SortedForRules(ctx context.Context, media string) []Folder {
	var out []Folder
	for _, f := range s.load(ctx) {
		if f.Media == media {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}

// strictlyUnder reports whether p lies inside dir (not dir itself), on a separator
// boundary. Plain cleaned strings, like the stored paths: library paths are compared the
// way they're recorded.
func strictlyUnder(p, dir string) bool {
	p, dir = filepath.Clean(p), filepath.Clean(dir)
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// StrictlyUnder is strictlyUnder for other packages.
func StrictlyUnder(p, dir string) bool { return strictlyUnder(p, dir) }

func samePath(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }
