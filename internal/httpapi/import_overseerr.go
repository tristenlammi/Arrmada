package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/overseerr"
	"github.com/tristenlammi/arrmada/internal/requests"
)

// handleImportOverseerr migrates an existing Overseerr/Jellyseerr request history
// into Arrmada. It fetches the request list up front (so it can report how many
// were found), then imports them in the background (overseerr.Classify decides each):
//   - approved and still being fetched: added to the library, searched a few per sweep;
//   - pending or failed: a pending request here, for staff to decide;
//   - finished (completed or available): kept, as ready, only when the library already
//     has the title — otherwise it would be downloaded again — and never searched;
//   - declined, or deleted from the server since: left out.
//
// Requests are attributed to a matching Arrmada user (by username) or to the admin
// running the import. Re-running is safe: media already requested is left alone.
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
	autoApprove := a.plexAutoApproval(ctx) // per type; movies only unless the owner chose

	// Import in the background so a large history (and the tunnel's request timeout)
	// can't cut it short; results land on the Requests page as they process.
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "requests.import-overseerr", Target: "all", Class: jobs.ClassExternalImport, Timeout: 30 * time.Minute,
		Fn: func(bg context.Context, p *jobs.Progress) (any, error) {
			client := overseerr.New(req.URL, req.APIKey)
			plexUsers := map[int]int64{} // requester's Plex account id → Arrmada user id (cached)
			var imported, skipped, declined, removed, finished, failed int
			for i := range items {
				if bg.Err() != nil {
					break
				}
				it := items[i]
				switch it.Status {
				case overseerr.StatusDeclined:
					declined++
					continue
				case overseerr.StatusRemoved:
					// Fetched, then deleted from the server (or blocklisted): nobody wants it now.
					removed++
					continue
				case overseerr.StatusFinished:
					// Old, finished history: kept only for a title the library already has, so
					// it shows as ready. Anything else would add it and download it again.
					on, err := a.deps.Requests.OnShelf(bg, it.MediaType, it.TMDBID)
					if err != nil {
						a.deps.Log.Warn("overseerr import: couldn't check the library", "tmdb", it.TMDBID, "err", err)
					}
					if !on {
						finished++
						continue
					}
				}
				client.Details(bg, &it)
				// Attribute to the requester. A Plex requester gets a real Plex-linked account
				// (created if needed) so when they Sign in with Plex they see their own requests.
				uid := adminID
				switch {
				case it.RequesterPlex > 0:
					if id, ok := plexUsers[it.RequesterPlex]; ok {
						uid = id
					} else if a.plexBlocked(bg, strconv.Itoa(it.RequesterPlex)) {
						// A blocked Plex account gets no account made for it; the request is
						// kept under the admin, like one from an unknown requester.
						plexUsers[it.RequesterPlex] = adminID
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
				// Silent: these are old requests, and their requesters mustn't get an inbox
				// full of 'approved' (or, from the next ready sweep, 'ready') for them.
				// DeferSearch: approved titles join the library monitored and marked to be
				// searched in their turn, a few per missing sweep, never hundreds at once. A
				// finished one is already on the shelf, so approving it searches nothing.
				created, subscribed, err := a.deps.Requests.Create(bg, in, requests.CreateOptions{
					AutoApprove: it.Status == overseerr.StatusApproved || it.Status == overseerr.StatusFinished,
					Silent:      true, DeferSearch: true,
				})
				switch {
				case errors.Is(err, requests.ErrExists), subscribed:
					skipped++ // already requested here — nothing new to import
				case err != nil:
					failed++
					a.deps.Log.Warn("overseerr import: request failed", "title", it.Title, "tmdb", it.TMDBID, "err", err)
				default:
					imported++
					// Already on the shelf: it counts as told, so nobody hears 'ready' about it.
					if err := a.deps.Requests.MarkReadyIfAvailable(bg, created.ID); err != nil {
						a.deps.Log.Warn("overseerr import: couldn't check whether the request is ready", "title", it.Title, "err", err)
					}
				}
			}
			a.deps.Log.Info("overseerr import finished",
				"imported", imported, "skipped", skipped, "declined", declined, "removed", removed,
				"finished_not_in_library", finished, "failed", failed, "total", len(items))
			msg := fmt.Sprintf("Imported %d of %d requests", imported, len(items))
			if left := declined + removed + finished; left > 0 {
				msg += fmt.Sprintf(" — %d left out (declined, deleted from the server, or finished and not in your library)", left)
			}
			p.SetMessage(msg)
			return map[string]int{
				"imported": imported, "skipped": skipped, "declined": declined, "removed": removed,
				"finished": finished, "failed": failed, "total": len(items),
			}, nil
		}})
	if !ok {
		return
	}
	if existing {
		a.alreadyRunning(w, jobID, "an import is already running")
		return
	}
	a.accepted(w, jobID, false, map[string]any{
		"status": "started",
		"found":  len(items),
	})
}
