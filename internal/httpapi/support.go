package httpapi

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/apikeys"
	"github.com/tristenlammi/arrmada/internal/applog"
	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/buildinfo"
	"github.com/tristenlammi/arrmada/internal/notify"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The support bundle is what an owner attaches to a bug report, so it's written as if it
// will be public. It carries the log history, the System status and health answers, the
// version, and the settings that are plainly safe — never the database. Everything in it
// goes through scrubbing that errs towards hiding:
//
//   - secrets: every credential the install holds (API keys, the Plex token, SMTP and
//     download-client passwords, indexer keys, Apprise links) is looked up and blanked
//     wherever it appears, on top of the key-name and URL redaction the log already had;
//   - people: usernames, Plex names and email addresses (Kindle addresses among them)
//     are replaced, and user-ish attrs lose their values, so no line names who did what;
//   - books: every book title, author, series and bookmark name in the library is
//     replaced, and audiobook paths and ids become {id} — admins see how much and when
//     people listen, never what, and a public file must not either.
//
// The names to hide are read from the database when the bundle is made; if that read
// fails, no bundle is made.

// handleSupportBundle — GET /api/v1/system/support-bundle (admin): a zip for sharing.
func (a *api) handleSupportBundle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := a.bundleScrubber(ctx)
	if err != nil {
		a.deps.Log.Warn("support bundle: couldn't read what to hide, so none was made", "err", err)
		a.writeError(w, http.StatusServiceUnavailable, "Couldn't prepare a safe support bundle; see the log for why.")
		return
	}

	// Everything but the logs is gathered before the headers go out, so a failure there
	// can still answer with an error rather than a broken zip.
	plain := r.Clone(ctx)
	plain.URL.RawQuery = "" // no ?refresh: report the cached health, as the page shows it
	docs := []struct {
		name string
		body []byte
	}{
		{"version.json", mustJSON(map[string]any{
			"version": buildinfo.Version, "commit": buildinfo.Commit, "go": runtime.Version(),
			"os": runtime.GOOS, "arch": runtime.GOARCH, "generated_at": time.Now().UTC().Format(time.RFC3339),
		})},
		{"status.json", captureJSON(a.handleSystemStatus, plain)},
		{"health.json", captureJSON(a.handleSystemHealth, plain)},
	}
	if a.deps.Settings != nil {
		docs = append(docs, struct {
			name string
			body []byte
		}{"settings.json", mustJSON(bundleSettings(a.deps.Settings.All()).doc)})
	}

	stamp := time.Now().UTC().Format("20060102-150405")
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="arrmada-support-`+stamp+`.zip"`)
	h.Set("Cache-Control", "no-store, private")
	w.WriteHeader(http.StatusOK)

	zw := zip.NewWriter(w)
	err = writeZipFile(zw, "README.txt", []byte(bundleReadme))
	for _, d := range docs {
		if err != nil {
			break
		}
		body := string(d.body)
		if d.name == "settings.json" {
			// Already an allowlist; the shape rules would only mangle the withheld names
			// ("apikey:tmdb" reads like a key and its value).
			body = sc.literal(body)
		} else {
			body = sc.text(body)
		}
		err = writeZipFile(zw, d.name, []byte(body))
	}
	lines := 0
	if err == nil {
		lines, err = a.bundleLogs(zw, sc)
	}
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		a.deps.Log.Warn("support bundle didn't finish", "err", err)
		return
	}
	a.deps.Log.Info("support bundle downloaded", "log_lines", lines)
}

const bundleReadme = `Arrmada support bundle

What's here: the application log (logs/), the version, System status and health as the
app reported them, and the settings that are plainly safe to share (settings.json lists
the names of the ones left out).

