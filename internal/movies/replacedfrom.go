package movies

import "context"

// SetReplacedFrom records release (size bytes) as the baseline of the track whose file is
// path: what that file replaced when it was downloaded to take a bigger file's place ("Find
// a smaller release"). It is the same baseline a conversion keeps (ConvertedFromRelease),
// so the upgrader judges releases against the file that was given up, never fetches it
// back, and a later conversion or revert treats it the same way. versionID 0 is the default
// track. Nothing is written when the track no longer records path (something else was
// imported meanwhile) — reported as false.
func (s *Service) SetReplacedFrom(ctx context.Context, movieID, versionID int64, path, release string, size int64) (bool, error) {
	if release == "" || path == "" {
		return false, nil
	}
	q := `UPDATE movies SET converted_from_release = ?, converted_from_size = ?
	       WHERE id = ? AND has_file = 1 AND movie_file_path = ?`
	args := []any{release, size, movieID, path}
	if versionID != 0 {
		q = `UPDATE movie_versions SET converted_from_release = ?, converted_from_size = ?
		      WHERE id = ? AND movie_id = ? AND has_file = 1 AND file_path = ?`
		args = []any{release, size, versionID, movieID, path}
	}
	res, err := s.repo.q().ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
