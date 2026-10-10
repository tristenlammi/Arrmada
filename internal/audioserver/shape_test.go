package audioserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The compatibility harness. testdata/abs/<version>/ holds Audiobookshelf's own replies
// to a fixed conversation (steps.json plus one <step>.json per JSON reply), recorded by
// cmd/abs-capture against a throwaway Audiobookshelf. TestRepliesMatchAudiobookshelf
// plays the same conversation against this server and compares the SHAPE of every reply:
// every key Audiobookshelf sends must be here, with the same JSON kind. Values don't
// matter. A known, deliberate difference is listed in testdata/abs/allowed_diffs.txt
// with its reason; anything else fails, so drift shows up in CI rather than as a phone
// app that quietly shows an empty library.

var uuidShape = regexp.MustCompile(`^(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// jsonKind names a decoded JSON value's kind.
func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64, json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

// isIDKey is an "id" field or a "<thing>Id" one.
func isIDKey(path string) bool {
	k := path
	if i := strings.LastIndexAny(path, ".]"); i >= 0 {
		k = path[i+1:]
	}
	return k == "id" || (strings.HasSuffix(k, "Id") && len(k) > 2)
}

// diffShape lists how got's shape falls short of want's, one "<path> <kind>: <detail>"
// per difference. Paths read "$.user.permissions.download", with "[]" for an array
// element. Rules:
//   - every key in want must exist in got; extra keys in got are fine
//   - JSON kinds must match; a null in want accepts anything, a null in got where want
//     has a value is reported
//   - arrays: each element of got is compared with want[0] (when both have elements)
//   - an id / *Id string is flagged "idshape" when want's is UUID-shaped and got's isn't
func diffShape(path string, want, got any) []string {
	var out []string
	if want == nil {
		return nil
	}
	if got == nil {
		return []string{path + " null: Audiobookshelf sends " + jsonKind(want) + ", got null"}
	}
	if wk, gk := jsonKind(want), jsonKind(got); wk != gk {
		return []string{path + " kind: Audiobookshelf sends " + wk + ", got " + gk}
	}
	switch w := want.(type) {
	case map[string]any:
		g := got.(map[string]any)
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			gv, ok := g[k]
			if !ok {
				out = append(out, path+"."+k+" missing: Audiobookshelf sends "+jsonKind(w[k]))
				continue
			}
			out = append(out, diffShape(path+"."+k, w[k], gv)...)
		}
	case []any:
		g := got.([]any)
		if len(w) == 0 || len(g) == 0 {
			return nil
		}
		// Report each distinct difference once, however many elements share it.
		seen := map[string]bool{}
		for _, e := range g {
			for _, d := range diffShape(path+"[]", w[0], e) {
				if !seen[d] {
					seen[d] = true
					out = append(out, d)
				}
			}
		}
	case string:
		if isIDKey(path) && uuidShape.MatchString(w) && !uuidShape.MatchString(got.(string)) {
			out = append(out, path+" idshape: Audiobookshelf sends a UUID, got "+fmt.Sprintf("%q", got))
		}
	}
	return out
}

// diffPath is the "<path>" part of a diffShape line.
func diffPath(d string) string {
	p, _, _ := strings.Cut(d, " ")
	return p
}

// --- fixtures ----------------------------------------------------------------

// absStep is one call in steps.json.
type absStep struct {
	Name    string            `json:"name"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    any               `json:"body"`
	Status  int               `json:"status"`
	Reply   string            `json:"reply"` // json, text or empty
	Note    string            `json:"note,omitempty"`
}

type absSteps struct {
	ABSVersion string    `json:"absVersion"`
	Origin     string    `json:"origin"`
	Steps      []absStep `json:"steps"`
}

// newestFixtures finds the newest recorded Audiobookshelf version.
func newestFixtures(t *testing.T) string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join("testdata", "abs"))
	if err != nil {
		t.Fatal(err)
	}
	best, bestV := "", []int(nil)
	for _, e := range ents {
		v, ok := parseVersion(e.Name())
		if !e.IsDir() || !ok {
			continue
		}
		if best == "" || versionLess(bestV, v) {
			best, bestV = e.Name(), v
		}
	}
	if best == "" {
		t.Fatal("no Audiobookshelf fixtures under testdata/abs")
	}
	return filepath.Join("testdata", "abs", best)
}

