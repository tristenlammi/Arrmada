// Command abs-capture records a real Audiobookshelf server's replies to the conversation
// a listening app has with it, for Arrmada's compatibility test
// (internal/audioserver/shape_test.go). It's a developer tool: the Docker image builds
// only ./cmd/arrmada, so this never ships.
//
// Recipe (never point it at a real Audiobookshelf with real people's listening in it):
//
//  1. Start a throwaway Audiobookshelf pinned to the version Arrmada reports
//     (audioserver.ServerVersion):
//     docker run --rm -p 13380:80 -v "$PWD/abs-books:/audiobooks" ghcr.io/advplyr/audiobookshelf:<ServerVersion>
//  2. In its web page (http://localhost:13380) create the root user, then a second,
//     ordinary user for the capture, and a Book library on /audiobooks holding ONE
//     public-domain book with a series, a narrator, a genre and a cover, made of the
//     LibriVox mp3s AND the Project Gutenberg EPUB of the same title (so the ebookFile
//     shape is recorded too). Let the scan finish.
//  3. From the repository root:
//     ABS_URL=http://localhost:13380 ABS_USER=<the ordinary user> ABS_PASS=<its password> go run ./cmd/abs-capture
//  4. Read the diff of internal/audioserver/testdata/abs/<version>/ before committing:
//     tokens, cookies, usernames, addresses and paths are scrubbed, but look anyway.
//  5. go test ./internal/audioserver -run 'TestRepliesMatchAudiobookshelf|TestAllowedDiffsAreNotStale'
//     and fix the replies (or list a deliberate difference in testdata/abs/allowed_diffs.txt).
//
// The credentials come from the environment only, so nothing is hardcoded, committed or
// pasted into a chat.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const usage = `abs-capture records a throwaway Audiobookshelf's replies for Arrmada's compatibility test.

Environment:
  ABS_URL   the throwaway server, e.g. http://localhost:13380
  ABS_USER  an ordinary (non-root) user on it
  ABS_PASS  that user's password
  ABS_OUT   where to write (default internal/audioserver/testdata/abs)
  ABS_QUERY a word from the book's title to search for (default: the title's first word)

Recipe: docker run --rm -p 13380:80 -v "$PWD/abs-books:/audiobooks" ghcr.io/advplyr/audiobookshelf:<ServerVersion>
then create a root user, an ordinary user and a Book library on /audiobooks with ONE
public-domain book (LibriVox mp3s + the Project Gutenberg EPUB of the same title, with a
series, narrator, genre and cover). Never point this at a real Audiobookshelf.
Read the written fixtures' diff before committing.
`

// step is one call of the conversation; its path and body may hold {placeholders} that
// earlier replies fill in.
type step struct {
	Name    string            `json:"name"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    any               `json:"body"`
	Status  int               `json:"status"`
	Reply   string            `json:"reply"`
	Note    string            `json:"note,omitempty"`
}

func deviceInfo() map[string]any {
	return map[string]any{"clientName": "abs-capture", "deviceId": "abs-capture-1", "deviceName": "Capture"}
}

