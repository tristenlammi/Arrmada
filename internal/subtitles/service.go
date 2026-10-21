package subtitles

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tristenlammi/arrmada/internal/langs"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Settings keys (stored in the shared settings service).
const (
	keyMoviesAuto = "subs_movies_auto"
	keySeriesAuto = "subs_series_auto"
	keyLanguages  = "subs_languages" // CSV of canonical internal/langs codes ("en", "pt-br")
)

// defaultLanguages is used until the admin configures otherwise.
var defaultLanguages = []string{"en"}

// Auto-ensure defaults ON. The point of the module is that a new download has its
// subtitles without anyone asking; a default that needed switching on meant the import
// hook and the sweep both existed and neither ever ran.
const (
	defaultMoviesAuto = true
	defaultSeriesAuto = true
)

// Service is the Subtitles module's logic: it derives per-title subtitle status from disk
// (sidecars) and grabs missing subtitles from the provider, saving them alongside media.
type Service struct {
	movies   *movies.Service
	series   *series.Service
	settings *settings.Service
	provider Provider
	ffmpeg   string
	ffprobe  string
	cache    *probeCache
	whisper  *whisperGen
	ledger   *Ledger // provenance, attempts and the blocklist (ledger.go); nil-safe
	log      *slog.Logger
	bin      library.Bin // where replaced/removed sidecars go (retireSidecar); nil = refuse
	// libraryChanged hears about the folder of each sidecar change (Plex's scanner; events.go).
	libraryChanged atomic.Pointer[func(kind, dir string)]

	// repair is the one-shot forced-track repair pass (repair.go).
	repair forcedRepair

	// Test seams for process(). Nil means the real code path: resolveFile, probeCached,
	// the ffmpeg extraction, and s.whisper.
	resolve func(ctx context.Context, job *Job) (fileRef, bool)
	probe   func(ctx context.Context, path string) (*mediaInfo, error)
	extract func(ctx context.Context, path string, picks []extractPick) error
	ai      aiRunner
	// rateWait replaces a 429's Retry-After wait when set (tests).
	rateWait time.Duration
	// ffprobeRun is probeCached's ffprobe call (probeSubs when nil).
	ffprobeRun func(ctx context.Context, ffprobe, path string) (*mediaInfo, error)
	// extractAudio is jobAudio's WAV extraction (extractWAV with s.ffmpeg when nil).
	extractAudio func(ctx context.Context, video string, stream int, dst string) error
	// passFn is one library pass (Library + SeriesGroups when nil).
	passFn func(ctx context.Context) ([]FileSubs, []SeriesGroup, error)

	// blocklist returns the provider file ids never to download again for a media key
	// (Job.key() format). The ledger (SUB-11) sets it; nil blocks nothing. An error stops
	// the download rung for that language (fail closed).
	blocklist func(ctx context.Context, mediaKey string) (map[string]bool, error)
	// syncGate checks a downloaded non-hash candidate against the file's audio before it
	// is saved, returning the bytes to save (shifted into sync if need be): gateSync
	// (syncgate.go). While it's nil (tests), candidates at or above the minimum score are
	// saved as they come, and the band between scoreFloor and the minimum is refused.
	syncGate func(ctx context.Context, ref fileRef, lang string, c ScoredCandidate, srt []byte) (out []byte, ok bool, detail string)

	mu      sync.Mutex
	jobs    []*Job                     // recent subtitle-ensure jobs (newest first), for the Queue tab
	pending [numLanes][numPrios][]*Job // waiting per lane, one oldest-first line per priority — unbounded, see Run
	active  map[string]*Job            // queued or running jobs by file and lane, for enqueue's dedupe
	wake    chan struct{}              // nudges the fast lane's workers when it gains a job
	wakeGPU chan struct{}              // the same for the GPU lane (made on first use)
	nextID  int64
	runs    map[int64]*runHandle // running jobs' stop levers, by job id (Stop button)
	busy    map[string]*Job      // files a job is working on (Job.key()); no second job may start on one
	lanes   bool                 // Run's GPU worker is up: a job's AI part moves to the GPU lane
	rtfMu   sync.Mutex           // serialises updates of the measured AI speed (noteRTF)
	logMu   sync.Mutex
	logBuf  []LogLine // recent activity console lines, for the Logs tab

	onJobDone func(Job) // SetJobDoneHook; under mu

	jobsDB    *jobStore   // the queue and its history, kept across restarts (jobstore.go); nil-safe
	bootSwept atomic.Bool // the catch-up sweep after start has been scheduled (bootSweep)

	snap librarySnapshot // the last library pass; what the Overview and Library tab read

	profiles profileStore // language profiles and per-title overrides, cached (profiles.go)
	searches searchCache  // the drawer's interactive searches, reused for a few minutes (search.go)

	upgrade upgradeBudget // downloads the upgrade loop may still spend today (upgrade.go)
}

