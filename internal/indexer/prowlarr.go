package indexer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// prowlarrIndexer is the subset of Prowlarr's /api/v1/indexer objects (its
// IndexerResource) a sync reads. protocol is "torrent" or "usenet"; capabilities lists the
// Newznab categories the indexer answers for, each with its subcategories.
type prowlarrIndexer struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Enable       bool   `json:"enable"`
	Protocol     string `json:"protocol"`
	Priority     int    `json:"priority"`
	Capabilities struct {
		Categories []prowlarrCategory `json:"categories"`
	} `json:"capabilities"`
}

type prowlarrCategory struct {
	ID            int                `json:"id"`
	SubCategories []prowlarrCategory `json:"subCategories"`
}

// SyncResult says what a Prowlarr sync changed, for the message on the Indexers page.
type SyncResult struct {
	Added     int `json:"added"`     // new rows
	Updated   int `json:"updated"`   // existing rows whose name, address or key changed
	Unchanged int `json:"unchanged"` // existing rows left as they were
	Disabled  int `json:"disabled"`  // turned off: disabled or removed in Prowlarr, or usenet
	Reenabled int `json:"reenabled"` // turned back on: enabled again in Prowlarr
	// SkippedUsenet counts Prowlarr's usenet indexers, which aren't imported: Arrmada has
	// no usenet download client, so nothing they find could be downloaded.
	SkippedUsenet int `json:"skipped_usenet"`
	// FlareSolverrReady is true only when Prowlarr, asked after the sync, lists a
	// FlareSolverr proxy — never just because adding one didn't error.
	FlareSolverrReady bool     `json:"flaresolverr_ready"`
	Notes             []string `json:"notes,omitempty"`
}

// ProwlarrSync is what a sync needs.
type ProwlarrSync struct {
	URL    string // must be reachable from the Arrmada server, e.g. http://arrmada-prowlarr:9696
	APIKey string
	// FlareSolverrURL is Arrmada's FlareSolverr, to add to Prowlarr as a proxy. Blank adds
	// nothing.
	FlareSolverrURL string
	// AddFlareSolverrProxy adds the proxy to a Prowlarr that isn't the bundled one. An
	// external Prowlarr is someone else's setup, so it's only changed when asked.
	AddFlareSolverrProxy bool
}

// Notes a sync leaves on a row it turned off.
const (
	noteDisabledInProwlarr  = "Disabled in Prowlarr"
	noteRemovedFromProwlarr = "Removed from Prowlarr"
	noteNeedsUsenetClient   = "Needs a usenet download client"
)

// prowlarrNamePrefix starts the name of every row a sync creates.
const prowlarrNamePrefix = "Prowlarr · "

// bundledProwlarrHost is the compose service name of the Prowlarr Arrmada ships with.
const bundledProwlarrHost = "arrmada-prowlarr"

// errProwlarrEmpty is a sync that got an empty list while rows from Prowlarr exist. A
// Prowlarr that has just been reset, or answers oddly, must not switch every indexer off.
var errProwlarrEmpty = errors.New("Prowlarr returned no indexers; nothing changed")

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

// prowlarrIDFromURL reads n from a Prowlarr indexer feed address "<base>/<n>/api".
func prowlarrIDFromURL(base, raw string) (int, bool) {
	base = strings.TrimRight(base, "/")
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" || len(raw) <= len(base)+1 || !strings.EqualFold(raw[:len(base)+1], base+"/") {
		return 0, false
	}
	rest, ok := strings.CutSuffix(raw[len(base)+1:], "/api")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// BackfillProwlarrIDs gives rows synced before prowlarr_id existed their Prowlarr id, read
