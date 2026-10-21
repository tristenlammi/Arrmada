package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/httpua"
	"github.com/tristenlammi/arrmada/internal/store"
)

// MusicBrainz is the music metadata provider. Like Open Library for books it needs no API
// key and is open data, which keeps the Music module off a single proprietary dependency.
// Arrmada talks to it directly (or to a mirror the owner runs), never through a hosted
// proxy of its own.
//
// Two hard requirements the service enforces and will block you for ignoring:
//   - every request must carry a descriptive User-Agent with contact info;
//   - anonymous clients get ONE request per second, from the whole address.
//
// Both are handled here rather than left to callers: one app-wide limiter (mblimit.go) and
// a permanent cache of every answer (mbcache.go), so a page opened twice costs one call and
// an outage costs nothing that was already known. main builds the only client (an archtest
// forbids a second).
type MusicBrainz struct {
	http *http.Client
	base string // web-service root override (tests); "" reads the configured address
	art  string // Cover Art Archive root; overridden in tests

	config func() MBConfig // the Settings → Connections values, read on every call
	cache  mbCache
	gate   *mbLimiter // mbGate: shared by every client in the process
	log    *slog.Logger
	now    func() time.Time

	// minInterval overrides the pacing gap when >= 0, so tests needn't sleep a second
	// between requests. Below zero the gap comes from the address (see interval).
	minInterval time.Duration

	refreshing sync.Map       // cache keys being refreshed in the background
	workers    chan struct{}  // slots for background refreshes (mbRefreshWorkers)
	bg         sync.WaitGroup // those refreshes, for tests
}

// MBConfig is how Arrmada reaches MusicBrainz.
type MBConfig struct {
	// BaseURL is the server: https://musicbrainz.org, or a self-hosted mirror ("" is the
	// public one). The web-service path /ws/2 is added when it's left off.
	BaseURL string
	// MirrorRate is requests a second allowed to a mirror (0 = one, like the public
	// server). The public server always gets one per 1.1 s, whatever this says.
	MirrorRate int
	// Contact is an email for the User-Agent, so MusicBrainz can reach the owner instead of
	// blocking the address. Optional.
	Contact string
}

const (
	// MBDefaultBaseURL is the public MusicBrainz server.
	MBDefaultBaseURL = "https://musicbrainz.org"
	// MBMaxMirrorRate bounds the mirror rate setting.
	MBMaxMirrorRate = 50
	caaBaseURL      = "https://coverartarchive.org"
	mbTimeout       = 20 * time.Second
	// mbRefreshTimeout bounds one background refresh of a stale answer.
	mbRefreshTimeout = 2 * time.Minute
)

// NewMusicBrainz builds the provider over the cache table in db (nil: no cache) with the
// configured address and contact from cfg (nil: the public server, no contact).
func NewMusicBrainz(db store.Pool, cfg func() MBConfig) *MusicBrainz {
	if cfg == nil {
		cfg = func() MBConfig { return MBConfig{} }
	}
	return &MusicBrainz{
		http: &http.Client{Timeout: mbTimeout}, art: caaBaseURL,
		config: cfg, cache: mbCache{db: db}, gate: mbGate, log: slog.Default(), now: time.Now,
		minInterval: -1, workers: make(chan struct{}, mbRefreshWorkers),
	}
}

// SetLogger sets where cache and refresh notes are logged.
func (m *MusicBrainz) SetLogger(l *slog.Logger) {
	if l != nil {
		m.log = l
	}
}

// mbWSRoot is the web-service root for a configured base URL.
func mbWSRoot(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = MBDefaultBaseURL
	}
	if strings.HasSuffix(base, "/ws/2") {
		return base
	}
	return base + "/ws/2"
}

// NormalizeMBBaseURL checks a MusicBrainz address from Settings: an http(s) URL with a host
// and nothing after the path ("" means the public server). A LAN or private address is
// fine — a self-hosted mirror usually is one — which is why only an admin may set it.
func NormalizeMBBaseURL(raw string) (string, error) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("the MusicBrainz address must be an http:// or https:// URL, like %s", MBDefaultBaseURL)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the MusicBrainz address can't carry a user name, a query or a fragment")
	}
	return s, nil
}

