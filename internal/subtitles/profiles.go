package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/tristenlammi/arrmada/internal/langs"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Language profiles: which subtitles a title wants, by language and variant. The kept
// languages used to be one flat list; a profile item can also ask for the forced subtitle
// (the foreign-dialogue lines), prefer or avoid SDH, or want a language only when the
// film's audio is in another one. Every title uses the default profile unless it has an
// override (one movie, or one series and all its episodes).
//
// The default profile IS the kept-language list: languages() reads its languages and
// SetSettings(languages) rewrites them, keeping each language's options. On an existing
// install it is seeded from the subs_languages setting the first time it's read, one
// plain item per language, so nothing counts differently until someone edits it.

// Profile item variants: which subtitle of the language is wanted.
const (
	WantFull   = "full"   // the full subtitle (an SDH one counts)
	WantForced = "forced" // only the forced (foreign-parts) subtitle
	WantBoth   = "both"   // both, as two separate wants
)

// SDH preferences for a full want: any (a plain subtitle first, SDH when that's all there
// is), prefer (SDH first) or avoid (never extract or download an SDH one).
const (
	SDHAny    = "any"
	SDHPrefer = "prefer"
	SDHAvoid  = "avoid"
)

// Override kinds: an override pins one movie, or one series with all its episodes.
const (
	OverrideMovie  = "movie"
	OverrideSeries = "series"
)

// maxProfileItems bounds a profile; nobody keeps fifty subtitle languages.
const maxProfileItems = 50

var (
	ErrProfileNotFound   = errors.New("subtitle profile not found")
	ErrProfileNameTaken  = errors.New("a subtitle profile with that name already exists")
	ErrDefaultProfile    = errors.New("the default subtitle profile can't be deleted")
	ErrProfileInvalid    = errors.New("invalid subtitle profile")
	ErrOverrideKind      = errors.New("override kind must be movie or series")
	ErrOverrideNoTitle   = errors.New("no such title")
	errProfilesUnwritten = errors.New("subtitle profiles need the database")
)

// ProfileItem is one language in a profile.
type ProfileItem struct {
	Lang    string `json:"lang"`    // canonical internal/langs code
	Variant string `json:"variant"` // WantFull | WantForced | WantBoth
	SDH     string `json:"sdh"`     // SDHAny | SDHPrefer | SDHAvoid (full wants only)
	// OnlyIfAudioDiffers skips the full subtitle when the file's main audio is already in
	// this language — English subtitles for foreign films only. A forced want ignores it:
	// forced subtitles are exactly for a film in your own language with a few lines in another.
	OnlyIfAudioDiffers bool `json:"only_if_audio_differs"`
}

// plain reports whether an item is what the flat language list meant: the full
// subtitle, any SDH, whatever the audio.
func (it ProfileItem) plain() bool {
	return it.Variant == WantFull && it.SDH == SDHAny && !it.OnlyIfAudioDiffers
}

// Profile is a named, ordered list of wanted languages.
type Profile struct {
	ID        int64         `json:"id"`
	Name      string        `json:"name"`
	Items     []ProfileItem `json:"items"`
	IsDefault bool          `json:"is_default"`
	Titles    int           `json:"titles"` // titles overridden to use it
}

// Override pins one title to a profile.
type Override struct {
	Kind      string `json:"kind"` // OverrideMovie | OverrideSeries
	ID        int64  `json:"id"`
	ProfileID int64  `json:"profile_id"`
	Title     string `json:"title,omitempty"`
}

// Want is one subtitle a file should have: a language in one variant. A profile item
// asking for both becomes two wants.
type Want struct {
	Lang               string
	Variant            string // VariantFull ("") or VariantForced
	SDH                string
	OnlyIfAudioDiffers bool
}

// key identifies a want within a file: its language (lower-case) and variant.
func (w Want) key() string { return strings.ToLower(w.Lang) + "|" + w.Variant }

// wants expands a profile's items, in order (a "both" item: full, then forced).
func (p Profile) wants() []Want {
	out := make([]Want, 0, len(p.Items))
	for _, it := range p.Items {
		if it.Variant != WantForced {
			out = append(out, Want{Lang: it.Lang, Variant: VariantFull, SDH: it.SDH, OnlyIfAudioDiffers: it.OnlyIfAudioDiffers})
		}
		if it.Variant == WantForced || it.Variant == WantBoth {
			out = append(out, Want{Lang: it.Lang, Variant: VariantForced, SDH: SDHAny})
		}
	}
	return out
}

