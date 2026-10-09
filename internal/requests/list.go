package requests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sections of the request list. Each is a plain predicate over status and ready_at, so
// paging and counts stay single queries (idx_requests_section).
const (
	SectionAll           = "all"            // everything, newest first (the old list)
	SectionNeedsApproval = "needs_approval" // pending, oldest first: whoever waited longest
	SectionInProgress    = "in_progress"    // approved, not ready yet, most recently changed first
	SectionReady         = "ready"          // approved and the requester has been told it's ready
	SectionDeclined      = "declined"
	// sectionStripMine is a requester's Discover strip: what's waiting or on its way, what
	// became ready in the last two weeks and what was declined in the last month.
	sectionStripMine = "strip_mine"
)

// ValidSection reports whether s names a section the list can show ("" is all).
func ValidSection(s string) bool {
	switch s {
	case "", SectionAll, SectionNeedsApproval, SectionInProgress, SectionReady, SectionDeclined:
		return true
	}
	return false
}

// Relation is how a viewer stands to a request in their own list.
const (
	RelationOwner      = "owner"      // they asked for it
	RelationSubscriber = "subscriber" // someone else asked first; they joined (follow) it
)

// ListFilter narrows a request list. Zero values mean "any".
type ListFilter struct {
	Section   string // a Section*; "" is all
	Status    string // legacy: one status
	MediaType string // movie | series | book
	Query     string // a title search
	// UserID scopes the list to one user's own requests (0: everyone's). IncludeJoined adds
	// the requests they follow, each marked with its Relation.
	UserID        int64
	IncludeJoined bool
	// ReadyWithinDays keeps only ready requests stamped in the last N days (0: any).
	ReadyWithinDays int
	Limit, Offset   int // Limit 0: no limit
}

// Counts is how many requests each section holds, for the same scope and filters.
type Counts struct {
	NeedsApproval int `json:"needs_approval"`
	InProgress    int `json:"in_progress"`
	Ready         int `json:"ready"`
	Declined      int `json:"declined"`
}

const (
	whereNeedsApproval = `status = 'pending'`
	whereInProgress    = `status = 'approved' AND ready_at = 0`
	whereReady         = `status = 'approved' AND ready_at > 0`
	whereDeclined      = `status = 'declined'`
)

// scopeWhere is the filter every list and count shares: the user scope, media type and
// title search. Section and status come on top.
func (f ListFilter) scopeWhere() (where []string, args []any) {
	if f.UserID != 0 {
		if f.IncludeJoined {
			where = append(where, `(requested_by = ? OR id IN (SELECT request_id FROM request_subscribers WHERE user_id = ?))`)
			args = append(args, f.UserID, f.UserID)
		} else {
			where = append(where, `requested_by = ?`)
			args = append(args, f.UserID)
		}
	}
	if f.MediaType != "" {
		where = append(where, `media_type = ?`)
		args = append(args, f.MediaType)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		where = append(where, `title LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likeEscape(q)+"%")
	}
	return where, args
}

// likeEscape makes a search term literal inside a LIKE pattern.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// sectionWhere is a section's predicate and order.
func (f ListFilter) sectionWhere(now time.Time) (where string, args []any, order string) {
	switch f.Section {
	case SectionNeedsApproval:
		return whereNeedsApproval, nil, `created_at ASC, id ASC`
	case SectionInProgress:
		return whereInProgress, nil, `updated_at DESC, id DESC`
	case SectionReady:
		if f.ReadyWithinDays > 0 {
			return whereReady + ` AND ready_at >= ?`, []any{now.AddDate(0, 0, -f.ReadyWithinDays).Unix()}, `ready_at DESC, id DESC`
		}
		return whereReady, nil, `ready_at DESC, id DESC`
	case SectionDeclined:
		return whereDeclined, nil, `updated_at DESC, id DESC`
	case sectionStripMine:
		return `(` + whereNeedsApproval + `) OR (` + whereInProgress + `) OR (` + whereReady + ` AND ready_at >= ?) OR (` +
				whereDeclined + ` AND updated_at >= ?)`,
			[]any{now.AddDate(0, 0, -14).Unix(), now.AddDate(0, 0, -30).UTC().Format("2006-01-02 15:04:05")},
			`updated_at DESC, id DESC`
	}
	return "", nil, `id DESC`
}

// List returns one page of requests and how many match in all.
func (r *Repo) List(ctx context.Context, f ListFilter) ([]Request, int, error) {
	where, args := f.scopeWhere()
	if f.Status != "" {
		where = append(where, `status = ?`)
		args = append(args, f.Status)
	}
	sw, sargs, order := f.sectionWhere(time.Now())
	if sw != "" {
		where = append(where, `(`+sw+`)`)
		args = append(args, sargs...)
	}
	clause := ""
	if len(where) > 0 {
		clause = ` WHERE ` + strings.Join(where, ` AND `)
	}
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests`+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	// The viewer's relation to each row, when the list is one viewer's.
	sel := `SELECT ` + cols + `, ''`
	selArgs := []any{}
	if f.UserID != 0 {
		sel = `SELECT ` + cols + `, CASE WHEN requested_by = ? THEN '` + RelationOwner + `' ELSE '` + RelationSubscriber + `' END`
		selArgs = append(selArgs, f.UserID)
	}
	q := sel + ` FROM requests` + clause + ` ORDER BY ` + order
	all := append(selArgs, args...)
	if f.Limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		all = append(all, f.Limit, max(f.Offset, 0))
	}
	rows, err := r.db.QueryContext(ctx, q, all...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		req, err := scanRelation(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, req)
	}
	return out, total, rows.Err()
}

// scanRelation is scan with the relation column after the usual ones.
func scanRelation(rows *sql.Rows) (Request, error) {
	var rel string
	req, err := scan(relationScanner{rows, &rel})
	req.Relation = rel
	return req, err
}

// relationScanner appends one more destination to a scan.
type relationScanner struct {
	rows *sql.Rows
	rel  *string
}

func (s relationScanner) Scan(dest ...any) error { return s.rows.Scan(append(dest, s.rel)...) }

// Counts is how many requests each section holds under f's scope and filters (its section,
// status and paging are ignored).
func (r *Repo) Counts(ctx context.Context, f ListFilter) (Counts, error) {
	where, args := f.scopeWhere()
	clause := ""
	if len(where) > 0 {
		clause = ` WHERE ` + strings.Join(where, ` AND `)
	}
	var c Counts
	err := r.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT
		COALESCE(SUM(CASE WHEN %s THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN %s THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN %s THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN %s THEN 1 ELSE 0 END), 0)
		FROM requests`, whereNeedsApproval, whereInProgress, whereReady, whereDeclined)+clause, args...).
		Scan(&c.NeedsApproval, &c.InProgress, &c.Ready, &c.Declined)
	return c, err
}

// RelationOf is how user uid stands to request id: owner, subscriber, or "" for neither.
func (r *Repo) RelationOf(ctx context.Context, id, uid int64) (string, error) {
	var rel string
	err := r.db.QueryRowContext(ctx, `SELECT CASE
		WHEN requested_by = ? THEN '`+RelationOwner+`'
		WHEN EXISTS (SELECT 1 FROM request_subscribers s WHERE s.request_id = requests.id AND s.user_id = ?) THEN '`+RelationSubscriber+`'
		ELSE '' END FROM requests WHERE id = ?`, uid, uid, id).Scan(&rel)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return rel, err
}
