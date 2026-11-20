// Package updatecheck asks GitHub whether a newer Arrmada release is out, for System →
// Status. It reads the repository's public releases (and, for a build of a commit, how
// it compares with main or with the latest release, whichever its install follows) with
// plain unauthenticated GETs: nothing about the server,
// its library or its users is sent. The User-Agent is the one every Arrmada request
// carries (httpua): the program, its version and its homepage. It is on by default, says what it does, and can be switched off; while off
// it makes no requests at all.
//
// Trouble with GitHub (no releases yet, the rate limit, no network) is never an error
// anyone is alerted about: the answer keeps what the last good check found and says, in
// plain words, why it couldn't check this time.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tristenlammi/arrmada/internal/httpua"
)

const (
	// DefaultRepo is where releases are published.
	DefaultRepo = "tristenlammi/Arrmada"
	// DefaultAPI is GitHub's REST API.
	DefaultAPI = "https://api.github.com"
	// KeyEnabled is the setting that switches the check off ("false"); on by default.
	KeyEnabled = "update_check_enabled"
	// Interval is how often the scheduled task asks. GitHub allows 60 unauthenticated
	// requests an hour per address; this uses at most two every 12 hours.
	Interval = 12 * time.Hour
	// MaxNotes caps the release notes kept and shown (bytes).
	MaxNotes = 4096
	// minManual is how soon after one check another "Check now" asks GitHub again;
	// sooner returns what the last one found.
	minManual = time.Minute
	// maxBody caps what is read of any one answer.
	maxBody = 1 << 20
)

// Channel says what the running build is compared with.
const (
	// ChannelRelease: a build of a release tag (v1.2.0), compared with the latest release.
	ChannelRelease = "release"
	// ChannelMain: a build of a commit on an install that follows main
	// (ARRMADA_CHANNEL=main), compared with main.
	ChannelMain = "main"
	// ChannelBuild: a build of a commit on an install that follows the releases
	// (ARRMADA_CHANNEL=stable): update.sh keeps it until a release includes its code, so
	// it's compared with the latest release.
	ChannelBuild = "build"
	// ChannelDev: a build that doesn't know its commit (go run, an old image).
	ChannelDev = "dev"
)

// The install's channel, as ARRMADA_CHANNEL names it.
const (
	InstallStable = "stable"
	InstallMain   = "main"
)

// HostUnraid is Status.Host for a container Unraid's own Docker manager runs (installed
// from Community Apps or the template). Unraid updates it from its Docker page, so the
// install line, which sets up a Compose install beside it, is the wrong advice there.
const HostUnraid = "unraid"

// HostFromEnv says how this container is managed, from its environment: Unraid's Docker
// manager gives every container it creates HOST_OS=Unraid. A Compose install on Unraid
// doesn't get it, and is rightly updated the Compose way. "" = anything else.
func HostFromEnv(getenv func(string) string) string {
	if strings.EqualFold(strings.TrimSpace(getenv("HOST_OS")), "unraid") {
		return HostUnraid
	}
	return ""
}

// Release is the newest published (non-draft, non-pre-release) release.
type Release struct {
	Tag  string `json:"tag"`
	Name string `json:"name"`
	// URL is the release's page on github.com, or "" if GitHub answered anything else.
	URL string `json:"url"`
	// Notes are the release notes as GitHub has them (Markdown), as text: HTML comments
	// removed, cut to MaxNotes. Show them as text, never as HTML.
	Notes          string    `json:"notes"`
	NotesTruncated bool      `json:"notes_truncated"`
	PublishedAt    time.Time `json:"published_at"`
}

// Status is the answer for System → Status.
type Status struct {
	Enabled bool `json:"enabled"`
	// Current is the running build's version; Channel how it's compared (above).
	Current string `json:"current"`
	Channel string `json:"channel"`
	// CheckedAt is when GitHub last answered; nil = not yet.
	CheckedAt *time.Time `json:"checked_at"`
	// Latest is the newest release; nil when none is published (or it isn't known yet).
	Latest *Release `json:"latest"`
	// Available: Latest is newer than this release build, or (channel build) Latest
	// includes this build's code and more, so update.sh moves the install to it.
	Available bool `json:"available"`
	// Behind is how many commits main has that this build doesn't; nil = unknown or
	// not a build of main.
	Behind *int `json:"behind"`
	// Ahead is how many commits this build has that Latest doesn't (channel build only):
	// more than 0 and update.sh keeps the build until a release includes it. nil =
	// unknown or not compared.
	Ahead *int `json:"ahead"`
	// Problem says why the last attempt couldn't check, in plain words; "" = it could.
	Problem string `json:"problem,omitempty"`
	// Host is HostUnraid when Unraid's Docker manager runs this container, which updates
	// it from its Docker page; "" = updated with the install line or Compose.
	Host string `json:"host,omitempty"`
}

