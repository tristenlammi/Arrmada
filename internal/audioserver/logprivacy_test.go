package audioserver

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/applog"
)

// lockedBuffer is a log sink handlers can write to from their own goroutines while the
// test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Admins see how much and when people listen, never what. The request log used to record
// the path of every call — which book was opened, played and bookmarked, which author was
// browsed — and the search text, and managers could read it on the Logs page.
func TestRequestLogNeverNamesABook(t *testing.T) {
	var buf lockedBuffer
	ring := applog.NewRing(1000)
	h := newHarnessLogging(t, slog.New(applog.NewHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}), ring)))

	key := itemKeyFor(h.book.ID, 0)
	aid := authorID("Matt Dinniman")
	code, out := h.do("POST", "/login", map[string]string{"username": "reader", "password": "listen-pass-1"}, map[string]string{"x-return-tokens": "true"})
	if code != 200 {
		t.Fatalf("login: HTTP %d %s", code, out)
	}
	var login map[string]any
	if err := json.Unmarshal(out, &login); err != nil {
		t.Fatal(err)
	}
	h.token = obj1(t, login["user"])["accessToken"].(string)

	h.json("GET", "/api/items/"+key, nil)
	play := h.json("POST", "/api/items/"+key+"/play", map[string]any{
		"deviceInfo": map[string]string{"clientName": "Lissen", "deviceId": "dev-1"}, "mediaPlayer": "exo",
	})
	sid := play["id"].(string)
	// Forward, then a big jump back: the jump is held, which logs at debug.
	h.do("POST", "/api/session/"+sid+"/sync", map[string]float64{"timeListened": 10, "currentTime": 3000}, nil)
	h.do("POST", "/api/session/"+sid+"/sync", map[string]float64{"timeListened": 10, "currentTime": 10}, nil)
	h.json("POST", "/api/me/item/"+key+"/bookmark", map[string]any{"time": 300, "title": "Good bit"})
	h.do("GET", "/api/authors/"+aid, nil, nil)
	h.do("GET", "/api/libraries/"+libraryID+"/search?q=dungeon&limit=5", nil, nil)
	h.do("GET", "/api/items/"+key+"/nope", nil, nil)
	h.http.Close() // waits for every handler, so every log line is written

	logged := buf.String()
	for _, e := range ring.Snapshot(applog.Filter{Min: slog.LevelDebug}) {
		logged += "\n" + e.Message + " " + e.Attrs
	}
	if regexp.MustCompile(`(^|[^a-z0-9])` + key + `([^0-9v]|$)`).MatchString(logged) {
		t.Errorf("the log names item %s:\n%s", key, logged)
	}
	for _, secret := range []string{"dungeon", "Dungeon", aid, "Good bit", "Dinniman"} {
		if strings.Contains(logged, secret) {
			t.Errorf("the log contains %q:\n%s", secret, logged)
		}
	}
	for _, want := range []string{
		"POST /api/items/{id}/play",
		"GET /api/libraries/{lib}/search",
		"query_keys=limit,q",
		"GET /api/items/{id}/nope", // an unsupported call still shows up, redacted
		"holding a jump back",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("the log is missing %q:\n%s", want, logged)
		}
	}
	// No line pairs the account with anything.
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, "reader") && strings.Contains(line, "{id}") {
			t.Errorf("a line pairs the user with an item route: %s", line)
		}
	}
}

// Lines an older version already wrote to disk get the same treatment.
func TestScrubLegacyEntry(t *testing.T) {
	cases := []struct {
		msg, attrs, want string
	}{
		{
			"audiobook server: request",
			"method=GET path=/api/libraries/arrmada-audiobooks/search query=limit=5&q=dungeon+crawler status=200 bytes=812 token=«redacted» client=Lissen/1.9 (Android 15)",
			"route=GET /api/libraries/arrmada-audiobooks/search query_keys=limit,q status=200 bytes=812 token=«redacted» client=Lissen/1.9 (Android 15)",
		},
		{
			"audiobook server: request",
			"method=POST path=/api/items/b45/play query= status=200 bytes=3000 token=«redacted» client=Plappa",
			"route=POST /api/items/{id}/play query_keys= status=200 bytes=3000 token=«redacted» client=Plappa",
		},
		{
			"audiobook server: holding a jump back until playback continues from it",
			"user=reader item=b45v2 saved=3000 reported=10",
			"saved=3000 reported=10",
		},
		{
			"audiobook server: holding a jump back until playback continues from it",
			"user=John Smith item=b45v2 saved=3000 reported=10",
			"saved=3000 reported=10",
		},
		{
			"audiobook server: refused a request",
			"path=/api/items/b45/cover token=true client=Lissen/1.9 (Android 15)",
			"route=/api/items/{id}/cover token=true client=Lissen/1.9 (Android 15)",
		},
		{
			"audiobook server: unsupported request",
			"method=GET path=/api/items/b45v2/chapters/3 client=Plappa",
			"route=GET /api/items/{id}/chapters/{id} client=Plappa",
		},
		{
			"audiobook server: panic",
			"path=/api/authors/au0123456789ab err=boom",
			"route=/api/authors/{id} err=boom",
		},
		{
			"request",
			"method=GET path=/api/v1/books/12/audiobook status=200 dur_ms=4",
			"method=GET path=/api/v1/books/{id}/audiobook status=200 dur_ms=4",
		},
		{
			"series: grabbing", "series=Taskmaster", "series=Taskmaster", // untouched
		},
	}
	for _, c := range cases {
		got, keep := ScrubLegacyEntry(applog.Entry{TimeMS: 1, Level: "INFO", Message: c.msg, Attrs: c.attrs})
		if !keep {
			t.Errorf("%s: dropped", c.msg)
		}
		if got.Attrs != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.msg, got.Attrs, c.want)
		}
	}
}
