package requests

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Request quotas: an optional limit on how much one person can ask for in a rolling
// window — movies, seasons and books counted apart. Usage is a ledger (request_usage):
// a row per new ask that counts, deleted or reduced when the ask is withdrawn, declined
// or trimmed, so a refund is immediate. Staff and imports are never limited, and
// following someone else's request is free.

// Quota kinds, as stored.
const (
	QuotaMovie  = "movie"
	QuotaSeason = "season"
	QuotaBook   = "book"
)

// DefaultQuotaDays is the window when none is set.
const DefaultQuotaDays = 7

// Limits are one person's limits: per Days days, at most Movie movies, Season seasons
// and Book books. 0 is unlimited.
type Limits struct {
	Days                int
	Movie, Season, Book int
}

func (l Limits) of(kind string) int {
	switch kind {
	case QuotaMovie:
		return l.Movie
	case QuotaSeason:
		return l.Season
	case QuotaBook:
		return l.Book
	}
	return 0
}

func (l Limits) window() time.Duration {
	if l.Days <= 0 {
		return DefaultQuotaDays * 24 * time.Hour
	}
	return time.Duration(l.Days) * 24 * time.Hour
}

// SetQuotaLimits wires where a user's limits come from (their own, else the global
// ones). Unset, nobody is limited.
func (s *Service) SetQuotaLimits(fn func(ctx context.Context, userID int64) (Limits, error)) {
	s.quotaLimits = fn
}

// ErrQuotaExceeded refuses an ask that would go over the requester's limit: how much of
// what they've used of how much, how much they asked for, and when the oldest use in the
// window frees up.
type ErrQuotaExceeded struct {
	Kind     string
	Limit    int
	Used     int
	Asked    int
	Days     int
	ResetsAt time.Time
}

func (e *ErrQuotaExceeded) Error() string {
	return fmt.Sprintf("request limit reached: %d of %d %s used in %d days", e.Used, e.Limit, e.Kind, e.Days)
}

// Left is how many more of this kind fit right now.
func (e *ErrQuotaExceeded) Left() int { return max(e.Limit-e.Used, 0) }

// QuotaUse is one kind's standing for GET /me/quota.
type QuotaUse struct {
	Limit int `json:"limit"` // 0: unlimited
	Used  int `json:"used"`
	// ResetsAt is when the oldest use in the window frees up (RFC3339); "" with none.
	ResetsAt string `json:"resets_at,omitempty"`
}

// QuotaStatus is a person's limits and use.
type QuotaStatus struct {
	Days   int      `json:"days"`
	Movie  QuotaUse `json:"movie"`
	Season QuotaUse `json:"season"`
	Book   QuotaUse `json:"book"`
}

// quotaCharge is what a non-exempt ask is checked against and charged to.
type quotaCharge struct {
	userID int64
	limits Limits
}

// quotaFor is the charge an ask runs under, nil when it isn't limited: staff (the
// caller's QuotaExempt), an import (Silent), nobody signed in, or no limits wired.
func (s *Service) quotaFor(ctx context.Context, in Request, opts CreateOptions) (*quotaCharge, error) {
	if opts.QuotaExempt || opts.Silent || in.RequestedBy <= 0 || s.quotaLimits == nil {
		return nil, nil
	}
	l, err := s.quotaLimits(ctx, in.RequestedBy)
	if err != nil {
		return nil, fmt.Errorf("read request limits: %w", err)
	}
	if l.Movie <= 0 && l.Season <= 0 && l.Book <= 0 {
		return nil, nil
	}
	return &quotaCharge{userID: in.RequestedBy, limits: l}, nil
}