// parseVersion reads "2.37.1" as [2 37 1].
func parseVersion(s string) ([]int, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return nil, false
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

func versionLess(a, b []int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func loadSteps(t *testing.T, dir string) absSteps {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "steps.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s absSteps
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("steps.json: %v", err)
	}
	return s
}

func loadReply(t *testing.T, dir, step string) any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, step+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s.json: %v", step, err)
	}
	return v
}

// allowedDiff is one line of allowed_diffs.txt: "<step|*> <path> <kind> <reason>". The
// kind (missing, null, kind, idshape, body, status) is part of the match, so allowing an
// id's shape never hides that id going missing. A path ending in "*" covers everything
// under it (e.g. "$.media.audioFiles[].metaTags.*").
type allowedDiff struct {
	line             int
	step, path, kind string
	reason, text     string
}

var diffKinds = map[string]bool{"missing": true, "null": true, "kind": true, "idshape": true, "body": true, "status": true}

// diffKind is the "<kind>" part of a diffShape line ("$.x missing: …" → "missing").
func diffKind(d string) string {
	_, rest, _ := strings.Cut(d, " ")
	k, _, _ := strings.Cut(rest, ":")
	return k
}

func loadAllowed(t *testing.T) []allowedDiff {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "abs", "allowed_diffs.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []allowedDiff
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 || !diffKinds[f[2]] {
			t.Fatalf("allowed_diffs.txt:%d: want \"<step> <path> <kind> <reason>\", got %q", n, line)
		}
		out = append(out, allowedDiff{line: n, step: f[0], path: f[1], kind: f[2], reason: strings.Join(f[3:], " "), text: line})
	}
	return out
}

func (a allowedDiff) allows(step, diff string) bool {
	if (a.step != "*" && a.step != step) || a.kind != diffKind(diff) {
		return false
	}
	if prefix, ok := strings.CutSuffix(a.path, "*"); ok {
		return strings.HasPrefix(diffPath(diff), prefix)
	}
	return a.path == diffPath(diff)
}

// --- playing the conversation ---------------------------------------------------

// fill swaps the {placeholders} in a path or request body for this server's ids.
func fill(v any, vars map[string]string) any {
	switch t := v.(type) {
	case string:
		for k, val := range vars {
			t = strings.ReplaceAll(t, "{"+k+"}", val)
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = fill(e, vars)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = fill(e, vars)
		}
		return out
	}
	return v
}

// shapeHarness is the test harness with a book that has everything Audiobookshelf's
// recorded one has that Arrmada can carry: a series, a cover and some progress later on.
func shapeHarness(t *testing.T) (*harness, map[string]string) {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()
	if err := h.srv.books.SetSeries(ctx, h.book.ID, "The Crawl", 1); err != nil {
		t.Fatal(err)
	}
	if err := h.srv.books.SetCover(ctx, h.book.ID, "https://covers.example/dcc.jpg"); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{
		"libraryId": libraryID,
		"itemId":    itemKeyFor(h.book.ID, 0),
		"authorId":  authorID("Matt Dinniman"),
		"seriesId":  seriesID("The Crawl"),
	}
	return h, vars
}

// stepReply is what this server answered one step with.
type stepReply struct {
	status int
	kind   string // json, text or empty
	body   any
	raw    []byte
}

