package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/jobs"
)

const (
	keyProwlarrURL = "prowlarr_url"
	keyProwlarrKey = "prowlarr_api_key"
)

// handleProwlarrInfo returns the Prowlarr connection defaults for prefilling the
// sync form: the URL (saved override, else the configured/bundled default) and
// whether an API key is already stored (so the user needn't re-enter it).
func (a *api) handleProwlarrInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	url := a.deps.Settings.Get(ctx, keyProwlarrURL, a.deps.Config.ProwlarrURL)
	a.writeJSON(w, http.StatusOK, map[string]any{
		"url":     url,
		"has_key": a.deps.Settings.Get(ctx, keyProwlarrKey, "") != "",
	})
}

// handleProwlarrSync pulls indexers from Prowlarr and mirrors them into Arrmada.
// Body {url, api_key} are optional — a blank URL falls back to the configured
// default, and a blank key reuses the stored one. Successful values are saved.
// add_flaresolverr_proxy adds Arrmada's FlareSolverr to a Prowlarr that isn't the
// bundled one (the bundled one always gets it).
func (a *api) handleProwlarrSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL                  string `json:"url"`
		APIKey               string `json:"api_key"`
		AddFlareSolverrProxy bool   `json:"add_flaresolverr_proxy"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	url := req.URL
	if url == "" {
		url = a.deps.Settings.Get(ctx, keyProwlarrURL, a.deps.Config.ProwlarrURL)
	}
	key := req.APIKey
	if key == "" {
		key = a.deps.Settings.Get(ctx, keyProwlarrKey, "")
	}
	// The FlareSolverr URL Prowlarr is pointed at is the one in Settings (env as fallback).
	flare := ""
	if a.deps.APIKeys != nil {
		flare = a.deps.APIKeys.Value(ctx, "flaresolverr")
	}
	res, err := a.deps.Indexers.SyncProwlarr(ctx, indexer.ProwlarrSync{
		URL: url, APIKey: key, FlareSolverrURL: flare, AddFlareSolverrProxy: req.AddFlareSolverrProxy,
	})
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// Remember what worked for next time.
	_ = a.deps.Settings.Set(ctx, keyProwlarrURL, url)
	_ = a.deps.Settings.Set(ctx, keyProwlarrKey, key)
	// Each new row's capabilities come from its own feed (one request per indexer through
	// the per-host throttle), so they're read in the background rather than holding up
	// the answer; the page shows them on its next read.
	if ids := res.AddedIDs; len(ids) > 0 {
		_, _, _ = a.submit(r, jobs.Spec{Kind: "indexer.caps", Target: "prowlarr", Class: jobs.ClassIndexerSearch, Timeout: 10 * time.Minute,
			Fn: func(ctx context.Context, _ *jobs.Progress) (any, error) {
				return nil, a.deps.Indexers.RefreshCaps(ctx, ids)
			}})
	}
	a.writeJSON(w, http.StatusOK, res)
}