// isPublicMB reports whether a web-service root is the public MusicBrainz service, which
// always gets the public pacing.
func isPublicMB(root string) bool {
	u, err := url.Parse(root)
	if err != nil {
		return true // unparseable: pace it as if it were
	}
	h := strings.ToLower(u.Hostname())
	return h == "musicbrainz.org" || strings.HasSuffix(h, ".musicbrainz.org")
}

// root is the web-service root in effect.
func (m *MusicBrainz) root() string {
	if m.base != "" {
		return m.base
	}
	return mbWSRoot(m.config().BaseURL)
}

// interval is the pacing gap for the address in effect: 1.1 s for musicbrainz.org, and a
// mirror's own rate otherwise (one a second unless the owner raised it).
func (m *MusicBrainz) interval(root string) time.Duration {
	if m.minInterval >= 0 {
		return m.minInterval
	}
	if isPublicMB(root) {
		return mbPublicInterval
	}
	rate := m.config().MirrorRate
	if rate <= 1 {
		return mbPublicInterval
	}
	if rate > MBMaxMirrorRate {
		rate = MBMaxMirrorRate
	}
	return time.Second / time.Duration(rate)
}

// userAgent says which program asks and how to reach whoever runs it, in MusicBrainz's
// "Name/version ( contact )" form, with the build's real version.
func (m *MusicBrainz) userAgent() string {
	contact := httpua.Homepage
	if c := strings.TrimSpace(m.config().Contact); c != "" {
		contact += "; " + c
	}
	return "Arrmada/" + httpua.Version() + " ( " + contact + " )"
}

// Available is always true — no key required.
func (m *MusicBrainz) Available() bool { return true }

// ArtistResult is one artist from a search.
type ArtistResult struct {
	MBID string `json:"mbid"`
	Name string `json:"name"`
	// Disambiguation is MusicBrainz's short qualifier ("UK punk band"), which is often the
	// only way to tell two same-named artists apart.
	Disambiguation string   `json:"disambiguation,omitempty"`
	SortName       string   `json:"sort_name,omitempty"`
	Country        string   `json:"country,omitempty"`
	Type           string   `json:"type,omitempty"` // Person | Group | …
	Genres         []string `json:"genres,omitempty"`
	// Aliases are the other names MusicBrainz knows the artist by (MUS-38): other
	// scripts ("Utada Hikaru" for 宇多田ヒカル), legal and old names, search hints.
	Aliases []ArtistAlias `json:"aliases,omitempty"`
	// AKA is the alias a search matched when the artist's own name doesn't contain what
	// was typed ("also known as"), set by the music service's lookup.
	AKA string `json:"aka,omitempty"`
}

// ArtistAlias is one of an artist's other names on MusicBrainz.
type ArtistAlias struct {
	Name     string `json:"name"`
	SortName string `json:"sort_name,omitempty"`
	// Locale is the language (and script) the alias is for ("ja", "en"); "" when it isn't
	// tied to one.
	Locale string `json:"locale,omitempty"`
	// Type is MusicBrainz's alias type: "Artist name", "Legal name", "Search hint".
	Type string `json:"type,omitempty"`
	// Primary marks the alias MusicBrainz treats as the name in its locale.
	Primary bool `json:"primary,omitempty"`
}

// mbAlias is an alias as MusicBrainz sends it (locale, type and primary may be null).
type mbAlias struct {
	Name     string `json:"name"`
	SortName string `json:"sort-name"`
	Locale   string `json:"locale"`
	Type     string `json:"type"`
	Primary  bool   `json:"primary"`
}

// aliasesOf turns MusicBrainz's alias list into ArtistAliases, dropping blanks and
// repeats of one name.
func aliasesOf(in []mbAlias) []ArtistAlias {
	if len(in) == 0 {
		return nil
	}
	out := make([]ArtistAlias, 0, len(in))
	seen := map[string]bool{}
	for _, a := range in {
		name := strings.TrimSpace(a.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, ArtistAlias{Name: name, SortName: strings.TrimSpace(a.SortName), Locale: a.Locale, Type: a.Type, Primary: a.Primary})
	}
	return out
}