// usage is how many units of kind userID used since since, and when the oldest of them was.
func (r *Repo) usage(ctx context.Context, userID int64, kind string, since int64) (used int, oldest int64, err error) {
	err = r.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(units), 0), COALESCE(MIN(created_at), 0) FROM request_usage WHERE user_id = ? AND kind = ? AND created_at > ?`,
		userID, kind, since).Scan(&used, &oldest)
	return used, oldest, err
}

// check refuses units more of kind when they'd go over the limit. Callers hold quotaMu
// from here until they've charged, so two asks at once can't both slip under it.
func (s *Service) checkQuota(ctx context.Context, q *quotaCharge, kind string, units int) error {
	if q == nil || units <= 0 {
		return nil
	}
	limit := q.limits.of(kind)
	if limit <= 0 {
		return nil
	}
	now := s.clock()
	used, oldest, err := s.repo.usage(ctx, q.userID, kind, now.Add(-q.limits.window()).Unix())
	if err != nil {
		return fmt.Errorf("read request usage: %w", err)
	}
	if used+units <= limit {
		return nil
	}
	days := q.limits.Days
	if days <= 0 {
		days = DefaultQuotaDays
	}
	e := &ErrQuotaExceeded{Kind: kind, Limit: limit, Used: used, Asked: units, Days: days, ResetsAt: now}
	if oldest > 0 {
		e.ResetsAt = time.Unix(oldest, 0).Add(q.limits.window())
	}
	return e
}

// charge records units of kind against the request.
func (s *Service) chargeQuota(ctx context.Context, q *quotaCharge, requestID int64, kind string, units int) {
	if q == nil || units <= 0 {
		return
	}
	if _, err := s.repo.db.ExecContext(ctx,
		`INSERT INTO request_usage (user_id, request_id, kind, units, created_at) VALUES (?, ?, ?, ?, ?)`,
		q.userID, requestID, kind, units, s.clock().Unix()); err != nil {
		s.log.Warn("request: couldn't record request usage", "request", requestID, "err", err)
	}
}

// refundQuota gives back everything a request was charged: it was withdrawn, declined or
// deleted.
func (r *Repo) refundQuota(ctx context.Context, requestID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM request_usage WHERE request_id = ?`, requestID)
	return err
}

// trimQuota lowers a series request's season charge to the seasons approved.
func (r *Repo) trimQuota(ctx context.Context, requestID int64, seasons int) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE request_usage SET units = MIN(units, ?) WHERE request_id = ? AND kind = ?`, seasons, requestID, QuotaSeason)
	return err
}

// Quota is a person's limits and what they've used of each, for the request sheet's
// "3 movie requests left this week". exempt (staff) reads as unlimited.
func (s *Service) Quota(ctx context.Context, userID int64, exempt bool) (QuotaStatus, error) {
	st := QuotaStatus{Days: DefaultQuotaDays}
	if exempt || s.quotaLimits == nil {
		return st, nil
	}
	l, err := s.quotaLimits(ctx, userID)
	if err != nil {
		return st, err
	}
	if l.Days > 0 {
		st.Days = l.Days
	}
	since := s.clock().Add(-l.window()).Unix()
	for _, k := range []struct {
		kind string
		into *QuotaUse
	}{{QuotaMovie, &st.Movie}, {QuotaSeason, &st.Season}, {QuotaBook, &st.Book}} {
		used, oldest, err := s.repo.usage(ctx, userID, k.kind, since)
		if err != nil {
			return st, err
		}
		*k.into = QuotaUse{Limit: max(l.of(k.kind), 0), Used: used}
		if oldest > 0 {
			k.into.ResetsAt = time.Unix(oldest, 0).Add(l.window()).UTC().Format(time.RFC3339)
		}
	}
	return st, nil
}

// quotaKind is what a request of mediaType counts as (series count seasons).
func quotaKind(mediaType string) string {
	switch mediaType {
	case "movie":
		return QuotaMovie
	case "book":
		return QuotaBook
	}
	return QuotaSeason
}

// Settings keys for the global limits: the window in days, and per window how many
// movies, seasons and books ("0" or unset: unlimited).
const (
	KeyQuotaDays    = "request_quota_days"
	KeyQuotaMovies  = "request_quota_movies"
	KeyQuotaSeasons = "request_quota_seasons"
	KeyQuotaBooks   = "request_quota_books"
)

// GlobalLimits reads the global limits through get (a settings lookup, "" when unset).
// Anything unreadable or negative is unlimited; the window defaults to a week.
func GlobalLimits(get func(key string) string) Limits {
	n := func(key string) int {
		v, err := strconv.Atoi(strings.TrimSpace(get(key)))
		if err != nil || v < 0 {
			return 0
		}
		return v
	}
	l := Limits{Days: n(KeyQuotaDays), Movie: n(KeyQuotaMovies), Season: n(KeyQuotaSeasons), Book: n(KeyQuotaBooks)}
	if l.Days <= 0 {
		l.Days = DefaultQuotaDays
	}
	return l
}

// LimitsFrom is a person's limits: their own where set (0 unlimited, n a limit), the
// global one where it's -1.
func LimitsFrom(global Limits, movies, seasons, books int) Limits {
	pick := func(own, g int) int {
		if own < 0 {
			return g
		}
		return own
	}
	return Limits{Days: global.Days, Movie: pick(movies, global.Movie), Season: pick(seasons, global.Season), Book: pick(books, global.Book)}
}