// langs is the profile's languages, each once, in order.
func (p Profile) langs() []string {
	var out []string
	seen := map[string]bool{}
	for _, it := range p.Items {
		if !seen[it.Lang] {
			seen[it.Lang] = true
			out = append(out, it.Lang)
		}
	}
	return out
}

// plainWants is the flat language list as wants: the full subtitle of each.
func plainWants(list []string) []Want {
	out := make([]Want, 0, len(list))
	for _, l := range list {
		out = append(out, Want{Lang: l, Variant: VariantFull, SDH: SDHAny})
	}
	return out
}

// wantLangs is the languages of the wants in one variant, in order.
func wantLangs(ws []Want, variant string) []string {
	var out []string
	for _, w := range ws {
		if w.Variant == variant {
			out = append(out, w.Lang)
		}
	}
	return out
}

// sdhFor is the SDH preference of a file's full want in lang (SDHAny when there's none).
func sdhFor(ws []Want, lang string) string {
	for _, w := range ws {
		if w.Variant == VariantFull && strings.EqualFold(w.Lang, lang) && w.SDH != "" {
			return w.SDH
		}
	}
	return SDHAny
}

// --- the loaded configuration ---

// profileConfig is every profile and override, read once and kept until one changes.
type profileConfig struct {
	profiles  []Profile        // the default first, then by name
	overrides map[string]int64 // overrideKey → profile id
	key       string           // what coverage was counted against (snapshot staleness)
}

func overrideKey(kind string, id int64) string { return kind + ":" + strconv.FormatInt(id, 10) }

func (c *profileConfig) def() Profile {
	for _, p := range c.profiles {
		if p.IsDefault {
			return p
		}
	}
	return Profile{Name: "Default", IsDefault: true, Items: plainItems(defaultLanguages)}
}

func (c *profileConfig) byID(id int64) (Profile, bool) {
	for _, p := range c.profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// forTitle is the profile a movie (kind "movie") or an episode or series (seriesID) uses,
// and whether it is an override.
func (c *profileConfig) forTitle(kind string, movieID, seriesID int64) (Profile, bool) {
	k := overrideKey(OverrideMovie, movieID)
	if kind != "movie" {
		k = overrideKey(OverrideSeries, seriesID)
	}
	if id, ok := c.overrides[k]; ok {
		if p, ok := c.byID(id); ok {
			return p, true
		}
	}
	return c.def(), false
}

// buildKey renders what decides coverage as one comparable string: the default profile's
// items and each override with its profile's items. A default of plain items and no
// overrides renders exactly as the flat list did ("en,fr"). A profile no title uses
// doesn't take part, so editing it recounts nothing.
func (c *profileConfig) buildKey() {
	var b strings.Builder
	b.WriteString(itemsKey(c.def().Items))
	keys := make([]string, 0, len(c.overrides))
	for k := range c.overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p, _ := c.byID(c.overrides[k])
		b.WriteString("|" + k + ">" + itemsKey(p.Items))
	}
	c.key = b.String()
}

func itemsKey(items []ProfileItem) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		if it.plain() {
			parts = append(parts, it.Lang)
			continue
		}
		t := it.Lang + "/" + it.Variant + "/" + it.SDH
		if it.OnlyIfAudioDiffers {
			t += "/a"
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, ",")
}

func plainItems(list []string) []ProfileItem {
	out := make([]ProfileItem, 0, len(list))
	for _, l := range list {
		out = append(out, ProfileItem{Lang: l, Variant: WantFull, SDH: SDHAny})
	}
	return out
}

// flatConfig is the configuration without a database (some tests): a default profile of
// the flat kept-language setting.
func flatConfig(list []string) *profileConfig {
	c := &profileConfig{profiles: []Profile{{Name: "Default", IsDefault: true, Items: plainItems(list)}}}
	c.buildKey()
	return c
}

// profileStore caches the loaded configuration; a change drops it.
type profileStore struct {
	mu  sync.Mutex
	cfg *profileConfig
}

