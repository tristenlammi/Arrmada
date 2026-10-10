package httpapi

import (
	"context"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/auth"
)

// keyPlexAutoApproveTypes is which media types a new Plex sign-in's requests approve
// themselves for, as "movie,series,book" (any subset; "" is none).
const keyPlexAutoApproveTypes = "plex_login_auto_approve_types"

// plexAutoApproval is the auto-approve a new Plex sign-in (or an Overseerr import's new
// account) gets. Until the per-type list is saved it follows the old single toggle — but
// only an owner who explicitly set that keeps its answer: on means every type, off none.
// An install that never touched it gives movies only, since a series request can pull a
// long show's every season.
func (a *api) plexAutoApproval(ctx context.Context) auth.AutoApproval {
	return plexAutoApprovalFrom(a.deps.Settings.Lookup)
}

func plexAutoApprovalFrom(lookup func(key string) (string, bool)) auth.AutoApproval {
	if v, ok := lookup(keyPlexAutoApproveTypes); ok {
		return auth.ParseAutoApproval(v)
	}
	if v, ok := lookup("plex_login_auto_approve"); ok {
		on, err := strconv.ParseBool(strings.TrimSpace(v))
		return auth.AllTypes(err == nil && on)
	}
	return auth.AutoApproval{Movie: true}
}

// plexAutoApproveUpdate is the per-type list a settings save asks for, if any: the list
// itself (normalised), or the legacy toggle as every type or none.
func plexAutoApproveUpdate(req *settingsUpdate) (string, bool) {
	switch {
	case req.PlexLoginAutoApproveTypes != nil:
		return auth.ParseAutoApproval(*req.PlexLoginAutoApproveTypes).String(), true
	case req.PlexLoginAutoApprove != nil:
		return auth.AllTypes(*req.PlexLoginAutoApprove).String(), true
	}
	return "", false
}

// autoApprovalFields are the per-type auto-approve flags a user create or update may send;
// each one present overrides that type.
type autoApprovalFields struct {
	AutoApproveMovie  *bool `json:"auto_approve_movie"`
	AutoApproveSeries *bool `json:"auto_approve_series"`
	AutoApproveBook   *bool `json:"auto_approve_book"`
}

func (f autoApprovalFields) apply(a auth.AutoApproval) auth.AutoApproval {
	if f.AutoApproveMovie != nil {
		a.Movie = *f.AutoApproveMovie
	}
	if f.AutoApproveSeries != nil {
		a.Series = *f.AutoApproveSeries
	}
	if f.AutoApproveBook != nil {
		a.Book = *f.AutoApproveBook
	}
	return a
}
