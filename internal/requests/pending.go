package requests

import "context"

// Pending is the requests waiting for approval, oldest first, at most limit (0: no cap) —
// straight from the table, without List's library lookups. The Needs-you feed reads it
// every 30 seconds, and a pending request has nothing on disk worth looking up.
func (s *Service) Pending(ctx context.Context, limit int) ([]Request, error) {
	q := `SELECT ` + cols + ` FROM requests WHERE status = ? ORDER BY id`
	args := []any{StatusPending}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.repo.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetAttentionKick installs the Needs-you feed's refresh request (attention.Service.Kick),
// called whenever a request is made, decided or withdrawn, so the staff badge catches up
// in seconds. A direct call, not a bus subscription: the feed's own 30-second refresh is
// the backstop.
func (s *Service) SetAttentionKick(fn func()) {
	if fn == nil {
		s.attentionKick.Store(nil)
		return
	}
	s.attentionKick.Store(&fn)
}

func (s *Service) kickAttention() {
	if fn := s.attentionKick.Load(); fn != nil {
		(*fn)()
	}
}