func (s *Service) profileDB() store.Pool {
	if s.ledger.ok() {
		return s.ledger.db
	}
	return nil
}

// profileCfg returns the profiles and overrides, loading them on first use (and seeding
// the default profile from the kept-language setting then). Reads are in memory after
// that. If the tables can't be read, coverage falls back to the flat setting rather than
// to nothing.
func (s *Service) profileCfg(ctx context.Context) *profileConfig {
	db := s.profileDB()
	if db == nil {
		return flatConfig(s.settingsLanguages(ctx))
	}
	s.profiles.mu.Lock()
	defer s.profiles.mu.Unlock()
	if s.profiles.cfg != nil {
		return s.profiles.cfg
	}
	cfg, err := s.loadProfiles(context.WithoutCancel(ctx), db)
	if err != nil {
		if s.log != nil {
			s.log.Warn("subtitles: could not read language profiles, using the kept languages", "err", err)
		}
		return flatConfig(s.settingsLanguages(ctx))
	}
	s.profiles.cfg = cfg
	return cfg
}

// dropProfiles forgets the cached configuration so the next read sees a change.
func (s *Service) dropProfiles() {
	s.profiles.mu.Lock()
	s.profiles.cfg = nil
	s.profiles.mu.Unlock()
}

func (s *Service) loadProfiles(ctx context.Context, db store.Pool) (*profileConfig, error) {
	read := func() ([]Profile, error) {
		rows, err := db.QueryContext(ctx, `SELECT id, name, items_json, is_default FROM subtitle_profiles
			ORDER BY is_default DESC, `+store.FoldASCII(db, "name"))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []Profile
		for rows.Next() {
			var p Profile
			var items string
			if err := rows.Scan(&p.ID, &p.Name, &items, &p.IsDefault); err != nil {
				return nil, err
			}
			if err := json.Unmarshal([]byte(items), &p.Items); err != nil {
				return nil, fmt.Errorf("profile %d: %w", p.ID, err)
			}
			p.Items = normalizeStored(p.Items)
			out = append(out, p)
		}
		return out, rows.Err()
	}
	profiles, err := read()
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 || !profiles[0].IsDefault {
		// First use: the default profile is the kept languages as they were.
		items, _ := json.Marshal(plainItems(s.settingsLanguages(ctx)))
		if _, err := db.ExecContext(ctx, `INSERT INTO subtitle_profiles (name, items_json, is_default) VALUES ('Default', ?, 1) ON CONFLICT DO NOTHING`, string(items)); err != nil {
			return nil, err
		}
		if profiles, err = read(); err != nil {
			return nil, err
		}
		if len(profiles) == 0 || !profiles[0].IsDefault {
			return nil, errors.New("no default subtitle profile after seeding")
		}
	}
	cfg := &profileConfig{profiles: profiles, overrides: map[string]int64{}}
	rows, err := db.QueryContext(ctx, `SELECT media_kind, media_id, profile_id FROM subtitle_profile_overrides`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	titles := map[int64]int{}
	for rows.Next() {
		var kind string
		var id, pid int64
		if err := rows.Scan(&kind, &id, &pid); err != nil {
			return nil, err
		}
		cfg.overrides[overrideKey(kind, id)] = pid
		titles[pid]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range cfg.profiles {
		cfg.profiles[i].Titles = titles[cfg.profiles[i].ID]
	}
	cfg.buildKey()
	return cfg, nil
}

// normalizeStored fills in a stored item's blanks (an older or hand-written row) without
// rejecting it: a profile that loaded once keeps loading.
func normalizeStored(items []ProfileItem) []ProfileItem {
	out := items[:0]
	for _, it := range items {
		if it.Lang = langs.Canonical(it.Lang); it.Lang == "" {
			continue
		}
		if it.Variant != WantForced && it.Variant != WantBoth {
			it.Variant = WantFull
		}
		if it.SDH != SDHPrefer && it.SDH != SDHAvoid {
			it.SDH = SDHAny
		}
		out = append(out, it)
	}
	return out
}

// cleanItems validates a profile's items from the API: known languages (stored
// canonically), each once, a known variant and SDH preference (blank = full, any).
func cleanItems(in []ProfileItem) ([]ProfileItem, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("%w: a profile needs at least one language", ErrProfileInvalid)
	}
	if len(in) > maxProfileItems {
		return nil, fmt.Errorf("%w: at most %d languages", ErrProfileInvalid, maxProfileItems)
	}
	out := make([]ProfileItem, 0, len(in))
	seen := map[string]bool{}
	for _, it := range in {
		l, ok := langs.Lookup(it.Lang)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownLanguage, strings.TrimSpace(it.Lang))
		}
		if seen[l.Code] {
			return nil, fmt.Errorf("%w: %s is listed twice", ErrProfileInvalid, l.Name)
		}
		seen[l.Code] = true
		it.Lang = l.Code
		switch it.Variant {
		case "":
			it.Variant = WantFull
		case WantFull, WantForced, WantBoth:
		default:
			return nil, fmt.Errorf("%w: variant must be full, forced or both", ErrProfileInvalid)
		}
		switch it.SDH {
		case "":
			it.SDH = SDHAny
		case SDHAny, SDHPrefer, SDHAvoid:
		default:
			return nil, fmt.Errorf("%w: sdh must be any, prefer or avoid", ErrProfileInvalid)
		}
		out = append(out, it)
	}
	return out, nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", fmt.Errorf("%w: a profile needs a name", ErrProfileInvalid)
	case len([]rune(name)) > 60:
		return "", fmt.Errorf("%w: the name is longer than 60 characters", ErrProfileInvalid)
	}
	return name, nil
}

