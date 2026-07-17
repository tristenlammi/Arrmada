package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/overseerr"
	"github.com/tristenlammi/arrmada/internal/requests"
)

// handleImportOverseerr migrates an existing Overseerr/Jellyseerr request history
// into Arrmada. It fetches the request list up front (so it can report how many
// were found), then imports them in the background — approved/available ones are
// added to the library and searched, pending ones become pending requests here.
// Requests are attributed to a matching Arrmada user (by username) or to the admin
// running the import. Declined requests are skipped. Re-running is safe: media
// already requested is left alone.
func (a *api) handleImportOverseerr(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.URL) == "" || strings.TrimSpace(req.APIKey) == "" {
		a.writeError(w, http.StatusBadRequest, "url and api_key are required")
		return
	}
	admin, _ := userFrom(r)
	if admin == nil {
		a.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	client := overseerr.New(req.URL, req.APIKey)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	items, err := client.List(ctx)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	// Resolve Overseerr requesters to Arrmada users by username (case-insensitive),
	// falling back to the admin running the import.
	byName := map[string]int64{}
	if users, e := a.deps.Auth.ListUsers(ctx); e == nil {
		for _, u := range users {
			byName[strings.ToLower(u.Username)] = u.ID
		}
	}
	adminID := admin.ID
	autoApprove := a.deps.Settings.GetBool(ctx, "plex_login_auto_approve", true)

	// Import in the background so a large history (and the tunnel's request timeout)
	// can't cut it short; results land on the Requests page as they process.
	go func(items []overseerr.Item) {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		client := overseerr.New(req.URL, req.APIKey)
		plexUsers := map[int]int64{} // requester's Plex account id → Arrmada user id (cached)
		var imported, skipped, declined, failed int
		for i := range items {
			if bg.Err() != nil {
				break
			}
			it := items[i]
			if it.Status == "declined" {
				declined++
				continue
			}
			client.Details(bg, &it)
			// Attribute to the requester. A Plex requester gets a real Plex-linked account
			// (created if needed) so when they Sign in with Plex they see their own requests.
			uid := adminID
			switch {
			case it.RequesterPlex > 0:
				if id, ok := plexUsers[it.RequesterPlex]; ok {
					uid = id
				} else if u, e := a.deps.Auth.FindOrCreatePlexUser(bg, strconv.Itoa(it.RequesterPlex), it.Requester, auth.RoleRequester, autoApprove); e == nil {
					plexUsers[it.RequesterPlex] = u.ID
					uid = u.ID
				}
			case it.Requester != "":
				if id, ok := byName[strings.ToLower(it.Requester)]; ok {
					uid = id
				}
			}
			in := requests.Request{
				MediaType:       it.MediaType,
				TMDBID:          it.TMDBID,
				Title:           it.Title,
				Year:            it.Year,
				PosterURL:       it.PosterURL,
				RequestedBy:     uid,
				RequestedByName: it.Requester,
			}
			_, err := a.deps.Requests.Create(bg, in, it.Status == "approved")
			switch {
			case errors.Is(err, requests.ErrExists):
				skipped++
			case err != nil:
				failed++
				a.deps.Log.Warn("overseerr import: request failed", "title", it.Title, "tmdb", it.TMDBID, "err", err)
			default:
				imported++
			}
		}
		a.deps.Log.Info("overseerr import finished",
			"imported", imported, "skipped", skipped, "declined", declined, "failed", failed, "total", len(items))
	}(items)

	a.writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "started",
		"found":  len(items),
	})
}