// NewService wires the module over the shared Movies/Series catalogs + a subtitle provider. db is
// used for the probe cache; ffmpeg/ffprobe drive embedded-track probing and extraction;
// whisperModelsDir is where the local AI (whisper.cpp) model files live.
func NewService(db store.Pool, mv *movies.Service, sr *series.Service, set *settings.Service, provider Provider, ffmpeg, ffprobe, whisperModelsDir string, log *slog.Logger) *Service {
	s := &Service{
		movies: mv, series: sr, settings: set, provider: provider,
		ffmpeg: ffmpeg, ffprobe: ffprobe, cache: &probeCache{db: db},
		whisper: detectWhisper(whisperModelsDir), ledger: NewLedger(db), log: log,
		wake: make(chan struct{}, 1), jobsDB: newJobStore(db),
	}
	// Before anything can be queued: the ids carry on from the saved ones.
	if err := s.loadQueue(context.Background()); err != nil {
		log.Warn("subtitles: could not restore the queue; starting with an empty one", "err", err)
		s.jobsDB = nil // writing on top of an unread table would clash with its ids
	}
	s.blocklist = s.ledgerBlocklist
	s.syncGate = s.gateSync
	// A silent fall-back from the oneAPI build would hide a broken GPU setup behind a
	// slow run; surface it as a warning event the first time it happens.
	s.whisper.note = func(level, msg string) { s.event(level, msg) }
	s.whisper.log = log
	return s
}

// Ledger is the module's record of sidecar sources, attempts and blocked provider files.
func (s *Service) Ledger() *Ledger { return s.ledger }

// ledgerBlocklist is the blocklist seam over the ledger: the OpenSubtitles file ids never
// to download again for a media key. Its error fails the download rung closed.
func (s *Service) ledgerBlocklist(ctx context.Context, mediaKey string) (map[string]bool, error) {
	return s.ledger.Blocklisted(ctx, providerOpenSubtitles, mediaKey)
}

// Settings is the module's configuration + provider readiness for the dashboard.
type Settings struct {
	MoviesAuto bool     `json:"movies_auto"`
	SeriesAuto bool     `json:"series_auto"`
	Languages  []string `json:"languages"` // the default profile's languages
	// KeepLanguages is every language any profile wants: what Convert's "Same as
	// Subtitles" keeps.
	KeepLanguages []string `json:"keep_languages"`
	ProviderReady bool     `json:"provider_ready"` // can search
	CanDownload   bool     `json:"can_download"`   // can actually grab (needs account)
	AIReady       bool     `json:"ai_ready"`       // local whisper.cpp binary + a model present
	AIBackend     string   `json:"ai_backend"`     // "sycl" | "vulkan" | "cpu" | "" until the first run
	AINote        string   `json:"ai_note"`        // why the oneAPI build isn't in use, when it was bundled but set aside
	AICPUOnly     bool     `json:"ai_cpu_only"`    // the bundled whisper has no GPU backend (the ARM image)
	// Download quota, when the provider reports one. OpenSubtitles free accounts get a
	// small number of downloads a day; once it's spent every download fails until the
	// reset, and the UI should say that rather than showing a run of failures.
	QuotaRemaining int   `json:"quota_remaining"` // -1 = unknown
	QuotaResetAt   int64 `json:"quota_reset_at"`  // unix seconds; 0 = not paused
	Pending        int   `json:"pending"`         // jobs waiting for the worker
	// Stale: the Library and Overview figures were counted for a different set of kept
	// languages; a recount is running or about to.
	Stale bool `json:"stale"`
	// MinScore is the match score (0-100) a download without a hash match needs.
	MinScore int `json:"min_score"`
	// What OpenSubtitles actually said: not_configured | needs_account | unverified | ok |
	// auth_failed | quota_spent | rate_limited. Only "ok" means a login has worked.
	ProviderState    string `json:"provider_state"`
	ProviderDetail   string `json:"provider_detail"`
	VIP              bool   `json:"vip"`
	AllowedDownloads int    `json:"allowed_downloads"` // per day; -1 = unknown
	// UpgradedWeek is how many weak subtitles the upgrade loop replaced in the last 7 days.
	UpgradedWeek int `json:"upgraded_week"`
}

