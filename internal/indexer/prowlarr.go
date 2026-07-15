package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// prowlarrIndexer is the subset of Prowlarr's /api/v1/indexer objects we use.
type prowlarrIndexer struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Enable   bool   `json:"enable"`
	Protocol string `json:"protocol"` // "torrent" | "usenet"
	Priority int    `json:"priority"`
}

// SyncResult reports the outcome of a Prowlarr sync.
type SyncResult struct {
	Synced            int  `json:"synced"`             // indexers added/updated
	FlareSolverrReady bool `json:"flaresolverr_ready"` // a FlareSolverr proxy is configured in Prowlarr
}

// prowlarrDo makes an authenticated Prowlarr API call.
func prowlarrDo(ctx context.Context, base, key, method, path string, body any) ([]byte, error) {
	base = strings.TrimRight(base, "/")
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("prowlarr: connect failed: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("prowlarr: invalid API key")
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("prowlarr: HTTP %d", resp.StatusCode)
	}
	return b, nil
}

// SyncProwlarr pulls every enabled indexer from a Prowlarr instance and mirrors
// it into Arrmada as a Torznab/Newznab source (upserting by name), so Arrmada
// searches through Prowlarr's fast API instead of scraping sites itself. When
// flareURL is set, it also auto-configures a FlareSolverr proxy in Prowlarr
// (pointing at Arrmada's bundled FlareSolverr) so the user needn't set that up.
//
// baseURL must be reachable from the Arrmada server (e.g. the compose service
// name http://arrmada-prowlarr:9696).
func (s *Service) SyncProwlarr(ctx context.Context, baseURL, apiKey, flareURL string) (SyncResult, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" || apiKey == "" {
		return SyncResult{}, fmt.Errorf("prowlarr: URL and API key are required")
	}
	var res SyncResult

	// Best-effort: make sure Prowlarr has a FlareSolverr proxy wired to the bundled
	// instance, so Cloudflare-protected trackers work without the user configuring
	// anything. A failure here never blocks the indexer sync.
	if flareURL != "" {
		if err := s.ensureFlareSolverrProxy(ctx, baseURL, apiKey, flareURL); err != nil {
			s.log.Warn("prowlarr: could not auto-configure FlareSolverr proxy", "err", err)
		} else {
			res.FlareSolverrReady = true
		}
	}

	body, err := prowlarrDo(ctx, baseURL, apiKey, http.MethodGet, "/api/v1/indexer", nil)
	if err != nil {
		return res, err
	}
	var remote []prowlarrIndexer
	if err := json.Unmarshal(body, &remote); err != nil {
		return res, fmt.Errorf("prowlarr: parse indexers: %w", err)
	}

	existing, err := s.repo.List(ctx)
	if err != nil {
		return res, err
	}
	byName := make(map[string]Indexer, len(existing))
	for _, e := range existing {
		byName[e.Name] = e
	}
	for _, pi := range remote {
		if !pi.Enable {
			continue
		}
		kind := KindTorznab
		if pi.Protocol == "usenet" {
			kind = KindNewznab
		}
		priority := pi.Priority
		if priority < 1 || priority > 50 {
			priority = 25
		}
		idx := Indexer{
			Name:     "Prowlarr · " + pi.Name,
			Kind:     kind,
			URL:      fmt.Sprintf("%s/%d/api", baseURL, pi.ID),
			APIKey:   apiKey,
			Priority: priority,
			Enabled:  true,
		}
		if ex, ok := byName[idx.Name]; ok {
			idx.ID = ex.ID
			idx.MinSeeders, idx.SeedEnabled, idx.SeedRatio, idx.SeedHours = ex.MinSeeders, ex.SeedEnabled, ex.SeedRatio, ex.SeedHours
			if err := s.repo.Update(ctx, idx); err != nil {
				return res, err
			}
		} else if _, err := s.repo.Create(ctx, idx); err != nil {
			return res, err
		}
		res.Synced++
	}
	return res, nil
}

// ensureFlareSolverrProxy makes sure Prowlarr has a FlareSolverr indexer proxy
// pointing at flareURL (Arrmada's bundled FlareSolverr), creating the required
// tag and proxy if absent. Idempotent.
func (s *Service) ensureFlareSolverrProxy(ctx context.Context, base, key, flareURL string) error {
	// Already configured?
	pb, err := prowlarrDo(ctx, base, key, http.MethodGet, "/api/v1/indexerproxy", nil)
	if err != nil {
		return err
	}
	var proxies []struct {
		Implementation string `json:"implementation"`
	}
	_ = json.Unmarshal(pb, &proxies)
	for _, p := range proxies {
		if strings.EqualFold(p.Implementation, "FlareSolverr") {
			return nil // already there
		}
	}

	tagID, err := s.ensureProwlarrTag(ctx, base, key, "flaresolverr")
	if err != nil {
		return err
	}

	sb, err := prowlarrDo(ctx, base, key, http.MethodGet, "/api/v1/indexerproxy/schema", nil)
	if err != nil {
		return err
	}
	var schemas []map[string]any
	if err := json.Unmarshal(sb, &schemas); err != nil {
		return err
	}
	var proxy map[string]any
	for _, sc := range schemas {
		if impl, _ := sc["implementation"].(string); strings.EqualFold(impl, "FlareSolverr") {
			proxy = sc
			break
		}
	}
	if proxy == nil {
		return fmt.Errorf("prowlarr: no FlareSolverr proxy schema available")
	}
	if fields, ok := proxy["fields"].([]any); ok {
		for _, f := range fields {
			if fm, ok := f.(map[string]any); ok {
				if name, _ := fm["name"].(string); name == "host" {
					fm["value"] = strings.TrimRight(flareURL, "/")
				}
			}
		}
	}
	proxy["name"] = "FlareSolverr (Arrmada)"
	proxy["tags"] = []int{tagID}
	_, err = prowlarrDo(ctx, base, key, http.MethodPost, "/api/v1/indexerproxy", proxy)
	return err
}

// ensureProwlarrTag returns the id of a Prowlarr tag with the given label,
// creating it if needed.
func (s *Service) ensureProwlarrTag(ctx context.Context, base, key, label string) (int, error) {
	tb, err := prowlarrDo(ctx, base, key, http.MethodGet, "/api/v1/tag", nil)
	if err != nil {
		return 0, err
	}
	var tags []struct {
		ID    int    `json:"id"`
		Label string `json:"label"`
	}
	_ = json.Unmarshal(tb, &tags)
	for _, t := range tags {
		if strings.EqualFold(t.Label, label) {
			return t.ID, nil
		}
	}
	cb, err := prowlarrDo(ctx, base, key, http.MethodPost, "/api/v1/tag", map[string]any{"label": label})
	if err != nil {
		return 0, err
	}
	var created struct {
		ID int `json:"id"`
	}
	_ = json.Unmarshal(cb, &created)
	return created.ID, nil
}
