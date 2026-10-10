package notify

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
)

// The alert catalog: every event a connection can subscribe to, with the words the
// Alerts page shows for it and how its message is written.
//
// Declaring an event: add an EventDef to builtinEvents below (or call Register from an
// init func, before Service.Run starts). Only declare events that something produces.
//
// Producing one, two ways:
//   - Topic set: publish that topic on the event bus with a map payload; Run formats it
//     with Format and dispatches it. Fire-and-forget — the bus can drop an event under
//     load — so this suits "nice to know" alerts (grabs, imports, Plex).
//   - No Topic: call Service.Emit (format + dispatch) or Service.Dispatch (a ready
//     Message) directly. Pass a dedupe key for exactly-once: a key already dispatched
//     to a connection isn't queued for it again (see Dispatch). Alerts that must not be
//     lost or doubled — needs-you, health — go this way.
//
// Never declare an event about what someone listens to (the audiobook privacy rule):
// admins see how much and when, never what.

// Message is one alert as it's sent: a title, a body and an in-app link ("/review",
// "/movies/12") a push notification opens.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Link  string `json:"link"`
}

// EventDef is one entry in the catalog.
type EventDef struct {
	Key   string `json:"key"`   // stable id stored in subscriptions ("book.imported")
	Topic string `json:"-"`     // the bus topic that produces it, "" if emitted directly
	Group string `json:"group"` // one of the Group* keys
	Label string `json:"label"` // the checkbox text
	Hint  string `json:"hint"`  // one line under it
	// DefaultOn: ticked on a new connection.
	DefaultOn bool `json:"default_on"`
	// Module hides the event while that module ("books", "music") is switched off.
	Module string `json:"module,omitempty"`
	// Format writes the message from the event's payload. ok=false means "nothing to
	// say" (no title, a library scan adopting files) and nothing is sent.
	Format func(data map[string]any) (m Message, ok bool) `json:"-"`
}

// The groups the Alerts page sorts events under, in page order. needs_you and convert
// are declared for the events later work adds (attention and health alerts, Convert);
// an empty group isn't shown.
const (
	GroupNeedsYou  = "needs_you"
	GroupRequests  = "requests"
	GroupDownloads = "downloads"
	GroupLibrary   = "library"
	GroupPlex      = "plex"
	GroupConvert   = "convert"
)

// Group is a catalog heading.
type Group struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

var groups = []Group{
	{GroupNeedsYou, "Needs you"},
	{GroupRequests, "Requests"},
	{GroupDownloads, "Downloads"},
	{GroupLibrary, "Library"},
	{GroupPlex, "Plex"},
	{GroupConvert, "Convert"},
}

// Groups returns the catalog headings in page order.
func Groups() []Group { return append([]Group(nil), groups...) }

var builtinEvents = []EventDef{
	{
		// Emitted directly by requests (announceCreated), exactly once per new ask: a
		// fresh request waiting for approval, or a declined title asked for again. Never
		// for an auto-approval, a follow or an import.
		Key: "request.created", Group: GroupRequests, DefaultOn: true,
		Label: "New request", Hint: "Someone asked for something and it's waiting for your approval.",
		Format: formatRequestCreated,
	},
	{
		Key: "request.auto_approved", Topic: "request.auto_approved", Group: GroupRequests,
		Label: "Auto-approved request", Hint: "Someone allowed to auto-approve asked for something, and it's on its way.",
		Format: func(d map[string]any) (Message, bool) {
			title := withYear(str(d, "title"), num(d, "year"))
			if title == "" {
				return Message{}, false
			}
			body := "✅ Auto-approved: " + title
			if who := str(d, "requested_by"); who != "" {
				body += " for " + who
			}
			return Message{Title: "Request auto-approved", Body: body, Link: "/discover"}, true
		},
	},
	{
		Key: "release.grabbed", Topic: "release.grabbed", Group: GroupDownloads,
		Label: "Grabbed", Hint: "A release was sent to the download client.",
		Format: func(d map[string]any) (Message, bool) {
			title := str(d, "title")
			if title == "" {
				return Message{}, false
			}
			return Message{Title: "Grabbed", Body: "🎬 " + title, Link: "/downloads"}, true
		},
	},
	{
		Key: "movie.imported", Topic: "movie.downloaded", Group: GroupLibrary, DefaultOn: true,
		Label: "Movie imported", Hint: "A movie file landed in the library.",
		Format: func(d map[string]any) (Message, bool) {
			title := str(d, "title")
			// A library scan adopting files already on disk isn't an import worth a ping
			// per film; those events carry source "scan" (and no title).
			if title == "" || str(d, "source") == "scan" {
				return Message{}, false
			}
			body := "📥 " + title
			if b, _ := d["upgrade"].(bool); b {
				body += " (upgraded)"
			}
			return Message{Title: "Imported", Body: body, Link: link("/movies/", num(d, "id"))}, true
		},
	},
	{
		Key: "episodes.imported", Topic: "series.imported", Group: GroupLibrary, DefaultOn: true,
		Label: "Episodes imported", Hint: "New episodes of a show landed in the library.",
		Format: func(d map[string]any) (Message, bool) {
			title := str(d, "title")
			if title == "" {
				return Message{}, false
			}
			body := "📥 " + title
			if n := num(d, "count"); n > 0 {
				body = fmt.Sprintf("📥 %s (%d episode%s)", title, n, plural(int(n)))
			}
			return Message{Title: "Imported", Body: body, Link: link("/series/", num(d, "id"))}, true
		},
	},
	{
		Key: "book.imported", Topic: "book.imported", Group: GroupLibrary, DefaultOn: true, Module: "books",
		Label: "Book imported", Hint: "An ebook or audiobook landed in the library.",
		Format: func(d map[string]any) (Message, bool) {
			title := str(d, "title")
			if title == "" {
				return Message{}, false
			}
			kind := "ebook"
			if str(d, "edition") == "audiobook" {
				kind = "audiobook"
			}
			if v := str(d, "version"); v != "" {
				kind += " · " + v
			}
			return Message{Title: "Imported", Body: fmt.Sprintf("📚 %s (%s)", title, kind), Link: link("/books/", num(d, "id"))}, true
		},
	},
	{
		Key: "music.imported", Topic: "music.imported", Group: GroupLibrary, Module: "music",
		Label: "Album imported", Hint: "Tracks of an album landed in the library.",
		Format: func(d map[string]any) (Message, bool) {
			album, artist := str(d, "album"), str(d, "artist")
			if album == "" {
				return Message{}, false
			}
			body := "🎵 " + album
			if artist != "" {
				body = "🎵 " + artist + " — " + album
			}
			if n := num(d, "placed"); n > 0 {
				body += fmt.Sprintf(" (%d track%s)", n, plural(int(n)))
			}
			return Message{Title: "Imported", Body: body, Link: link("/music/album/", num(d, "album_id"))}, true
		},
	},
	{
		Key: "plex.stream.started", Topic: "plex.stream.started", Group: GroupPlex,
		Label: "Stream started", Hint: "Someone started playing something on Plex.",
		Format: func(d map[string]any) (Message, bool) {
			title := str(d, "title")
			if title == "" {
				return Message{}, false
			}
			return Message{Title: "Now playing", Body: fmt.Sprintf("▶️ %s started %s", str(d, "user"), title), Link: "/insights"}, true
		},
	},
	{
		Key: "plex.buffering", Topic: "plex.buffering", Group: GroupPlex,
		Label: "Buffering", Hint: "A Plex stream started buffering.",
		Format: func(d map[string]any) (Message, bool) {
			title := str(d, "title")
			if title == "" {
				return Message{}, false
			}
			return Message{Title: "Buffering", Body: fmt.Sprintf("⏳ %s’s stream is buffering — %s", str(d, "user"), title), Link: "/insights?tab=reliability"}, true
		},
	},
}