// GetSettings returns the current configuration + provider status.
func (s *Service) GetSettings(ctx context.Context) Settings {
	out := Settings{
		MoviesAuto:     s.settings.GetBool(ctx, keyMoviesAuto, defaultMoviesAuto),
		SeriesAuto:     s.settings.GetBool(ctx, keySeriesAuto, defaultSeriesAuto),
		Languages:      s.languages(ctx),
		KeepLanguages:  s.keepLanguages(ctx),
		ProviderReady:  s.provider.Available(),
		CanDownload:    s.provider.CanDownload(),
		AIReady:        s.whisper.available(),
		AIBackend:      s.whisper.Backend(),
		AINote:         s.whisper.SyclNote(),
		AICPUOnly:      s.whisper.CPUOnly(),
		QuotaRemaining: -1,
		Pending:        s.Pending(),
		MinScore:       s.minScore(ctx),
	}
	s.snap.mu.Lock()
	out.Stale = s.staleLocked(ctx)
	s.snap.mu.Unlock()
	st := s.providerStatus()
	out.ProviderState, out.ProviderDetail, out.VIP, out.AllowedDownloads = st.State, st.Detail, st.VIP, st.Allowed
	if n, err := s.ledger.CountUpgraded(ctx, time.Now().Add(-7*24*time.Hour)); err == nil {
		out.UpgradedWeek = n
	}
	if q, ok := s.provider.(quotaReporter); ok {
		remaining, resetAt := q.Quota()
		out.QuotaRemaining = remaining
		if !resetAt.IsZero() && resetAt.After(time.Now()) {
			out.QuotaResetAt = resetAt.Unix()
		}
	}
	return out
}

// providerStatus is the provider's real state; one that can't report it is judged by
// what's configured, and never "ok".
func (s *Service) providerStatus() ProviderStatus {
	if sr, ok := s.provider.(statusReporter); ok {
		return sr.Status()
	}
	switch {
	case s.provider == nil || !s.provider.Available():
		return ProviderStatus{State: ProviderNotConfigured, Allowed: -1}
	case !s.provider.CanDownload():
		return ProviderStatus{State: ProviderNeedsAccount, Allowed: -1}
	}
	return ProviderStatus{State: ProviderUnverified, Allowed: -1}
}

// quotaReporter is implemented by providers that meter downloads.
type quotaReporter interface {
	// Quota returns downloads remaining (-1 unknown) and when a spent quota resets (zero
	// time when not paused).
	Quota() (remaining int, resetAt time.Time)
}

// ErrUnknownLanguage is returned by SetSettings for a language code the shared table
// (internal/langs) doesn't know; nothing is saved.
var ErrUnknownLanguage = errors.New("unknown language")

// SetSettings updates whichever fields are provided (nil = leave unchanged). Languages are
// stored in their canonical spelling ("fre" → "fr", "pt-BR" → "pt-br"), in the order given
// — the first is the language an untagged sidecar counts as.
func (s *Service) SetSettings(ctx context.Context, moviesAuto, seriesAuto *bool, languages []string) error {
	var clean []string
	if languages != nil {
		var err error
		if clean, err = canonicalLanguages(languages); err != nil {
			return err // checked before anything is written, so a bad list saves nothing
		}
		if len(clean) == 0 {
			clean = defaultLanguages
		}
	}
	if moviesAuto != nil {
		if err := s.settings.SetBool(ctx, keyMoviesAuto, *moviesAuto); err != nil {
			return err
		}
	}
	if seriesAuto != nil {
		if err := s.settings.SetBool(ctx, keySeriesAuto, *seriesAuto); err != nil {
			return err
		}
	}
	if clean != nil {
		// The kept languages are the default profile's (profiles.go); the setting keeps
		// the same list, so a downgrade still finds it.
		before := s.wantsKey(ctx)
		if err := s.settings.Set(ctx, keyLanguages, strings.Join(clean, ",")); err != nil {
			return err
		}
		if err := s.setDefaultLanguages(ctx, clean); err != nil {
			return err
		}
		// A language the AI couldn't make from that audio may be makeable now (or no
		// longer wanted); let the next sweep look again.
		if s.wantsKey(ctx) != before {
			s.ClearBackoff(ctx, EventLangs)
			// And every count is stale: recount now rather than at the next six-hourly
			// pass. Saving the same list starts nothing.
			s.recountIfLanguagesChanged(ctx, before)
		}
	}
	return nil
}