// ArtistDetails is a full artist record.
type ArtistDetails struct {
	ArtistResult
	Overview string `json:"overview,omitempty"`
}

// AlbumResult is one release group (an "album") by an artist.
type AlbumResult struct {
	MBID        string `json:"mbid"`
	Title       string `json:"title"`
	Type        string `json:"type,omitempty"` // the label: Album | EP | Single | Live | Compilation…
	ReleaseDate string `json:"release_date,omitempty"`
	Year        int    `json:"year,omitempty"`
	CoverURL    string `json:"cover_url,omitempty"`
	// PrimaryType and SecondaryTypes are MusicBrainz's own (Album; Live, Compilation…).
	// Type is the label made from them: the first secondary type when there is one.
	PrimaryType    string   `json:"primary_type,omitempty"`
	SecondaryTypes []string `json:"secondary_types,omitempty"`
}

// OtherReleaseGroupsProvider lists an artist's release groups outside albums and EPs:
// singles, broadcasts and "other". A separate browse, asked only for artists whose
// release types include one of those, so a big artist's hundreds of singles cost nothing
// for everyone else.
type OtherReleaseGroupsProvider interface {
	ArtistOtherReleaseGroups(ctx context.Context, mbid string) ([]AlbumResult, error)
}

// ReleaseGroupProvider looks one release group up by MBID. MusicBrainz answers a merged
// group under its new MBID, which is how an album that left its artist's list is told
// apart from one that was merged into another.
type ReleaseGroupProvider interface {
	ReleaseGroup(ctx context.Context, mbid string) (AlbumResult, error)
}

// TrackResult is one track of an album's listing.
type TrackResult struct {
	// MBID is the release track's own id (MusicBrainz's media[].tracks[].id), not the
	// recording's: RecordingMBID is that.
	MBID          string `json:"mbid,omitempty"`
	RecordingMBID string `json:"recording_mbid,omitempty"`
	// ReleaseMBID is the release the listing was read from (the same on every track).
	ReleaseMBID string `json:"release_mbid,omitempty"`
	Title       string `json:"title"`
	Disc        int    `json:"disc"`
	Number      int    `json:"number"`
	// Printed is the number as the release prints it ("A1" on vinyl), when it says.
	Printed     string `json:"printed,omitempty"`
	DurationSec int    `json:"duration_sec,omitempty"`
}

// MusicProvider is the metadata surface the Music module needs.
type MusicProvider interface {
	Available() bool
	SearchArtists(ctx context.Context, query string) ([]ArtistResult, error)
	GetArtist(ctx context.Context, mbid string) (*ArtistDetails, error)
	// ArtistAlbums returns the artist's album-type release groups, newest first.
	ArtistAlbums(ctx context.Context, mbid string) ([]AlbumResult, error)
	// AlbumTracks returns the track listing for a release group, taken from a
	// representative release within it.
	AlbumTracks(ctx context.Context, releaseGroupMBID string) ([]TrackResult, error)
}

// SearchArtists finds artists by name.
func (m *MusicBrainz) SearchArtists(ctx context.Context, query string) ([]ArtistResult, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	var body struct {
		Artists []struct {
			ID             string    `json:"id"`
			Name           string    `json:"name"`
			SortName       string    `json:"sort-name"`
			Disambiguation string    `json:"disambiguation"`
			Country        string    `json:"country"`
			Type           string    `json:"type"`
			Aliases        []mbAlias `json:"aliases"`
		} `json:"artists"`
	}
	if err := m.lookup(ctx, mbKindSearch, "/artist", url.Values{"query": {strings.ToLower(strings.Join(strings.Fields(q), " "))}, "limit": {"15"}}, &body); err != nil {
		return nil, err
	}
	out := make([]ArtistResult, 0, len(body.Artists))
	for _, a := range body.Artists {
		out = append(out, ArtistResult{
			MBID: a.ID, Name: a.Name, SortName: a.SortName,
			Disambiguation: a.Disambiguation, Country: a.Country, Type: a.Type,
			Aliases: aliasesOf(a.Aliases),
		})
	}
	return out, nil
}

