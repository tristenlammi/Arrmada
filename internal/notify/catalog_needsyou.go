package notify

import (
	"fmt"
	"strings"
)

// The Needs-you alerts. None has a bus topic: the attention feed's alerter
// (internal/attention/alerts.go) emits them with EmitOnce after diffing the feed, so they
// are neither lost when the bus is busy nor repeated after a restart.
//
// Pending requests are not here: request.created announces a new request itself, and an
// alert from the feed as well would tell the owner twice.
const (
	EventImportHeld      = "import.held"
	EventImportStuck     = "import.stuck"
	EventDownloadFailed  = "download.failed"
	EventDownloadStalled = "download.stalled"
	EventHealthProblem   = "health.problem"
	EventHealthResolved  = "health.resolved"
)

// The payload every Needs-you event takes. One item: count 1 with its title (a full
// sentence), detail and link. Several gathered into one message: count n with names
// (the items' short names, in order) and the link of the page that lists them all.
//
//	{"count": 1, "title": "...", "detail": "...", "link": "/review", "level": "error"}
//	{"count": 6, "names": ["A", "B", ...], "link": "/downloads?show=problems"}
func init() {
	for _, d := range []EventDef{
		needsYouEvent(EventImportHeld, "Held for review", true,
			"Import held for review", "A finished download needs a decision before it can be imported.",
			"✋", "imports are held for review"),
		needsYouEvent(EventImportStuck, "Import stuck", true,
			"Import keeps failing", "A finished download keeps failing to import, or finished in a category Arrmada doesn't import.",
			"⚠️", "imports keep failing"),
		needsYouEvent(EventDownloadFailed, "Download failed", true,
			"Download failed", "The download client stopped a download with an error.",
			"❌", "downloads failed"),
		needsYouEvent(EventDownloadStalled, "Download stalled", false,
			"Download stalled", "Nobody has sent a download any data for an hour.",
			"🐢", "downloads have stalled"),
		{
			Key: EventHealthProblem, Group: GroupNeedsYou, DefaultOn: true,
			Label: "Health problem", Hint: "A health check found something wrong: a client or Plex unreachable, downloads paused for disk space, a folder missing.",
			Format: func(d map[string]any) (Message, bool) {
				icon := "🟠"
				if str(d, "level") == "error" {
					icon = "🔴"
				}
				return needsYouMessage(d, "Health problem", icon, "health problems")
			},
		},
		{
			Key: EventHealthResolved, Group: GroupNeedsYou, DefaultOn: true,
			Label: "Health problem resolved", Hint: "A health problem you were told about has cleared.",
			Format: func(d map[string]any) (Message, bool) {
				n := num(d, "count")
				if n == 1 {
					title := str(d, "title")
					if title == "" {
						return Message{}, false
					}
					return Message{Title: "Resolved", Body: "✅ Resolved: " + title, Link: str(d, "link")}, true
				}
				return needsYouMessage(d, "Resolved", "✅", "health problems resolved")
			},
		},
	} {
		Register(d)
	}
}

func needsYouEvent(key, title string, on bool, label, hint, icon, many string) EventDef {
	return EventDef{
		Key: key, Group: GroupNeedsYou, DefaultOn: on, Label: label, Hint: hint,
		Format: func(d map[string]any) (Message, bool) { return needsYouMessage(d, title, icon, many) },
	}
}

// needsYouMessage writes one item ("❌ Dune.2021 errored in the download client") or a
// summary of several ("❌ 6 downloads failed: A, B, C and 3 more").
func needsYouMessage(d map[string]any, title, icon, many string) (Message, bool) {
	n := num(d, "count")
	link := str(d, "link")
	if n == 1 {
		t := str(d, "title")
		if t == "" {
			return Message{}, false
		}
		body := icon + " " + t
		if detail := str(d, "detail"); detail != "" {
			body += "\n" + detail
		}
		return Message{Title: title, Body: body, Link: link}, true
	}
	names := strs(d, "names")
	if n < 2 || len(names) == 0 {
		return Message{}, false
	}
	return Message{Title: title, Body: fmt.Sprintf("%s %d %s: %s", icon, n, many, listSome(names, int(n), 3)), Link: link}, true
}

// listSome is "A, B, C and 3 more": the first few names of n.
func listSome(names []string, n, show int) string {
	if len(names) > show {
		names = names[:show]
	}
	out := strings.Join(names, ", ")
	if rest := n - len(names); rest > 0 {
		out += fmt.Sprintf(" and %d more", rest)
	}
	return out
}

// strs reads a list of strings, as Go passes it or after a JSON round trip.
func strs(d map[string]any, key string) []string {
	switch v := d[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
