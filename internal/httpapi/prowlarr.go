package httpapi

import "net/http"

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
func (a *api) handleProwlarrSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
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
	res, err := a.deps.Indexers.SyncProwlarr(ctx, url, key, a.deps.Config.FlaresolverrURL)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// Remember what worked for next time.
	_ = a.deps.Settings.Set(ctx, keyProwlarrURL, url)
	_ = a.deps.Settings.Set(ctx, keyProwlarrKey, key)
	a.writeJSON(w, http.StatusOK, map[string]any{"synced": res.Synced, "flaresolverr_ready": res.FlareSolverrReady})
}