// runStep plays one recorded step against the harness. Sign-in steps use the harness's
// reader; the token and session id are picked up for the steps after.
func runStep(t *testing.T, h *harness, st absStep, vars map[string]string) stepReply {
	t.Helper()
	path := fill(st.Path, vars).(string)
	body := fill(st.Body, vars)
	if st.Path == "/login" {
		body = map[string]string{"username": "reader", "password": "listen-pass-1"}
	}
	hdr := map[string]string{}
	for k, v := range st.Headers {
		hdr[k] = v
	}
	code, out := h.do(st.Method, path, body, hdr)
	r := stepReply{status: code, raw: out}
	switch t := bytes.TrimSpace(out); {
	case len(t) == 0:
		r.kind = "empty"
	case json.Unmarshal(t, &r.body) == nil && (t[0] == '{' || t[0] == '['):
		r.kind = "json"
	default:
		r.kind = "text"
	}
	if st.Path == "/login" && code == 200 {
		if m, ok := r.body.(map[string]any); ok {
			if u, ok := m["user"].(map[string]any); ok {
				if tok, _ := u["accessToken"].(string); tok != "" {
					h.token = tok
				}
			}
		}
	}
	if st.Name == "play" {
		if m, ok := r.body.(map[string]any); ok {
			if id, _ := m["id"].(string); id != "" {
				vars["sessionId"] = id
			}
		}
	}
	return r
}

// compareStep lists every difference between Audiobookshelf's recorded reply to a step
// and this server's.
func compareStep(t *testing.T, dir string, st absStep, got stepReply) []string {
	t.Helper()
	var diffs []string
	if got.status != st.Status {
		diffs = append(diffs, fmt.Sprintf("$status status: Audiobookshelf answers %d, got %d %s", st.Status, got.status, truncate(got.raw)))
	}
	if got.kind != st.Reply {
		diffs = append(diffs, fmt.Sprintf("$body body: Audiobookshelf replies %s, got %s", st.Reply, got.kind))
	}
	if st.Reply == "json" && got.kind == "json" {
		diffs = append(diffs, diffShape("$", loadReply(t, dir, st.Name), got.body)...)
	}
	return diffs
}

func truncate(b []byte) string {
	if len(b) > 120 {
		return string(b[:120]) + "…"
	}
	return string(b)
}

// replyDiffs plays the whole recorded conversation and returns every difference, keyed
// by step.
func replyDiffs(t *testing.T) (map[string][]string, []absStep) {
	t.Helper()
	dir := newestFixtures(t)
	steps := loadSteps(t, dir)
	h, vars := shapeHarness(t)
	out := map[string][]string{}
	for _, st := range steps.Steps {
		got := runStep(t, h, st, vars)
		if d := compareStep(t, dir, st, got); len(d) > 0 {
			out[st.Name] = d
		}
	}
	return out, steps.Steps
}