What isn't: the database, API keys, passwords, tokens and other credentials, email
addresses, the names of the people who use this server, and the titles, authors and
series of the books in its library. Those are replaced with «redacted», «user», «email»
or «name», and audiobook ids in paths with {id}.
`

// bundleLogs writes the log history — the files on disk oldest first, or the memory window
// when the log isn't persisted — line by line through the scrubber.
func (a *api) bundleLogs(zw *zip.Writer, sc *bundleScrubber) (int, error) {
	if a.deps.Logs == nil {
		return 0, nil
	}
	a.deps.Logs.Flush(2 * time.Second)
	files := a.deps.Logs.Files()
	if len(files) == 0 {
		entries := a.deps.Logs.Snapshot(applog.Filter{Min: slog.LevelDebug})
		return len(entries), writeZipEntries(zw, "logs/arrmada.log.jsonl", entries, sc.entry)
	}
	total := 0
	for _, name := range files {
		n, err := scrubLogFile(zw, name, "logs/"+filepath.Base(name), sc)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// scrubLogFile copies one JSON Lines log file into the zip, each entry scrubbed; torn and
// malformed lines are dropped, since they can't be scrubbed reliably.
func scrubLogFile(zw *zip.Writer, src, name string, sc *bundleScrubber) (int, error) {
	f, err := os.Open(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	zf, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: st.ModTime()})
	if err != nil {
		return 0, err
	}
	out := bufio.NewWriterSize(zf, 64<<10)
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	rd := bufio.NewReaderSize(io.LimitReader(f, st.Size()), 64<<10)
	n := 0
	for {
		line, readErr := rd.ReadBytes('\n')
		if raw := bytes.TrimRight(line, "\r\n"); len(raw) > 0 {
			var e applog.Entry
			if json.Unmarshal(raw, &e) == nil && e.TimeMS != 0 {
				if e, keep := sc.entry(e); keep {
					if err := enc.Encode(e); err != nil {
						return n, err
					}
					n++
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return n, readErr
		}
	}
	return n, out.Flush()
}

// bundleScrubber is everything a bundle hides: the install's own secret values, and the
// names of its people and books.
type bundleScrubber struct {
	secrets *strings.Replacer
	names   *applog.TermScrubber
}

// minSecretLen is the shortest credential value blanked by value. Shorter ones would
// replace ordinary words; they're still caught by key-name redaction.
const minSecretLen = 6

// bundleScrubber reads what to hide from the database and the credential store.
func (a *api) bundleScrubber(ctx context.Context) (*bundleScrubber, error) {
	if a.deps.Store == nil {
		return nil, errors.New("no database")
	}
	db := a.deps.Store.DB()
	var secrets []string
	addSecret := func(v string) {
		v = strings.TrimSpace(v)
		if len(v) < minSecretLen {
			return
		}
		secrets = append(secrets, v)
		// A link (Apprise, a webhook) carries its token as one of its parts; a log line
		// may quote the part without the rest.
		if strings.Contains(v, "://") {
			for _, part := range strings.FieldsFunc(v, func(r rune) bool { return strings.ContainsRune("/:@?&=#", r) }) {
				if len(part) >= 12 {
					secrets = append(secrets, part)
				}
			}
		}
	}

	if a.deps.Settings != nil {
		for _, v := range bundleSettings(a.deps.Settings.All()).secretValues {
			addSecret(v)
		}
	}
	if a.deps.APIKeys != nil {
		for _, k := range apikeys.Catalog {
			addSecret(a.deps.APIKeys.Value(ctx, k.ID))
		}
	}
	cfg := a.deps.Config
	for _, v := range []string{cfg.PlexToken, cfg.TMDBAPIKey, cfg.OMDBAPIKey, cfg.OpenSubtitlesAPIKey, cfg.OpenSubtitlesUsername, cfg.OpenSubtitlesPassword} {
		addSecret(v)
	}
	// The bundled qBittorrent's login Arrmada made, even when the client's card now has
	// another (download_clients' passwords are withheld below).
	if l, ok := a.bundledQbitLogin(); ok {
		addSecret(l.Password)
	}

	names := applog.NewTermScrubber()
	queries := []struct {
		sql string
		add func(vals []string)
	}{
		{`SELECT api_key, password FROM indexers`, func(v []string) { addSecret(v[0]); addSecret(v[1]) }},
		{`SELECT password FROM download_clients`, func(v []string) { addSecret(v[0]) }},
		{`SELECT url FROM notifications`, func(v []string) { addSecret(v[0]) }},
		// A preset's secret fields too: an n8n webhook's auth and signing secret are in
		// no URL at all.
		{`SELECT kind, config FROM notifications`, func(v []string) {
			for _, s := range notify.SecretValues(v[0], v[1]) {
				addSecret(s)
			}
		}},
		// Calendar feed links: only their hashes are stored (the token itself never is, and
		// a "token=" in any line is blanked by shape), but the hash is withheld too.
		{`SELECT token_hash FROM calendar_feeds`, func(v []string) { addSecret(v[0]) }},
		// Personal API keys: the same. The keys themselves are blanked by shape (arrk_…).
		{`SELECT key_hash FROM api_keys`, func(v []string) { addSecret(v[0]) }},
		// Two-factor secrets: never logged, but a line quoting one by mistake mustn't
		// carry it out.
		{`SELECT secret FROM user_totp`, func(v []string) { addSecret(v[0]) }},
		{`SELECT username, plex_username, apprise_url, kindle_email, jellyfin_username FROM users`, func(v []string) {
			names.Add(v[0], "«user»")
			names.Add(v[1], "«user»")
			addSecret(v[2])
			addSecret(v[3])
			names.Add(v[4], "«user»")
		}},
		{`SELECT title, author, series_name FROM books`, func(v []string) {
			names.Add(v[0], "«name»")
			// "Title: Subtitle" is often logged as just "Title".
			if i := strings.IndexAny(v[0], ":–—"); i > 0 {
				names.Add(v[0][:i], "«name»")
			}
			names.Add(v[1], "«name»")
			names.Add(v[2], "«name»")
		}},
		{`SELECT name FROM authors`, func(v []string) { names.Add(v[0], "«name»") }},
		{`SELECT title FROM listen_bookmarks`, func(v []string) { names.Add(v[0], "«name»") }},
		{`SELECT title FROM audio_file_meta`, func(v []string) { names.Add(v[0], "«name»") }},
	}
	for _, q := range queries {
		if err := scanStrings(ctx, db, q.sql, q.add); err != nil {
			return nil, err
		}
	}
	names.Seal()

	// Longest first, so a secret that contains another is blanked whole.
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	pairs := make([]string, 0, 2*len(secrets))
	seen := map[string]bool{}
	for _, s := range secrets {
		if !seen[s] {
			seen[s] = true
			pairs = append(pairs, s, "«redacted»")
		}
	}
	return &bundleScrubber{secrets: strings.NewReplacer(pairs...), names: names}, nil
}

// scanStrings runs a query whose columns are all text (NULL reads as "") and hands each
// row to add.
func scanStrings(ctx context.Context, db store.Pool, query string, add func([]string)) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("%s: %w", query, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	out := make([]string, len(cols))
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for i, v := range vals {
			out[i] = v.String
		}
		add(out)
	}
	return rows.Err()
}

// apiPath is a request path inside log text: the main API, the audiobook server's app
// routes, its public sessions, and OPDS.
var apiPath = regexp.MustCompile(`/(?:api|public|opds|s|hls)/[^\s"'?&,;)]*`)

// audioItemID is an audiobook item or media key, whatever attr it sits in.
var audioItemID = regexp.MustCompile(`^m?b\d+(v\d+)?$`)

// Attr names whose values say who someone is, which book a line is about, or which book or
// listening session it is by id. Matched on the last dotted segment, lower-cased.
var (
	bundleUserAttrs = map[string]bool{
		"user": true, "username": true, "user_id": true, "userid": true, "uid": true, "by": true,
		"requester": true, "requested_by": true, "owner": true, "account": true, "email": true,
		"kindle": true, "kindle_email": true, "plex_user": true, "plex_username": true, "who": true,
		"jellyfin_user": true, "jellyfin_username": true,
		"person": true, "decided_by": true, "decider": true, "follower": true,
	}
	bundleNameAttrs = map[string]bool{
		"book": true, "author": true, "narrator": true, "bookmark": true, "chapter": true,
	}
	bundleItemAttrs = map[string]bool{
		"book_id": true, "bookid": true, "item": true, "item_id": true, "itemid": true,
		"library_item": true, "library_item_id": true, "session": true, "session_id": true,
	}
)

// literal blanks only the install's own secret values and its people and book names.
func (s *bundleScrubber) literal(t string) string {
	return s.names.Scrub(s.secrets.Replace(t))
}

// text scrubs free text: the install's secrets by value, then secrets by shape, then
// audiobook ids in paths, then names.
func (s *bundleScrubber) text(t string) string {
	t = s.secrets.Replace(t)
	t = applog.RedactText(t)
	t = apiPath.ReplaceAllStringFunc(t, applog.RedactPath)
	return s.names.Scrub(t)
}

// entry scrubs one log entry.
func (s *bundleScrubber) entry(e applog.Entry) (applog.Entry, bool) {
	e, keep := audioserver.ScrubLegacyEntry(e)
	if !keep {
		return e, false
	}
	e.Seq = 0
	e.Attrs = applog.RewriteAttrs(e.Attrs, func(key, val string) string {
		leaf := strings.ToLower(key)
		if i := strings.LastIndexByte(leaf, '.'); i >= 0 {
			leaf = leaf[i+1:]
		}
		switch {
		case bundleUserAttrs[leaf]:
			return "«user»"
		case bundleNameAttrs[leaf]:
			return "«name»"
		case bundleItemAttrs[leaf]:
			return "{id}"
		case audioItemID.MatchString(strings.TrimSpace(val)):
			return "{id}"
		}
		return val
	})
	e.Message = s.text(e.Message)
	e.Attrs = s.text(e.Attrs)
	return e, true
}

// settingsDoc is the bundle's settings.json: the settings plainly safe to share, and the
// names of the ones left out.
type settingsDoc struct {
	Included map[string]string `json:"included"`
	Withheld []string          `json:"withheld"`
}

type settingsSplit struct {
	doc          settingsDoc
	secretValues []string // the values of settings whose names say they're credentials
}

// Words in a setting's name that keep it out of the bundle. credentialSettingWords name a
// credential or a link that may carry one: its value is also blanked wherever it turns up
// in the logs. privateSettingWords are only kept out — their values are blanked from the
// logs too, unless the value is a plain word that would wreck ordinary text ("always").
var (
	credentialSettingWords = []string{
		"apikey", "api_key", "token", "password", "passwd", "secret", "apprise", "cookie",
		"credential", "smtp", "webhook", "url", "kindle", "insights_plex_", "jellyfin_",
	}
	privateSettingWords = []string{"key", "session", "auth", "mail", "user", "machine", "client_id", "pin", "contact"}
)

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// safeSettingValue is the shape of a value that can be shown: a yes/no, a number, a short
// lower-case word or list of words ("hevc", "en,fr", "v1"), a clock time or a duration.
// Anything else — free text, paths, links, JSON, mixed-case or digit-heavy strings that
// could be a key — is left out.
var (
	safeWord   = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[_.\-][a-z0-9]+)*$`)
	safeNumber = regexp.MustCompile(`^-?\d{1,10}(?:\.\d{1,4})?$`)
	safeClock  = regexp.MustCompile(`^\d{1,2}:\d{2}$`)
)

func safeSettingValue(v string) bool {
	if v == "" || len(v) > 64 {
		return v == ""
	}
	if safeNumber.MatchString(v) || safeClock.MatchString(v) {
		return true
	}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if len(part) > 32 || !safeWord.MatchString(part) {
			return false
		}
		digits := 0
		for _, r := range part {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits > 4 {
			return false // a run of digits in a word looks more like a key than a choice
		}
	}
	return true
}

// bundleSettings splits the settings into what the bundle shows and what it leaves out.
// It's an allowlist by shape: a setting is shown only when its name says nothing about
// credentials and its value is plainly a choice, not data.
func bundleSettings(all map[string]string) settingsSplit {
	out := settingsSplit{doc: settingsDoc{Included: map[string]string{}, Withheld: []string{}}}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := all[k]
		lk := strings.ToLower(k)
		safe := safeSettingValue(strings.TrimSpace(v))
		switch {
		case containsAny(lk, credentialSettingWords):
			out.doc.Withheld = append(out.doc.Withheld, k)
			out.secretValues = append(out.secretValues, v)
			continue
		case containsAny(lk, privateSettingWords):
			out.doc.Withheld = append(out.doc.Withheld, k)
			if !safe {
				out.secretValues = append(out.secretValues, v)
			}
			continue
		}
		if !safe {
			out.doc.Withheld = append(out.doc.Withheld, k)
			continue
		}
		out.doc.Included[k] = v
	}
	return out
}

// captureJSON runs a JSON handler against r and returns its body, or an error document
// when it didn't answer 200.
func captureJSON(h http.HandlerFunc, r *http.Request) []byte {
	rec := &bufferWriter{header: http.Header{}, status: http.StatusOK}
	h(rec, r)
	if rec.status != http.StatusOK {
		return mustJSON(map[string]any{"error": fmt.Sprintf("HTTP %d", rec.status)})
	}
	return rec.buf.Bytes()
}

// bufferWriter is a ResponseWriter that keeps the body in memory.
type bufferWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (b *bufferWriter) Header() http.Header         { return b.header }
func (b *bufferWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bufferWriter) WriteHeader(code int)        { b.status = code }

func mustJSON(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return []byte(`{"error":` + strconv.Quote(err.Error()) + `}`)
	}
	return b
}

func writeZipFile(zw *zip.Writer, name string, body []byte) error {
	f, err := zw.CreateHeader(&zip.FileHeader{Name: path.Clean(name), Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return err
	}
	_, err = f.Write(body)
	return err
}