// isUniqueErr reports a UNIQUE constraint failure (a taken profile name).
func isUniqueErr(err error) bool {
	return store.IsUniqueViolation(err)
}

// --- reading ---

// Profiles lists every profile, the default first.
func (s *Service) Profiles(ctx context.Context) []Profile {
	cfg := s.profileCfg(ctx)
	out := make([]Profile, len(cfg.profiles))
	copy(out, cfg.profiles)
	return out
}

// Overrides lists every title pinned to a profile, with its title when it still exists.
func (s *Service) Overrides(ctx context.Context) []Override {
	cfg := s.profileCfg(ctx)
	out := make([]Override, 0, len(cfg.overrides))
	for k, pid := range cfg.overrides {
		kind, idStr, _ := strings.Cut(k, ":")
		id, _ := strconv.ParseInt(idStr, 10, 64)
		o := Override{Kind: kind, ID: id, ProfileID: pid}
		switch {
		case kind == OverrideMovie && s.movies != nil:
			if m, err := s.movies.Get(ctx, id); err == nil {
				o.Title = m.Title
			}
		case kind == OverrideSeries && s.series != nil:
			if sr, err := s.series.Get(ctx, id); err == nil {
				o.Title = sr.Title
			}
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool {
		if !strings.EqualFold(out[i].Title, out[j].Title) {
			return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
		}
		return out[i].Kind+strconv.FormatInt(out[i].ID, 10) < out[j].Kind+strconv.FormatInt(out[j].ID, 10)
	})
	return out
}

// titleWants is what a movie (kind "movie") or an episode (kind "episode", its series)
// wants, before the file's audio is considered.
func (s *Service) titleWants(ctx context.Context, kind string, movieID, seriesID int64) []Want {
	p, _ := s.profileCfg(ctx).forTitle(kind, movieID, seriesID)
	return p.wants()
}

// titleLangs is the languages a title's profile wants the full subtitle of, each once —
// what the orphan actions treat as kept for it (the first is what an untagged subtitle
// stands for). A profile of forced wants only falls back to its languages, so there is
// always a first.
func (s *Service) titleLangs(ctx context.Context, kind string, movieID, seriesID int64) []string {
	p, _ := s.profileCfg(ctx).forTitle(kind, movieID, seriesID)
	if l := wantLangs(p.wants(), VariantFull); len(l) > 0 {
		return l
	}
	if l := p.langs(); len(l) > 0 {
		return l
	}
	return s.languages(ctx)
}

// wantsKey is what the snapshot's figures depend on (profileConfig.buildKey).
func (s *Service) wantsKey(ctx context.Context) string { return s.profileCfg(ctx).key }

// keepLanguages is every language any profile wants, the default profile's first: what
// Convert's "Same as Subtitles" keeps. A union, so Convert never strips an embedded track
// that a title's own profile would extract.
func (s *Service) keepLanguages(ctx context.Context) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range s.profileCfg(ctx).profiles {
		for _, l := range p.langs() {
			if !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	if len(out) == 0 {
		return s.languages(ctx)
	}
	return out
}

// --- the file's audio ---

// primaryAudio is the language of a file's main audio: the track flagged default, else
// the first. An untagged track counts as what whisper confidently heard on it
// (withDetected). "" when unknown (no probe, no audio, untagged and not heard).
func primaryAudio(mi *mediaInfo) string {
	mi = mi.withDetected()
	if mi == nil || len(mi.Audio) == 0 {
		return ""
	}
	t := mi.Audio[0]
	for _, a := range mi.Audio {
		if a.Default {
			t = a
			break
		}
	}
	if t.untagged() {
		return ""
	}
	return langs.Canonical(t.Lang)
}

// activeWants drops the full wants a file doesn't need because its main audio is already
// in that language (OnlyIfAudioDiffers). Unknown audio keeps the want: better to look for
// a subtitle that wasn't needed than to skip one that was.
func activeWants(ws []Want, mi *mediaInfo) []Want {
	audio := ""
	for _, w := range ws {
		if w.OnlyIfAudioDiffers {
			audio = primaryAudio(mi)
			break
		}
	}
	if audio == "" {
		return ws
	}
	out := make([]Want, 0, len(ws))
	for _, w := range ws {
		if w.Variant == VariantFull && w.OnlyIfAudioDiffers && langMatches(audio, langs.Base(w.Lang)) {
			continue
		}
		out = append(out, w)
	}
	return out
}

// needsAudio reports whether any want depends on the file's audio.
func needsAudio(ws []Want) bool {
	for _, w := range ws {
		if w.OnlyIfAudioDiffers && w.Variant == VariantFull {
			return true
		}
	}
	return false
}

// peekAudio is a file's probe from the cache only, for the passes that never run ffprobe
// (the sweep, the TV roll-up); nil when it was never probed. Only read when a want
// depends on the audio.
func (s *Service) peekAudio(ctx context.Context, path string, ws []Want) *mediaInfo {
	if !needsAudio(ws) {
		return nil
	}
	if s.probe != nil {
		mi, _ := s.probe(ctx, path)
		return mi
	}
	return s.cachedProbe(ctx, path)
}

// --- coverage ---

// fileCoverage is one file's wants against its paired sidecars.
type fileCoverage struct {
	active   []Want          // the wants this file needs (after its audio)
	have     map[string]bool // Want.key() → met by a paired sidecar
	orphaned map[string]bool // lower-case language → a full want covered only by an orphan
	sc       sidecarScan
}

// coverageOf scans a file's sidecars against a title's wants. A full want is met by a
// paired full or SDH sidecar in its language (an untagged "<base>.srt" counts for the
// profile's first full language, whatever the audio); a forced want only by a paired
// forced one in its language.
func coverageOf(path, kind string, all []Want, mi *mediaInfo) fileCoverage {
	fulls := wantLangs(all, VariantFull)
	c := fileCoverage{active: activeWants(all, mi), have: map[string]bool{}}
	c.sc = scanSidecars(path, fulls, kind)
	for _, l := range c.sc.Present {
		c.have[Want{Lang: l}.key()] = true
	}
	for _, w := range all {
		if w.Variant != VariantForced {
			continue
		}
		for _, lv := range c.sc.Variants {
			if lv.Variant == VariantForced && lv.Lang != "" && langMatches(lv.Lang, w.Lang) {
				c.have[w.key()] = true
				break
			}
		}
	}
	c.orphaned = orphanCovered(fulls, c.sc.Present, c.sc.Orphans)
	return c
}

// missing is the active wants no paired sidecar meets.
func (c fileCoverage) missing() []Want {
	var out []Want
	for _, w := range c.active {
		if !c.have[w.key()] {
			out = append(out, w)
		}
	}
	return out
}

// sweepable is the missing wants the sweep would make: for a movie, a full language an
// orphaned subtitle covers is the owner's call, not the sweep's.
func (c fileCoverage) sweepable() []Want {
	var out []Want
	for _, w := range c.missing() {
		if w.Variant == VariantFull && c.orphaned[strings.ToLower(w.Lang)] {
			continue
		}
		out = append(out, w)
	}
	return out
}

// sweepWants is the wants the sweep would make for a file (fileCoverage.sweepable): its
// profile's wants minus what's there, minus what its audio makes unnecessary when the
// probe cache knows it.
func (s *Service) sweepWants(ctx context.Context, path, kind string, wants []Want) []Want {
	return coverageOf(path, kind, wants, s.peekAudio(ctx, path, wants)).sweepable()
}

// --- writing ---

// changed runs after a profile or override write: the cache is dropped, and when what
// coverage depends on changed, languages parked as impossible are retried and the counts
// are recounted.
func (s *Service) profilesChanged(ctx context.Context, before string) {
	s.dropProfiles()
	if s.wantsKey(ctx) == before {
		return
	}
	s.ClearBackoff(ctx, EventLangs)
	s.recountIfLanguagesChanged(ctx, before)
}

// CreateProfile adds a profile.
func (s *Service) CreateProfile(ctx context.Context, name string, items []ProfileItem) (Profile, error) {
	db := s.profileDB()
	if db == nil {
		return Profile{}, errProfilesUnwritten
	}
	name, err := cleanName(name)
	if err != nil {
		return Profile{}, err
	}
	if items, err = cleanItems(items); err != nil {
		return Profile{}, err
	}
	s.profileCfg(ctx) // the default exists before any other profile can take its name
	raw, _ := json.Marshal(items)
	id, err := store.InsertID(ctx, db, `INSERT INTO subtitle_profiles (name, items_json, is_default) VALUES (?, ?, 0)`, name, string(raw))
	if isUniqueErr(err) {
		return Profile{}, ErrProfileNameTaken
	}
	if err != nil {
		return Profile{}, err
	}
	s.dropProfiles() // nothing uses it yet: no recount
	p, _ := s.profileCfg(ctx).byID(id)
	return p, nil
}

// UpdateProfile renames a profile and/or replaces its items (nil = leave as is). The
// default profile's languages are the kept languages, so they're written to that setting
// too (a downgrade then still finds them).
func (s *Service) UpdateProfile(ctx context.Context, id int64, name *string, items []ProfileItem) (Profile, error) {
	db := s.profileDB()
	if db == nil {
		return Profile{}, errProfilesUnwritten
	}
	cur, ok := s.profileCfg(ctx).byID(id)
	if !ok {
		return Profile{}, ErrProfileNotFound
	}
	newName, newItems := cur.Name, cur.Items
	var err error
	if name != nil {
		if newName, err = cleanName(*name); err != nil {
			return Profile{}, err
		}
	}
	if items != nil {
		if newItems, err = cleanItems(items); err != nil {
			return Profile{}, err
		}
	}
	before := s.wantsKey(ctx)
	if err := s.writeProfile(ctx, db, cur, newName, newItems); err != nil {
		return Profile{}, err
	}
	s.profilesChanged(ctx, before)
	p, _ := s.profileCfg(ctx).byID(id)
	return p, nil
}

// writeProfile saves a profile's name and items (and, for the default, the kept-language
// setting).
func (s *Service) writeProfile(ctx context.Context, db store.Pool, cur Profile, name string, items []ProfileItem) error {
	raw, _ := json.Marshal(items)
	_, err := db.ExecContext(ctx, `UPDATE subtitle_profiles SET name = ?, items_json = ? WHERE id = ?`, name, string(raw), cur.ID)
	if isUniqueErr(err) {
		return ErrProfileNameTaken
	}
	if err != nil {
		return err
	}
	if cur.IsDefault && s.settings != nil {
		return s.settings.Set(ctx, keyLanguages, strings.Join(Profile{Items: items}.langs(), ","))
	}
	return nil
}

// DeleteProfile removes a profile; the titles overridden to it go back to the default.
func (s *Service) DeleteProfile(ctx context.Context, id int64) error {
	db := s.profileDB()
	if db == nil {
		return errProfilesUnwritten
	}
	cur, ok := s.profileCfg(ctx).byID(id)
	if !ok {
		return ErrProfileNotFound
	}
	if cur.IsDefault {
		return ErrDefaultProfile
	}
	before := s.wantsKey(ctx)
	err := store.WithTx(ctx, db, func(tx *store.Transaction) error {
		// The foreign key cascades too; deleting them here doesn't depend on it.
		if _, err := tx.ExecContext(ctx, `DELETE FROM subtitle_profile_overrides WHERE profile_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM subtitle_profiles WHERE id = ? AND is_default = 0`, id)
		return err
	})
	if err != nil {
		return err
	}
	s.profilesChanged(ctx, before)
	return nil
}

// setDefaultLanguages rewrites the default profile's languages (the kept-language list),
// keeping each remaining language's options; a new language is a plain item.
func (s *Service) setDefaultLanguages(ctx context.Context, list []string) error {
	db := s.profileDB()
	if db == nil {
		return nil // the setting is the whole configuration
	}
	def := s.profileCfg(ctx).def()
	if def.ID == 0 {
		return errors.New("no default subtitle profile")
	}
	items := make([]ProfileItem, 0, len(list))
	for _, l := range list {
		it := ProfileItem{Lang: l, Variant: WantFull, SDH: SDHAny}
		for _, old := range def.Items {
			if old.Lang == l {
				it = old
				break
			}
		}
		items = append(items, it)
	}
	if err := s.writeProfile(ctx, db, def, def.Name, items); err != nil {
		return err
	}
	s.dropProfiles()
	return nil
}

// SetOverride pins a movie or a series (every episode) to a profile.
func (s *Service) SetOverride(ctx context.Context, kind string, id, profileID int64) error {
	db := s.profileDB()
	if db == nil {
		return errProfilesUnwritten
	}
	if err := s.checkTitle(ctx, kind, id); err != nil {
		return err
	}
	cfg := s.profileCfg(ctx)
	p, ok := cfg.byID(profileID)
	if !ok {
		return ErrProfileNotFound
	}
	if p.IsDefault {
		return s.ClearOverride(ctx, kind, id) // pinning to the default is no override
	}
	before := cfg.key
	if _, err := db.ExecContext(ctx, `INSERT INTO subtitle_profile_overrides (media_kind, media_id, profile_id) VALUES (?, ?, ?)
		ON CONFLICT(media_kind, media_id) DO UPDATE SET profile_id = excluded.profile_id`, kind, id, profileID); err != nil {
		return err
	}
	s.overrideChanged(ctx, kind, id, before)
	return nil
}

// ClearOverride puts a title back on the default profile.
func (s *Service) ClearOverride(ctx context.Context, kind string, id int64) error {
	db := s.profileDB()
	if db == nil {
		return errProfilesUnwritten
	}
	if kind != OverrideMovie && kind != OverrideSeries {
		return ErrOverrideKind
	}
	before := s.wantsKey(ctx)
	if _, err := db.ExecContext(ctx, `DELETE FROM subtitle_profile_overrides WHERE media_kind = ? AND media_id = ?`, kind, id); err != nil {
		return err
	}
	s.overrideChanged(ctx, kind, id, before)
	return nil
}

func (s *Service) checkTitle(ctx context.Context, kind string, id int64) error {
	switch kind {
	case OverrideMovie:
		if s.movies == nil {
			return ErrOverrideNoTitle
		}
		if _, err := s.movies.Get(ctx, id); err != nil {
			return ErrOverrideNoTitle
		}
	case OverrideSeries:
		if s.series == nil {
			return ErrOverrideNoTitle
		}
		if _, err := s.series.Get(ctx, id); err != nil {
			return ErrOverrideNoTitle
		}
	default:
		return ErrOverrideKind
	}
	return nil
}

// overrideChanged recounts just the one title an override moved, rather than the whole
// library: its snapshot entry is recomputed and, if the snapshot was current before, it
// is current again. A pass already running gets a follow-up instead (Rescan).
func (s *Service) overrideChanged(ctx context.Context, kind string, id int64, before string) {
	s.dropProfiles()
	after := s.wantsKey(ctx)
	if after == before {
		return
	}
	s.ClearBackoff(ctx, EventLangs)
	s.snap.mu.Lock()
	scanning, seen, current := s.snap.scanning, !s.snap.at.IsZero(), s.snap.langsKey == before
	s.snap.mu.Unlock()
	if scanning || !seen || !current {
		s.recountIfLanguagesChanged(ctx, before)
		return
	}
	ctx = context.WithoutCancel(ctx)
	if kind == OverrideMovie {
		s.refreshSnapshot(ctx, &Job{Kind: "movie", MovieID: id})
	} else {
		s.refreshSnapshot(ctx, &Job{Kind: "episode", SeriesID: id})
	}
	s.snap.mu.Lock()
	if !s.snap.scanning && s.snap.langsKey == before {
		s.snap.langsKey = after
	}
	s.snap.mu.Unlock()
}

// langsOf is the wants' languages, each once, in order.
func langsOf(ws []Want) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range ws {
		if l := strings.ToLower(w.Lang); !seen[l] {
			seen[l] = true
			out = append(out, w.Lang)
		}
	}
	return out
}

// forcedOutcomeLangs is the languages a run recorded forced-want outcomes for, first seen
// first (a GPU-lane job settles the fast lane's forced backoff with them).
func forcedOutcomeLangs(outs []Outcome) []string {
	var out []string
	for _, o := range outs {
		if o.Variant == VariantForced {
			out = unionLangs(out, []string{o.Lang})
		}
	}
	return out
}

// wantLabel names a want in notes: "en", or "en forced".
func wantLabel(lang, variant string) string {
	if variant == VariantForced {
		return strings.ToLower(lang) + " forced"
	}
	return strings.ToLower(lang)
}

// --- the forced rung ---

// rungForced makes a profile's forced wants: the file's forced text track in that
// language, else an OpenSubtitles foreign-parts-only upload. Whisper can't tell the
// foreign lines from the rest, so there is no AI step: what neither source has is
// recorded as impossible. A forced track the extract rung already pulled out as an extra
// in this run counts as made; an existing forced file is never written over.
func (s *Service) rungForced(ctx context.Context, job *Job, ref fileRef, mi *mediaInfo, langs []string) []Outcome {
	if ctx.Err() != nil || len(langs) == 0 {
		return nil
	}
	var outs []Outcome
	var picks []extractPick
	var rest []string
	for _, l := range langs {
		p := sidecarPathV(ref.Path, strings.ToLower(l), VariantForced)
		if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
			outs = append(outs, mkOutcomeV(l, VariantForced, RungExtract, ResultOK, "extracted"))
			continue
		}
		if mi != nil {
			if t, ok := pickForcedTrack(mi.Subs, l); ok {
				picks = append(picks, extractPick{Index: t.Index, Lang: l, Variant: VariantForced, Path: p})
				continue
			}
		}
		rest = append(rest, l)
	}
	if len(picks) > 0 {
		s.update(job, func(j *Job) { j.Stage = "extracting forced subtitles" })
		err := s.runExtract(ctx, ref.Path, picks)
		if ctx.Err() != nil {
			return outs
		}
		if err != nil {
			s.event("warn", fmt.Sprintf("%s: forced extract failed: %v", ref.Title, err))
		}
		for _, p := range picks {
			fi, serr := os.Stat(p.Path)
			switch {
			case serr != nil:
				outs = append(outs, mkOutcomeV(p.Lang, VariantForced, RungExtract, ResultExtractFailed, "extract failed"))
				rest = append(rest, p.Lang)
			case fi.Size() == 0:
				_ = os.Remove(p.Path)
				outs = append(outs, mkOutcomeV(p.Lang, VariantForced, RungExtract, ResultEmpty, "embedded track was empty"))
				rest = append(rest, p.Lang)
			default:
				idx := p.Index
				s.recordFile(ctx, job, ref, SubtitleFile{Path: p.Path, Lang: strings.ToLower(p.Lang), Variant: VariantForced, Source: SourceEmbedded, EmbeddedIndex: &idx})
				outs = append(outs, mkOutcomeV(p.Lang, VariantForced, RungExtract, ResultOK, "extracted"))
			}
		}
	}
	rest, douts := s.rungDownloadV(ctx, job, ref, rest, VariantForced)
	outs = append(outs, douts...)
	if ctx.Err() != nil {
		return outs
	}
	detail := "AI can't make a forced subtitle"
	if s.provider == nil || !s.provider.CanDownload() {
		detail = "no forced track, OpenSubtitles downloads aren't set up, and " + detail
	}
	for _, l := range rest {
		outs = append(outs, mkOutcomeV(l, VariantForced, RungAI, ResultImpossible, detail))
	}
	return outs
}