// conversation is the calls a listening app makes, in order: sign in, browse, open a book,
// play, sync, close, set a place, bookmark, upload offline listening, read it all back.
func conversation() []step {
	lib := "/api/libraries/{libraryId}"
	return []step{
		{Name: "status", Method: "GET", Path: "/status"},
		{Name: "ping", Method: "GET", Path: "/ping"},
		{Name: "login", Method: "POST", Path: "/login", Headers: map[string]string{"x-return-tokens": "true"},
			Body: map[string]any{"username": "{username}", "password": "{password}"}},
		{Name: "login_cookie", Method: "POST", Path: "/login", Body: map[string]any{"username": "{username}", "password": "{password}"},
			Note: "without x-return-tokens: the refresh token comes as a cookie"},
		{Name: "authorize", Method: "POST", Path: "/api/authorize"},
		{Name: "me", Method: "GET", Path: "/api/me"},
		{Name: "libraries", Method: "GET", Path: "/api/libraries"},
		{Name: "libraries_stats", Method: "GET", Path: "/api/libraries?include=stats"},
		{Name: "library", Method: "GET", Path: lib + "?include=filterdata"},
		{Name: "library_items", Method: "GET", Path: lib + "/items?minified=1&limit=10&page=0"},
		{Name: "personalized", Method: "GET", Path: lib + "/personalized"},
		{Name: "search", Method: "GET", Path: lib + "/search?q={query}"},
		{Name: "authors", Method: "GET", Path: lib + "/authors"},
		{Name: "authors_paged", Method: "GET", Path: lib + "/authors?limit=10&page=0"},
		{Name: "series", Method: "GET", Path: lib + "/series", Note: "without ?limit Audiobookshelf answers no results (total still counts them)"},
		{Name: "series_paged", Method: "GET", Path: lib + "/series?limit=10&page=0"},
		{Name: "filterdata", Method: "GET", Path: lib + "/filterdata"},
		{Name: "library_stats", Method: "GET", Path: lib + "/stats"},
		{Name: "narrators", Method: "GET", Path: lib + "/narrators"},
		{Name: "author", Method: "GET", Path: "/api/authors/{authorId}?include=items"},
		{Name: "series_one", Method: "GET", Path: "/api/series/{seriesId}"},
		{Name: "item", Method: "GET", Path: "/api/items/{itemId}?expanded=1&include=progress"},
		{Name: "batch_get", Method: "POST", Path: "/api/items/batch/get", Body: map[string]any{"libraryItemIds": []any{"{itemId}"}}},
		{Name: "play", Method: "POST", Path: "/api/items/{itemId}/play", Body: map[string]any{
			"deviceInfo": deviceInfo(), "supportedMimeTypes": []any{"audio/mpeg", "audio/mp4"}, "mediaPlayer": "AVPlayer",
			"forceDirectPlay": true, "forceTranscode": false}},
		{Name: "sync", Method: "POST", Path: "/api/session/{sessionId}/sync",
			Body: map[string]any{"currentTime": 120, "timeListened": 10, "duration": 3600}},
		{Name: "close", Method: "POST", Path: "/api/session/{sessionId}/close", Note: "empty body: what does the server do with no position?"},
		{Name: "progress_patch", Method: "PATCH", Path: "/api/me/progress/{itemId}",
			Body: map[string]any{"currentTime": 150, "duration": 3600, "progress": 150.0 / 3600, "isFinished": false}},
		{Name: "progress_get", Method: "GET", Path: "/api/me/progress/{itemId}"},
		{Name: "progress_all", Method: "GET", Path: "/api/me/progress"},
		{Name: "items_in_progress", Method: "GET", Path: "/api/me/items-in-progress"},
		{Name: "listening_stats", Method: "GET", Path: "/api/me/listening-stats"},
		{Name: "listening_sessions", Method: "GET", Path: "/api/me/listening-sessions"},
		{Name: "bookmark_add", Method: "POST", Path: "/api/me/item/{itemId}/bookmark", Body: map[string]any{"time": 60, "title": "Example bookmark"}},
		{Name: "local_all", Method: "POST", Path: "/api/session/local-all", Body: map[string]any{
			"deviceInfo": deviceInfo(),
			"sessions": []any{map[string]any{
				"id": "{offlineId}", "libraryItemId": "{itemId}", "episodeId": nil, "mediaType": "book", "duration": 3600,
				"playMethod": 3, "mediaPlayer": "AVPlayer", "deviceInfo": deviceInfo(), "startTime": 150, "currentTime": 210,
				"timeListening": 60, "startedAt": "{startedAt}", "updatedAt": "{updatedAt}", "date": "{date}", "dayOfWeek": "{dayOfWeek}",
				"displayTitle": "x", "displayAuthor": "x",
			}},
		}},
		{Name: "personalized_after", Method: "GET", Path: lib + "/personalized", Note: "with a book in progress now"},
		{Name: "me_after", Method: "GET", Path: "/api/me", Note: "with places and a bookmark now"},
		{Name: "authorize_after", Method: "POST", Path: "/api/authorize", Note: "with places and a bookmark now"},
	}
}

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()
	base, user, pass := strings.TrimRight(os.Getenv("ABS_URL"), "/"), os.Getenv("ABS_USER"), os.Getenv("ABS_PASS")
	if base == "" || user == "" || pass == "" {
		flag.Usage()
		os.Exit(2)
	}
	out := os.Getenv("ABS_OUT")
	if out == "" {
		out = filepath.Join("internal", "audioserver", "testdata", "abs")
	}
	if err := run(base, user, pass, out, os.Getenv("ABS_QUERY")); err != nil {
		fmt.Fprintln(os.Stderr, "abs-capture:", err)
		os.Exit(1)
	}
}

type capture struct {
	base   string
	client *http.Client
	token  string
	vars   map[string]string
	scrub  *scrubber
}