var catalog = struct {
	sync.RWMutex
	defs  []EventDef
	byKey map[string]int
}{byKey: map[string]int{}}

func init() {
	for _, d := range builtinEvents {
		Register(d)
	}
}

// Register adds an event to the catalog. It panics on a duplicate key, an unknown group
// or a missing Format: those are programming mistakes, caught by the first test run.
// Call it before Service.Run starts (an init func does), or Run won't hear its topic.
func Register(d EventDef) {
	catalog.Lock()
	defer catalog.Unlock()
	if d.Key == "" || d.Label == "" || d.Format == nil {
		panic("notify: event " + d.Key + " needs a key, a label and a Format")
	}
	if _, dup := catalog.byKey[d.Key]; dup {
		panic("notify: event " + d.Key + " registered twice")
	}
	known := false
	for _, g := range groups {
		known = known || g.Key == d.Group
	}
	if !known {
		panic("notify: event " + d.Key + " has unknown group " + d.Group)
	}
	catalog.byKey[d.Key] = len(catalog.defs)
	catalog.defs = append(catalog.defs, d)
}

// Catalog returns every event, grouped in page order and in declaration order within
// a group.
func Catalog() []EventDef {
	catalog.RLock()
	out := append([]EventDef(nil), catalog.defs...)
	catalog.RUnlock()
	rank := map[string]int{}
	for i, g := range groups {
		rank[g.Key] = i
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Group] < rank[out[j].Group] })
	return out
}

// Lookup finds an event by key.
func Lookup(key string) (EventDef, bool) {
	catalog.RLock()
	defer catalog.RUnlock()
	i, ok := catalog.byKey[key]
	if !ok {
		return EventDef{}, false
	}
	return catalog.defs[i], true
}

// ValidEvents reports the first key that isn't in the catalog.
func ValidEvents(keys []string) error {
	for _, k := range keys {
		if _, ok := Lookup(k); !ok {
			return fmt.Errorf("unknown alert event %q", k)
		}
	}
	return nil
}

// DefaultEvents are the keys a new connection starts with.
func DefaultEvents() []string {
	var out []string
	for _, d := range Catalog() {
		if d.DefaultOn {
			out = append(out, d.Key)
		}
	}
	return out
}

// topics maps each bus topic to the events it produces.
func topics() map[string][]EventDef {
	out := map[string][]EventDef{}
	for _, d := range Catalog() {
		if d.Topic != "" {
			out[d.Topic] = append(out[d.Topic], d)
		}
	}
	return out
}

// payload turns a bus event's data into the map Format reads. Producers publish maps;
// anything else goes through JSON (so a struct's json names become the keys).
func payload(data any) map[string]any {
	if m, ok := data.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func str(d map[string]any, key string) string {
	switch v := d[key].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	}
	return ""
}

// num reads a whole number whatever type the producer used (an int from Go, a float64
// after a JSON round trip).
func num(d map[string]any, key string) int64 {
	switch v := d[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

func withYear(title string, year int64) string {
	if title != "" && year > 0 {
		return fmt.Sprintf("%s (%d)", title, year)
	}
	return title
}

func link(prefix string, id int64) string {
	if id <= 0 {
		return ""
	}
	return prefix + strconv.FormatInt(id, 10)
}
