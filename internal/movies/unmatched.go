package movies

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The needs-review list: folders a library scan couldn't confidently identify. It lives in
// library_unmatched (media-agnostic, so series can share it) rather than in memory, so a
// restart doesn't empty it and force another full scan to get it back.

// mediaMovie is this module's key in library_unmatched.
const mediaMovie = "movie"

// replaceUnmatched records a scan's unmatched folders, each under the library folder it
// was found in (UnmatchedFolder.Root). A finished scan (complete) replaces every row the
// media had — the list is what the newest scan saw — in one transaction; a scan that
// stopped early only adds what it found, so the folders it never reached keep their rows.
// keep are library folders the scan couldn't read: their rows stay even after a complete
// scan, since nobody looked at them.
func (r *Repo) replaceUnmatched(ctx context.Context, media string, list []UnmatchedFolder, complete bool, keep []string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return r.inTx(ctx, func(tx *store.Transaction, _ *Repo) error {
		if complete {
			q, args := `DELETE FROM library_unmatched WHERE media = ?`, []any{media}
			if len(keep) > 0 {
				marks := make([]string, 0, len(keep))
				for _, k := range keep {
					marks = append(marks, "?")
					args = append(args, filepath.Clean(k))
				}
				q += ` AND root NOT IN (` + strings.Join(marks, ",") + `)`
			}
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				return err
			}
		}
		for _, u := range list {
			cands := u.Candidates
			if cands == nil {
				cands = []metadata.MovieResult{}
			}
			b, err := json.Marshal(cands)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO library_unmatched (media, root, folder, title, year, candidates_json, scanned_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(media, root, folder) DO UPDATE SET title = excluded.title, year = excluded.year,
					candidates_json = excluded.candidates_json, scanned_at = excluded.scanned_at`,
				media, filepath.Clean(u.Root), u.Folder, u.Title, u.Year, string(b), now); err != nil {
				return err
			}
		}
		return nil
	})
}

// unmatched lists the media's unmatched folders under any of roots, by folder name. Rows a
// scan of a different root left behind (the folder setting changed since) aren't shown:
// they can't be imported from the folders in use now.
func (r *Repo) unmatched(ctx context.Context, media string, roots []string) ([]UnmatchedFolder, error) {
	out := []UnmatchedFolder{}
	if len(roots) == 0 {
		return out, nil
	}
	args := []any{media}
	marks := make([]string, 0, len(roots))
	for _, root := range roots {
		args = append(args, filepath.Clean(root))
		marks = append(marks, "?")
	}
	rows, err := r.q().QueryContext(ctx, `SELECT folder, root, title, year, candidates_json FROM library_unmatched
		WHERE media = ? AND root IN (`+strings.Join(marks, ",")+`) ORDER BY `+store.FoldASCII(r.q(), "folder"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			u     UnmatchedFolder
			cands string
		)
		if err := rows.Scan(&u.Folder, &u.Root, &u.Title, &u.Year, &cands); err != nil {
			return nil, err
		}
		if cands != "" {
			_ = json.Unmarshal([]byte(cands), &u.Candidates)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// dropUnmatched removes one folder of one library folder (root) from the media's list (a
// manual pick settled it); the same name in another library folder stays.
func (r *Repo) dropUnmatched(ctx context.Context, media, root, folder string) error {
	_, err := r.q().ExecContext(ctx, `DELETE FROM library_unmatched WHERE media = ? AND root = ? AND folder = ?`, media, filepath.Clean(root), folder)
	return err
}

// recordUnmatched saves a scan's unmatched folders. It runs after the scan's own context
// may have ended (a timeout, a cancel), so it gets a short budget of its own: the folders
// a stopped scan did look at are still worth keeping. keep: folders it couldn't read.
func (s *Service) recordUnmatched(ctx context.Context, list []UnmatchedFolder, complete bool, keep []string) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.repo.replaceUnmatched(wctx, mediaMovie, list, complete, keep); err != nil {
		s.log.Warn("library scan: couldn't save the folders that need review", "err", err)
	}
}

// LastUnmatched returns the folders scans of the movies folders couldn't identify, each
// with the candidates offered for a manual pick and the folder it was found in. The list
// survives restarts.
func (s *Service) LastUnmatched(ctx context.Context) ([]UnmatchedFolder, error) {
	return s.repo.unmatched(ctx, mediaMovie, s.libRoots())
}