// mbArtistInc is what an artist lookup includes. Changing it changes every artist's cache
// key, so a migration has to move the cached answers along (0344 did, for aliases).
const mbArtistInc = "aliases genres"

// GetArtist fetches one artist, including genres and its aliases (MUS-38). The aliases
// come in the same call, so knowing an artist's other names costs nothing extra.
func (m *MusicBrainz) GetArtist(ctx context.Context, mbid string) (*ArtistDetails, error) {
	if strings.TrimSpace(mbid) == "" {
		return nil, fmt.Errorf("musicbrainz: empty artist id")
	}
	var body struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		SortName       string `json:"sort-name"`
		Disambiguation string `json:"disambiguation"`
		Country        string `json:"country"`
		Type           string `json:"type"`
		Genres         []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"genres"`
		Aliases []mbAlias `json:"aliases"`
	}
	if err := m.lookup(ctx, mbKindArtist, "/artist/"+url.PathEscape(mbid), url.Values{"inc": {mbArtistInc}}, &body); err != nil {
		return nil, err
	}
	// Genres come back unordered with a vote count; the most-voted few are the useful ones.
	sort.Slice(body.Genres, func(i, j int) bool { return body.Genres[i].Count > body.Genres[j].Count })
	genres := make([]string, 0, 5)
	for _, g := range body.Genres {
		if len(genres) == 5 {
			break
		}
		genres = append(genres, g.Name)
	}
	return &ArtistDetails{ArtistResult: ArtistResult{
		MBID: body.ID, Name: body.Name, SortName: body.SortName,
		Disambiguation: body.Disambiguation, Country: body.Country, Type: body.Type, Genres: genres,
		Aliases: aliasesOf(body.Aliases),
	}}, nil
}

// ArtistAlbums returns the artist's albums and EPs, newest first, with their secondary
// types (live, compilation, remix…): the music service decides which to list from the
// artist's release types, so changing them costs no MusicBrainz call. The browse asks
// exactly what it always has, so the cached answers stay valid.
func (m *MusicBrainz) ArtistAlbums(ctx context.Context, mbid string) ([]AlbumResult, error) {
	return m.artistReleaseGroups(ctx, mbid, "album|ep")
}

// ArtistOtherReleaseGroups returns the artist's singles, broadcasts and "other" release
// groups, newest first (see OtherReleaseGroupsProvider).
func (m *MusicBrainz) ArtistOtherReleaseGroups(ctx context.Context, mbid string) ([]AlbumResult, error) {
	return m.artistReleaseGroups(ctx, mbid, "single|broadcast|other")
}

// mbReleaseGroup is a release group as browses and lookups return it.
type mbReleaseGroup struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	PrimaryType      string   `json:"primary-type"`
	SecondaryTypes   []string `json:"secondary-types"`
	FirstReleaseDate string   `json:"first-release-date"`
}

func (m *MusicBrainz) albumResult(rg mbReleaseGroup) AlbumResult {
	label := rg.PrimaryType
	if len(rg.SecondaryTypes) > 0 {
		label = rg.SecondaryTypes[0]
	}
	return AlbumResult{
		MBID: rg.ID, Title: rg.Title, Type: mbOrDefault(label, "Album"),
		ReleaseDate: rg.FirstReleaseDate, Year: mbYearOf(rg.FirstReleaseDate),
		CoverURL:    m.art + "/release-group/" + rg.ID + "/front-500",
		PrimaryType: rg.PrimaryType, SecondaryTypes: rg.SecondaryTypes,
	}
}

// mbReleaseGroupPages bounds an artist's browse: 500 release groups of one kind, past any
// real discography's albums and EPs.
const mbReleaseGroupPages = 5

// MBReleaseGroupBrowseCap is the most release groups one artist browse returns. A browse
// that came back this full may be cut short, so a release group missing from it may only
// be past the end.
const MBReleaseGroupBrowseCap = mbReleaseGroupPages * 100

func (m *MusicBrainz) artistReleaseGroups(ctx context.Context, mbid, types string) ([]AlbumResult, error) {
	if strings.TrimSpace(mbid) == "" {
		return nil, fmt.Errorf("musicbrainz: empty artist id")
	}
	var out []AlbumResult
	for page := 0; page < mbReleaseGroupPages; page++ {
		var body struct {
			ReleaseGroups []mbReleaseGroup `json:"release-groups"`
			Count         int              `json:"release-group-count"`
		}
		q := url.Values{
			"artist": {mbid}, "type": {types},
			"limit": {"100"}, "offset": {strconv.Itoa(page * 100)},
		}
		if err := m.lookup(ctx, mbKindReleaseGroup, "/release-group", q, &body); err != nil {
			return nil, err
		}
		for _, rg := range body.ReleaseGroups {
			out = append(out, m.albumResult(rg))
		}
		if len(body.ReleaseGroups) < 100 {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Year > out[j].Year })
	return out, nil
}

// ReleaseGroup looks one release group up by MBID. A merged group comes back under its
// new MBID; a removed one is a not-found error (IsNotFound).
func (m *MusicBrainz) ReleaseGroup(ctx context.Context, mbid string) (AlbumResult, error) {
	if strings.TrimSpace(mbid) == "" {
		return AlbumResult{}, fmt.Errorf("musicbrainz: empty release-group id")
	}
	var rg mbReleaseGroup
	if err := m.lookup(ctx, mbKindReleaseGroupLookup, "/release-group/"+url.PathEscape(mbid), url.Values{}, &rg); err != nil {
		return AlbumResult{}, err
	}
	return m.albumResult(rg), nil
}

var (
	_ OtherReleaseGroupsProvider = (*MusicBrainz)(nil)
	_ ReleaseGroupProvider       = (*MusicBrainz)(nil)
)

// AlbumTracks returns a release group's track listing.
//
// A release group holds many releases (regional pressings, reissues, deluxe editions) with
// different track counts. We take the earliest official release with a track listing, which
// is the standard edition a release is most likely to match — picking the deluxe edition
// would mark bonus tracks missing forever on a perfectly complete album.
func (m *MusicBrainz) AlbumTracks(ctx context.Context, releaseGroupMBID string) ([]TrackResult, error) {
	if strings.TrimSpace(releaseGroupMBID) == "" {
		return nil, fmt.Errorf("musicbrainz: empty release-group id")
	}
	var list struct {
		Releases []struct {
			ID     string `json:"id"`
			Date   string `json:"date"`
			Status string `json:"status"`
		} `json:"releases"`
	}
	q := url.Values{"release-group": {releaseGroupMBID}, "limit": {"25"}}
	if err := m.lookup(ctx, mbKindReleases, "/release", q, &list); err != nil {
		return nil, err
	}
	pick := ""
	pickDate := ""
	for _, r := range list.Releases {
		if r.Status != "" && !strings.EqualFold(r.Status, "Official") {
			continue
		}
		if pick == "" || (r.Date != "" && (pickDate == "" || r.Date < pickDate)) {
			pick, pickDate = r.ID, r.Date
		}
	}
	if pick == "" {
		return nil, nil // nothing official to read a listing from — not an error
	}

	var rel struct {
		Media []struct {
			Position int `json:"position"`
			Tracks   []struct {
				ID        string `json:"id"` // the release track's MBID
				Title     string `json:"title"`
				Number    string `json:"number"`
				Position  int    `json:"position"`
				Length    int    `json:"length"` // milliseconds
				Recording struct {
					ID string `json:"id"`
				} `json:"recording"`
			} `json:"tracks"`
		} `json:"media"`
	}
	if err := m.lookup(ctx, mbKindRelease, "/release/"+url.PathEscape(pick), url.Values{"inc": {"recordings"}}, &rel); err != nil {
		return nil, err
	}
	var out []TrackResult
	for _, md := range rel.Media {
		disc := md.Position
		if disc < 1 {
			disc = 1
		}
		for _, t := range md.Tracks {
			out = append(out, TrackResult{
				MBID: t.ID, RecordingMBID: t.Recording.ID, ReleaseMBID: pick,
				Title: t.Title, Disc: disc, Number: t.Position, Printed: t.Number,
				DurationSec: t.Length / 1000,
			})
		}
	}
	return out, nil
}

// mbRetries is how many times a 503 is retried before giving up. MusicBrainz answers 503
// both for "you're over the rate limit" and for ordinary server load, and it does so often
// enough that a single-shot client fails on perfectly normal use — the first live run of
// this client got one on its second call.
const mbRetries = 3

// mbMaxBody bounds one answer; the biggest real ones (a release with every recording) are
// a few hundred KB.
const mbMaxBody = 8 << 20

// fetch performs one paced web-service request against root and returns the body and
// status: 200, or 404 for "no such thing" (an answer, not a failure). Interactive calls
// retry a 503 or 429 with backoff; a background call gives up at once and rests the
// background lane, so a refresh never keeps pushing at a service that is refusing us.
// Every failure wraps ErrMusicBrainzUnavailable.
func (m *MusicBrainz) fetch(ctx context.Context, root, path string, q url.Values) ([]byte, int, error) {
	qq := url.Values{}
	for k, v := range q {
		qq[k] = v
	}
	qq.Set("fmt", "json")
	lane := mbLaneOf(ctx)
	interval := m.interval(root)
	retries := mbRetries
	if lane == MBBackground {
		retries = 0
	}

	var lastStatus string
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			// Back off progressively on top of the normal pacing. Scaled off the pacing
			// interval so a test that disables pacing doesn't sleep for seconds either.
			if err := sleepCtx(ctx, time.Duration(attempt)*interval); err != nil {
				return nil, 0, err
			}
		}
		if err := m.gate.wait(ctx, lane, interval); err != nil {
			return nil, 0, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+path+"?"+qq.Encode(), nil)
		if err != nil {
			return nil, 0, err
		}
		// MusicBrainz blocks requests without a descriptive User-Agent outright.
		req.Header.Set("User-Agent", m.userAgent())
		req.Header.Set("Accept", "application/json")

		resp, err := m.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, 0, ctx.Err()
			}
			return nil, 0, errMusicBrainzUnavailable(err.Error())
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, mbMaxBody))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound:
			m.gate.answered()
			return body, http.StatusNotFound, nil
		case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests:
			lastStatus = resp.Status
			if lane == MBBackground {
				m.gate.refused()
			}
			continue // rate-limited or briefly overloaded — wait and try again
		case resp.StatusCode != http.StatusOK:
			return nil, 0, errMusicBrainzUnavailable(fmt.Sprintf("%s returned %s", path, resp.Status))
		case rerr != nil:
			return nil, 0, errMusicBrainzUnavailable(rerr.Error())
		case !json.Valid(body):
			return nil, 0, errMusicBrainzUnavailable(path + " answered with something that isn't JSON")
		}
		m.gate.answered()
		return body, http.StatusOK, nil
	}
	return nil, 0, errMusicBrainzUnavailable(fmt.Sprintf("%s still returning %s after %d retries", path, lastStatus, retries))
}

// mbKey is the cache key for a question: its kind, path and query (without fmt). The host
// isn't in it, so a switch to or from a mirror keeps the cache.
func mbKey(k mbKind, path string, q url.Values) string {
	return k.name + ":" + path + "?" + q.Encode()
}

// lookup answers a question from the cache when it can, from MusicBrainz otherwise:
//   - fresh in the cache: the cached answer, no call;
//   - stale in the cache: the cached answer now, refetched in the background;
//   - not cached: fetched on the caller's lane and kept;
//   - not cached and MusicBrainz can't be asked: ErrMusicBrainzUnavailable.
//
// A 404 decodes as errNotFound.
//
// A caller that marked ctx WithMBRevalidate (the scheduled artist refresh) asks again for a
// stale answer itself, on its own lane, instead of leaving it to the background: it needs
// the newest list it can get, and is pacing itself on the calls it makes. If MusicBrainz
// can't answer, the stale answer is still used (and the trace says so).
func (m *MusicBrainz) lookup(ctx context.Context, k mbKind, path string, q url.Values, out any) error {
	key := mbKey(k, path, q)
	tr := mbTraceOf(ctx)
	if e, ok := m.cache.get(ctx, key); ok {
		if e.fresh(k, m.now()) {
			tr.add(traceFromCache)
			return decodeMB(e.status, e.body, out)
		}
		if !mbRevalidating(ctx) {
			tr.add(traceFromCache)
			m.refreshLater(key)
			return decodeMB(e.status, e.body, out)
		}
		if _, busy := m.refreshing.LoadOrStore(key, true); busy {
			tr.add(traceFromCache) // a background refresh has it in hand
			return decodeMB(e.status, e.body, out)
		}
		body, status, err := m.fetch(ctx, m.root(), path, q)
		m.refreshing.Delete(key)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			tr.add(traceStaleFallback)
			return decodeMB(e.status, e.body, out)
		}
		tr.add(traceFetched)
		m.keep(ctx, key, status, body)
		if status == http.StatusNotFound && mbFreshNotFound(ctx) {
			// The caller asked whether the thing still exists, and MusicBrainz just said
			// no: that is the answer, though the cache keeps what it had for everyone else.
			return errNotFound
		}
		// keep may have kept the old answer (an empty list or a 404 where we had content):
		// read back whatever the cache now holds.
		if now, ok := m.cache.get(ctx, key); ok {
			return decodeMB(now.status, now.body, out)
		}
		return decodeMB(status, body, out)
	}
	root := m.root()
	body, status, err := m.fetch(ctx, root, path, q)
	if err != nil {
		if ctx.Err() == nil {
			tr.add(traceFailed)
		}
		return err
	}
	tr.add(traceFetched)
	m.keep(ctx, key, status, body)
	return decodeMB(status, body, out)
}

func decodeMB(status int, body []byte, out any) error {
	if status == http.StatusNotFound {
		return errNotFound
	}
	return json.Unmarshal(body, out)
}

// keep stores an answer, unless it would lose something the cache already has: a 404 or an
// empty list where the cached answer had content is MusicBrainz having a bad moment (or an
// entity being merged), not the music going away, so the old answer stays and is marked as
// checked. That's the difference from Lidarr, which emptied libraries on an empty answer.
func (m *MusicBrainz) keep(ctx context.Context, key string, status int, body []byte) {
	now := m.now()
	if old, ok := m.cache.get(ctx, key); ok && old.status == http.StatusOK {
		k, _ := mbKindOfKey(key)
		switch {
		case status == http.StatusNotFound:
			m.log.Warn("musicbrainz: an answer we had now says not found — keeping what we had", "key", key)
			m.cache.touch(ctx, key, now)
			return
		case k.list != "" && mbListLen(body, k.list) == 0 && mbListLen(old.body, k.list) > 0:
			m.log.Warn("musicbrainz: answered with an empty list where we had entries — keeping what we had", "key", key)
			m.cache.touch(ctx, key, now)
			return
		}
	}
	if err := m.cache.put(ctx, key, status, body, now); err != nil {
		m.log.Warn("musicbrainz: couldn't cache an answer", "key", key, "err", err)
	}
	m.keepUnderNewID(ctx, key, status, body, now)
}

// keepUnderNewID also files an entity answer under the id it came back with, when that
// differs from the one asked for: MusicBrainz merged it, and the new id is what later
// lookups will use.
func (m *MusicBrainz) keepUnderNewID(ctx context.Context, key string, status int, body []byte, now time.Time) {
	if status != http.StatusOK {
		return
	}
	k, ok := mbKindOfKey(key)
	if !ok || (k != mbKindArtist && k != mbKindRelease && k != mbKindReleaseGroupLookup) {
		return
	}
	var ent struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &ent) != nil || ent.ID == "" {
		return
	}
	// key: "<kind>:/<entity>/<mbid>?<query>"
	rest := strings.TrimPrefix(key, k.name+":")
	path, query, _ := strings.Cut(rest, "?")
	i := strings.LastIndex(path, "/")
	if i < 0 || path[i+1:] == ent.ID {
		return
	}
	newKey := k.name + ":" + path[:i+1] + ent.ID + "?" + query
	_ = m.cache.put(ctx, newKey, status, body, now)
}

// mbListLen is how many entries a list field of an answer holds (0 when unreadable).
func mbListLen(body []byte, field string) int {
	var v map[string]json.RawMessage
	if json.Unmarshal(body, &v) != nil {
		return 0
	}
	var list []json.RawMessage
	if json.Unmarshal(v[field], &list) != nil {
		return 0
	}
	return len(list)
}

// mbRefreshWorkers bounds the background refreshes of stale answers running at once.
// They all wait their turn at the one rate limit anyway; the bound keeps a page that opens
// dozens of stale albums from parking dozens of goroutines on it.
const mbRefreshWorkers = 4

// refreshLater refetches a stale answer in the background, once per key at a time and at
// most mbRefreshWorkers at once; with every worker busy it does nothing, and the key is
// tried again the next time it's read (or by the refresh task). The caller already has the
// cached answer; this only brings the cache up to date.
func (m *MusicBrainz) refreshLater(key string) {
	select {
	case m.workers <- struct{}{}:
	default:
		return // every worker busy: the answer stays stale a little longer, nothing more
	}
	if _, busy := m.refreshing.LoadOrStore(key, true); busy {
		<-m.workers
		return
	}
	m.bg.Add(1)
	go func() {
		defer m.bg.Done()
		defer func() { <-m.workers }()
		defer m.refreshing.Delete(key)
		ctx, cancel := context.WithTimeout(WithMBLane(context.Background(), MBBackground), mbRefreshTimeout)
		defer cancel()
		_ = m.refreshKey(ctx, key)
	}()
}

// refreshKey refetches one cached question. A failure leaves the cached answer as it is.
func (m *MusicBrainz) refreshKey(ctx context.Context, key string) error {
	k, ok := mbKindOfKey(key)
	if !ok {
		return nil
	}
	path, query, _ := strings.Cut(strings.TrimPrefix(key, k.name+":"), "?")
	q, err := url.ParseQuery(query)
	if err != nil {
		return nil
	}
	body, status, err := m.fetch(ctx, m.root(), path, q)
	if err != nil {
		return err
	}
	m.keep(ctx, key, status, body)
	return nil
}

// RefreshStale refetches up to limit cached answers past their freshness, longest
// unchecked first, on the background lane. It stops early when MusicBrainz starts refusing
// (the background lane is then resting); nothing cached is ever removed. It returns how
// many answers were checked.
func (m *MusicBrainz) RefreshStale(ctx context.Context, limit int) (int, error) {
	m.cache.pruneSearches(ctx, m.now())
	keys, err := m.cache.stalest(ctx, m.now(), limit)
	if err != nil {
		return 0, err
	}
	ctx = WithMBLane(ctx, MBBackground)
	n := 0
	for _, key := range keys {
		// A key a page's background refresh already has in hand is left to it.
		if _, busy := m.refreshing.LoadOrStore(key, true); busy {
			continue
		}
		err := m.refreshKey(ctx, key)
		m.refreshing.Delete(key)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, errMBPaused) {
				return n, nil
			}
			continue
		}
		n++
	}
	return n, nil
}

// CacheStats is how many answers are cached, their size and how many are stale.
func (m *MusicBrainz) CacheStats(ctx context.Context) (MBCacheStats, error) {
	return m.cache.stats(ctx, m.now())
}

// mbTestArtist is a well-known artist (Radiohead) the connection test looks up.
const mbTestArtist = "a74b1b7f-71a5-4011-9441-d0b5e4122711"

// Test checks that a MusicBrainz address answers, looking up a well-known artist there
// (not from the cache). base "" tests the public server.
func (m *MusicBrainz) Test(ctx context.Context, base string) error {
	root := mbWSRoot(base)
	body, status, err := m.fetch(WithMBLane(ctx, MBInteractive), root, "/artist/"+mbTestArtist, url.Values{})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s answered, but doesn't know a well-known artist: is it a MusicBrainz server?", root)
	}
	var a struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &a) != nil || a.ID == "" {
		return fmt.Errorf("%s answered with something that isn't a MusicBrainz artist", root)
	}
	return nil
}

func mbOrDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// mbYearOf reads the leading year from a MusicBrainz date ("1997", "1997-09", "1997-09-30").
func mbYearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return y
}