// CredentialsChanged is called after an OpenSubtitles credential is saved or cleared:
// the provider forgets the last login (the next job or Test signs in fresh), and files
// parked on a rejected login are tried again by the next sweep.
func (s *Service) CredentialsChanged(ctx context.Context) {
	if r, ok := s.provider.(interface{ ResetAuth() }); ok {
		r.ResetAuth()
	}
	s.ClearBackoff(ctx, EventCreds)
}

// canonicalLanguages spells each code canonically, drops blanks and repeats (keeping the
// first), and refuses any code the language table doesn't know.
func canonicalLanguages(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, l := range in {
		if strings.TrimSpace(l) == "" {
			continue
		}
		lang, ok := langs.Lookup(l)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownLanguage, strings.TrimSpace(l))
		}
		if !seen[lang.Code] {
			seen[lang.Code] = true
			out = append(out, lang.Code)
		}
	}
	return out, nil
}

// Languages is what Convert's "Same as Subtitles" keeps: every language any profile
// wants, the default profile's first, forced and full alike (an embedded track of a kept
// language is never stripped, whichever variant a title wants).
func (s *Service) Languages(ctx context.Context) []string { return s.keepLanguages(ctx) }

// languages is the kept-language list: the default profile's languages, in the owner's
// order. The first is the language an untagged sidecar counts for.
func (s *Service) languages(ctx context.Context) []string {
	if l := s.profileCfg(ctx).def().langs(); len(l) > 0 {
		return l
	}
	return defaultLanguages
}

// settingsLanguages is the flat kept-language setting, canonical and in the owner's order
// — what the default profile is seeded from. A value saved before the shared table
// existed ("eng") reads back canonical ("en"); a code the table doesn't know is kept as
// written rather than silently dropped.
func (s *Service) settingsLanguages(ctx context.Context) []string {
	if s.settings == nil {
		return defaultLanguages
	}
	raw := s.settings.Get(ctx, keyLanguages, strings.Join(defaultLanguages, ","))
	var out []string
	seen := map[string]bool{}
	for _, l := range strings.Split(raw, ",") {
		if l = langs.Canonical(l); l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return defaultLanguages
	}
	return out
}

// AutoGrab queues subtitle-ensure jobs for whichever media types have auto-mode enabled — the
// scheduled sweep. The worker then makes each missing kept-language SRT by the best-source
// pipeline (extract → download → …), so auto-mode isn't download-only.
func (s *Service) AutoGrab(ctx context.Context) {
	if n, err := s.ledger.PruneAttempts(ctx, time.Now().Add(-AttemptRetention)); err != nil {
		s.log.Warn("subtitles: could not prune old attempts", "err", err)
	} else if n > 0 {
		s.log.Info("subtitles: pruned old attempts", "count", n)
	}
	s.PruneJobs(ctx)
	set := s.GetSettings(ctx)
	if set.MoviesAuto {
		if n, _ := s.SweepMissing(ctx, "movies"); n > 0 {
			s.log.Info("subtitles: auto-sweep queued movies", "count", n)
		}
	}
	if set.SeriesAuto {
		if n, _ := s.SweepMissing(ctx, "tv"); n > 0 {
			s.log.Info("subtitles: auto-sweep queued series", "count", n)
		}
	}
}

// providerOpenSubtitles is the provider name the ledger records downloads and blocks under.
const providerOpenSubtitles = "opensubtitles"

// grabResult is what one language's download attempt came to.
type grabResult struct {
	Written bool
	Path    string          // the sidecar written
	Pick    ScoredCandidate // the candidate saved, or the best one refused
	Outcome string          // "done" | "no_match" | "below_score" | "out_of_sync"
	Detail  string          // human wording for the job note
}

// grabOne searches for the best subtitle for one media file + language and writes it as
// a sidecar, if one is good enough. Candidates are scored against the file (scoring.go):
// a hash match always wins; otherwise the best must reach the minimum match score. A
// candidate between scoreFloor and the minimum is used only if the sync check (syncGate,
// SUB-19) passes it — refused while there is none. Blocklisted and rejected candidates
// are never downloaded.
func (s *Service) grabOne(ctx context.Context, ref fileRef, hash, lang string) (grabResult, error) {
	return s.grabOneV(ctx, ref, hash, lang, VariantFull)
}

