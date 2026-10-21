package movies

import (
	"context"
	"fmt"

	"github.com/tristenlammi/arrmada/internal/store"
)

// BulkChange is what the mass editor sets on every selected movie. A nil field is left
// as each movie has it.
type BulkChange struct {
	Monitored       *bool
	QualityProfile  *string
	MinAvailability *string
}

// MissingIDsError means some of a bulk change's movies aren't in the library, so nothing
// was changed.
type MissingIDsError struct{ IDs []int64 }

func (e *MissingIDsError) Error() string {
	return fmt.Sprintf("%d of the selected movies are no longer in the library — nothing was changed", len(e.IDs))
}

func (e *MissingIDsError) Unwrap() error { return ErrNotFound }

// ValidAvailability reports whether s is a minimum-availability value a movie can have.
func ValidAvailability(s string) bool {
	switch s {
	case "announced", "inCinemas", "released":
		return true
	}
	return false
}

// BulkUpdate applies one change to many movies in a single transaction: every movie
// changes, or — if any is missing or a write fails — none does. The quality profile is
// the caller's to validate (it knows the profiles); the availability is checked here.
func (s *Service) BulkUpdate(ctx context.Context, ids []int64, ch BulkChange) error {
	if ch.MinAvailability != nil && !ValidAvailability(*ch.MinAvailability) {
		return fmt.Errorf("invalid availability %q", *ch.MinAvailability)
	}
	return s.repo.inTx(ctx, func(tx *store.Transaction, r *Repo) error {
		if missing, err := r.missingIDs(ctx, ids); err != nil {
			return err
		} else if len(missing) > 0 {
			return &MissingIDsError{IDs: missing}
		}
		for _, id := range ids {
			if ch.Monitored != nil {
				if err := r.SetMonitored(ctx, id, *ch.Monitored); err != nil {
					return err
				}
			}
			if ch.QualityProfile != nil {
				// A real profile change ends the default file's upgrade hold, as on the
				// movie page.
				if err := r.SetQualityProfile(ctx, id, *ch.QualityProfile); err != nil {
					return err
				}
			}
			if ch.MinAvailability != nil {
				if err := r.SetMinAvailability(ctx, id, *ch.MinAvailability); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// missingIDs returns the ids that aren't movies, in the order given.
func (r *Repo) missingIDs(ctx context.Context, ids []int64) ([]int64, error) {
	found := make(map[int64]bool, len(ids))
	for rest := ids; len(rest) > 0; {
		chunk := rest
		if len(chunk) > holdChunk {
			chunk = rest[:holdChunk]
		}
		rest = rest[len(chunk):]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := r.q().QueryContext(ctx, `SELECT id FROM movies WHERE id IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			found[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	var missing []int64
	for _, id := range ids {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}