// Options configures a Checker. Only Version, Commit and Enabled are needed in
// production; tests point BaseURL at an httptest server.
type Options struct {
	Client  *http.Client
	BaseURL string
	Repo    string
	Version string
	Commit  string
	// Install is the install's channel (ARRMADA_CHANNEL): InstallStable, InstallMain or
	// "" when it isn't known, which compares a build of a commit with main as before.
	Install string
	// Host is how the container is managed (HostFromEnv). It only changes the advice on
	// how to update, never what's compared.
	Host string
	// Enabled reads the setting on each use. nil = always on.
	Enabled func(ctx context.Context) bool
	Log     *slog.Logger
	Now     func() time.Time
}

// Checker runs the check and keeps its result in memory.
type Checker struct {
	o     Options
	runMu sync.Mutex // one check at a time (the schedule and Check now)

	mu      sync.Mutex
	last    Status
	etag    string    // the latest release's ETag, for a cheap "not modified"
	release *Release  // what that ETag describes
	tried   time.Time // when the last check started
}

// New returns a Checker; nothing is fetched until Run.
func New(o Options) *Checker {
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 20 * time.Second}
	}
	if o.BaseURL == "" {
		o.BaseURL = DefaultAPI
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	if o.Repo == "" {
		o.Repo = DefaultRepo
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	c := &Checker{o: o}
	c.last = Status{Current: o.Version, Channel: channelOf(o.Version, o.Commit, o.Install)}
	return c
}

// Enabled reports whether the check is switched on.
func (c *Checker) Enabled(ctx context.Context) bool {
	return c == nil || c.o.Enabled == nil || c.o.Enabled(ctx)
}

// Status is the last result, or just the build when the check is off.
func (c *Checker) Status(ctx context.Context) Status {
	if c == nil {
		return Status{}
	}
	if !c.Enabled(ctx) {
		return Status{Enabled: false, Current: c.o.Version, Channel: channelOf(c.o.Version, c.o.Commit, c.o.Install), Host: c.o.Host}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.last
	st.Enabled = true
	st.Host = c.o.Host
	return st
}

// Run is the scheduled task: one check, unless the check is off. GitHub's answers never
// make it fail (they're reported in Status.Problem), so a bad day at GitHub doesn't
// count as a failing task.
func (c *Checker) Run(ctx context.Context) error {
	if c == nil || !c.Enabled(ctx) {
		return nil
	}
	c.runMu.Lock()
	defer c.runMu.Unlock()
	c.check(ctx)
	return nil
}

// CheckNow is "Check now": a check, unless one ran in the last minute, then the status.
func (c *Checker) CheckNow(ctx context.Context) Status {
	if c == nil {
		return Status{}
	}
	if c.Enabled(ctx) {
		c.runMu.Lock()
		c.mu.Lock()
		recent := !c.tried.IsZero() && c.o.Now().Sub(c.tried) < minManual
		c.mu.Unlock()
		if !recent {
			c.check(ctx)
		}
		c.runMu.Unlock()
	}
	return c.Status(ctx)
}

// check asks GitHub and records the result. Callers hold runMu.
func (c *Checker) check(ctx context.Context) {
	now := c.o.Now()
	c.mu.Lock()
	c.tried = now
	lastTag := ""
	if c.last.Latest != nil {
		lastTag = c.last.Latest.Tag
	}
	c.mu.Unlock()

	channel := channelOf(c.o.Version, c.o.Commit, c.o.Install)
	rel, relErr := c.latestRelease(ctx)

	var behind *int
	var vsRelease *comparison
	var cmpErr error
	switch channel {
	case ChannelMain:
		behind, cmpErr = c.behindMain(ctx)
	case ChannelBuild:
		// Compared with the release this check found, or, when GitHub didn't answer for
		// it, the one the last good check found (which is the one still shown).
		tag := lastTag
		if relErr == nil {
			tag = ""
			if rel != nil {
				tag = rel.Tag
			}
		}
		if tag != "" {
			vsRelease, cmpErr = c.compare(ctx, c.o.Commit, tag)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.last
	st.Current, st.Channel = c.o.Version, channel
	problem := ""
	if relErr != nil {
		// Keep what the last good check found.
		problem = describe(relErr)
		c.o.Log.Debug("update check: latest release", "err", relErr)
	} else {
		st.Latest = rel
		st.Available = channel == ChannelRelease && rel != nil && newer(rel.Tag, c.o.Version)
		t := now
		st.CheckedAt = &t
	}
	switch channel {
	case ChannelMain:
		if cmpErr != nil {
			if problem == "" {
				problem = describe(cmpErr)
			}
			c.o.Log.Debug("update check: compare with main", "err", cmpErr)
		} else {
			st.Behind = behind
		}
	case ChannelBuild:
		if cmpErr != nil {
			if problem == "" {
				problem = describe(cmpErr)
			}
			c.o.Log.Debug("update check: compare with the latest release", "err", cmpErr)
		} else {
			// base = this build, head = the release: Behind is what the build has that the
			// release doesn't. None, and the release has more: it's an update.
			st.Ahead, st.Available = nil, false
			if vsRelease != nil {
				ahead := vsRelease.Behind
				st.Ahead = &ahead
				// A build with local edits (-dirty) has code GitHub has never seen, and
				// update.sh keeps it, so no release is offered as its update.
				st.Available = vsRelease.Behind == 0 && vsRelease.Ahead > 0 && !strings.HasSuffix(c.o.Version, "-dirty")
			}
		}
	}
	st.Problem = problem
	c.last = st
}

// errRateLimited: GitHub asked us to wait.
var errRateLimited = errors.New("rate limited")

// httpError is an answer that isn't one we use.
type httpError struct{ code int }

func (e httpError) Error() string { return "HTTP " + strconv.Itoa(e.code) }

// describe puts an error in words for the Status card.
func describe(err error) string {
	var he httpError
	switch {
	case errors.Is(err, errRateLimited):
		return "GitHub asked Arrmada to wait before checking again; it will try later."
	case errors.As(err, &he):
		return fmt.Sprintf("GitHub answered with an error (HTTP %d); it will try again later.", he.code)
	default:
		return "Couldn't reach GitHub to check for updates; it will try again later."
	}
}

// get makes one plain GET. etag, when set, makes it conditional.
func (c *Checker) get(ctx context.Context, path, etag string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.o.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", httpua.UserAgent())
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.o.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != "")) {
		drain(resp)
		return nil, errRateLimited
	}
	return resp, nil
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	_ = resp.Body.Close()
}

// latestRelease is the newest release, nil when the repository has none.
func (c *Checker) latestRelease(ctx context.Context) (*Release, error) {
	c.mu.Lock()
	etag, cached := c.etag, c.release
	c.mu.Unlock()

	resp, err := c.get(ctx, "/repos/"+c.o.Repo+"/releases/latest", etag)
	if err != nil {
		return nil, err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusNotModified:
		return cached, nil
	case http.StatusNotFound:
		// No release published yet (or the repository isn't public): nothing to offer.
		c.mu.Lock()
		c.etag, c.release = "", nil
		c.mu.Unlock()
		return nil, nil
	case http.StatusOK:
	default:
	}
	var body struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		HTMLURL     string    `json:"html_url"`
		Body        string    `json:"body"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	var rel *Release
	// /releases/latest never answers with these, but nothing is offered that isn't final.
	if body.TagName != "" && !body.Draft && !body.Prerelease {
		notes, cut := cleanNotes(body.Body)
		rel = &Release{
			Tag:            clip(body.TagName, 100),
			Name:           clip(body.Name, 200),
			URL:            githubURL(body.HTMLURL),
			Notes:          notes,
			NotesTruncated: cut,
			PublishedAt:    body.PublishedAt,
		}
	}
	c.mu.Lock()
	c.etag, c.release = resp.Header.Get("ETag"), rel
	c.mu.Unlock()
	return rel, nil
}

// shaRE is a commit as update.sh and the release build stamp it.
var shaRE = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// behindMain is how many commits main has that this build's commit doesn't. A commit
// GitHub doesn't know (built from local, unpushed work) is unknown, not an error.
func (c *Checker) behindMain(ctx context.Context) (*int, error) {
	cmp, err := c.compare(ctx, c.o.Commit, "main")
	if err != nil || cmp == nil {
		return nil, err
	}
	n := cmp.Ahead
	return &n, nil
}

// comparison is GitHub's compare of base...head: Ahead is how many commits head has that
// base doesn't, Behind how many base has that head doesn't.
type comparison struct{ Ahead, Behind int }

// compare asks GitHub how base and head (commits, tags or branches) differ. Either one
// unknown to GitHub (local, unpushed work) is nil, not an error.
func (c *Checker) compare(ctx context.Context, base, head string) (*comparison, error) {
	resp, err := c.get(ctx, "/repos/"+c.o.Repo+"/compare/"+url.PathEscape(base)+"..."+url.PathEscape(head)+"?per_page=1", "")
	if err != nil {
		return nil, err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, nil
	default:
		return nil, httpError{resp.StatusCode}
	}
	return readCompare(io.LimitReader(resp.Body, 8*maxBody))
}

// readCompare reads ahead_by and behind_by from a compare answer. The answer can be
// large (it lists commits and files), so it's read field by field and left as soon as
// both are found (GitHub puts them near the top).
func readCompare(r io.Reader) (*comparison, error) {
	dec := json.NewDecoder(r)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("compare: not an object")
	}
	var cmp comparison
	var gotAhead, gotBehind bool
	for dec.More() && !(gotAhead && gotBehind) {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("compare: %w", err)
		}
		key, _ := tok.(string)
		switch key {
		case "ahead_by":
			if err := dec.Decode(&cmp.Ahead); err != nil {
				return nil, fmt.Errorf("compare: ahead_by: %w", err)
			}
			gotAhead = true
		case "behind_by":
			if err := dec.Decode(&cmp.Behind); err != nil {
				return nil, fmt.Errorf("compare: behind_by: %w", err)
			}
			gotBehind = true
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, fmt.Errorf("compare: %w", err)
			}
		}
	}
	if !gotAhead || !gotBehind {
		return nil, fmt.Errorf("compare: no ahead_by/behind_by")
	}
	return &cmp, nil
}

// channelOf says what a build is compared with. Only a "v"-prefixed version is a release:
// update.sh stamps other builds with their commit date (2026.10.13), which would
// otherwise read as version 2026. On an install that follows main, a build of a commit
// is compared with main even when that commit is a release's (update.sh then stamps it
// with the tag). On one that follows the releases, a build of a commit is one update.sh
// keeps until a release includes it, so it's compared with the latest release. An
// install that doesn't say compares a build of a commit with main, as it always did.
func channelOf(version, commit, install string) string {
	release := false
	if _, ok := parseSemver(version); ok && strings.HasPrefix(version, "v") {
		release = true
	}
	known := shaRE.MatchString(commit)
	switch {
	case install == InstallMain && known:
		return ChannelMain
	case release:
		return ChannelRelease
	case install == InstallStable && known:
		return ChannelBuild
	case known:
		return ChannelMain
	}
	return ChannelDev
}

var commentRE = regexp.MustCompile(`(?s)<!--.*?-->`)

// cleanNotes drops HTML comments (GitHub's generated notes start with one), normalises
// line ends and cuts the notes to MaxNotes on a line (or at least a character) boundary.
func cleanNotes(s string) (string, bool) {
	s = commentRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSpace(s)
	if len(s) <= MaxNotes {
		return s, false
	}
	cut := s[:MaxNotes]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	if i := strings.LastIndexByte(cut, '\n'); i > MaxNotes/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " \n"), true
}

// clip shortens s to n bytes on a character boundary.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// githubURL keeps a link only if it's an https page on github.com, so the Status card
// never renders a link GitHub didn't mean.
func githubURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" {
		return ""
	}
	return u.String()
}