// from their feed address. Idempotent; rows that already have one are left alone. It runs
// at startup so a synced row is recognised as Prowlarr's before the next sync, and again
// at the start of every sync.
func (s *Service) BackfillProwlarrIDs(ctx context.Context, prowlarrURL string) (int, error) {
	base := strings.TrimRight(strings.TrimSpace(prowlarrURL), "/")
	if base == "" {
		return 0, nil
	}
	list, err := s.repo.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, idx := range list {
		if idx.ProwlarrID != 0 || (idx.Kind != KindTorznab && idx.Kind != KindNewznab) {
			continue
		}
		pid, ok := prowlarrIDFromURL(base, idx.URL)
		if !ok {
			continue
		}
		if _, err := s.repo.db.ExecContext(ctx, `UPDATE indexers SET prowlarr_id=? WHERE id=? AND prowlarr_id=0`, pid, idx.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// mediaTypesFor scopes a newly synced indexer from the categories Prowlarr says it has:
// 2000 movies, 5000 TV, 7000 and 3030 (audiobooks) books, any other 3000 music. A
// category with subcategories counts by its subcategories, so an audiobook-only tracker
// (3000 > 3030) is a book indexer, not a music one. Nothing recognised, or every area,
// is nil: used for everything.
func mediaTypesFor(cats []prowlarrCategory) []string {
	has := map[string]bool{}
	var visit func(c prowlarrCategory)
	visit = func(c prowlarrCategory) {
		if len(c.SubCategories) > 0 {
			for _, sc := range c.SubCategories {
				visit(sc)
			}
			return
		}
		switch {
		case c.ID == 3030:
			has[MediaBook] = true
		case c.ID >= 2000 && c.ID < 3000:
			has[MediaMovie] = true
		case c.ID >= 3000 && c.ID < 4000:
			has[MediaMusic] = true
		case c.ID >= 5000 && c.ID < 6000:
			has[MediaSeries] = true
		case c.ID >= 7000 && c.ID < 8000:
			has[MediaBook] = true
		}
	}
	for _, c := range cats {
		visit(c)
	}
	var out []string
	for _, m := range []string{MediaMovie, MediaSeries, MediaBook, MediaMusic} {
		if has[m] {
			out = append(out, m)
		}
	}
	if len(out) == 4 {
		return nil
	}
	return out
}

// SyncProwlarr mirrors a Prowlarr's indexers into Arrmada as Torznab sources, so Arrmada
// searches through Prowlarr's API instead of scraping sites itself.
//
// A sync owns only what it can know: a row's name, feed address, key and Prowlarr id.
// Whether it's used for movies or books, its priority, seeder floor and seed rules are the
// owner's, set once on a new row and never touched again. Rows are matched on Prowlarr's
// id, so a rename in Prowlarr renames the row (and the grabs that name it) instead of
// adding a second one. An indexer disabled or removed in Prowlarr is switched off here —
// never deleted, so its grabs keep their seed rules — and switched back on when it
// returns, unless the owner switched it off themselves. Usenet indexers aren't imported.
//
// Nothing changes when Prowlarr can't be read, or says it has no indexers while rows
// from it exist.
func (s *Service) SyncProwlarr(ctx context.Context, o ProwlarrSync) (SyncResult, error) {
	base := strings.TrimRight(strings.TrimSpace(o.URL), "/")
	if base == "" || o.APIKey == "" {
		return SyncResult{}, fmt.Errorf("prowlarr: URL and API key are required")
	}

	body, err := prowlarrDo(ctx, base, o.APIKey, http.MethodGet, "/api/v1/indexer", nil)
	if err != nil {
		return SyncResult{}, err
	}
	var remote []prowlarrIndexer
	if err := json.Unmarshal(body, &remote); err != nil {
		return SyncResult{}, fmt.Errorf("prowlarr: couldn't read its indexer list: %w", err)
	}

	if _, err := s.BackfillProwlarrIDs(ctx, base); err != nil {
		return SyncResult{}, err
	}
	existing, err := s.repo.List(ctx)
	if err != nil {
		return SyncResult{}, err
	}

	var (
		res    SyncResult
		resets []int64 // rows whose address, key or on/off changed: old failures no longer apply
	)
	err = store.WithTx(ctx, s.repo.db, func(tx *sql.Tx) error {
		res, resets = SyncResult{}, nil
		return s.applyProwlarr(ctx, tx, base, o.APIKey, remote, existing, &res, &resets)
	})
	if err != nil {
		return SyncResult{}, err
	}
	for _, id := range resets {
		s.resetStatus(ctx, id, false)
	}

	// FlareSolverr: the bundled Prowlarr is Arrmada's own, so it's pointed at Arrmada's
	// FlareSolverr; any other Prowlarr only when asked. A failure here never undoes the
	// indexer sync.
	switch {
	case o.FlareSolverrURL != "" && (isBundledProwlarr(base) || o.AddFlareSolverrProxy):
		if err := s.ensureFlareSolverrProxy(ctx, base, o.APIKey, o.FlareSolverrURL); err != nil {
			s.log.Warn("prowlarr: could not add the FlareSolverr proxy", "err", err)
			res.Notes = append(res.Notes, "Couldn't add Arrmada's FlareSolverr to Prowlarr: "+err.Error())
		}
	case o.AddFlareSolverrProxy:
		res.Notes = append(res.Notes, "FlareSolverr isn't set up in Arrmada, so it wasn't added to Prowlarr. Add its URL in Settings → System → API keys.")
	}
	res.FlareSolverrReady = s.prowlarrHasFlareSolverr(ctx, base, o.APIKey)
	return res, nil
}

// applyProwlarr writes one sync inside its transaction.
func (s *Service) applyProwlarr(ctx context.Context, tx *sql.Tx, base, key string, remote []prowlarrIndexer, existing []Indexer, res *SyncResult, resets *[]int64) error {
	byPID := map[int]Indexer{}
	byName := map[string]Indexer{} // rows synced before prowlarr_id, matched once by name
	for _, e := range existing {
		switch {
		case e.ProwlarrID > 0:
			if _, dup := byPID[e.ProwlarrID]; !dup {
				byPID[e.ProwlarrID] = e
			}
		case (e.Kind == KindTorznab || e.Kind == KindNewznab) && strings.HasPrefix(e.Name, prowlarrNamePrefix):
			byName[e.Name] = e
		}
	}
	if len(remote) == 0 {
		if len(byPID)+len(byName) > 0 {
			return errProwlarrEmpty
		}
		res.Notes = append(res.Notes, "Prowlarr has no indexers yet. Add trackers in Prowlarr, then sync again.")
		return nil
	}

	matched := map[int64]bool{}
	for _, pi := range remote {
		name := prowlarrNamePrefix + pi.Name
		feed := fmt.Sprintf("%s/%d/api", base, pi.ID)
		ex, found := byPID[pi.ID]
		if !found {
			if byN, ok := byName[name]; ok && !matched[byN.ID] {
				ex, found = byN, true
			}
		}
		if found {
			matched[ex.ID] = true
		}

		if strings.EqualFold(pi.Protocol, "usenet") {
			// Nothing can download what a usenet indexer finds; one synced before this check
			// existed is switched off with the reason.
			res.SkippedUsenet++
			if found {
				if err := s.syncOff(ctx, tx, ex, noteNeedsUsenetClient, res); err != nil {
					return err
				}
			}
			continue
		}

		if !found {
			if !pi.Enable {
				continue // only what's switched on in Prowlarr is brought in
			}
			priority := pi.Priority
			if priority < 1 || priority > 50 {
				priority = 25
			}
			// A new synced indexer must not inherit Go's zero values: SeedEnabled=false
			// means "remove as soon as it's imported", i.e. never seed at all — a
			// hit-and-run on any private tracker. Seed for the default window instead.
			idx := Indexer{
				Name: name, Kind: KindTorznab, URL: feed, APIKey: key, Priority: priority,
				MediaTypes:  mediaTypesFor(pi.Capabilities.Categories),
				SeedEnabled: true, SeedHours: DefaultSeedHours, Enabled: true, ProwlarrID: pi.ID,
			}
			if _, err := createIn(ctx, tx, idx); err != nil {
				return err
			}
			res.Added++
			continue
		}

		if ex.Name != name || ex.URL != feed || ex.APIKey != key || ex.ProwlarrID != pi.ID {
			if err := updateManaged(ctx, tx, ex.ID, pi.ID, name, feed, key); err != nil {
				return err
			}
			if ex.Name != name {
				if err := renameRefs(ctx, tx, ex.Name, name); err != nil {
					return err
				}
			}
			if ex.URL != feed || ex.APIKey != key {
				*resets = append(*resets, ex.ID)
			}
			res.Updated++
		} else {
			res.Unchanged++
		}

		switch {
		case !pi.Enable:
			if err := s.syncOff(ctx, tx, ex, noteDisabledInProwlarr, res); err != nil {
				return err
			}
		case !ex.Enabled && ex.DisabledBy == DisabledByProwlarr:
			if err := setManagedState(ctx, tx, ex.ID, true, "", ""); err != nil {
				return err
			}
			*resets = append(*resets, ex.ID)
			res.Reenabled++
		}
	}

	// Rows from Prowlarr it no longer lists. Rows matched only by name before are left
	// alone: without an id there's no telling they came from this Prowlarr.
	for _, e := range byPID {
		if matched[e.ID] {
			continue
		}
		if err := s.syncOff(ctx, tx, e, noteRemovedFromProwlarr, res); err != nil {
			return err
		}
	}
	return nil
}

// syncOff switches a row off for a sync with note. A row already off for the owner's own
// reason stays theirs; one a sync turned off just gets the newer note.
func (s *Service) syncOff(ctx context.Context, tx *sql.Tx, e Indexer, note string, res *SyncResult) error {
	switch {
	case e.Enabled:
		res.Disabled++
		return setManagedState(ctx, tx, e.ID, false, DisabledByProwlarr, note)
	case e.DisabledBy == DisabledByProwlarr && e.ManagedNote != note:
		return setManagedState(ctx, tx, e.ID, false, DisabledByProwlarr, note)
	}
	return nil
}

// isBundledProwlarr reports whether base is the Prowlarr that ships with Arrmada.
func isBundledProwlarr(base string) bool {
	u, err := url.Parse(base)
	return err == nil && strings.EqualFold(u.Hostname(), bundledProwlarrHost)
}

// prowlarrHasFlareSolverr asks Prowlarr whether it has a FlareSolverr proxy.
func (s *Service) prowlarrHasFlareSolverr(ctx context.Context, base, key string) bool {
	pb, err := prowlarrDo(ctx, base, key, http.MethodGet, "/api/v1/indexerproxy", nil)
	if err != nil {
		return false
	}
	var proxies []struct {
		Implementation string `json:"implementation"`
	}
	if json.Unmarshal(pb, &proxies) != nil {
		return false
	}
	for _, p := range proxies {
		if strings.EqualFold(p.Implementation, "FlareSolverr") {
			return true
		}
	}
	return false
}

// ensureFlareSolverrProxy makes sure Prowlarr has a FlareSolverr indexer proxy
// pointing at flareURL (Arrmada's bundled FlareSolverr), creating the required
// tag and proxy if absent. Idempotent.
func (s *Service) ensureFlareSolverrProxy(ctx context.Context, base, key, flareURL string) error {
	if s.prowlarrHasFlareSolverr(ctx, base, key) {
		return nil
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