// Every reply has every key and JSON kind Audiobookshelf's has, unless allowed_diffs.txt
// says why not.
func TestRepliesMatchAudiobookshelf(t *testing.T) {
	diffs, steps := replyDiffs(t)
	allowed := loadAllowed(t)
	for _, st := range steps {
		for _, d := range diffs[st.Name] {
			ok := false
			for _, a := range allowed {
				if a.allows(st.Name, d) {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("%s %s %s: %s", st.Name, st.Method, st.Path, d)
			}
		}
	}
	if t.Failed() {
		t.Log("Fix the reply, or (for a deliberate difference) add \"<step> <path> <reason>\" to testdata/abs/allowed_diffs.txt")
	}
}

// An allowance that no longer matches any difference is stale and must go, so the list
// only ever shrinks to what's really still different.
func TestAllowedDiffsAreNotStale(t *testing.T) {
	diffs, _ := replyDiffs(t)
	for _, a := range loadAllowed(t) {
		used := false
		for step, ds := range diffs {
			for _, d := range ds {
				if a.allows(step, d) {
					used = true
				}
			}
		}
		if !used {
			t.Errorf("allowed_diffs.txt:%d is stale (nothing differs there any more): %s", a.line, a.text)
		}
	}
}

// Every step in the fixtures has its reply recorded, and the fixtures carry nothing
// secret: no JWT, no token value, no address.
func TestFixturesAreCompleteAndClean(t *testing.T) {
	dir := newestFixtures(t)
	steps := loadSteps(t, dir)
	if steps.ABSVersion == "" || filepath.Base(dir) != steps.ABSVersion {
		t.Fatalf("steps.json absVersion %q doesn't match its folder %s", steps.ABSVersion, dir)
	}
	jwt := regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.`)
	ip := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	for _, st := range steps.Steps {
		if st.Reply != "json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, st.Name+".json"))
		if err != nil {
			t.Errorf("step %s: %v", st.Name, err)
			continue
		}
		s := string(b)
		if jwt.MatchString(s) {
			t.Errorf("%s.json holds a JWT", st.Name)
		}
		if m := ip.FindString(s); m != "" && !strings.HasPrefix(m, "192.0.2.") {
			t.Errorf("%s.json holds an IP address %s", st.Name, m)
		}
		var v any
		_ = json.Unmarshal(b, &v)
		walkStrings(v, "", func(key, val string) {
			switch key {
			case "token", "accessToken", "refreshToken":
				if val != "<redacted>" {
					t.Errorf("%s.json: %s isn't redacted", st.Name, key)
				}
			}
		})
	}
}

func walkStrings(v any, key string, fn func(key, val string)) {
	switch t := v.(type) {
	case string:
		fn(key, t)
	case map[string]any:
		for k, e := range t {
			walkStrings(e, k, fn)
		}
	case []any:
		for _, e := range t {
			walkStrings(e, key, fn)
		}
	}
}

// diffShape itself: the rules the harness relies on.
func TestDiffShapeCatchesTypeAndIdDrift(t *testing.T) {
	const u = "0f9a3a1c-1111-4c3e-9d8e-2b7f5a6c7d8e"
	want := map[string]any{
		"id": u, "libraryItemId": u, "title": "x", "duration": 1.5, "isFinished": false,
		"ebookFile": nil, "chapters": []any{map[string]any{"id": 0.0, "start": 0.0, "title": "c"}},
		"tags": []any{}, "media": map[string]any{"coverPath": "/x"},
	}
	got := map[string]any{
		"id": "b12", "libraryItemId": u, "title": 3.0, "isFinished": false,
		"ebookFile": map[string]any{"x": 1.0}, // null in want: anything goes
		"chapters": []any{
			map[string]any{"id": 0.0, "start": "0", "title": "c"},
			map[string]any{"id": 1.0, "start": "1", "title": "d"}, // same difference, reported once
		},
		"tags": []any{"a"}, "media": map[string]any{"coverPath": nil}, "extra": true,
	}
	d := diffShape("$", want, got)
	wantDiffs := []string{
		"$.chapters[].start kind: Audiobookshelf sends number, got string",
		"$.duration missing: Audiobookshelf sends number",
		`$.id idshape: Audiobookshelf sends a UUID, got "b12"`,
		"$.media.coverPath null: Audiobookshelf sends string, got null",
		"$.title kind: Audiobookshelf sends string, got number",
	}
	if strings.Join(d, "\n") != strings.Join(wantDiffs, "\n") {
		t.Fatalf("diffShape:\n%s\nwant:\n%s", strings.Join(d, "\n"), strings.Join(wantDiffs, "\n"))
	}
	if len(diffShape("$", []any{}, []any{1.0})) != 0 || len(diffShape("$", []any{1.0}, []any{})) != 0 {
		t.Fatal("an empty array on either side has no element shape to compare")
	}
	if d := diffShape("$", []any{map[string]any{"id": u}}, []any{map[string]any{"id": u}}); len(d) != 0 {
		t.Fatalf("UUID against UUID: %v", d)
	}
	if !(allowedDiff{step: "*", path: "$.id", kind: "idshape"}).allows("me", "$.id idshape: x") ||
		(allowedDiff{step: "me", path: "$.id", kind: "idshape"}).allows("item", "$.id idshape: x") ||
		(allowedDiff{step: "me", path: "$.id", kind: "missing"}).allows("me", "$.idx missing: x") ||
		(allowedDiff{step: "*", path: "$.userId", kind: "idshape"}).allows("me", "$.userId missing: x") || // a shape allowance never hides a missing id
		!(allowedDiff{step: "item", path: "$.a.metaTags.*", kind: "missing"}).allows("item", "$.a.metaTags.tagAlbum missing: x") ||
		(allowedDiff{step: "item", path: "$.a.metaTags.*", kind: "missing"}).allows("item", "$.a.other missing: x") {
		t.Fatal("allowance matching")
	}
}