// grabOneV is grabOne for one variant: VariantForced searches only the foreign-parts-only
// uploads and writes the ".forced" sidecar; a full want follows its profile's SDH
// preference.
func (s *Service) grabOneV(ctx context.Context, ref fileRef, hash, lang, variant string) (grabResult, error) {
	// A paused quota means no requests at all: searching would spend the API's rate
	// limit on results that can't be downloaded until the reset.
	if s.providerPaused() {
		if res, msg := s.pauseOutcome(); res == ResultAuthFailed {
			return grabResult{}, fmt.Errorf("%w: %s", ErrAuthFailed, msg)
		}
		return grabResult{}, ErrQuotaExhausted
	}
	req := SearchRequest{
		IMDBID: ref.IMDB, Title: ref.Title, Year: ref.Year, Season: ref.Season, Episode: ref.Episode,
		Language: lang, MovieHash: hash,
	}
	m := matchContextFor(ref)
	if variant == VariantForced {
		req.ForeignParts, m.Forced = "only", true
	} else {
		m.SDH = sdhFor(ref.wants, lang)
	}
	results, err := s.provider.Search(ctx, req)
	if err != nil {
		return grabResult{}, err
	}
	noMatch := grabResult{Outcome: "no_match", Detail: "no OpenSubtitles match"}
	if len(results) == 0 {
		return noMatch, nil
	}
	blocked, err := s.blockedFiles(ctx, ref.MediaKey)
	if err != nil {
		return grabResult{}, fmt.Errorf("read subtitle blocklist: %w", err)
	}
	ranked := rankCandidates(results, m, blocked)
	if !ranked[0].usable() {
		noMatch.Detail = "no usable OpenSubtitles match"
		return noMatch, nil
	}
	best := ranked[0]
	need := s.minScore(ctx)
	s.log.Info("subtitle candidates scored", "media", ref.MediaKey, "lang", lang, "candidates", len(results),
		"best", best.Score, "hash_match", best.HashMatch, "min", need)

	tries := 0
	var refusal string
	for _, c := range ranked {
		if !c.usable() || c.Score < scoreFloor || tries == maxDownloadTries {
			break
		}
		if !c.HashMatch && c.Score < need && s.syncGate == nil {
			break // this band is accepted only when a sync check passes
		}
		if !c.HashMatch && c.Score < need {
			// Don't spend a download on a weak match whose sync can't be checked.
			if err := s.syncReady(ctx, ref, lang); err != nil {
				refusal = "a weak match whose sync couldn't be checked: " + shortErr(err, 100)
				break
			}
		}
		data, err := s.provider.Download(ctx, c.FileID)
		if err != nil {
			return grabResult{}, err
		}
		tries++
		if !c.HashMatch && s.syncGate != nil {
			out, ok, detail := s.syncGate(ctx, ref, lang, c, data)
			if !ok {
				refusal = detail
				continue
			}
			data = out
		}
		// A hearing-impaired upload is still the full subtitle; the ".sdh" qualifier lets
		// Plex label it as such. A forced want's file is the ".forced" one.
		written := variant
		if variant == VariantFull && c.HearingImpaired {
			written = VariantSDH
		}
		dst := sidecarPathV(ref.Path, lang, written)
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return grabResult{}, err
		}
		s.log.Info("subtitle saved", "path", dst, "lang", lang, "release", c.Release, "score", c.Score,
			"hash_match", c.HashMatch, "hearing_impaired", c.HearingImpaired)
		s.event("info", fmt.Sprintf("%s: %s subtitle scored %d (%s)", ref.Title, lang, c.Score, strings.Join(c.Reasons, ", ")))
		return grabResult{Written: true, Path: dst, Pick: c, Outcome: "done", Detail: "downloaded"}, nil
	}
	if refusal != "" {
		return grabResult{Pick: best, Outcome: "out_of_sync", Detail: refusal}, nil
	}
	detail := fmt.Sprintf("best OpenSubtitles match scored %d, below %d: %s", best.Score, need, best.mismatches())
	s.event("info", fmt.Sprintf("%s: %s — %s", ref.Title, lang, detail))
	return grabResult{Pick: best, Outcome: "below_score", Detail: detail}, nil
}

// nonNil returns an empty (non-nil) slice for nil, so JSON emits [] not null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
