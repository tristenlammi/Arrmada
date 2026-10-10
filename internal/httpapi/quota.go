package httpapi

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/requests"
)

// quotaNouns are how a limit's units read: one, many.
var quotaNouns = map[string][2]string{
	requests.QuotaMovie:  {"movie request", "movie requests"},
	requests.QuotaSeason: {"season", "seasons"},
	requests.QuotaBook:   {"book request", "book requests"},
}

// quotaWindow is "this week" for the usual seven days, else "in N days".
func quotaWindow(days int) string {
	if days == 7 {
		return "this week"
	}
	return fmt.Sprintf("in %d days", days)
}

// quotaMessage is what a requester over their limit reads: "You've used your 10 movie
// requests this week. The next one frees up Tue 14 Oct." — or, asking for more seasons
// than are left, "You have 2 seasons left this week …".
func quotaMessage(e *requests.ErrQuotaExceeded) string {
	n := quotaNouns[e.Kind]
	noun := func(k int) string {
		if k == 1 {
			return n[0]
		}
		return n[1]
	}
	when := e.ResetsAt.Local().Format("Mon 2 Jan")
	if left := e.Left(); left > 0 {
		return fmt.Sprintf("You have %d %s left %s — ask for fewer, or more frees up %s.", left, noun(left), quotaWindow(e.Days), when)
	}
	return fmt.Sprintf("You've used your %d %s %s. The next one frees up %s.", e.Limit, noun(e.Limit), quotaWindow(e.Days), when)
}

// handleMyQuota is the caller's request limits and use: {days, movie, season, book}, each
// {limit (0: unlimited), used, resets_at}. Staff read as unlimited.
//
//	GET /api/v1/me/quota
func (a *api) handleMyQuota(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	st, err := a.deps.Requests.Quota(r.Context(), u.ID, u.Role.AtLeast(auth.RoleManager))
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read your request limits")
		return
	}
	a.writeJSON(w, http.StatusOK, st)
}

// The global request limits in the settings API: request_quota_days (1–365) and per
// window request_quota_movies / _seasons / _books (0 = unlimited, at most 1000).
const maxQuota = 1000

// quotaChanged reports whether a settings save would change the global limits from cur
// (a client sending the whole object back unchanged isn't a change).
func quotaChanged(req *settingsUpdate, cur requests.Limits) bool {
	differs := func(p *int, v int) bool { return p != nil && *p != v }
	return differs(req.QuotaDays, cur.Days) || differs(req.QuotaMovies, cur.Movie) ||
		differs(req.QuotaSeasons, cur.Season) || differs(req.QuotaBooks, cur.Book)
}

// validQuota says what's wrong with a quota field, "" when it's fine.
func validQuota(days, movies, seasons, books *int) string {
	if days != nil && (*days < 1 || *days > 365) {
		return "the request limit window must be 1 to 365 days"
	}
	for _, v := range []*int{movies, seasons, books} {
		if v != nil && (*v < 0 || *v > maxQuota) {
			return "a request limit must be between 0 (no limit) and " + strconv.Itoa(maxQuota)
		}
	}
	return ""
}