func run(base, user, pass, outDir, query string) error {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return fmt.Errorf("ABS_URL %q isn't a URL", base)
	}
	now := time.Now()
	c := &capture{
		base: base, client: &http.Client{Timeout: 60 * time.Second},
		vars: map[string]string{
			"username": user, "password": pass, "offlineId": newUUID(),
			"startedAt": fmt.Sprint(now.Add(-2 * time.Minute).UnixMilli()), "updatedAt": fmt.Sprint(now.UnixMilli()),
			"date": now.Format("2006-01-02"), "dayOfWeek": now.Weekday().String(),
		},
		scrub: newScrubber(u.Hostname(), user),
	}
	steps := conversation()
	replies := map[string]any{}
	version := ""
	for i := range steps {
		st := &steps[i]
		if err := c.resolve(st, replies, query); err != nil {
			return fmt.Errorf("step %s: %w", st.Name, err)
		}
		status, kind, body, err := c.do(*st)
		if err != nil {
			return fmt.Errorf("step %s: %w", st.Name, err)
		}
		st.Status, st.Reply = status, kind
		fmt.Fprintf(os.Stderr, "%-20s %s %s → %d %s\n", st.Name, st.Method, st.Path, status, kind)
		if kind == "json" {
			replies[st.Name] = body
		}
		if st.Name == "status" {
			if m, ok := body.(map[string]any); ok {
				version, _ = m["serverVersion"].(string)
			}
		}
		if st.Path == "/login" && status == 200 && c.token == "" {
			c.token = dig(body, "user", "accessToken")
			if c.token == "" {
				c.token = dig(body, "user", "token")
			}
		}
	}
	if version == "" {
		return errors.New("/status didn't say which version this is")
	}
	dir := filepath.Join(outDir, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, st := range steps {
		if body, ok := replies[st.Name]; ok {
			if err := writeJSON(filepath.Join(dir, st.Name+".json"), c.scrub.value("", body)); err != nil {
				return err
			}
		}
	}
	// steps keeps the request bodies in {placeholder} form (do fills a copy), so the
	// password and the real ids never reach steps.json.
	meta := map[string]any{"absVersion": version, "origin": "captured with cmd/abs-capture on " + now.Format("2006-01-02"), "steps": steps}
	if err := writeJSON(filepath.Join(dir, "steps.json"), meta); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s — read its diff before committing\n", dir)
	return nil
}

// resolve fills the ids a step needs from earlier replies.
func (c *capture) resolve(st *step, replies map[string]any, query string) error {
	need := func(name, val string) error {
		if val == "" {
			return fmt.Errorf("couldn't find {%s} in the earlier replies (is the library set up as the recipe says?)", name)
		}
		c.vars[name] = val
		return nil
	}
	if strings.Contains(st.Path, "{libraryId}") && c.vars["libraryId"] == "" {
		id := ""
		libs, _ := asMap(replies["libraries"])["libraries"].([]any)
		for _, l := range libs {
			if m := asMap(l); m["mediaType"] == "book" && id == "" {
				id = str(m["id"])
			}
		}
		if err := need("libraryId", id); err != nil {
			return err
		}
	}
	if strings.Contains(st.Path, "{itemId}") && c.vars["itemId"] == "" {
		res, _ := asMap(replies["library_items"])["results"].([]any)
		if len(res) == 0 {
			return need("itemId", "")
		}
		if err := need("itemId", str(asMap(res[0])["id"])); err != nil {
			return err
		}
	}
	if strings.Contains(st.Path, "{query}") && c.vars["query"] == "" {
		q := query
		if q == "" {
			res, _ := asMap(replies["library_items"])["results"].([]any)
			if len(res) > 0 {
				title := dig(res[0], "media", "metadata", "title")
				if f := strings.Fields(title); len(f) > 0 {
					q = f[0]
				}
			}
		}
		if err := need("query", url.QueryEscape(q)); err != nil {
			return err
		}
	}
	if strings.Contains(st.Path, "{authorId}") && c.vars["authorId"] == "" {
		res, _ := asMap(replies["authors"])["authors"].([]any)
		if len(res) == 0 {
			res, _ = asMap(replies["authors"])["results"].([]any)
		}
		if len(res) == 0 {
			return need("authorId", "")
		}
		if err := need("authorId", str(asMap(res[0])["id"])); err != nil {
			return err
		}
	}
	if strings.Contains(st.Path, "{seriesId}") && c.vars["seriesId"] == "" {
		res, _ := asMap(replies["series_paged"])["results"].([]any)
		if len(res) == 0 {
			return need("seriesId", "")
		}
		if err := need("seriesId", str(asMap(res[0])["id"])); err != nil {
			return err
		}
	}
	if strings.Contains(st.Path, "{sessionId}") && c.vars["sessionId"] == "" {
		if err := need("sessionId", str(asMap(replies["play"])["id"])); err != nil {
			return err
		}
	}
	return nil
}

func (c *capture) do(st step) (status int, kind string, body any, err error) {
	var rd io.Reader
	if b := fillAny(st.Body, c.vars); b != nil {
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(st.Method, c.base+fill(st.Path, c.vars), rd)
	if err != nil {
		return 0, "", nil, err
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "abs-capture/1")
	for k, v := range st.Headers {
		req.Header.Set(k, v)
	}
	if c.token != "" && st.Path != "/login" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return 0, "", nil, err
	}
	t := bytes.TrimSpace(raw)
	switch {
	case len(t) == 0:
		return resp.StatusCode, "empty", nil, nil
	case (t[0] == '{' || t[0] == '[') && json.Unmarshal(t, &body) == nil:
		return resp.StatusCode, "json", body, nil
	}
	return resp.StatusCode, "text", nil, nil
}

func fill(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

func fillAny(v any, vars map[string]string) any {
	switch t := v.(type) {
	case string:
		return fill(t, vars)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = fillAny(e, vars)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = fillAny(e, vars)
		}
		return out
	}
	return v
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// dig follows keys through nested objects to a string.
func dig(v any, keys ...string) string {
	for _, k := range keys {
		v = asMap(v)[k]
	}
	return str(v)
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
