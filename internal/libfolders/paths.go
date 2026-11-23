package libfolders

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
)

// pathColumn is one stored absolute path to a Movies or TV library file. Every table that
// remembers where a library file is must be listed here, or a title moved to another
// folder would keep pointing at its old one there (TestRewriteCoversEveryPathColumn
// checks the schema for path columns this list doesn't know).
type pathColumn struct {
	table, column string
	// pk: the path is (part of) the primary key, so a row already at the new path is
	// replaced rather than failing the move. Only caches are keyed by path.
	pk bool
	// jsonPath: the column is a JSON blob whose value at this path is a file path.
	jsonPath string
}

// pathColumns are the stored paths that can name a movie or episode file or a subtitle
// beside one. Book, audiobook and music paths are absent on purpose: those libraries
// have one folder.
var pathColumns = []pathColumn{
	{table: "movies", column: "movie_file_path"},
	{table: "movies", column: "media_json", jsonPath: "$.path"},
	{table: "movie_versions", column: "file_path"},
	{table: "movie_versions", column: "media_json", jsonPath: "$.path"},
	{table: "episodes", column: "file_path"},
	// The import ledger: "is this download already in the library" compares target paths.
	{table: "imports", column: "target_path"},
	// A pending upgrade remembers the file it will replace.
	{table: "grabs", column: "replaces_path"},
	// Convert's facts and history, keyed or described by the file's path.
	{table: "convert_library", column: "path", pk: true},
	{table: "convert_probe_cache", column: "path", pk: true},
	{table: "convert_choices", column: "path", pk: true},
	{table: "convert_measured", column: "path", pk: true},
	// The file a skip or a failure count is about (0355): moved with it, so a verdict keeps
	// holding for the same file in its new folder.
	{table: "convert_skips", column: "file_path"},
	{table: "convert_failures", column: "file_path"},
	{table: "convert_history", column: "src_path"},
	{table: "convert_history", column: "out_path"},
	// Subtitles: the ledger of sidecars and what was tried for each video.
	{table: "subtitle_files", column: "video_path"},
	{table: "subtitle_files", column: "path"},
	{table: "subtitle_attempts", column: "video_path"},
	{table: "subtitle_backoff", column: "video_path"},
	{table: "subtitle_probe_cache", column: "path", pk: true},
	// A one-click issue fix remembers the file it acted on.
	{table: "issues", column: "action_file"},
}

// ignoredPathColumns are path columns that never hold a Movies or TV library file, or
// hold one only in a place a move doesn't touch. Listed so the schema test knows they
// were considered.
var ignoredPathColumns = map[string]string{
	"books.file_path":                      "books have one folder",
	"books.ebook_path":                     "books have one folder",
	"books.audiobook_path":                 "books have one folder",
	"book_audio_versions.path":             "audiobooks have one folder",
	"audio_file_meta.path":                 "audiobooks have one folder",
	"book_rename_journal.from_path":        "books have one folder",
	"book_rename_journal.to_path":          "books have one folder",
	"book_rename_journal.old_edition_path": "books have one folder",
	"book_rename_journal.new_edition_path": "books have one folder",
	"tracks.file_path":                     "music has one folder",
	"music_track_files.path":               "music has one folder",
	"music_migration_notes.path":           "music has one folder (and the notes are an audit trail)",
	"music_scan_dirs.path":                 "music has one folder (and it's only the library scan's cache)",
	"imports.source_path":                  "the download, not the library file",
	"import_reviews.content_path":          "the download, not the library file",
	"convert_history.hold_path":            "the original stays in its folder's hold until the undo window ends",
	"convert_swaps.hold_path":              "a move is refused while a Convert swap is open",
	"library_unmatched.folder":             "a scan's needs-review list, relative to its root",
	"library_folders.path":                 "the library folders themselves",
	// A folder's own downloads folder: where its torrents save, never a library file. A
	// move leaves a seeding download where it is.
	"library_folders.downloads_path": "a downloads folder, not a library file",
}

// RewriteCount is how many stored paths a rewrite changed, by "table.column".
type RewriteCount map[string]int64

// Total is the number of paths changed.
func (c RewriteCount) Total() int64 {
	var n int64
	for _, v := range c {
		n += v
	}
	return n
}

// RewritePrefix repoints every stored path at or under oldDir to the same place under
// newDir, in tx. It runs after a title's folder has been moved on disk, inside the same
// transaction that reassigns the title, so the database never describes a half-moved
// title. Comparisons are on whole path segments ("/movies/Heat (1995)" never matches
// "/movies/Heat (1995) Director's Cut") and use substr rather than LIKE, so a "%" or "_"
// in a folder name means itself.
func RewritePrefix(ctx context.Context, tx *sql.Tx, oldDir, newDir string) (RewriteCount, error) {
	oldDir, newDir = filepath.Clean(oldDir), filepath.Clean(newDir)
	if oldDir == newDir {
		return RewriteCount{}, nil
	}
	if oldDir == "." || oldDir == string(filepath.Separator) || strings.TrimSpace(oldDir) == "" {
		return nil, fmt.Errorf("refusing to rewrite every path under %q", oldDir)
	}
	sep := string(filepath.Separator)
	oldPrefix := oldDir + sep
	out := RewriteCount{}
	for _, c := range pathColumns {
		var (
			q    string
			args []any
		)
		if c.jsonPath != "" {
			// ?1 old folder, ?2 old folder + separator, ?3 new folder.
			val := fmt.Sprintf("json_extract(%s, '%s')", c.column, c.jsonPath)
			q = fmt.Sprintf(`UPDATE %s SET %s = json_set(%s, '%s', ?3 || substr(%s, length(?1) + 1))
				WHERE json_valid(%s) AND (%s = ?1 OR substr(%s, 1, length(?2)) = ?2)`,
				c.table, c.column, c.column, c.jsonPath, val, c.column, val, val)
			args = []any{oldDir, oldPrefix, newDir}
		} else {
			verb := "UPDATE"
			if c.pk {
				verb = "UPDATE OR REPLACE"
			}
			q = fmt.Sprintf(`%s %s SET %s = ?3 || substr(%s, length(?1) + 1)
				WHERE %s = ?1 OR substr(%s, 1, length(?2)) = ?2`,
				verb, c.table, c.column, c.column, c.column, c.column)
			args = []any{oldDir, oldPrefix, newDir}
		}
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("repoint %s.%s: %w", c.table, c.column, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			out[c.table+"."+c.column] = n
		}
	}
	return out, nil
}
